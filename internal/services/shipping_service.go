// Package services — shipping_service.go
//
// This file is the business logic for Shippo shipping end-to-end:
//
//	CUSTOMER                          SELLER
//	--------                          ------
//	QuoteDelivery  (JSON only)        GetRates     → fresh Shippo rates + courier lock
//	place-order saves pending         BuyLabel     → buy locked courier label
//	  marketplace.shipments           LabelURL     → presigned PDF
//	                                  Manual / Local delivery fallbacks
//	Shippo webhook → HandleTrackingWebhook (in_transit / delivered)
//
// Important design rules used throughout:
//  1. Checkout Shippo rate_object_id EXPIRES — never buy with the checkout id.
//  2. Courier lock = provider + service_name stored in shipments.provider_metadata.
//  3. Parcel size/weight comes from product.parcel_* (or POST body), not shipments.
//  4. Idempotency keys prevent double-charging on BuyLabel retries.
//
// Handlers call these methods; repositories do SQL; ShippoClient does HTTP to api.goshippo.com.
package services

import (
	"context"       // request-scoped cancellation / deadlines
	"encoding/json" // marshal/unmarshal provider_metadata and Shippo payloads
	"errors"        // sentinel errors (ErrShippingNotReady, etc.)
	"fmt"           // wrap errors with %w for handler mapping
	"math"          // used when comparing rate amounts
	"net/http"      // download label PDF from Shippo label_url
	"sort"          // sort courier options cheapest-first for quote UI
	"strconv"       // parse weight strings / format multiplied weight
	"strings"       // trim / normalize courier names and countries
	"time"          // label link TTL, delivery dates, delivered_at

	"github.com/google/uuid" // order_item / shop ids

	"myapp/internal/models"
	"myapp/internal/repository"
)

// Sentinel errors returned to handlers (mapped to HTTP 4xx/5xx).
var (
	ErrShippingNotConfigured = errors.New("shipping provider not configured")     // SHIPPO_API_KEY missing
	ErrShippingNotReady      = errors.New("order item is not ready for shipping") // wrong fulfilment_status
	ErrShippingAddress       = errors.New("shipping addresses are incomplete")    // missing street/city/country
	ErrShippingProvider      = errors.New("shipping provider error")              // Shippo API failure
	ErrShippingLabelNotFound = errors.New("shipping label not found")             // no PDF bought yet
	// ErrCourierChangeRequiresChat: customer locked a courier at checkout; seller must
	// message the customer before buying a different carrier/service. Handler → HTTP 409.
	ErrCourierChangeRequiresChat = errors.New("use the customer-selected courier, or message the customer to agree a change first")
)

// LabelLink is a short-lived link to a bought label PDF.
type LabelLink struct {
	URL            string  `json:"url"`
	MimeType       string  `json:"mime_type"`
	ExpiresInSecs  int     `json:"expires_in_seconds"`
	TrackingNumber *string `json:"tracking_number,omitempty"`
	Provider       *string `json:"provider,omitempty"`
}

// labelLinkTTL is how long a label download link stays valid. Short, because
// the PDF carries the recipient's full address and the link needs no auth.
const labelLinkTTL = 10 * time.Minute

// ShippingService orchestrates rates, label purchase, and tracking webhooks.
// Flow: accept order item → GetRates (stores pending shipment) → BuyLabel (updates same row).
type ShippingService struct {
	shippo      *ShippoClient
	shipments   *repository.ShipmentRepository
	idempotency *repository.IdempotencyRepository
	media       *repository.MediaRepository
	// orders resolves a cart's products to their shops when quoting delivery
	// at checkout, before any order row exists.
	orders      *repository.OrderRepository
	s3          *S3Service
	labelBucket string // S3 bucket name stored on media.media_assets for label PDFs
}

func NewShippingService(
	shippo *ShippoClient,
	shipments *repository.ShipmentRepository,
	idempotency *repository.IdempotencyRepository,
	media *repository.MediaRepository,
	orders *repository.OrderRepository,
	s3 *S3Service,
	labelBucket string,
) *ShippingService {
	if labelBucket == "" {
		labelBucket = "sendagift-labels"
	}
	return &ShippingService{
		shippo:      shippo,
		shipments:   shipments,
		idempotency: idempotency,
		media:       media,
		orders:      orders,
		s3:          s3,
		labelBucket: labelBucket,
	}
}

// CheckoutSelectedRate is the courier the customer was shown/selected at checkout
// (saved on place-order). Seller UI should highlight this; BuyLabel still needs a
// fresh rate_object_id from Rates because checkout Shippo rate ids expire.
type CheckoutSelectedRate struct {
	Provider     string `json:"provider"`
	ServiceName  string `json:"service_name"`
	Amount       int    `json:"amount"`                 // minor units (cents)
	AmountMajor  string `json:"amount_major,omitempty"` // e.g. "45.65" for display
	Currency     string `json:"currency"`
	RateObjectID string `json:"rate_object_id,omitempty"` // expired checkout id; informational only
}

// ShippingRatesResult is returned by GetRates for the seller to pick a carrier rate.
type ShippingRatesResult struct {
	ShipmentObjectID     string                `json:"shipment_object_id"`               // Shippo shipment id
	CustomsDeclarationID string                `json:"customs_declaration_id,omitempty"` // set for international
	International        bool                  `json:"international"`
	Rates                []ShippoRate          `json:"rates"`
	CheckoutSelected     *CheckoutSelectedRate `json:"checkout_selected,omitempty"`
	// Fresh Shippo rate_object_id matching checkout provider+service, when available.
	RecommendedRateObjectID string `json:"recommended_rate_object_id,omitempty"`
	// What the customer paid at checkout to deliver this shop's parcel. 0 when
	// the shop was not priced at checkout (see shop_delivery).
	CustomerDeliveryAmount int `json:"customer_delivery_amount"`
	// The checkout delivery row for this shop, or null when it was not priced.
	ShopDelivery *models.OrderShopDelivery `json:"shop_delivery"`
	Currency     string                    `json:"currency,omitempty"`
	// MustBuyCustomerCourier is true when checkout locked a courier; BuyLabel must
	// use recommended_rate_object_id (or matching provider+service). To use another
	// rate, message the customer first — the API will reject a silent change.
	MustBuyCustomerCourier bool `json:"must_buy_customer_courier"`
	// SellerDelivery is the shop's own delivery for this recipient, priced from
	// the shop's delivery zones (use .../shipping/local or .../shipping/manual).
	SellerDelivery *SellerDeliveryOption `json:"seller_delivery"`
	// CustomerSelectedMode is what the customer picked at checkout:
	// "courier", "seller_delivery", or "" when nothing was saved.
	CustomerSelectedMode string `json:"customer_selected_mode"`
	// CarrierRatesError is set when Shippo could not quote but seller delivery is available.
	CarrierRatesError string `json:"carrier_rates_error,omitempty"`
	// CombinedItemCount is how many products on this order share this one parcel.
	CombinedItemCount int `json:"combined_item_count,omitempty"`
}

// BuyLabelInput is the body for purchasing a label from a previously quoted rate.
type BuyLabelInput struct {
	RateObjectID   string `json:"rate_object_id"`
	Provider       string `json:"provider"`
	IdempotencyKey string `json:"idempotency_key"`
	// UseCustomerSelected ignores a stale/wrong rate_object_id and buys the
	// courier locked at checkout from the latest /shipping/rates response.
	// Preferred when GetRates was called more than once (Shippo rate ids change).
	UseCustomerSelected bool `json:"use_customer_selected"`
}

// ═══════════════════════════════════════════════════════════════════════════
// SELLER: GetRates — POST /sellers/me/orders/{orderID}/shops/{shopID}/shipping/rates
// Why: checkout rate ids expire; seller needs fresh Shippo rates. We also
// re-surface the customer's locked courier from pending shipment metadata.
// ═══════════════════════════════════════════════════════════════════════════

