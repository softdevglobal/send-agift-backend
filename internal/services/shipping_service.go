// Package services — shipping_service.go
//
// Delivery is priced only from each shop's delivery zones. There is no carrier
// quote. Checkout calls QuoteDelivery; place-order stores the zone price; the
// seller hands the parcel over with StartLocalDelivery / CompleteLocalDelivery.
package services

import (
	"context"
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
	ErrShippingNotReady = errors.New("order item is not ready for shipping")
	ErrShippingAddress  = errors.New("shipping addresses are incomplete")
)

// ShippingService prices zone delivery and records the seller handing a parcel over.
type ShippingService struct {
	shipments *repository.ShipmentRepository
	orders    *repository.OrderRepository
}

func NewShippingService(shipments *repository.ShipmentRepository, orders *repository.OrderRepository) *ShippingService {
	return &ShippingService{shipments: shipments, orders: orders}
}

// ResolveShopParcelItem finds the order item that the order + shop shipping
// routes work from. Lines with a status listed in prefer are tried first.
func (s *ShippingService) ResolveShopParcelItem(ctx context.Context, sellerID, orderID, shopID string, prefer ...string) (string, error) {
	oid, err := uuid.Parse(strings.TrimSpace(orderID))
	if err != nil {
		return "", ErrOrderNotFound
	}
	sid, err := uuid.Parse(strings.TrimSpace(shopID))
	if err != nil {
		return "", ErrOrderNotFound
	}
	lines, err := s.shipments.ListShopOrderLines(ctx, sellerID, oid, sid)
	if err != nil {
		if errors.Is(err, repository.ErrOrderNotFound) {
			return "", ErrOrderNotFound
		}
		return "", err
	}
	if len(lines) == 0 {
		return "", ErrOrderNotFound
	}
	for _, status := range prefer {
		for _, line := range lines {
			if line.FulfilmentStatus == status {
				return line.OrderItemID.String(), nil
			}
		}
	}
	return lines[0].OrderItemID.String(), nil
}

// shopLinesToShip is every not-yet-shipped product from this shop on the order.
func (s *ShippingService) shopLinesToShip(ctx context.Context, sellerID string, sc *repository.ShippingContext) ([]repository.ShopOrderLine, error) {
	lines, err := s.shipments.ListShopOrderLines(ctx, sellerID, sc.OrderID, sc.ShopID)
	if err != nil {
		return nil, err
	}
	open := make([]repository.ShopOrderLine, 0, len(lines))
	for _, line := range lines {
		switch line.FulfilmentStatus {
		case "cancelled", "dispatched", "delivered":
			continue
		case "pending":
			return nil, fmt.Errorf("%w: accept every product on this order before shipping — they leave as one parcel", ErrShippingNotReady)
		default:
			if line.FulfilmentStatus != "accepted" && line.FulfilmentStatus != "preparing" && line.FulfilmentStatus != "ready" {
				return nil, ErrShippingNotReady
			}
			open = append(open, line)
		}
	}
	if len(open) == 0 {
		return nil, ErrShippingNotReady
	}
	return open, nil
}

func shopLineIDs(lines []repository.ShopOrderLine) []uuid.UUID {
	ids := make([]uuid.UUID, len(lines))
	for i, line := range lines {
		ids[i] = line.OrderItemID
	}
	return ids
}

func (s *ShippingService) sellerDeliveryFor(ctx context.Context, sc *repository.ShippingContext) (*SellerDeliveryOption, error) {
	zonesByShop, err := s.shipments.DeliveryZonesForShops(ctx, []uuid.UUID{sc.ShopID})
	if err != nil {
		return nil, err
	}
	return buildSellerDeliveryOption(sc.FromLat, sc.FromLng, sc.ToLat, sc.ToLng, zonesByShop[sc.ShopID], time.Now().UTC()), nil
}

func applyAgreedSellerDelivery(s *models.Shipment, sc *repository.ShippingContext) {
	if sc.PendingDeliveryMode == nil || *sc.PendingDeliveryMode != "seller_managed" || sc.PendingPriceAmount == nil {
		return
	}
	price := *sc.PendingPriceAmount
	s.PriceAmount = &price
	if sc.PendingCurrency != nil {
		currency := *sc.PendingCurrency
		s.Currency = &currency
	}
	if sc.PendingEstimatedDays != nil {
		setEstimatedDays(s, *sc.PendingEstimatedDays)
	}
}

