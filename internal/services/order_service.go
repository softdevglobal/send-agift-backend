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
	// ErrGiftPoints is points attached to a gift that cannot be sent.
	ErrGiftPoints = errors.New("gift points cannot be sent")
	// ErrOrderRewardSpent is cancelling an order whose reward points have
	// already been spent.
	ErrOrderRewardSpent = errors.New("this order's reward points have already been spent, so it can no longer be cancelled here — contact support")
	// ErrGiftPointsBalance is more points attached than the customer holds.
	ErrGiftPointsBalance = errors.New("not enough points to attach to this gift")
)

// maxGiftPoints caps what one gift can carry.
const maxGiftPoints = 1_000_000

type OrderService struct {
	orders    *repository.OrderRepository
	customers *repository.CustomerRepository
	countries *repository.CountryRepository
	shipments *repository.ShipmentRepository
	gifts     *GiftRecipientService
}

// NotifyWith sends order emails and gives gift recipients their accounts.
func (s *OrderService) NotifyWith(gifts *GiftRecipientService) { s.gifts = gifts }

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

// OrderShippingQuoteInput is the shop the customer is ordering from.
// Price and days are always recomputed from that shop's delivery zones.
// Mode must be "seller_delivery" when sent; carrier quotes are not accepted.
type OrderShippingQuoteInput struct {
	ShopID   string `json:"shop_id"`
	Mode     string `json:"mode"`
	Amount   int    `json:"amount"`
	Currency string `json:"currency"`
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
	// GiftPoints are points from the customer's own balance to send with
	// the gift. They reach the recipient on delivery if the recipient's
	// email belongs to a SendAGift account, and come back otherwise.
	GiftPoints int64 `json:"gift_points"`
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

	if in.GiftPoints < 0 || in.GiftPoints > maxGiftPoints {
		return nil, fmt.Errorf("%w: gift_points must be 0 to %d", ErrGiftPoints, maxGiftPoints)
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
		// Points find the recipient's account by email, so there has to be one.
		if in.GiftPoints > 0 && (rec.Email == nil || strings.TrimSpace(*rec.Email) == "") {
			return nil, fmt.Errorf("%w: add the recipient's email address to send them points", ErrGiftPoints)
		}
	}
	if in.GiftPoints > 0 && recipientID == nil {
		return nil, fmt.Errorf("%w: choose a recipient to send points to", ErrGiftPoints)
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

	for _, q := range in.ShippingQuotes {
		mode := strings.ToLower(strings.TrimSpace(q.Mode))
		if mode != "" && mode != SellerDeliveryModeName {
			return nil, fmt.Errorf("%w: only shop delivery zones are available", ErrInvalidOrder)
		}
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
			// Reserved from the seller when the order is written; dropped to
			// zero there if they cannot cover it.
			RewardPointsPerUnit: snap.RewardPoints,
		})
	}

	// Every shop is priced from its delivery zones. Client amounts are ignored.
	sellerDeliveries, err := s.priceShopDeliveries(ctx, customerID, recipientID, shopsInOrder, currency)
	if err != nil {
		return nil, err
	}
	deliveries, err := shopDeliveriesFromZones(sellerDeliveries, shopsInOrder, currency)
	if err != nil {
		return nil, err
	}
	deliveryAmount := 0
	for _, d := range deliveries {
		deliveryAmount += d.Amount
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
		GiftPoints:      in.GiftPoints,
	}

	err = s.orders.Create(ctx, order, items, deliveries)
	if errors.Is(err, repository.ErrOrderDuplicate) {
		order.OrderNumber = newOrderNumber()
		err = s.orders.Create(ctx, order, items, deliveries)
	}
	if errors.Is(err, repository.ErrPointsInsufficient) {
		return nil, ErrGiftPointsBalance
	}
	if err != nil {
		return nil, err
	}

	if err := s.persistCheckoutQuotes(ctx, order.ID, items, sellerDeliveries); err != nil {
		return nil, err
	}

	shopDeliveries, err := s.orders.ListShopDeliveries(ctx, order.ID)
	if err != nil {
		return nil, err
	}
	s.gifts.OrderPlaced(ctx, order.ID)
	return &models.OrderDetails{Order: *order, Items: items, ShopDeliveries: shopDeliveries}, nil
}