// GetRates creates a Shippo shipment (with customs if international), returns carrier rates,
// and upserts a pending row on marketplace.shipments with customs_declaration / metadata.
// Parcel size/weight comes from the request body or seller.products.parcel_* (not copied onto shipments).
func (s *ShippingService) GetRates(ctx context.Context, sellerID, orderItemID string, posted ShippingShipmentInput) (*ShippingRatesResult, error) {
	// One SQL join: order item + shop ship-from + recipient ship-to + pending
	// shipment metadata (checkout courier) + product parcel columns.
	sc, err := s.shipments.GetShippingContext(ctx, sellerID, orderItemID)
	if err != nil {
		if errors.Is(err, repository.ErrOrderNotFound) {
			return nil, ErrOrderNotFound // wrong id or not this seller's item
		}
		return nil, err
	}
	// Rates/labels only after seller accepted the line (or preparing/ready).
	if sc.FulfilmentStatus != "accepted" && sc.FulfilmentStatus != "preparing" && sc.FulfilmentStatus != "ready" {
		return nil, ErrShippingNotReady
	}
	// Every product from this shop on the order ships in one parcel, same as checkout.
	openLines, err := s.shopLinesToShip(ctx, sellerID, sc)
	if err != nil {
		return nil, err
	}

	sellerDelivery, err := s.sellerDeliveryFor(ctx, sc)
	if err != nil {
		return nil, err
	}
	shopDelivery, err := s.orders.GetShopDelivery(ctx, sc.OrderID, sc.ShopID)
	if err != nil {
		return nil, err
	}
	shopDeliveryAmount := 0
	if shopDelivery != nil {
		shopDeliveryAmount = shopDelivery.Amount
	}
	selectedMode := customerSelectedMode(sc)
	// When carriers cannot quote, still return the shop's own delivery if it reaches the recipient.
	sellerOnly := func(cause error) (*ShippingRatesResult, error) {
		if !sellerDelivery.Available {
			return nil, cause
		}
		return &ShippingRatesResult{
			Rates:                  []ShippoRate{},
			CheckoutSelected:       parseCheckoutSelected(sc.StoredMetadata),
			CustomerDeliveryAmount: shopDeliveryAmount,
			ShopDelivery:           shopDelivery,
			Currency:               sc.Currency,
			SellerDelivery:         sellerDelivery,
			CustomerSelectedMode:   selectedMode,
			CarrierRatesError:      cause.Error(),
			CombinedItemCount:      len(openLines),
		}, nil
	}

	// Guard: without an API key we cannot call Shippo at all.
	if !s.shippo.Enabled() {
		return sellerOnly(ErrShippingNotConfigured)
	}

	// Convert DB address fields into Shippo's address JSON shape.
	from, to, err := s.toShippoAddresses(sc)
	if err != nil {
		return sellerOnly(fmt.Errorf("%w: seller ship-from and recipient ship-to addresses must include name, street, city, and country (ISO2)", ErrShippingAddress))
	}
	// Different countries → customs form required by carriers/Shippo.
	international := isInternationalShipment(from.Country, to.Country)

	// Parcel priority: POST body → product.parcel_* columns.
	// Customs priority: POST body → previously stored customs on pending shipment.
	shippingIn, err := mergeShippingInput(posted, productParcelStoredJSON(sc), sc.StoredCustoms)
	if err != nil {
		return nil, ErrInvalidInput
	}

	// Domestic can fall back to a default box if nothing was set; international cannot.
	// Several products: ignore the single-item form and merge each product's parcel,
	// which is how checkout priced the delivery the customer paid.
	parcel := defaultDomesticParcel()
	if len(openLines) > 1 {
		parcel = parcelsForShopLines(openLines)
		if shippingIn.CustomsDeclaration != nil && len(shippingIn.CustomsDeclaration.Items) > 0 {
			shippingIn.CustomsDeclaration.Items[0].NetWeight = parcel.Weight
			shippingIn.CustomsDeclaration.Items[0].MassUnit = parcel.MassUnit
		}
	} else if shippingIn.Parcel != nil {
		parcel = *shippingIn.Parcel // seller override or product dims
		// The form is filled with one unit. Scale it to the line quantity unless
		// the seller already typed a different box.
		if unit := unitParcelFromContext(sc); unit != nil && parcelsMatch(parcel, *unit) {
			qty := sc.ItemQuantity
			if qty < 1 {
				qty = 1
			}
			parcel = scaleParcelForQuantity(parcel, qty)
		}
	}
	if err := validateParcelInput(&parcel, international); err != nil {
		if international {
			return nil, fmt.Errorf("%w: parcel is required for international shipments", ErrInvalidInput)
		}
	}

	var customsDeclarationID string // Shippo customs object_id attached to shipment
	var customsJSON json.RawMessage // copy stored on our pending shipment row

	// International: create Shippo customs declaration first, then attach its id to the shipment.
	if international {
		if err := validateCustomsInput(shippingIn.CustomsDeclaration, from.Country, to.Country, sc.FromName); err != nil {
			return nil, err
		}
		// POST https://api.goshippo.com/customs/declarations/
		decl, err := s.shippo.CreateCustomsDeclaration(ctx, customsToShippo(*shippingIn.CustomsDeclaration))
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrShippingProvider, err)
		}
		customsDeclarationID = decl.ObjectID
		if customsBytes, err := json.Marshal(shippingIn.CustomsDeclaration); err == nil {
			customsJSON = customsBytes // so a second rates call can omit customs body
		}
	}

	// POST https://api.goshippo.com/shipments/ → returns rates[] with NEW object_ids.
	shippoShipment, err := s.shippo.CreateShipment(ctx, from, to, parcelToShippo(parcel), customsDeclarationID)
	if err != nil {
		return sellerOnly(fmt.Errorf("%w: %v", ErrShippingProvider, err))
	}
	if len(shippoShipment.Rates) == 0 && sellerDelivery.Available {
		return sellerOnly(fmt.Errorf("%w: no carrier rates for this route", ErrShippingProvider))
	}
	if len(shippoShipment.Rates) == 0 {
		// Shippo returns 200/SUCCESS with an empty rates array (not an error)
		// when it has no carrier account able to quote this lane — most often
		// a real domestic or international route with no test-mode simulation,
		// e.g. neither address is one of Shippo's US test addresses. Shippo's
		// own per-shipment messages say why; surface them instead of only the
		// generic hint, which otherwise reads as a config problem every time.
		if detail := formatShippoMessages(shippoShipment.Messages); detail != "" && detail != "unknown error" {
			return nil, fmt.Errorf("%w: no rates returned — %s", ErrShippingProvider, detail)
		}
		return nil, fmt.Errorf("%w: no rates returned — Shippo's test carriers do not quote every lane; use one of Shippo's documented US test addresses, or connect a real carrier account for this route", ErrShippingProvider)
	}

	rates := mapShippoRates(shippoShipment.Rates)                           // normalize servicelevel.name → service_name
	checkoutSelected := parseCheckoutSelected(sc.StoredMetadata)            // courier locked at place-order (or nil)
	recommendedRateID := matchCheckoutRateObjectID(rates, checkoutSelected) // fresh id for that courier

	// Persist quote so BuyLabel can update this same pending row.
	// Keep checkout_selected so later rates calls still know what the customer picked.
	metaPayload := map[string]any{"rates": shippoShipment.Rates} // raw Shippo rates for findStoredRate later
	if checkoutSelected != nil {
		// Nested checkout_quote survives rates refresh (flat fields would be overwritten by rates JSON).
		metaPayload["checkout_quote"] = map[string]any{
			"rate_object_id": checkoutSelected.RateObjectID, // old checkout id — informational only
			"provider":       checkoutSelected.Provider,     // LOCK: carrier name
			"service_name":   checkoutSelected.ServiceName,  // LOCK: service name
			"amount":         checkoutSelected.Amount,       // what customer paid (cents)
			"currency":       checkoutSelected.Currency,
			"source":         "checkout_quote",
		}
	}
	if recommendedRateID != "" {
		metaPayload["recommended_rate_object_id"] = recommendedRateID // BuyLabel / use_customer_selected uses this
	}
	ratesMeta, _ := json.Marshal(metaPayload)
	providerShipmentID := shippoShipment.ObjectID
	quote := &models.Shipment{
		OrderID:            sc.OrderID,
		OrderItemID:        &sc.OrderItemID,
		SellerID:           sc.SellerID,
		DeliveryMode:       "courier",
		Status:             "pending", // still a quote — not bought yet
		IsInternational:    international,
		CustomsDeclaration: customsJSON,
		ProviderShipmentID: &providerShipmentID,
		ProviderMetadata:   ratesMeta,
	}
	if customsDeclarationID != "" {
		quote.ProviderCustomsID = &customsDeclarationID
	}
	// Same quote on every item in the parcel, so buying the label covers all of them.
	for _, line := range openLines {
		itemID := line.OrderItemID
		row := *quote
		row.OrderItemID = &itemID
		if err := s.shipments.UpsertQuote(ctx, &row); err != nil {
			return nil, err
		}
	}

	return &ShippingRatesResult{
		ShipmentObjectID:        shippoShipment.ObjectID,
		CustomsDeclarationID:    customsDeclarationID,
		International:           international,
		Rates:                   rates,              // full list (other couriers visible)
		CheckoutSelected:        checkoutSelected,   // highlight customer's choice
		RecommendedRateObjectID: recommendedRateID,  // only safe id to buy
		CustomerDeliveryAmount:  shopDeliveryAmount, // this shop's checkout delivery
		ShopDelivery:            shopDelivery,
		Currency:                sc.Currency,
		MustBuyCustomerCourier:  checkoutSelected != nil, // UI + BuyLabel enforce lock
		SellerDelivery:          sellerDelivery,
		CustomerSelectedMode:    selectedMode,
		CombinedItemCount:       len(openLines),
	}, nil
}

// ResolveShopParcelItem finds the order item that the order + shop shipping
// routes work from. Lines with a status listed in prefer are tried first, in
// that order; otherwise the first line is used so its status drives the error.
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
// A product still waiting for accept blocks the quote: they leave in one box.
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

