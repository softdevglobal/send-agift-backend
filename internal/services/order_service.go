package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"myapp/internal/models"
	"myapp/internal/repository"
)

var (
	ErrOrderNotFound       = errors.New("order not found")
	ErrInvalidOrder        = errors.New("invalid order")
	ErrOrderProduct        = errors.New("product not available")
	ErrOrderCurrencyMix    = errors.New("items must share the same currency")
	ErrOrderProductVisibility = errors.New("product not visible for this customer type")
	ErrOrderNotCancellable    = errors.New("order cannot be cancelled")
	ErrOrderItemNotFound      = errors.New("order item not found")
	ErrOrderItemNotAcceptable = errors.New("order item cannot be accepted")
)

type OrderService struct {
	orders    *repository.OrderRepository
	customers *repository.CustomerRepository
	countries *repository.CountryRepository
	shipments *repository.ShipmentRepository
}

func NewOrderService(
	orders *repository.OrderRepository,
	customers *repository.CustomerRepository,
	countries *repository.CountryRepository,
	shipments *repository.ShipmentRepository,
) *OrderService {
	return &OrderService{orders: orders, customers: customers, countries: countries, shipments: shipments}
}

type OrderItemInput struct {
	ProductID string `json:"product_id"`
	Quantity  int    `json:"quantity"`
}

// OrderShippingQuoteInput is a checkout quote the client echoes back on place-order
// so we can persist a pending marketplace.shipments row per shop.
type OrderShippingQuoteInput struct {
	ShopID           string `json:"shop_id"`
	RateObjectID     string `json:"rate_object_id"`
	ShipmentObjectID string `json:"shipment_object_id"`
	Provider         string `json:"provider"`
	ServiceName      string `json:"service_name"`
	Amount           int    `json:"amount"`
	Currency         string `json:"currency"`
}

type OrderCreateInput struct {
	RecipientID     *string                   `json:"recipient_id"`
	CountryID       string                    `json:"country_id"`
	CustomerType    string                    `json:"customer_type"`
	DeliveryDate    string                    `json:"delivery_date"`
	GiftMessage     *string                   `json:"gift_message"`
	MediaGreetingID *string                   `json:"media_greeting_id"`
	DeliveryAmount  *int                      `json:"delivery_amount"`
	Items           []OrderItemInput          `json:"items"`
	ShippingQuotes  []OrderShippingQuoteInput `json:"shipping_quotes"`
}

