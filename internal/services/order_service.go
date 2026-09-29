package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"myapp/internal/models"
	"myapp/internal/repository"
)

var (
	ErrOrderNotFound          = errors.New("order not found")
	ErrInvalidOrder           = errors.New("invalid order")
	ErrOrderProduct           = errors.New("product not available")
	ErrOrderCurrencyMix       = errors.New("items must share the same currency")
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
	ShopID string `json:"shop_id"`
	// Mode is "courier" (default) or "seller_delivery". For seller_delivery the
	// price and days are recomputed from the shop's delivery zones; courier
	// fields and amount in the body are ignored.
	Mode             string `json:"mode"`
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
	// shop id → seller id, for every shop that has a product on this order.
	shopsInOrder := map[string]uuid.UUID{}

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
		shopsInOrder[shopID.String()] = sellerID
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

	// A quote for a shop that has nothing on this order is ignored.
	for shopKey := range quotesByShop {
		if _, ok := shopsInOrder[shopKey]; !ok {
			delete(quotesByShop, shopKey)
		}
	}
	for shopKey, q := range quotesByShop {
		mode := strings.ToLower(strings.TrimSpace(q.Mode))
		if mode == "" || mode == "courier" {
			if strings.TrimSpace(q.Provider) == "" || strings.TrimSpace(q.ServiceName) == "" {
				return nil, fmt.Errorf("%w: courier quote for shop %s needs provider and service_name", ErrInvalidOrder, shopKey)
			}
		}
	}

	sellerDeliveries, err := s.priceSellerDeliveries(ctx, customerID, recipientID, quotesByShop)
	if err != nil {
		return nil, err
	}

	// Every shop's parcel is priced separately, so the order's delivery is the
	// sum of the per-shop quotes. A client-sent delivery_amount is only used
	// when no shop was quoted at all.
	deliveries, err := shopDeliveriesFromQuotes(quotesByShop, sellerDeliveries, shopsInOrder, currency)
	if err != nil {
		return nil, err
	}
	deliveryAmount := 0
	for _, d := range deliveries {
		deliveryAmount += d.Amount
	}
	if len(deliveries) == 0 && in.DeliveryAmount != nil {
		if *in.DeliveryAmount < 0 {
			return nil, ErrInvalidOrder
		}
		deliveryAmount = *in.DeliveryAmount
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

	if err := s.orders.Create(ctx, order, items, deliveries); err != nil {
		if errors.Is(err, repository.ErrOrderDuplicate) {
			order.OrderNumber = newOrderNumber()
			if err := s.orders.Create(ctx, order, items, deliveries); err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}

	if err := s.persistCheckoutQuotes(ctx, order.ID, items, quotesByShop, sellerDeliveries); err != nil {
		return nil, err
	}

	shopDeliveries, err := s.orders.ListShopDeliveries(ctx, order.ID)
	if err != nil {
		return nil, err
	}
	return &models.OrderDetails{Order: *order, Items: items, ShopDeliveries: shopDeliveries}, nil
}

// shopDeliveriesFromQuotes turns the per-shop checkout quotes into the rows
// saved on marketplace.order_shop_deliveries. Delivery must be in the order
// currency, so it can be added to the order total.
func shopDeliveriesFromQuotes(
	quotesByShop map[string]OrderShippingQuoteInput,
	sellerDeliveries map[string]*SellerDeliveryOption,
	shopsInOrder map[string]uuid.UUID,
	currency string,
) ([]models.OrderShopDelivery, error) {
	keys := make([]string, 0, len(quotesByShop))
	for k := range quotesByShop {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]models.OrderShopDelivery, 0, len(keys))
	for _, shopKey := range keys {
		q := quotesByShop[shopKey]
		sellerID, ok := shopsInOrder[shopKey]
		if !ok {
			continue
		}
		shopID, err := uuid.Parse(shopKey)
		if err != nil {
			return nil, ErrInvalidOrder
		}
		qCurrency := strings.ToUpper(strings.TrimSpace(q.Currency))
		if qCurrency == "" {
			qCurrency = strings.ToUpper(currency)
		}
		if !strings.EqualFold(qCurrency, currency) {
			return nil, fmt.Errorf("%w: delivery for shop %s is quoted in %s but the order is in %s", ErrInvalidOrder, shopKey, qCurrency, strings.ToUpper(currency))
		}
		d := models.OrderShopDelivery{
			ShopID:      shopID,
			SellerID:    sellerID,
			Mode:        "courier",
			Provider:    strings.TrimSpace(q.Provider),
			ServiceName: strings.TrimSpace(q.ServiceName),
			Amount:      q.Amount,
			Currency:    currency,
		}
		if opt, isSeller := sellerDeliveries[shopKey]; isSeller {
			days := opt.EstimatedDays
			d.Mode = SellerDeliveryModeName
			d.Provider = "Seller delivery"
			d.ServiceName = fmt.Sprintf("Within %g km", opt.MaxKm)
			d.Amount = opt.PriceAmount
			d.EstimatedDays = &days
			d.DistanceKm = opt.DistanceKm
		}
		out = append(out, d)
	}
	return out, nil
}

// priceSellerDeliveries re-prices every shop the customer chose seller delivery for,
// from the shop's delivery zones and the recipient's distance. The quote amount is
// replaced with the server price so the order total can't be tampered with.
func (s *OrderService) priceSellerDeliveries(
	ctx context.Context,
	customerID string,
	recipientID *uuid.UUID,
	quotesByShop map[string]OrderShippingQuoteInput,
) (map[string]*SellerDeliveryOption, error) {
	out := map[string]*SellerDeliveryOption{}
	var shopIDs []uuid.UUID
	for shopKey, q := range quotesByShop {
		mode := strings.ToLower(strings.TrimSpace(q.Mode))
		if mode == "" || mode == "courier" {
			continue
		}
		if mode != SellerDeliveryModeName {
			return nil, ErrInvalidOrder
		}
		sid, err := uuid.Parse(shopKey)
		if err != nil {
			return nil, ErrInvalidOrder
		}
		shopIDs = append(shopIDs, sid)
	}
	if len(shopIDs) == 0 {
		return out, nil
	}
	if recipientID == nil || s.shipments == nil {
		return nil, fmt.Errorf("%w: seller_delivery needs a recipient_id", ErrInvalidOrder)
	}
	to, err := s.shipments.ShipToForRecipient(ctx, customerID, recipientID.String())
	if err != nil {
		return nil, err
	}
	froms, err := s.shipments.ShipFromForShops(ctx, shopIDs)
	if err != nil {
		return nil, err
	}
	zones, err := s.shipments.DeliveryZonesForShops(ctx, shopIDs)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	for _, sid := range shopIDs {
		from := froms[sid]
		opt := buildSellerDeliveryOption(from.Latitude, from.Longitude, to.Latitude, to.Longitude, zones[sid], now)
		if !opt.Available {
			return nil, fmt.Errorf("%w: %s", ErrOutsideDeliveryZone, opt.Reason)
		}
		key := sid.String()
		q := quotesByShop[key]
		q.Amount = opt.PriceAmount
		q.Currency = opt.Currency
		quotesByShop[key] = q
		out[key] = opt
	}
	return out, nil
}

// persistCheckoutQuotes writes pending courier shipments from the selected
// checkout quote. Seller GetRates later upserts the same pending row with fresh rates.
func (s *OrderService) persistCheckoutQuotes(
	ctx context.Context,
	orderID uuid.UUID,
	items []models.OrderItem,
	quotesByShop map[string]OrderShippingQuoteInput,
	sellerDeliveries map[string]*SellerDeliveryOption,
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
		if opt, isSeller := sellerDeliveries[item.ShopID.String()]; isSeller {
			meta, _ := json.Marshal(map[string]any{"source": "checkout_quote", "mode": SellerDeliveryModeName})
			itemID := item.ID
			shipment := &models.Shipment{
				OrderID:          orderID,
				OrderItemID:      &itemID,
				SellerID:         item.SellerID,
				DeliveryMode:     "seller_managed",
				Status:           "pending",
				ProviderMetadata: meta,
			}
			opt.applyToShipment(shipment)
			if err := s.shipments.UpsertQuote(ctx, shipment); err != nil {
				return err
			}
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
	shopDeliveries, err := s.orders.ListShopDeliveries(ctx, order.ID)
	if err != nil {
		return nil, err
	}
	return &models.OrderDetails{Order: *order, Items: items, ShopDeliveries: shopDeliveries}, nil
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
	delivery, err := s.orders.GetShopDelivery(ctx, item.OrderID, item.ShopID)
	if err != nil {
		return nil, err
	}
	item.ShopDelivery = delivery
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