func parcelsForShopLines(lines []repository.ShopOrderLine) ParcelInput {
	parcels := make([]ParcelInput, 0, len(lines))
	for _, line := range lines {
		parcels = append(parcels, parcelFromCheckoutProduct(&repository.CheckoutProduct{
			ParcelLength:       line.ParcelLength,
			ParcelWidth:        line.ParcelWidth,
			ParcelHeight:       line.ParcelHeight,
			ParcelDistanceUnit: line.ParcelDistanceUnit,
			ParcelWeight:       line.ParcelWeight,
			ParcelMassUnit:     line.ParcelMassUnit,
		}, line.Quantity))
	}
	return mergeParcels(parcels)
}

func shopLineIDs(lines []repository.ShopOrderLine) []uuid.UUID {
	ids := make([]uuid.UUID, len(lines))
	for i, line := range lines {
		ids[i] = line.OrderItemID
	}
	return ids
}

// sellerDeliveryFor prices the shop's own delivery for one order item's recipient.
func (s *ShippingService) sellerDeliveryFor(ctx context.Context, sc *repository.ShippingContext) (*SellerDeliveryOption, error) {
	zonesByShop, err := s.shipments.DeliveryZonesForShops(ctx, []uuid.UUID{sc.ShopID})
	if err != nil {
		return nil, err
	}
	return buildSellerDeliveryOption(sc.FromLat, sc.FromLng, sc.ToLat, sc.ToLng, zonesByShop[sc.ShopID], time.Now().UTC()), nil
}

// customerSelectedMode reports what the pending shipment says the customer chose at checkout.
func customerSelectedMode(sc *repository.ShippingContext) string {
	if sc.PendingDeliveryMode == nil {
		return ""
	}
	if *sc.PendingDeliveryMode == "seller_managed" {
		return SellerDeliveryModeName
	}
	if parseCheckoutSelected(sc.StoredMetadata) != nil {
		return "courier"
	}
	return ""
}

// ── Courier lock helpers (match by NAME, not expired rate_object_id) ───────