// ReceivedGifts lists the delivered gifts other customers sent to this one.
func (s *OrderService) ReceivedGifts(ctx context.Context, customerID string) ([]models.ReceivedGift, error) {
	if s.gifts == nil {
		return []models.ReceivedGift{}, nil
	}
	return s.gifts.ReceivedGifts(ctx, customerID)
}

// shopDeliveriesFromZones turns each shop's zone price into the rows saved on
// marketplace.order_shop_deliveries.
func shopDeliveriesFromZones(
	sellerDeliveries map[string]*SellerDeliveryOption,
	shopsInOrder map[string]uuid.UUID,
	currency string,
) ([]models.OrderShopDelivery, error) {
	keys := make([]string, 0, len(sellerDeliveries))
	for k := range sellerDeliveries {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]models.OrderShopDelivery, 0, len(keys))
	for _, shopKey := range keys {
		opt := sellerDeliveries[shopKey]
		if opt == nil || !opt.Available {
			continue
		}
		sellerID, ok := shopsInOrder[shopKey]
		if !ok {
			continue
		}
		shopID, err := uuid.Parse(shopKey)
		if err != nil {
			return nil, ErrInvalidOrder
		}
		days := opt.EstimatedDays
		out = append(out, models.OrderShopDelivery{
			ShopID:        shopID,
			SellerID:      sellerID,
			Mode:          SellerDeliveryModeName,
			Provider:      "Seller delivery",
			ServiceName:   fmt.Sprintf("Within %g km", opt.MaxKm),
			Amount:        opt.PriceAmount,
			Currency:      currency,
			EstimatedDays: &days,
			DistanceKm:    opt.DistanceKm,
		})
	}
	return out, nil
}

// priceShopDeliveries prices every shop on the order from its delivery zones
// and the recipient's distance. The client amount is never used.
func (s *OrderService) priceShopDeliveries(
	ctx context.Context,
	customerID string,
	recipientID *uuid.UUID,
	shopsInOrder map[string]uuid.UUID,
	currency string,
) (map[string]*SellerDeliveryOption, error) {
	out := map[string]*SellerDeliveryOption{}
	if len(shopsInOrder) == 0 {
		return out, nil
	}
	if recipientID == nil || s.shipments == nil {
		return nil, fmt.Errorf("%w: a recipient is required so delivery can be priced from the shop's zones", ErrInvalidOrder)
	}
	shopIDs := make([]uuid.UUID, 0, len(shopsInOrder))
	for shopKey := range shopsInOrder {
		sid, err := uuid.Parse(shopKey)
		if err != nil {
			return nil, ErrInvalidOrder
		}
		shopIDs = append(shopIDs, sid)
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
		if opt.Currency != "" && !strings.EqualFold(opt.Currency, currency) {
			return nil, fmt.Errorf("%w: delivery for shop %s is in %s but the order is in %s", ErrInvalidOrder, sid, opt.Currency, strings.ToUpper(currency))
		}
		out[sid.String()] = opt
	}
	return out, nil
}

// persistCheckoutQuotes writes a pending shop-delivery shipment for each line.
func (s *OrderService) persistCheckoutQuotes(
	ctx context.Context,
	orderID uuid.UUID,
	items []models.OrderItem,
	sellerDeliveries map[string]*SellerDeliveryOption,
) error {
	if s.shipments == nil || len(sellerDeliveries) == 0 {
		return nil
	}
	for i := range items {
		item := &items[i]
		opt, ok := sellerDeliveries[item.ShopID.String()]
		if !ok {
			continue
		}
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
		if errors.Is(err, repository.ErrRewardSpent) {
			return nil, ErrOrderRewardSpent
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