func setEstimatedDays(s *models.Shipment, days int) {
	d := days
	date := time.Now().UTC().AddDate(0, 0, days)
	date = time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	s.EstimatedDays = &d
	s.EstimatedDeliveryDate = &date
}

// LocalDeliveryInput is the optional body when the seller starts hand-delivery.
type LocalDeliveryInput struct {
	Note string `json:"note"`
}

// StartLocalDelivery begins shop delivery inside the shop's zones.
func (s *ShippingService) StartLocalDelivery(ctx context.Context, sellerID, orderItemID string, in LocalDeliveryInput) (*models.Shipment, error) {
	sc, err := s.shipments.GetShippingContext(ctx, sellerID, orderItemID)
	if err != nil {
		if errors.Is(err, repository.ErrOrderNotFound) {
			return nil, ErrOrderNotFound
		}
		return nil, err
	}
	openLines, err := s.shopLinesToShip(ctx, sellerID, sc)
	if err != nil {
		return nil, err
	}
	option, err := s.sellerDeliveryFor(ctx, sc)
	if err != nil {
		return nil, err
	}
	if option.hasZoneAndPoints() && !option.Available {
		return nil, fmt.Errorf("%w: %s", ErrOutsideDeliveryZone, option.Reason)
	}

	provider := "Local delivery"
	shipment := &models.Shipment{
		OrderID:         sc.OrderID,
		OrderItemID:     &sc.OrderItemID,
		SellerID:        sc.SellerID,
		CourierProvider: &provider,
		DeliveryMode:    "seller_managed",
		Status:          "in_transit",
	}
	option.applyToShipment(shipment)
	applyAgreedSellerDelivery(shipment, sc)

	meta := map[string]any{"type": "local_delivery", "seller_delivery": option}
	if note := strings.TrimSpace(in.Note); note != "" {
		meta["note"] = note
	}
	shipment.ProviderMetadata, _ = json.Marshal(meta)

	if err := s.shipments.DispatchSellerManagedItems(ctx, shipment, shopLineIDs(openLines)); err != nil {
		return nil, err
	}
	return shipment, nil
}

// CompleteLocalDelivery marks a shop's local delivery as handed over.
func (s *ShippingService) CompleteLocalDelivery(ctx context.Context, sellerID, orderItemID string) (*models.Shipment, error) {
	sc, err := s.shipments.GetShippingContext(ctx, sellerID, orderItemID)
	if err != nil {
		if errors.Is(err, repository.ErrOrderNotFound) {
			return nil, ErrOrderNotFound
		}
		return nil, err
	}
	if sc.FulfilmentStatus != "dispatched" {
		return nil, ErrShippingNotReady
	}
	shipment, err := s.shipments.GetLatestForOrderItem(ctx, sellerID, orderItemID)
	if err != nil {
		return nil, err
	}
	if shipment.DeliveryMode != "seller_managed" {
		return nil, ErrInvalidInput
	}
	now := time.Now().UTC()
	if err := s.shipments.MarkShopLocalDelivered(ctx, sc.OrderID, sc.ShopID, sc.SellerID, now); err != nil {
		return nil, err
	}
	shipment.Status = "delivered"
	shipment.DeliveredAt = &now
	return shipment, nil
}

// QuoteLineInput is one cart line to be delivered.
type QuoteLineInput struct {
	ProductID string `json:"product_id"`
	Quantity  int    `json:"quantity"`
}

// DeliveryQuoteInput asks what zone delivery will cost for a cart.
type DeliveryQuoteInput struct {
	RecipientID  string           `json:"recipient_id"`
	DeliveryDate string           `json:"delivery_date"`
	Items        []QuoteLineInput `json:"items"`
}

// QuotedPostalAddress is a shop dispatch address shown at checkout.
type QuotedPostalAddress struct {
	Name       string `json:"name,omitempty"`
	Line1      string `json:"line1"`
	Line2      string `json:"line2,omitempty"`
	City       string `json:"city,omitempty"`
	Region     string `json:"region,omitempty"`
	PostalCode string `json:"postal_code,omitempty"`
	Country    string `json:"country,omitempty"`
}

// QuotedShopDelivery is one shop's zone delivery for this recipient.
type QuotedShopDelivery struct {
	ShopID         string                `json:"shop_id"`
	ShopName       string                `json:"shop_name"`
	From           *QuotedPostalAddress  `json:"from,omitempty"`
	SellerDelivery *SellerDeliveryOption `json:"seller_delivery"`
}