// parseCheckoutSelected reads the courier chosen at customer checkout from pending
// shipment provider_metadata (flat checkout fields or nested checkout_quote).
func parseCheckoutSelected(meta json.RawMessage) *CheckoutSelectedRate {
	if len(meta) == 0 || string(meta) == "null" || string(meta) == "{}" {
		return nil // no lock — seller may pick any rate
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(meta, &root); err != nil {
		return nil
	}
	src := root
	// After GetRates, lock lives under checkout_quote so it is not lost among rates[].
	if nested, ok := root["checkout_quote"]; ok {
		var inner map[string]json.RawMessage
		if err := json.Unmarshal(nested, &inner); err == nil {
			src = inner
		}
	}
	getStr := func(key string) string {
		raw, ok := src[key]
		if !ok {
			return ""
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return strings.TrimSpace(s)
		}
		return ""
	}
	provider := getStr("provider")
	service := getStr("service_name")
	if provider == "" && service == "" {
		return nil // metadata exists but is not a checkout quote
	}
	amount := 0
	if raw, ok := src["amount"]; ok {
		_ = json.Unmarshal(raw, &amount) // cents
	}
	return &CheckoutSelectedRate{
		Provider:     provider,
		ServiceName:  service,
		Amount:       amount,
		AmountMajor:  fmt.Sprintf("%.2f", float64(amount)/100.0), // UI display e.g. "46.33"
		Currency:     getStr("currency"),
		RateObjectID: getStr("rate_object_id"), // expired; never use for BuyLabel
	}
}

// matchCheckoutRateObjectID finds the fresh Shippo rate id whose provider+service
// match the checkout lock. That id is safe to pass to BuyLabel.
func matchCheckoutRateObjectID(rates []ShippoRate, selected *CheckoutSelectedRate) string {
	if selected == nil {
		return ""
	}
	for _, r := range rates {
		if rateMatchesCheckout(r.Provider, r.ServiceName, selected) {
			return r.ObjectID // fresh id for the SAME courier name
		}
	}
	return "" // courier not available on this rates response
}

// normalizeCourierLabel lowercases and collapses spaces so "USPS" ≈ "usps".
func normalizeCourierLabel(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}

// courierLabelsCompatible treats equal or substring names as the same courier
// (Shippo sometimes shortens or lengthens service names across calls).
func courierLabelsCompatible(a, b string) bool {
	a, b = normalizeCourierLabel(a), normalizeCourierLabel(b)
	if a == "" || b == "" {
		return a == b
	}
	if a == b {
		return true
	}
	return strings.Contains(a, b) || strings.Contains(b, a)
}

// rateMatchesCheckout is true when this rate is the customer's locked courier.
// We match NAMES only — amount can change between quote and label buy.
func rateMatchesCheckout(provider, serviceName string, selected *CheckoutSelectedRate) bool {
	if selected == nil {
		return true // no lock
	}
	prov := strings.TrimSpace(selected.Provider)
	svc := strings.TrimSpace(selected.ServiceName)
	if prov == "" && svc == "" {
		return false
	}
	gotProv := strings.TrimSpace(provider)
	gotSvc := strings.TrimSpace(serviceName)

	if prov != "" && !courierLabelsCompatible(gotProv, prov) {
		// Sometimes checkout stores the full label in service_name only.
		combined := strings.TrimSpace(gotProv + " " + gotSvc)
		if !courierLabelsCompatible(combined, prov) && !courierLabelsCompatible(gotSvc, prov) {
			return false
		}
	}
	if svc != "" {
		if courierLabelsCompatible(gotSvc, svc) {
			return true
		}
		combined := strings.TrimSpace(gotProv + " " + gotSvc)
		if courierLabelsCompatible(combined, svc) {
			return true
		}
		return false
	}
	return true
}

// formatMinorMoney formats minor units for human-readable errors (4565 → "45.65 USD").
func formatMinorMoney(amountMinor int, currency string) string {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "" {
		currency = "USD"
	}
	major := float64(amountMinor) / 100.0
	return fmt.Sprintf("%.2f %s", major, currency)
}

// findStoredRate looks up a rate_object_id inside pending provider_metadata.rates
// (Shippo raw or mapped shape). Used by BuyLabel to verify the id is from the
// latest GetRates call and to read provider+service for the courier lock check.
func findStoredRate(meta json.RawMessage, rateObjectID string) (provider, serviceName string, ok bool) {
	rateObjectID = strings.TrimSpace(rateObjectID)
	if rateObjectID == "" || len(meta) == 0 {
		return "", "", false
	}
	var root struct {
		Rates []json.RawMessage `json:"rates"` // array saved by GetRates UpsertQuote
	}
	if err := json.Unmarshal(meta, &root); err != nil || len(root.Rates) == 0 {
		return "", "", false
	}
	for _, raw := range root.Rates {
		// Prefer our mapped shape (service_name flat field).
		var mapped ShippoRate
		if err := json.Unmarshal(raw, &mapped); err == nil && strings.TrimSpace(mapped.ObjectID) == rateObjectID {
			return mapped.Provider, mapped.ServiceName, true
		}
		// Fall back to raw Shippo JSON (servicelevel.name nested).
		var shippoRaw struct {
			ObjectID     string `json:"object_id"`
			Provider     string `json:"provider"`
			ServiceLevel struct {
				Name string `json:"name"`
			} `json:"servicelevel"`
		}
		if err := json.Unmarshal(raw, &shippoRaw); err == nil && strings.TrimSpace(shippoRaw.ObjectID) == rateObjectID {
			return shippoRaw.Provider, shippoRaw.ServiceLevel.Name, true
		}
	}
	return "", "", false // id not in latest rates → stale / wrong
}

// recommendedRateIDFromMetadata returns the fresh rate id matching checkout,
// from the latest GetRates upsert (ids change every rates call).
// Prefer the precomputed recommended_rate_object_id; else scan rates[] by name.
func recommendedRateIDFromMetadata(meta json.RawMessage, selected *CheckoutSelectedRate) string {
	if len(meta) == 0 {
		return ""
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(meta, &root); err != nil {
		return ""
	}
	// Fast path: GetRates already stored the matching fresh id.
	if raw, ok := root["recommended_rate_object_id"]; ok {
		var id string
		if err := json.Unmarshal(raw, &id); err == nil && strings.TrimSpace(id) != "" {
			return strings.TrimSpace(id)
		}
	}
	// Slow path: rebuild rates list and name-match (handles older metadata).
	ratesRaw, ok := root["rates"]
	if !ok {
		return ""
	}
	var rawRates []json.RawMessage
	if err := json.Unmarshal(ratesRaw, &rawRates); err != nil {
		return ""
	}
	rates := make([]ShippoRate, 0, len(rawRates))
	for _, raw := range rawRates {
		var mapped ShippoRate
		if err := json.Unmarshal(raw, &mapped); err == nil && mapped.ObjectID != "" {
			if mapped.ServiceName == "" {
				var shippoRaw struct {
					ObjectID     string `json:"object_id"`
					Provider     string `json:"provider"`
					ServiceLevel struct {
						Name string `json:"name"`
					} `json:"servicelevel"`
				}
				if err := json.Unmarshal(raw, &shippoRaw); err == nil {
					mapped.ObjectID = shippoRaw.ObjectID
					mapped.Provider = shippoRaw.Provider
					mapped.ServiceName = shippoRaw.ServiceLevel.Name
				}
			}
			rates = append(rates, mapped)
			continue
		}
		var shippoRaw struct {
			ObjectID     string `json:"object_id"`
			Provider     string `json:"provider"`
			ServiceLevel struct {
				Name string `json:"name"`
			} `json:"servicelevel"`
		}
		if err := json.Unmarshal(raw, &shippoRaw); err == nil && shippoRaw.ObjectID != "" {
			rates = append(rates, ShippoRate{
				ObjectID: shippoRaw.ObjectID, Provider: shippoRaw.Provider, ServiceName: shippoRaw.ServiceLevel.Name,
			})
		}
	}
	return matchCheckoutRateObjectID(rates, selected)
}

// productParcelStoredJSON builds a ParcelInput JSON blob from seller.products.parcel_*
// columns so GetRates can treat the product as a stored parcel fallback (same shape as POST body).
func productParcelStoredJSON(sc *repository.ShippingContext) json.RawMessage {
	parcel := models.ProductParcelFromNullable(
		sc.ProductParcelLength, sc.ProductParcelWidth, sc.ProductParcelHeight,
		sc.ProductParcelDistanceUnit, sc.ProductParcelWeight, sc.ProductParcelMassUnit,
	)
	if parcel == nil || parcel.Length == "" || parcel.Width == "" || parcel.Height == "" || parcel.Weight == "" {
		return nil // product has no dims — GetRates may use default domestic box
	}
	in := ParcelInput{
		Length: parcel.Length, Width: parcel.Width, Height: parcel.Height,
		DistanceUnit: parcel.DistanceUnit, Weight: parcel.Weight, MassUnit: parcel.MassUnit,
	}
	if in.DistanceUnit == "" {
		in.DistanceUnit = "cm"
	}
	if in.MassUnit == "" {
		in.MassUnit = "kg"
	}
	b, err := json.Marshal(in)
	if err != nil {
		return nil
	}
	return b
}

func unitParcelFromContext(sc *repository.ShippingContext) *ParcelInput {
	raw := productParcelStoredJSON(sc)
	if len(raw) == 0 {
		return nil
	}
	var in ParcelInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil
	}
	return &in
}

func parcelsMatch(a, b ParcelInput) bool {
	eq := func(x, y string) bool {
		fx, ex := strconv.ParseFloat(strings.TrimSpace(x), 64)
		fy, ey := strconv.ParseFloat(strings.TrimSpace(y), 64)
		if ex != nil || ey != nil {
			return strings.TrimSpace(x) == strings.TrimSpace(y)
		}
		return math.Abs(fx-fy) < 0.001
	}
	return eq(a.Length, b.Length) && eq(a.Width, b.Width) && eq(a.Height, b.Height) && eq(a.Weight, b.Weight)
}

// ═══════════════════════════════════════════════════════════════════════════
// SELLER: BuyLabel — POST /sellers/me/orders/{orderID}/shops/{shopID}/shipping/labels
// Why: purchases ONE Shippo label. Checkout lock = provider+service names.
// Never buy with the checkout rate_object_id (it expires). Idempotency key
// prevents double-charge on retries. PDF is stored in S3 via media assets.
// ═══════════════════════════════════════════════════════════════════════════

// BuyLabel purchases a Shippo label for rate_object_id, uploads the PDF to S3,
// and updates the pending shipment to status=label_created.
// IdempotencyKey prevents buying twice if the seller retries the same request.
// When the customer locked a courier at checkout, rate_object_id must match that
// provider+service (use recommended_rate_object_id from GetRates).
func (s *ShippingService) BuyLabel(ctx context.Context, sellerID, orderItemID string, in BuyLabelInput) (*models.Shipment, error) {
	// Guard: Shippo API key required to create a transaction.
	if !s.shippo.Enabled() {
		return nil, ErrShippingNotConfigured
	}
	// Without this key, a network retry would create a second paid label.
	if strings.TrimSpace(in.IdempotencyKey) == "" {
		return nil, fmt.Errorf("%w: idempotency_key is required", ErrInvalidInput)
	}
	// Empty rate_object_id → buy the customer-selected courier from the latest rates call.
	if strings.TrimSpace(in.RateObjectID) == "" {
		in.UseCustomerSelected = true
	}

	// Load pending shipment metadata (checkout lock + latest rates[]).
	sc, err := s.shipments.GetShippingContext(ctx, sellerID, orderItemID)
	if err != nil {
		return nil, err
	}
	openLines, err := s.shopLinesToShip(ctx, sellerID, sc)
	if err != nil {
		return nil, err
	}

	// ── Courier lock resolution (BEFORE idempotency acquire) ──────────────
	// Validate first so a 409 mismatch never leaves a stuck in_progress key.
	if selected := parseCheckoutSelected(sc.StoredMetadata); selected != nil {
		recommendedID := recommendedRateIDFromMetadata(sc.StoredMetadata, selected) // fresh id for locked courier
		wantID := strings.TrimSpace(in.RateObjectID)
		// Auto-buy when: flag set, id omitted, OR client still sent the expired checkout id.
		autoBuy := in.UseCustomerSelected || wantID == "" ||
			(selected.RateObjectID != "" && strings.EqualFold(wantID, selected.RateObjectID))

		if autoBuy {
			if recommendedID == "" {
				// Seller must call GetRates again so we have a live matching rate id.
				return nil, fmt.Errorf(
					"%w: call /shipping/rates first, then buy the customer-selected courier %s %s (%s)",
					ErrCourierChangeRequiresChat,
					selected.Provider,
					selected.ServiceName,
					formatMinorMoney(selected.Amount, selected.Currency),
				)
			}
			in.RateObjectID = recommendedID // swap in the safe fresh id
			if strings.TrimSpace(in.Provider) == "" {
				in.Provider = selected.Provider
			}
		} else {
			// Seller explicitly picked a rate id — verify it is still in metadata AND matches lock.
			provider, serviceName, found := findStoredRate(sc.StoredMetadata, wantID)
			if !found {
				// Stale id from an older GetRates call (Shippo rotates object_ids every quote).
				return nil, fmt.Errorf(
					"%w: rate id is from an older /shipping/rates call — call rates again and buy recommended_rate_object_id, or omit rate_object_id (%s %s, %s)",
					ErrCourierChangeRequiresChat,
					selected.Provider,
					selected.ServiceName,
					formatMinorMoney(selected.Amount, selected.Currency),
				)
			}
			if !rateMatchesCheckout(provider, serviceName, selected) {
				// Different courier than customer paid for → require chat agreement (HTTP 409).
				return nil, fmt.Errorf(
					"%w: customer selected %s %s (%s) — message the customer before changing",
					ErrCourierChangeRequiresChat,
					selected.Provider,
					selected.ServiceName,
					formatMinorMoney(selected.Amount, selected.Currency),
				)
			}
			if strings.TrimSpace(in.Provider) == "" {
				in.Provider = selected.Provider
			}
		}
	} else if strings.TrimSpace(in.RateObjectID) == "" {
		// No checkout lock and no rate id → cannot know what to buy.
		return nil, fmt.Errorf("%w: rate_object_id is required (call /shipping/rates first)", ErrInvalidInput)
	}

	// Acquire after validation so a 409 courier mismatch does not lock the key.
	scope := repository.ShipmentLabelScope()
	cached, skip, err := s.idempotency.Acquire(ctx, scope, in.IdempotencyKey)
	if err != nil {
		if errors.Is(err, repository.ErrIdempotencyConflict) {
			return nil, fmt.Errorf("%w: wait a moment or use a new idempotency_key", err)
		}
		return nil, err
	}
	if skip {
		// Same key already succeeded — return cached shipment (no second Shippo charge).
		var shipment models.Shipment
		if err := json.Unmarshal(cached, &shipment); err != nil {
			return nil, err
		}
		return &shipment, nil
	}

	// Actually call Shippo + persist (separate func so Fail/Complete stay in one place).
	shipment, err := s.buyLabelAfterAcquire(ctx, sc, in, shopLineIDs(openLines))
	if err != nil {
		_ = s.idempotency.Fail(ctx, scope, in.IdempotencyKey) // release lock for retry
		return nil, err
	}
	resp, _ := json.Marshal(shipment)
	_ = s.idempotency.Complete(ctx, scope, in.IdempotencyKey, resp) // cache for identical retries
	return shipment, nil
}

// buyLabelAfterAcquire runs only after the idempotency key is owned by this request.
// Steps: Shippo transaction → download PDF → S3 media asset → CompleteLabel on DB row.
func (s *ShippingService) buyLabelAfterAcquire(ctx context.Context, sc *repository.ShippingContext, in BuyLabelInput, itemIDs []uuid.UUID) (*models.Shipment, error) {
	// POST https://api.goshippo.com/transactions/ — creates paid label for this rate id.
	txn, err := s.shippo.CreateTransaction(ctx, in.RateObjectID)
	if err != nil {
		return nil, err
	}

	// Shippo label_url is temporary; copy PDF into our S3 so LabelURL can re-sign later.
	labelKey, err := s.downloadAndStoreLabel(ctx, txn.LabelURL)
	if err != nil {
		return nil, err
	}

	// media.media_assets row points at the PDF object (used by LabelURL presign).
	asset := &models.MediaAsset{
		OwnerType:        "system",
		AssetType:        "label",
		Bucket:           s.labelBucket,
		ObjectPath:       labelKey,
		MimeType:         "application/pdf",
		ProcessingStatus: "ready",
		ModerationStatus: "approved",
	}
	if err := s.media.Create(ctx, asset); err != nil {
		return nil, err
	}

	provider := strings.TrimSpace(in.Provider)
	if provider == "" {
		provider = "courier" // generic fallback if client omitted provider
	}
	tracking := txn.TrackingNumber
	providerID := txn.ObjectID // Shippo transaction object_id
	trackingURL := txn.TrackingURLProvider
	shipment := &models.Shipment{
		CourierProvider:     &provider,
		TrackingNumber:      &tracking,
		LabelMediaID:        &asset.ID, // FK to media asset with PDF
		Status:              "label_created",
		ProviderShipmentID:  &providerID,
		ProviderTrackingURL: &trackingURL,
		ProviderMetadata:    txn.Raw, // keep raw Shippo transaction for debugging
	}
	// One label is written onto every item in the shop parcel, and each is dispatched.
	if err := s.shipments.AttachBoughtLabel(ctx, sc.OrderID, sc.SellerID, itemIDs, shipment); err != nil {
		if errors.Is(err, repository.ErrShipmentNotFound) {
			return nil, fmt.Errorf("%w: call /shipping/rates first to create a shipment quote", ErrShippingNotReady)
		}
		return nil, err
	}
	return shipment, nil
}

// ═══════════════════════════════════════════════════════════════════════════
// WEBHOOK: HandleTrackingWebhook — Shippo track_updated
// Why: keep marketplace.shipments + order_items in sync when the parcel moves.
// ═══════════════════════════════════════════════════════════════════════════

// shippoWebhookPayload is the subset of Shippo track_updated we care about.
type shippoWebhookPayload struct {
	Event string `json:"event"` // only "track_updated" is handled
	Data  struct {
		TrackingNumber string `json:"tracking_number"` // matches shipments.tracking_number
		TrackingStatus struct {
			Status     string `json:"status"`      // PRE_TRANSIT / TRANSIT / DELIVERED / ...
			StatusDate string `json:"status_date"` // used as delivered_at when DELIVERED
		} `json:"tracking_status"`
	} `json:"data"`
}

// HandleTrackingWebhook processes Shippo track_updated events and syncs shipment/order status.
func (s *ShippingService) HandleTrackingWebhook(ctx context.Context, body []byte) error {
	var payload shippoWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return ErrInvalidInput // malformed JSON — reject so Shippo may retry
	}
	if payload.Event != "track_updated" {
		return nil // ignore other event types (transaction_created, etc.)
	}
	trackingNumber := strings.TrimSpace(payload.Data.TrackingNumber)
	if trackingNumber == "" {
		return ErrInvalidInput
	}
	status := mapShippoTrackingStatus(payload.Data.TrackingStatus.Status) // our DB enum
	var deliveredAt *time.Time
	if status == "delivered" {
		if t, err := time.Parse(time.RFC3339, payload.Data.TrackingStatus.StatusDate); err == nil {
			deliveredAt = &t // stamp when the carrier marked delivered
		}
	}
	if err := s.shipments.UpdateTrackingStatus(ctx, trackingNumber, status, deliveredAt); err != nil {
		return err
	}
	if status != "delivered" {
		return nil // in_transit / label_created — order header stays open
	}

	// A delivered parcel completes one seller's line. The order header only flips
	// to delivered once every line on it is delivered or cancelled, so a two-seller
	// order is not reported complete when the first parcel arrives.
	shipment, err := s.shipments.GetByTrackingNumber(ctx, trackingNumber)
	if err != nil {
		return err
	}
	if shipment.OrderItemID != nil {
		if err := s.shipments.MarkOrderItemDelivered(ctx, *shipment.OrderItemID); err != nil {
			return err
		}
	}
	if _, err := s.shipments.MarkOrderDeliveredIfComplete(ctx, shipment.OrderID); err != nil {
		return err
	}
	return nil
}