func (s *OrderService) Create(ctx context.Context, customerID string, in OrderCreateInput) (*models.OrderDetails, error) {
	if _, err := s.customers.GetByID(ctx, customerID); err != nil {
		if errors.Is(err, repository.ErrCustomerNotFound) {
			return nil, ErrCustomerNotFound
		}
		return nil, err
	}

	cid, err := uuid.Parse(customerID)
	if err != nil {
		return nil, ErrCustomerNotFound
	}

	countryID, err := uuid.Parse(strings.TrimSpace(in.CountryID))
	if err != nil {
		return nil, ErrInvalidCountry
	}
	if _, err := s.countries.GetByID(ctx, countryID.String()); err != nil {
		if errors.Is(err, repository.ErrCountryNotFound) {
			return nil, ErrInvalidCountry
		}
		return nil, err
	}

	customerType := strings.TrimSpace(in.CustomerType)
	if customerType == "" {
		customerType = "personal"
	}
	if customerType != "personal" && customerType != "corporate" {
		return nil, ErrInvalidOrder
	}

	deliveryDate, err := repository.ParseDate(in.DeliveryDate)
	if err != nil || deliveryDate == nil {
		return nil, ErrInvalidOrder
	}

	var recipientID *uuid.UUID
	if in.RecipientID != nil && strings.TrimSpace(*in.RecipientID) != "" {
		rec, err := s.customers.GetRecipientByID(ctx, customerID, strings.TrimSpace(*in.RecipientID))
		if err != nil {
			if errors.Is(err, repository.ErrRecipientNotFound) {
				return nil, ErrRecipientNotFound
			}
			return nil, err
		}
		recipientID = &rec.ID
	}

	var mediaGreetingID *uuid.UUID
	if in.MediaGreetingID != nil && strings.TrimSpace(*in.MediaGreetingID) != "" {
		mid, err := uuid.Parse(strings.TrimSpace(*in.MediaGreetingID))
		if err != nil {
			return nil, ErrInvalidOrder
		}
		mediaGreetingID = &mid
	}

	if len(in.Items) == 0 {
		return nil, ErrInvalidOrder
	}

	quotesByShop := map[string]OrderShippingQuoteInput{}
	for _, q := range in.ShippingQuotes {
		shopKey := strings.TrimSpace(q.ShopID)
		if shopKey == "" {
			continue
		}
		if q.Amount < 0 {
			return nil, ErrInvalidOrder
		}
		quotesByShop[shopKey] = q
	}

	items := make([]models.OrderItem, 0, len(in.Items))
	subtotal := 0
	currency := ""
	shopsInOrder := map[string]struct{}{}

	for _, line := range in.Items {
		if line.Quantity < 1 {
			return nil, ErrInvalidOrder
		}
		pid, err := uuid.Parse(strings.TrimSpace(line.ProductID))
		if err != nil {
			return nil, ErrOrderProduct
		}
		snap, err := s.orders.GetCheckoutProduct(ctx, pid.String())
		if err != nil {
			if errors.Is(err, repository.ErrOrderProductNotFound) {
				return nil, ErrOrderProduct
			}
			return nil, err
		}
		if snap.Status != "published" || snap.ShopStatus != "active" {
			return nil, ErrOrderProduct
		}
		if snap.CustomerTypeVisibility != "both" && snap.CustomerTypeVisibility != customerType {
			return nil, ErrOrderProductVisibility
		}
		if currency == "" {
			currency = snap.Currency
		} else if snap.Currency != currency {
			return nil, ErrOrderCurrencyMix
		}

		sellerID, _ := uuid.Parse(snap.SellerID)
		shopID, _ := uuid.Parse(snap.ShopID)
		lineTotal := snap.PriceAmount * line.Quantity
		subtotal += lineTotal
		shopsInOrder[shopID.String()] = struct{}{}
		items = append(items, models.OrderItem{
			SellerID:         sellerID,
			ShopID:           shopID,
			ProductID:        pid,
			Quantity:         line.Quantity,
			UnitAmount:       snap.PriceAmount,
			TotalAmount:      lineTotal,
			FulfilmentStatus: "pending",
		})
	}

	deliveryAmount := 0
	if in.DeliveryAmount != nil {
		if *in.DeliveryAmount < 0 {
			return nil, ErrInvalidOrder
		}
		deliveryAmount = *in.DeliveryAmount
	} else if len(quotesByShop) > 0 {
		for shopID := range shopsInOrder {
			if q, ok := quotesByShop[shopID]; ok {
				deliveryAmount += q.Amount
			}
		}
	}

	order := &models.Order{
		OrderNumber:     newOrderNumber(),
		CustomerID:      cid,
		RecipientID:     recipientID,
		CountryID:       countryID,
		CustomerType:    customerType,
		DeliveryDate:    *deliveryDate,
		Status:          "pending_payment",
		SubtotalAmount:  subtotal,
		DeliveryAmount:  deliveryAmount,
		TotalAmount:     subtotal + deliveryAmount,
		Currency:        currency,
		GiftMessage:     in.GiftMessage,
		MediaGreetingID: mediaGreetingID,
	}

	if err := s.orders.Create(ctx, order, items); err != nil {
		if errors.Is(err, repository.ErrOrderDuplicate) {
			order.OrderNumber = newOrderNumber()
			if err := s.orders.Create(ctx, order, items); err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}

	if err := s.persistCheckoutQuotes(ctx, order.ID, items, quotesByShop); err != nil {
		return nil, err
	}

	return &models.OrderDetails{Order: *order, Items: items}, nil
}

// persistCheckoutQuotes writes pending courier shipments from the selected
// checkout quote. Seller GetRates later upserts the same pending row with fresh rates.
func (s *OrderService) persistCheckoutQuotes(
	ctx context.Context,
	orderID uuid.UUID,
	items []models.OrderItem,
	quotesByShop map[string]OrderShippingQuoteInput,
) error {
	if s.shipments == nil || len(quotesByShop) == 0 {
		return nil
	}
	for i := range items {
		item := &items[i]
		q, ok := quotesByShop[item.ShopID.String()]
		if !ok {
			continue
		}
		meta, err := json.Marshal(map[string]any{
			"rate_object_id": strings.TrimSpace(q.RateObjectID),
			"service_name":   strings.TrimSpace(q.ServiceName),
			"amount":         q.Amount,
			"currency":       strings.TrimSpace(q.Currency),
			"provider":       strings.TrimSpace(q.Provider),
			"source":         "checkout_quote",
		})
		if err != nil {
			return err
		}
		var providerShipmentID *string
		if sid := strings.TrimSpace(q.ShipmentObjectID); sid != "" {
			providerShipmentID = &sid
		}
		itemID := item.ID
		shipment := &models.Shipment{
			OrderID:            orderID,
			OrderItemID:        &itemID,
			SellerID:           item.SellerID,
			DeliveryMode:       "courier",
			Status:             "pending",
			ProviderShipmentID: providerShipmentID,
			ProviderMetadata:   meta,
		}
		if err := s.shipments.UpsertQuote(ctx, shipment); err != nil {
			return err
		}
	}
	return nil
}

func (s *OrderService) List(ctx context.Context, customerID string) ([]models.Order, error) {
	if _, err := s.customers.GetByID(ctx, customerID); err != nil {
		if errors.Is(err, repository.ErrCustomerNotFound) {
			return nil, ErrCustomerNotFound
		}
		return nil, err
	}
	return s.orders.ListByCustomer(ctx, customerID)
}

func (s *OrderService) Get(ctx context.Context, customerID, orderID string) (*models.OrderDetails, error) {
	order, err := s.orders.GetByIDForCustomer(ctx, customerID, orderID)
	if err != nil {
		if errors.Is(err, repository.ErrOrderNotFound) {
			return nil, ErrOrderNotFound
		}
		return nil, err
	}
	items, err := s.orders.ListItems(ctx, order.ID.String())
	if err != nil {
		return nil, err
	}
	return &models.OrderDetails{Order: *order, Items: items}, nil
}

func (s *OrderService) Cancel(ctx context.Context, customerID, orderID string) (*models.OrderDetails, error) {
	err := s.orders.CancelForCustomer(ctx, customerID, orderID)
	if err != nil {
		if errors.Is(err, repository.ErrOrderNotFound) {
			return nil, ErrOrderNotFound
		}
		if errors.Is(err, repository.ErrOrderNotCancellable) {
			return nil, ErrOrderNotCancellable
		}
		return nil, err
	}
	return s.Get(ctx, customerID, orderID)
}

func (s *OrderService) ListItemsForSeller(ctx context.Context, sellerID string) ([]models.SellerOrderItemSummary, error) {
	return s.orders.ListItemsBySeller(ctx, sellerID)
}

func (s *OrderService) GetItemForSeller(ctx context.Context, sellerID, itemID string) (*models.SellerOrderItemDetails, error) {
	item, err := s.orders.GetItemBySeller(ctx, sellerID, itemID)
	if err != nil {
		if errors.Is(err, repository.ErrOrderItemNotFound) {
			return nil, ErrOrderItemNotFound
		}
		return nil, err
	}
	return item, nil
}

func (s *OrderService) AcceptItemForSeller(ctx context.Context, sellerID, itemID string) (*models.OrderItem, error) {
	item, err := s.orders.AcceptItemForSeller(ctx, sellerID, itemID)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrOrderItemNotFound):
			return nil, ErrOrderItemNotFound
		case errors.Is(err, repository.ErrOrderItemNotAcceptable):
			return nil, ErrOrderItemNotAcceptable
		default:
			return nil, err
		}
	}
	return item, nil
}

func newOrderNumber() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("SAG-%s-%s", time.Now().UTC().Format("20060102"), strings.ToUpper(hex.EncodeToString(b)))
}