// QuotedShipment is the zone price for one shop, echoed on place-order.
type QuotedShipment struct {
	ShopID        string `json:"shop_id"`
	ShopName      string `json:"shop_name"`
	Mode          string `json:"mode"`
	Provider      string `json:"provider"`
	ServiceName   string `json:"service_name"`
	Amount        int    `json:"amount"`
	Currency      string `json:"currency"`
	EstimatedDays int    `json:"estimated_days"`
	DaysAvailable int    `json:"days_available"`
}

// DeliveryQuote is the cart's zone-delivery cost.
type DeliveryQuote struct {
	Shops     []QuotedShopDelivery `json:"shops"`
	Shipments []QuotedShipment     `json:"shipments"`
	Amount    int                  `json:"amount"`
	Currency  string               `json:"currency"`
	Complete  bool                 `json:"complete"`
	Unquoted  []string             `json:"unquoted,omitempty"`
}

// QuoteDelivery prices each shop from its delivery zones. It does not write an order.
func (s *ShippingService) QuoteDelivery(ctx context.Context, customerID string, in DeliveryQuoteInput) (*DeliveryQuote, error) {
	if len(in.Items) == 0 || strings.TrimSpace(in.RecipientID) == "" {
		return nil, ErrInvalidInput
	}
	to, err := s.shipments.ShipToForRecipient(ctx, customerID, strings.TrimSpace(in.RecipientID))
	if err != nil {
		if errors.Is(err, repository.ErrShipmentNotFound) {
			return nil, ErrOrderNotFound
		}
		return nil, err
	}

	shopIDs := []uuid.UUID{}
	seen := map[uuid.UUID]struct{}{}
	for _, line := range in.Items {
		product, err := s.orders.GetCheckoutProduct(ctx, strings.TrimSpace(line.ProductID))
		if err != nil {
			return nil, ErrInvalidInput
		}
		shopID, err := uuid.Parse(product.ShopID)
		if err != nil {
			return nil, ErrInvalidInput
		}
		if _, ok := seen[shopID]; ok {
			continue
		}
		seen[shopID] = struct{}{}
		shopIDs = append(shopIDs, shopID)
	}

	froms, err := s.shipments.ShipFromForShops(ctx, shopIDs)
	if err != nil {
		return nil, err
	}
	zonesByShop, err := s.shipments.DeliveryZonesForShops(ctx, shopIDs)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()

	quote := &DeliveryQuote{
		Shops:     []QuotedShopDelivery{},
		Shipments: []QuotedShipment{},
		Complete:  true,
	}
	for _, shopID := range shopIDs {
		from := froms[shopID]
		sellerOpt := buildSellerDeliveryOption(from.Latitude, from.Longitude, to.Latitude, to.Longitude, zonesByShop[shopID], now)
		quote.Shops = append(quote.Shops, QuotedShopDelivery{
			ShopID:         shopID.String(),
			ShopName:       from.Name,
			From:           quotedFromAddress(from),
			SellerDelivery: sellerOpt,
		})
		if !sellerOpt.Available {
			quote.Complete = false
			reason := sellerOpt.Reason
			if reason == "" {
				reason = "shop delivery is not available"
			}
			if from.Name != "" {
				reason = from.Name + ": " + reason
			}
			quote.Unquoted = append(quote.Unquoted, reason)
			continue
		}
		quote.Shipments = append(quote.Shipments, sellerDeliveryQuotedShipment(shopID.String(), from.Name, sellerOpt))
		quote.Amount += sellerOpt.PriceAmount
		if quote.Currency == "" {
			quote.Currency = sellerOpt.Currency
		}
	}
	return quote, nil
}

func quotedFromAddress(from repository.CartShipFrom) *QuotedPostalAddress {
	if from.Street1 == "" && from.City == "" {
		return nil
	}
	return &QuotedPostalAddress{
		Name:       from.Name,
		Line1:      from.Street1,
		Line2:      from.Street2,
		City:       from.City,
		Region:     from.Region,
		PostalCode: from.PostalCode,
		Country:    from.CountryISO,
	}
}

func sellerDeliveryQuotedShipment(shopID, shopName string, opt *SellerDeliveryOption) QuotedShipment {
	return QuotedShipment{
		ShopID:        shopID,
		ShopName:      shopName,
		Mode:          SellerDeliveryModeName,
		Provider:      "Seller delivery",
		ServiceName:   fmt.Sprintf("Within %g km", opt.MaxKm),
		Amount:        opt.PriceAmount,
		Currency:      opt.Currency,
		EstimatedDays: opt.EstimatedDays,
		DaysAvailable: opt.EstimatedDays,
	}
}