// toShippoAddresses builds Shippo address payloads from DB ship-from / ship-to context.
func (s *ShippingService) toShippoAddresses(sc *repository.ShippingContext) (ShippoAddressInput, ShippoAddressInput, error) {
	if sc.FromStreet1 == "" || sc.FromCity == "" || sc.FromCountryISO == "" ||
		sc.ToStreet1 == "" || sc.ToCity == "" || sc.ToCountryISO == "" || sc.ToName == "" {
		return ShippoAddressInput{}, ShippoAddressInput{}, ErrShippingAddress
	}
	from := ShippoAddressInput{
		Name:    sc.FromName,
		Street1: sc.FromStreet1,
		Street2: sc.FromStreet2,
		City:    sc.FromCity,
		State:   sc.FromRegion,
		Zip:     sc.FromPostalCode,
		Country: normalizeCountryISO(sc.FromCountryISO),
		Phone:   sc.FromPhone,
		Email:   sc.FromEmail,
	}
	to := ShippoAddressInput{
		Name:          sc.ToName,
		Street1:       sc.ToStreet1,
		Street2:       sc.ToStreet2,
		City:          sc.ToCity,
		State:         sc.ToRegion,
		Zip:           sc.ToPostalCode,
		Country:       normalizeCountryISO(sc.ToCountryISO),
		Phone:         sc.ToPhone,
		Email:         sc.ToEmail,
		IsResidential: true,
	}
	return from, to, nil
}

// isInternationalShipment is true when normalized ISO2 ship-from != ship-to.
func isInternationalShipment(fromISO, toISO string) bool {
	from := normalizeCountryISO(fromISO)
	to := normalizeCountryISO(toISO)
	return from != "" && to != "" && from != to
}

// normalizeCountryISO maps common 3-letter codes to ISO2 for Shippo.
func normalizeCountryISO(iso string) string {
	iso = strings.ToUpper(strings.TrimSpace(iso))
	switch iso {
	case "USA":
		return "US"
	case "AUS":
		return "AU"
	case "GBR":
		return "GB"
	case "LKA":
		return "LK"
	default:
		return iso
	}
}

// mapShippoTrackingStatus converts Shippo tracking statuses to our shipment.status values.
func mapShippoTrackingStatus(raw string) string {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "PRE_TRANSIT":
		return "label_created"
	case "TRANSIT":
		return "in_transit"
	case "DELIVERED":
		return "delivered"
	case "FAILURE":
		return "failed"
	case "RETURNED":
		return "returned"
	default:
		return "in_transit"
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// SELLER helpers: label PDF storage, manual ship, LabelURL, local delivery
// ═══════════════════════════════════════════════════════════════════════════

// downloadAndStoreLabel fetches the label PDF from Shippo's temporary label_url
// and uploads it to our private S3 bucket. Why: Shippo links expire; we need a
// durable copy so LabelURL can re-presign for the seller later.
func (s *ShippingService) downloadAndStoreLabel(ctx context.Context, labelURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, labelURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("download label: status %d", resp.StatusCode)
	}
	// Prefix "labels/" in bucket; returns object key used as media_assets.object_path.
	return s.s3.Upload(ctx, "labels", "label.pdf", resp.Body, "application/pdf")
}

// ManualShipmentInput is the body for marking an order item shipped without a
// Shippo label — the fallback for a lane no connected carrier account quotes
// (a common case: Shippo's test carriers are all US/Canada/Europe and will
// never return a rate for, say, a domestic Sri Lanka shipment).
type ManualShipmentInput struct {
	// The seller's own courier, e.g. "Kandy Express Couriers". Required: the
	// customer needs some name to ask about, even without live tracking.
	CourierProvider string `json:"courier_provider"`
	// The seller's own tracking number or reference. Required for the same
	// reason.
	TrackingNumber string `json:"tracking_number"`
	// A tracking page URL, if the seller's courier has one. Optional.
	TrackingURL string `json:"tracking_url"`
	// Days until delivery for this courier. Optional; defaults to the matched
	// delivery zone's estimated_days when the recipient is inside a zone.
	EstimatedDays *int `json:"estimated_days"`
}

// MarkShippedManually records a seller-arranged shipment — no Shippo label,
// no carrier rate — and dispatches the order item. It exists for exactly the
// case GetRates cannot help with: a real shipment on a lane none of Shippo's
// carrier accounts serve, where waiting for a rate that will never come would
// leave the order stuck at "accepted" forever.
func (s *ShippingService) MarkShippedManually(ctx context.Context, sellerID, orderItemID string, in ManualShipmentInput) (*models.Shipment, error) {
	provider := strings.TrimSpace(in.CourierProvider)
	tracking := strings.TrimSpace(in.TrackingNumber)
	if provider == "" || tracking == "" {
		return nil, ErrInvalidInput // both required so customer has something to reference
	}

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
	if in.EstimatedDays != nil && *in.EstimatedDays < 0 {
		return nil, fmt.Errorf("%w: estimated_days must be >= 0", ErrInvalidInput)
	}
	option, err := s.sellerDeliveryFor(ctx, sc)
	if err != nil {
		return nil, err
	}

	shipment := &models.Shipment{
		OrderID:         sc.OrderID,
		OrderItemID:     &sc.OrderItemID,
		SellerID:        sc.SellerID,
		CourierProvider: &provider,
		TrackingNumber:  &tracking,
		// No carrier account, no label — the seller is fulfilling this
		// themselves, so there is nothing further for Shippo's webhook to
		// update. "in_transit" is the closest existing status to "the seller
		// has sent this and here is a number to ask about it".
		DeliveryMode: "seller_managed",
		Status:       "in_transit",
	}
	trackingURL := strings.TrimSpace(in.TrackingURL)
	if trackingURL != "" {
		shipment.ProviderTrackingURL = &trackingURL
	}
	// A courier can go beyond the zones, so distance is recorded but not enforced.
	option.applyToShipment(shipment)
	applyAgreedSellerDelivery(shipment, sc)
	if in.EstimatedDays != nil {
		setEstimatedDays(shipment, *in.EstimatedDays)
	}
	meta, _ := json.Marshal(map[string]any{"type": "manual_courier", "seller_delivery": option})
	shipment.ProviderMetadata = meta

	if err := s.shipments.DispatchSellerManagedItems(ctx, shipment, shopLineIDs(openLines)); err != nil {
		return nil, err
	}
	return shipment, nil
}

// applyAgreedSellerDelivery keeps the price/days the customer accepted at checkout
// when they chose seller delivery, so later zone edits don't change the order.
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

// LabelURL returns a temporary download link for the label PDF the seller
// already bought for this order item.
//
// Labels live in a private bucket, so they are never served by a public URL —
// the seller gets a short-lived presigned link instead, and only for an order
// item that is their own.
func (s *ShippingService) LabelURL(ctx context.Context, sellerID, orderItemID string) (*LabelLink, error) {
	// Ownership-checked: only this seller's labeled shipment for this item.
	label, err := s.shipments.GetLabelForSeller(ctx, sellerID, orderItemID)
	if err != nil {
		if errors.Is(err, repository.ErrShipmentLabelNotFound) {
			return nil, ErrShippingLabelNotFound // BuyLabel not done yet
		}
		if errors.Is(err, repository.ErrShipmentNotFound) {
			return nil, ErrShippingNotReady
		}
		return nil, err
	}

	// Short TTL so a leaked link cannot be reused indefinitely.
	url, err := s.s3.PresignGetURL(ctx, label.ObjectPath, labelLinkTTL)
	if err != nil {
		return nil, err
	}
	return &LabelLink{
		URL:            url,
		MimeType:       label.MimeType,
		ExpiresInSecs:  int(labelLinkTTL.Seconds()),
		TrackingNumber: label.TrackingNumber,
		Provider:       label.Provider,
	}, nil
}

// ═══════════════════════════════════════════════════════════════════════════
// SELLER: local delivery (hand-deliver) — no Shippo, no tracking number
// ═══════════════════════════════════════════════════════════════════════════

// LocalDeliveryInput is the optional body when the seller starts hand-delivery.
type LocalDeliveryInput struct {
	Note string `json:"note"` // Optional free text e.g. "Delivery by bike today"
}

// StartLocalDelivery is called when the seller begins delivering the item personally
// (no courier label). Creates a seller_managed shipment and marks the item dispatched.
func (s *ShippingService) StartLocalDelivery(ctx context.Context, sellerID, orderItemID string, in LocalDeliveryInput) (*models.Shipment, error) {
	// Ownership check: load order item + order only if it belongs to this seller.
	sc, err := s.shipments.GetShippingContext(ctx, sellerID, orderItemID)
	if err != nil {
		if errors.Is(err, repository.ErrOrderNotFound) {
			return nil, ErrOrderNotFound
		}
		return nil, err // any other DB error is passed up unchanged
	}

	// Only accepted/preparing/ready items can start delivery.
	// Every product from this shop on the order goes out together.
	openLines, err := s.shopLinesToShip(ctx, sellerID, sc)
	if err != nil {
		return nil, err
	}

	// Price and days come from the shop's delivery zone for this recipient's distance.
	option, err := s.sellerDeliveryFor(ctx, sc)
	if err != nil {
		return nil, err
	}
	// Hand delivery only within the zones, when both points and zones are known.
	if option.hasZoneAndPoints() && !option.Available {
		return nil, fmt.Errorf("%w: %s", ErrOutsideDeliveryZone, option.Reason)
	}

	// Label shown as the "courier" so the UI has something to display.
	provider := "Local delivery"

	// Build the new shipment row in memory.
	shipment := &models.Shipment{
		OrderID:         sc.OrderID,       // parent order
		OrderItemID:     &sc.OrderItemID,  // specific line (pointer: column is nullable)
		SellerID:        sc.SellerID,      // owner of the shipment
		CourierProvider: &provider,        // "Local delivery" instead of a carrier name
		DeliveryMode:    "seller_managed", // seller handles it (same mode as manual ship)
		Status:          "in_transit",     // on the way immediately; no label/pickup step
		// TrackingNumber left nil on purpose: local delivery has no tracking.
	}
	option.applyToShipment(shipment)
	applyAgreedSellerDelivery(shipment, sc)

	meta := map[string]any{"type": "local_delivery", "seller_delivery": option}
	if note := strings.TrimSpace(in.Note); note != "" {
		meta["note"] = note
	}
	shipment.ProviderMetadata, _ = json.Marshal(meta)

	// One transaction: drop the pending courier quote, insert this shipment,
	// and set order_items.fulfilment_status = 'dispatched' (which also stops a
	// second "start" call: the status check above will then fail).
	if err := s.shipments.DispatchSellerManagedItems(ctx, shipment, shopLineIDs(openLines)); err != nil {
		return nil, err
	}
	return shipment, nil // handler returns this as JSON 201
}

// CompleteLocalDelivery is called when the seller has handed the item to the customer.
func (s *ShippingService) CompleteLocalDelivery(ctx context.Context, sellerID, orderItemID string) (*models.Shipment, error) {
	// Same ownership lookup and error mapping as StartLocalDelivery.
	sc, err := s.shipments.GetShippingContext(ctx, sellerID, orderItemID)
	if err != nil {
		if errors.Is(err, repository.ErrOrderNotFound) {
			return nil, ErrOrderNotFound
		}
		return nil, err
	}

	// You can only complete something that was started (i.e. is dispatched).
	if sc.FulfilmentStatus != "dispatched" {
		return nil, ErrShippingNotReady
	}

	// Get the most recent shipment for this item.
	shipment, err := s.shipments.GetLatestForOrderItem(ctx, sellerID, orderItemID)
	if err != nil {
		return nil, err
	}

	// Safety: this endpoint may only complete *local* deliveries.
	// Reject courier (manual/Shippo) parcels so they cannot be marked delivered here.
	if shipment.DeliveryMode != "seller_managed" {
		return nil, ErrInvalidInput
	}

	now := time.Now().UTC() // one timestamp for DB + response

	// Every local-delivery item from this shop on the order is handed over together.
	if err := s.shipments.MarkShopLocalDelivered(ctx, sc.OrderID, sc.ShopID, sc.SellerID, now); err != nil {
		return nil, err
	}

	// Update in-memory struct so the response matches the DB without re-query.
	shipment.Status = "delivered"
	shipment.DeliveredAt = &now
	return shipment, nil
}

// ── Checkout delivery quote ───────────────────────────────────────────────

// QuoteLineInput is one cart line to be delivered.
type QuoteLineInput struct {
	ProductID string `json:"product_id"`
	Quantity  int    `json:"quantity"`
}

// DeliveryQuoteInput asks what delivery will cost for a cart, before any
// order exists.
type DeliveryQuoteInput struct {
	RecipientID string `json:"recipient_id"`
	// The date the gift should arrive. Used to pick the cheapest service that
	// still gets there in time; ignored when absent.
	DeliveryDate string           `json:"delivery_date"`
	Items        []QuoteLineInput `json:"items"`
}

// QuotedDeliveryOption is one courier choice for a shop (AliExpress-style row).
type QuotedDeliveryOption struct {
	Provider      string `json:"provider"`
	ServiceName   string `json:"service_name"`
	Amount        int    `json:"amount"`
	Currency      string `json:"currency"`
	EstimatedDays int    `json:"estimated_days"`
	// DaysAvailable mirrors estimated_days for frontend delivery pickers.
	DaysAvailable int `json:"days_available"`
	// True when this is the recommended pick for the requested delivery_date.
	Recommended      bool   `json:"recommended"`
	RateObjectID     string `json:"rate_object_id"`
	ShipmentObjectID string `json:"shipment_object_id"`
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

// QuotedShopDelivery is all courier options for one shop's parcel.
type QuotedShopDelivery struct {
	ShopID           string                 `json:"shop_id"`
	ShopName         string                 `json:"shop_name"`
	ShipmentObjectID string                 `json:"shipment_object_id"`
	Options          []QuotedDeliveryOption `json:"options"`
	// From is the shop address the parcel leaves from.
	From *QuotedPostalAddress `json:"from,omitempty"`
	// SellerDelivery is the shop's own delivery priced by distance from its delivery zones.
	SellerDelivery *SellerDeliveryOption `json:"seller_delivery"`
}

// QuotedShipment is the recommended service for one shop (compat + place-order echo).
type QuotedShipment struct {
	ShopID      string `json:"shop_id"`
	ShopName    string `json:"shop_name"`
	Mode        string `json:"mode"` // "courier" | "seller_delivery"
	Provider    string `json:"provider"`
	ServiceName string `json:"service_name"`
	// Minor units, in Currency — matches how every other amount is carried.
	Amount        int    `json:"amount"`
	Currency      string `json:"currency"`
	EstimatedDays int    `json:"estimated_days"`
	DaysAvailable int    `json:"days_available"`
	// True when nothing quoted could make the requested date, so the cheapest
	// available service was chosen instead.
	MissesDeliveryDate bool `json:"misses_delivery_date"`
	// Shippo ids echoed back on place-order so pending shipments can be saved.
	RateObjectID     string `json:"rate_object_id"`
	ShipmentObjectID string `json:"shipment_object_id"`
}

// DeliveryQuote is the whole cart's delivery cost.
type DeliveryQuote struct {
	// Shops holds AliExpress-style courier options per shop (customer picks one).
	Shops []QuotedShopDelivery `json:"shops"`
	// Shipments is the recommended option per shop (same as shops[].options where recommended).
	Shipments []QuotedShipment `json:"shipments"`
	// Sum of recommended Shipments, in Currency.
	Amount   int    `json:"amount"`
	Currency string `json:"currency"`
	// True when every shop quoted successfully. False means at least one shop
	// could not be quoted — no carrier serves that lane, or shipping is not
	// configured — and delivery for it will be arranged after the order.
	Complete bool `json:"complete"`
	// Human-readable reason when Complete is false.
	Unquoted []string `json:"unquoted,omitempty"`
}

// ═══════════════════════════════════════════════════════════════════════════
// CUSTOMER: QuoteDelivery — POST /customers/me/shipping/quote (JSON only)
// Why: show AliExpress-style courier options + totals BEFORE place-order.
// Does NOT write marketplace.shipments — place-order persists the chosen option.
// ═══════════════════════════════════════════════════════════════════════════

// QuoteDelivery prices delivery for a cart so the customer sees a real total
// before paying.
//
// One parcel per shop: a cart can span several shops, each dispatching from
// its own address. Parcel size comes from product.parcel when set, else default.
// Anything that cannot be quoted is reported rather than guessed at.
func (s *ShippingService) QuoteDelivery(ctx context.Context, customerID string, in DeliveryQuoteInput) (*DeliveryQuote, error) {
	if len(in.Items) == 0 || strings.TrimSpace(in.RecipientID) == "" {
		return nil, ErrInvalidInput // need at least one line + where to ship
	}

	// Recipient address from customer's saved recipients (ship-to).
	to, err := s.shipments.ShipToForRecipient(ctx, customerID, strings.TrimSpace(in.RecipientID))
	if err != nil {
		if errors.Is(err, repository.ErrShipmentNotFound) {
			return nil, ErrOrderNotFound
		}
		return nil, err
	}
	if to.Street1 == "" || to.City == "" || to.CountryISO == "" || to.Name == "" {
		return nil, fmt.Errorf("%w: the recipient needs a street, city and country before delivery can be priced", ErrShippingAddress)
	}

	// Group cart lines by shop — each shop is one outbound parcel / one Shippo quote.
	type shopCart struct {
		shopID  uuid.UUID
		parcels []ParcelInput // one entry per cart line (weight scaled by qty)
	}
	byShop := map[uuid.UUID]*shopCart{}
	shopIDs := []uuid.UUID{} // preserve insertion order for stable response
	for _, line := range in.Items {
		product, err := s.orders.GetCheckoutProduct(ctx, strings.TrimSpace(line.ProductID))
		if err != nil {
			return nil, ErrInvalidInput
		}
		shopID, err := uuid.Parse(product.ShopID)
		if err != nil {
			return nil, ErrInvalidInput
		}
		sc, ok := byShop[shopID]
		if !ok {
			sc = &shopCart{shopID: shopID}
			byShop[shopID] = sc
			shopIDs = append(shopIDs, shopID)
		}
		qty := line.Quantity
		if qty < 1 {
			qty = 1
		}
		// product.parcel_* × quantity (weight multiplied; dims take max later in merge).
		sc.parcels = append(sc.parcels, parcelFromCheckoutProduct(product, qty))
	}

	quote := &DeliveryQuote{
		Shops:     []QuotedShopDelivery{},
		Shipments: []QuotedShipment{}, // recommended pick per shop (used as place-order default)
		Complete:  true,               // flipped false if any shop cannot be priced
	}

	// One ship-from address per shop (seller shop profile) + each shop's delivery zones.
	froms, err := s.shipments.ShipFromForShops(ctx, shopIDs)
	if err != nil {
		return nil, err
	}
	zonesByShop, err := s.shipments.DeliveryZonesForShops(ctx, shopIDs)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()

	deliverBy := parseDeliveryDate(in.DeliveryDate) // optional customer "need by" date
	toAddr := ShippoAddressInput{
		Name: to.Name, Street1: to.Street1, Street2: to.Street2, City: to.City,
		State: to.Region, Zip: to.PostalCode, Country: normalizeCountryISO(to.CountryISO),
		Phone: to.Phone, Email: to.Email, IsResidential: true,
	}

	for _, shopID := range shopIDs {
		from, hasFrom := froms[shopID]
		sellerOpt := buildSellerDeliveryOption(from.Latitude, from.Longitude, to.Latitude, to.Longitude, zonesByShop[shopID], now)
		// Carriers can't quote this shop: fall back to the shop's own delivery when it reaches the recipient.
		sellerOnly := func(reason string) {
			quote.Shops = append(quote.Shops, QuotedShopDelivery{
				ShopID:         shopID.String(),
				ShopName:       from.Name,
				Options:        []QuotedDeliveryOption{},
				From:           quotedFromAddress(from),
				SellerDelivery: sellerOpt,
			})
			if !sellerOpt.Available {
				quote.Complete = false
				quote.Unquoted = append(quote.Unquoted, reason)
				return
			}
			quote.Shipments = append(quote.Shipments, sellerDeliveryQuotedShipment(shopID.String(), from.Name, sellerOpt))
			quote.Amount += sellerOpt.PriceAmount
			if quote.Currency == "" {
				quote.Currency = sellerOpt.Currency
			}
		}

		if !s.shippo.Enabled() {
			sellerOnly(fmt.Sprintf("%s: shipping provider not configured", from.Name))
			continue
		}
		if !hasFrom || from.Street1 == "" || from.City == "" || from.CountryISO == "" {
			sellerOnly("a shop has no dispatch address set")
			continue
		}
		fromAddr := ShippoAddressInput{
			Name: from.Name, Street1: from.Street1, Street2: from.Street2, City: from.City,
			State: from.Region, Zip: from.PostalCode, Country: normalizeCountryISO(from.CountryISO),
			Phone: from.Phone, Email: from.Email,
		}

		// Merge all lines for this shop into one ParcelInput for Shippo.
		parcel := mergeParcels(byShop[shopID].parcels)
		// POST https://api.goshippo.com/shipments/ — quote only (no customs at checkout).
		shipment, err := s.shippo.CreateShipment(ctx, fromAddr, toAddr, parcelToShippo(parcel), "")
		if err != nil || len(shipment.Rates) == 0 {
			sellerOnly(fmt.Sprintf("%s: no carrier available for this route", from.Name))
			continue
		}

		rates := mapShippoRates(shipment.Rates)
		best, missed := pickBestRate(rates, deliverBy) // cheapest that meets date (or fastest fallback)
		if best == nil {
			sellerOnly(fmt.Sprintf("%s: no usable rate", from.Name))
			continue
		}

		shopQuote := QuotedShopDelivery{
			ShopID:           shopID.String(),
			ShopName:         from.Name,
			ShipmentObjectID: shipment.ObjectID, // informational; expires — place-order stores names
			Options:          make([]QuotedDeliveryOption, 0, len(rates)),
			From:             quotedFromAddress(from),
			SellerDelivery:   sellerOpt,
		}
		for _, r := range rates {
			amount, err := rateAmountMinor(r.Amount) // Shippo "45.65" → 4565 cents
			if err != nil {
				continue
			}
			opt := QuotedDeliveryOption{
				Provider:         r.Provider,
				ServiceName:      r.ServiceName,
				Amount:           amount,
				Currency:         r.Currency,
				EstimatedDays:    r.EstimatedDays,
				DaysAvailable:    r.EstimatedDays,
				Recommended:      r.ObjectID == best.ObjectID, // UI highlight
				RateObjectID:     r.ObjectID,                  // expires; still useful until place-order soon
				ShipmentObjectID: shipment.ObjectID,
			}
			shopQuote.Options = append(shopQuote.Options, opt)
		}
		if len(shopQuote.Options) == 0 {
			sellerOnly(fmt.Sprintf("%s: unreadable rates", from.Name))
			continue
		}
		// Cheapest-first for AliExpress-style lists.
		sort.SliceStable(shopQuote.Options, func(i, j int) bool {
			if shopQuote.Options[i].Amount == shopQuote.Options[j].Amount {
				return shopQuote.Options[i].EstimatedDays < shopQuote.Options[j].EstimatedDays
			}
			return shopQuote.Options[i].Amount < shopQuote.Options[j].Amount
		})
		quote.Shops = append(quote.Shops, shopQuote)

		bestAmount, err := rateAmountMinor(best.Amount)
		if err != nil {
			quote.Complete = false
			quote.Unquoted = append(quote.Unquoted, fmt.Sprintf("%s: unreadable rate", from.Name))
			continue
		}
		// Flat "selected" list used when place-order omits per-shop shipping_quotes.
		quote.Shipments = append(quote.Shipments, QuotedShipment{
			ShopID:             shopID.String(),
			ShopName:           from.Name,
			Mode:               "courier",
			Provider:           best.Provider,
			ServiceName:        best.ServiceName,
			Amount:             bestAmount,
			Currency:           best.Currency,
			EstimatedDays:      best.EstimatedDays,
			DaysAvailable:      best.EstimatedDays,
			MissesDeliveryDate: missed, // true if we had to pick a late fallback
			RateObjectID:       best.ObjectID,
			ShipmentObjectID:   shipment.ObjectID,
		})
		quote.Amount += bestAmount // cart delivery total (cents)
		if quote.Currency == "" {
			quote.Currency = best.Currency
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

// sellerDeliveryQuotedShipment is the recommended row for a shop that only offers its own delivery.
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

// parcelFromCheckoutProduct maps product.parcel_* (+ qty) into a Shippo parcel.
// Missing product dims → defaultDomesticParcel so quote still works for simple SKUs.
func parcelFromCheckoutProduct(p *repository.CheckoutProduct, quantity int) ParcelInput {
	parcel := models.ProductParcelFromNullable(
		p.ParcelLength, p.ParcelWidth, p.ParcelHeight,
		p.ParcelDistanceUnit, p.ParcelWeight, p.ParcelMassUnit,
	)
	if parcel == nil || parcel.Length == "" || parcel.Width == "" || parcel.Height == "" || parcel.Weight == "" {
		return defaultDomesticParcel()
	}
	out := ParcelInput{
		Length: parcel.Length, Width: parcel.Width, Height: parcel.Height,
		DistanceUnit: parcel.DistanceUnit, Weight: parcel.Weight, MassUnit: parcel.MassUnit,
	}
	if out.DistanceUnit == "" {
		out.DistanceUnit = "cm"
	}
	if out.MassUnit == "" {
		out.MassUnit = "kg"
	}
	if quantity < 1 {
		quantity = 1
	}
	if quantity > 1 {
		out = scaleParcelForQuantity(out, quantity)
	}
	return out
}

// scaleParcelForQuantity turns one unit's parcel into the shipment for `quantity`.
// Weight is multiplied. The smallest side is stacked so the box grows with the units.
func scaleParcelForQuantity(in ParcelInput, quantity int) ParcelInput {
	if quantity < 2 {
		return in
	}
	if w, err := strconv.ParseFloat(strings.TrimSpace(in.Weight), 64); err == nil && w > 0 {
		in.Weight = strconv.FormatFloat(w*float64(quantity), 'f', 3, 64)
	}
	type side struct {
		value float64
		set   func(string)
	}
	sides := []side{
		{set: func(v string) { in.Length = v }},
		{set: func(v string) { in.Width = v }},
		{set: func(v string) { in.Height = v }},
	}
	raw := []string{in.Length, in.Width, in.Height}
	smallest := -1
	smallestVal := 0.0
	for i, text := range raw {
		value, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
		if err != nil || value <= 0 {
			return in
		}
		sides[i].value = value
		if smallest < 0 || value < smallestVal {
			smallest = i
			smallestVal = value
		}
	}
	sides[smallest].set(strconv.FormatFloat(smallestVal*float64(quantity), 'f', 3, 64))
	return in
}

// mergeParcels combines line parcels into one Shippo parcel: max dims, summed weight.
func mergeParcels(parcels []ParcelInput) ParcelInput {
	if len(parcels) == 0 {
		return defaultDomesticParcel()
	}
	out := parcels[0]
	sumW := 0.0
	for i, p := range parcels {
		if w, err := strconv.ParseFloat(strings.TrimSpace(p.Weight), 64); err == nil {
			sumW += w
		}
		if i == 0 {
			continue
		}
		out.Length = maxDimString(out.Length, p.Length)
		out.Width = maxDimString(out.Width, p.Width)
		out.Height = maxDimString(out.Height, p.Height)
	}
	if sumW > 0 {
		out.Weight = strconv.FormatFloat(sumW, 'f', 3, 64)
	}
	if out.DistanceUnit == "" {
		out.DistanceUnit = "cm"
	}
	if out.MassUnit == "" {
		out.MassUnit = "kg"
	}
	return out
}

func maxDimString(a, b string) string {
	fa, ea := strconv.ParseFloat(strings.TrimSpace(a), 64)
	fb, eb := strconv.ParseFloat(strings.TrimSpace(b), 64)
	if ea != nil {
		return b
	}
	if eb != nil {
		return a
	}
	if fb > fa {
		return strings.TrimSpace(b)
	}
	return strings.TrimSpace(a)
}

// parseDeliveryDate reads the requested arrival date; a zero time means the
// customer did not pin one down.
func parseDeliveryDate(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	for _, layout := range []string{"2006-01-02", time.RFC3339} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t
		}
	}
	return time.Time{}
}

// pickBestRate chooses the service the customer would pick themselves: the
// cheapest one that still arrives by the date they asked for. When nothing can
// make that date it falls back to the fastest available and says so, rather
// than quietly quoting something that will turn up late.
func pickBestRate(rates []ShippoRate, deliverBy time.Time) (*ShippoRate, bool) {
	if len(rates) == 0 {
		return nil, false
	}

	inTime := []ShippoRate{}
	if !deliverBy.IsZero() {
		// Calendar days, not elapsed hours: a date two days out is two days of
		// delivery time, even though midnight on that date is only ~1.5 days
		// away. Truncating the fraction would reject a 2-day service that
		// arrives on the morning of the date asked for, and quote an express
		// service the customer never needed.
		now := time.Now().UTC()
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		target := time.Date(deliverBy.Year(), deliverBy.Month(), deliverBy.Day(), 0, 0, 0, 0, time.UTC)
		daysAvailable := int(target.Sub(today).Hours() / 24)
		for _, rate := range rates {
			// A rate with no estimate is not evidence it will arrive in time.
			if rate.EstimatedDays > 0 && rate.EstimatedDays <= daysAvailable {
				inTime = append(inTime, rate)
			}
		}
	}

	if len(inTime) > 0 {
		best := cheapest(inTime)
		return best, false
	}
	if deliverBy.IsZero() {
		// No date asked for: cheapest overall is the sensible default.
		return cheapest(rates), false
	}
	// Nothing makes the date — the fastest is the closest we can get.
	fastest := rates[0]
	for _, rate := range rates[1:] {
		if rate.EstimatedDays > 0 && (fastest.EstimatedDays == 0 || rate.EstimatedDays < fastest.EstimatedDays) {
			fastest = rate
		}
	}
	return &fastest, true
}

func cheapest(rates []ShippoRate) *ShippoRate {
	best := rates[0]
	bestAmount, err := rateAmountMinor(best.Amount)
	if err != nil {
		bestAmount = math.MaxInt32
	}
	for _, rate := range rates[1:] {
		amount, err := rateAmountMinor(rate.Amount)
		if err != nil {
			continue
		}
		if amount < bestAmount {
			best, bestAmount = rate, amount
		}
	}
	return &best
}

// rateAmountMinor turns Shippo's decimal string ("5.68") into minor units.
func rateAmountMinor(raw string) (int, error) {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0, err
	}
	return int(math.Round(value * 100)), nil
}
