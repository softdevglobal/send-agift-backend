package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"myapp/internal/models"
	"myapp/internal/repository"
)

var (
	ErrShippingNotConfigured = errors.New("shipping provider not configured")
	ErrShippingNotReady      = errors.New("order item is not ready for shipping")
	ErrShippingAddress       = errors.New("shipping addresses are incomplete")
	ErrShippingProvider      = errors.New("shipping provider error")
	ErrShippingLabelNotFound = errors.New("shipping label not found")
	// ErrCourierChangeRequiresChat: customer locked a courier at checkout; seller must
	// message the customer before buying a different carrier/service.
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
	Amount       int    `json:"amount"` // minor units (cents)
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
	// Customer-facing shipping total on the order (same figure shown at checkout).
	CustomerDeliveryAmount int    `json:"customer_delivery_amount"`
	Currency               string `json:"currency,omitempty"`
	// MustBuyCustomerCourier is true when checkout locked a courier; BuyLabel must
	// use recommended_rate_object_id (or matching provider+service). To use another
	// rate, message the customer first — the API will reject a silent change.
	MustBuyCustomerCourier bool `json:"must_buy_customer_courier"`
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

// GetRates creates a Shippo shipment (with customs if international), returns carrier rates,
// and upserts a pending row on marketplace.shipments with customs_declaration / metadata.
// Parcel size/weight comes from the request body or seller.products.parcel_* (not copied onto shipments).
func (s *ShippingService) GetRates(ctx context.Context, sellerID, orderItemID string, posted ShippingShipmentInput) (*ShippingRatesResult, error) {
	if !s.shippo.Enabled() {
		return nil, ErrShippingNotConfigured
	}

	// Load order item + seller ship-from + recipient ship-to from DB.
	sc, err := s.shipments.GetShippingContext(ctx, sellerID, orderItemID)
	if err != nil {
		if errors.Is(err, repository.ErrOrderNotFound) {
			return nil, ErrOrderNotFound
		}
		return nil, err
	}
	if sc.FulfilmentStatus != "accepted" && sc.FulfilmentStatus != "preparing" && sc.FulfilmentStatus != "ready" {
		return nil, ErrShippingNotReady
	}

	from, to, err := s.toShippoAddresses(sc)
	if err != nil {
		return nil, fmt.Errorf("%w: seller ship-from and recipient ship-to addresses must include name, street, city, and country (ISO2)", ErrShippingAddress)
	}
	international := isInternationalShipment(from.Country, to.Country)

	// Prefer posted body parcel; fall back to product.parcel. Reuse stored customs if omitted.
	shippingIn, err := mergeShippingInput(posted, productParcelStoredJSON(sc), sc.StoredCustoms)
	if err != nil {
		return nil, ErrInvalidInput
	}

	parcel := defaultDomesticParcel()
	if shippingIn.Parcel != nil {
		parcel = *shippingIn.Parcel
	}
	if err := validateParcelInput(&parcel, international); err != nil {
		if international {
			return nil, fmt.Errorf("%w: parcel is required for international shipments", ErrInvalidInput)
		}
	}

	var customsDeclarationID string
	var customsJSON json.RawMessage

	// International: create Shippo customs declaration first, then attach its id to the shipment.
	if international {
		if err := validateCustomsInput(shippingIn.CustomsDeclaration, from.Country, to.Country, sc.FromName); err != nil {
			return nil, err
		}
		decl, err := s.shippo.CreateCustomsDeclaration(ctx, customsToShippo(*shippingIn.CustomsDeclaration))
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrShippingProvider, err)
		}
		customsDeclarationID = decl.ObjectID
		if customsBytes, err := json.Marshal(shippingIn.CustomsDeclaration); err == nil {
			customsJSON = customsBytes
		}
	}

	shippoShipment, err := s.shippo.CreateShipment(ctx, from, to, parcelToShippo(parcel), customsDeclarationID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrShippingProvider, err)
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

	rates := mapShippoRates(shippoShipment.Rates)
	checkoutSelected := parseCheckoutSelected(sc.StoredMetadata)
	recommendedRateID := matchCheckoutRateObjectID(rates, checkoutSelected)

	// Persist quote so BuyLabel can update this same pending row.
	// Keep checkout_selected so later rates calls still know what the customer picked.
	metaPayload := map[string]any{"rates": shippoShipment.Rates}
	if checkoutSelected != nil {
		metaPayload["checkout_quote"] = map[string]any{
			"rate_object_id": checkoutSelected.RateObjectID,
			"provider":       checkoutSelected.Provider,
			"service_name":   checkoutSelected.ServiceName,
			"amount":         checkoutSelected.Amount,
			"currency":       checkoutSelected.Currency,
			"source":         "checkout_quote",
		}
	}
	if recommendedRateID != "" {
		metaPayload["recommended_rate_object_id"] = recommendedRateID
	}
	ratesMeta, _ := json.Marshal(metaPayload)
	providerShipmentID := shippoShipment.ObjectID
	quote := &models.Shipment{
		OrderID:            sc.OrderID,
		OrderItemID:        &sc.OrderItemID,
		SellerID:           sc.SellerID,
		DeliveryMode:       "courier",
		Status:             "pending",
		IsInternational:    international,
		CustomsDeclaration: customsJSON,
		ProviderShipmentID: &providerShipmentID,
		ProviderMetadata:   ratesMeta,
	}
	if customsDeclarationID != "" {
		quote.ProviderCustomsID = &customsDeclarationID
	}
	if err := s.shipments.UpsertQuote(ctx, quote); err != nil {
		return nil, err
	}

	return &ShippingRatesResult{
		ShipmentObjectID:        shippoShipment.ObjectID,
		CustomsDeclarationID:    customsDeclarationID,
		International:           international,
		Rates:                   rates,
		CheckoutSelected:        checkoutSelected,
		RecommendedRateObjectID: recommendedRateID,
		CustomerDeliveryAmount:  sc.DeliveryAmount,
		Currency:                sc.Currency,
		MustBuyCustomerCourier:  checkoutSelected != nil,
	}, nil
}

// parseCheckoutSelected reads the courier chosen at customer checkout from pending
// shipment provider_metadata (flat checkout fields or nested checkout_quote).
func parseCheckoutSelected(meta json.RawMessage) *CheckoutSelectedRate {
	if len(meta) == 0 || string(meta) == "null" || string(meta) == "{}" {
		return nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(meta, &root); err != nil {
		return nil
	}
	src := root
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
		return nil
	}
	amount := 0
	if raw, ok := src["amount"]; ok {
		_ = json.Unmarshal(raw, &amount)
	}
	return &CheckoutSelectedRate{
		Provider:     provider,
		ServiceName:  service,
		Amount:       amount,
		AmountMajor:  fmt.Sprintf("%.2f", float64(amount)/100.0),
		Currency:     getStr("currency"),
		RateObjectID: getStr("rate_object_id"),
	}
}

func matchCheckoutRateObjectID(rates []ShippoRate, selected *CheckoutSelectedRate) string {
	if selected == nil {
		return ""
	}
	// Prefer exact provider+service match.
	for _, r := range rates {
		if rateMatchesCheckout(r.Provider, r.ServiceName, selected) {
			return r.ObjectID
		}
	}
	return ""
}

func normalizeCourierLabel(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}

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

func rateMatchesCheckout(provider, serviceName string, selected *CheckoutSelectedRate) bool {
	if selected == nil {
		return true
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
// (Shippo raw or mapped shape).
func findStoredRate(meta json.RawMessage, rateObjectID string) (provider, serviceName string, ok bool) {
	rateObjectID = strings.TrimSpace(rateObjectID)
	if rateObjectID == "" || len(meta) == 0 {
		return "", "", false
	}
	var root struct {
		Rates []json.RawMessage `json:"rates"`
	}
	if err := json.Unmarshal(meta, &root); err != nil || len(root.Rates) == 0 {
		return "", "", false
	}
	for _, raw := range root.Rates {
		var mapped ShippoRate
		if err := json.Unmarshal(raw, &mapped); err == nil && strings.TrimSpace(mapped.ObjectID) == rateObjectID {
			return mapped.Provider, mapped.ServiceName, true
		}
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
	return "", "", false
}

// recommendedRateIDFromMetadata returns the fresh rate id matching checkout,
// from the latest GetRates upsert (ids change every rates call).
func recommendedRateIDFromMetadata(meta json.RawMessage, selected *CheckoutSelectedRate) string {
	if len(meta) == 0 {
		return ""
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(meta, &root); err != nil {
		return ""
	}
	if raw, ok := root["recommended_rate_object_id"]; ok {
		var id string
		if err := json.Unmarshal(raw, &id); err == nil && strings.TrimSpace(id) != "" {
			return strings.TrimSpace(id)
		}
	}
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

func productParcelStoredJSON(sc *repository.ShippingContext) json.RawMessage {
	parcel := models.ProductParcelFromNullable(
		sc.ProductParcelLength, sc.ProductParcelWidth, sc.ProductParcelHeight,
		sc.ProductParcelDistanceUnit, sc.ProductParcelWeight, sc.ProductParcelMassUnit,
	)
	if parcel == nil || parcel.Length == "" || parcel.Width == "" || parcel.Height == "" || parcel.Weight == "" {
		return nil
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

// BuyLabel purchases a Shippo label for rate_object_id, uploads the PDF to S3,
// and updates the pending shipment to status=label_created.
// IdempotencyKey prevents buying twice if the seller retries the same request.
// When the customer locked a courier at checkout, rate_object_id must match that
// provider+service (use recommended_rate_object_id from GetRates).
func (s *ShippingService) BuyLabel(ctx context.Context, sellerID, orderItemID string, in BuyLabelInput) (*models.Shipment, error) {
	if !s.shippo.Enabled() {
		return nil, ErrShippingNotConfigured
	}
	if strings.TrimSpace(in.IdempotencyKey) == "" {
		return nil, fmt.Errorf("%w: idempotency_key is required", ErrInvalidInput)
	}
	// Empty rate_object_id → buy the customer-selected courier from the latest rates call.
	if strings.TrimSpace(in.RateObjectID) == "" {
		in.UseCustomerSelected = true
	}

	sc, err := s.shipments.GetShippingContext(ctx, sellerID, orderItemID)
	if err != nil {
		return nil, err
	}

	if selected := parseCheckoutSelected(sc.StoredMetadata); selected != nil {
		recommendedID := recommendedRateIDFromMetadata(sc.StoredMetadata, selected)
		wantID := strings.TrimSpace(in.RateObjectID)
		autoBuy := in.UseCustomerSelected || wantID == "" ||
			(selected.RateObjectID != "" && strings.EqualFold(wantID, selected.RateObjectID))

		if autoBuy {
			if recommendedID == "" {
				return nil, fmt.Errorf(
					"%w: call /shipping/rates first, then buy the customer-selected courier %s %s (%s)",
					ErrCourierChangeRequiresChat,
					selected.Provider,
					selected.ServiceName,
					formatMinorMoney(selected.Amount, selected.Currency),
				)
			}
			in.RateObjectID = recommendedID
			if strings.TrimSpace(in.Provider) == "" {
				in.Provider = selected.Provider
			}
		} else {
			provider, serviceName, found := findStoredRate(sc.StoredMetadata, wantID)
			if !found {
				return nil, fmt.Errorf(
					"%w: rate id is from an older /shipping/rates call — call rates again and buy recommended_rate_object_id, or omit rate_object_id (%s %s, %s)",
					ErrCourierChangeRequiresChat,
					selected.Provider,
					selected.ServiceName,
					formatMinorMoney(selected.Amount, selected.Currency),
				)
			}
			if !rateMatchesCheckout(provider, serviceName, selected) {
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
		var shipment models.Shipment
		if err := json.Unmarshal(cached, &shipment); err != nil {
			return nil, err
		}
		return &shipment, nil
	}

	shipment, err := s.buyLabelAfterAcquire(ctx, sc, in)
	if err != nil {
		_ = s.idempotency.Fail(ctx, scope, in.IdempotencyKey)
		return nil, err
	}
	resp, _ := json.Marshal(shipment)
	_ = s.idempotency.Complete(ctx, scope, in.IdempotencyKey, resp)
	return shipment, nil
}

func (s *ShippingService) buyLabelAfterAcquire(ctx context.Context, sc *repository.ShippingContext, in BuyLabelInput) (*models.Shipment, error) {
	txn, err := s.shippo.CreateTransaction(ctx, in.RateObjectID)
	if err != nil {
		return nil, err
	}

	labelKey, err := s.downloadAndStoreLabel(ctx, txn.LabelURL)
	if err != nil {
		return nil, err
	}

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
		provider = "courier"
	}
	tracking := txn.TrackingNumber
	providerID := txn.ObjectID
	trackingURL := txn.TrackingURLProvider
	shipment := &models.Shipment{
		CourierProvider:     &provider,
		TrackingNumber:      &tracking,
		LabelMediaID:        &asset.ID,
		Status:              "label_created",
		ProviderShipmentID:  &providerID,
		ProviderTrackingURL: &trackingURL,
		ProviderMetadata:    txn.Raw,
	}
	if err := s.shipments.CompleteLabel(ctx, sc.OrderItemID, shipment); err != nil {
		if errors.Is(err, repository.ErrShipmentNotFound) {
			return nil, fmt.Errorf("%w: call /shipping/rates first to create a shipment quote", ErrShippingNotReady)
		}
		return nil, err
	}
	if err := s.shipments.MarkOrderItemDispatched(ctx, sc.OrderItemID); err != nil {
		return nil, err
	}
	return shipment, nil
}

// shippoWebhookPayload is the subset of Shippo track_updated we care about.
type shippoWebhookPayload struct {
	Event string `json:"event"`
	Data  struct {
		TrackingNumber string `json:"tracking_number"`
		TrackingStatus struct {
			Status     string `json:"status"`
			StatusDate string `json:"status_date"`
		} `json:"tracking_status"`
	} `json:"data"`
}

// HandleTrackingWebhook processes Shippo track_updated events and syncs shipment/order status.
func (s *ShippingService) HandleTrackingWebhook(ctx context.Context, body []byte) error {
	var payload shippoWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return ErrInvalidInput
	}
	if payload.Event != "track_updated" {
		return nil // ignore other event types
	}
	trackingNumber := strings.TrimSpace(payload.Data.TrackingNumber)
	if trackingNumber == "" {
		return ErrInvalidInput
	}
	status := mapShippoTrackingStatus(payload.Data.TrackingStatus.Status)
	var deliveredAt *time.Time
	if status == "delivered" {
		if t, err := time.Parse(time.RFC3339, payload.Data.TrackingStatus.StatusDate); err == nil {
			deliveredAt = &t
		}
	}
	if err := s.shipments.UpdateTrackingStatus(ctx, trackingNumber, status, deliveredAt); err != nil {
		return err
	}
	if status != "delivered" {
		return nil
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

// downloadAndStoreLabel fetches the label PDF from Shippo and uploads it to S3.
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
		return nil, ErrInvalidInput
	}

	sc, err := s.shipments.GetShippingContext(ctx, sellerID, orderItemID)
	if err != nil {
		if errors.Is(err, repository.ErrOrderNotFound) {
			return nil, ErrOrderNotFound
		}
		return nil, err
	}
	if sc.FulfilmentStatus != "accepted" && sc.FulfilmentStatus != "preparing" && sc.FulfilmentStatus != "ready" {
		return nil, ErrShippingNotReady
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

	if err := s.shipments.Create(ctx, shipment); err != nil {
		return nil, err
	}
	if err := s.shipments.MarkOrderItemDispatched(ctx, sc.OrderItemID); err != nil {
		return nil, err
	}
	return shipment, nil
}

// LabelURL returns a temporary download link for the label PDF the seller
// already bought for this order item.
//
// Labels live in a private bucket, so they are never served by a public URL —
// the seller gets a short-lived presigned link instead, and only for an order
// item that is their own.
func (s *ShippingService) LabelURL(ctx context.Context, sellerID, orderItemID string) (*LabelLink, error) {
	label, err := s.shipments.GetLabelForSeller(ctx, sellerID, orderItemID)
	if err != nil {
		if errors.Is(err, repository.ErrShipmentLabelNotFound) {
			return nil, ErrShippingLabelNotFound
		}
		if errors.Is(err, repository.ErrShipmentNotFound) {
			return nil, ErrShippingNotReady
		}
		return nil, err
	}

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

// Request body for start local dilicery only optional field
type LocalDeliveryInput struct {
	Note string `json:"note"` // Optional free text "Delivery by bike today"
}

// startLocalDelivery is called when the seller begins a delivery the item personaly 
func(s *ShippingService) StartLocalDelivery(ctx context.Context, sellerID, orderItemID string, in LocalDeliveryInput) (*models.Shipment, error) {
	// Load ther order item puls its order info but only if it belongs to the seller 
	// Ownership check
	sc, err := s.shipments.GetShippingContext(ctx, sellerID, orderItemID)
	if err != nil {
		if errors.Is(err, repository.ErrOrderNotFound) {
			return nil, ErrOrderNotFound
		}
		return nil, err // any other DB error is passed up unchanged
	}

	// only item the seller has accepted for delivery can start delivery
	// Allowed status : accepted, preparing, ready
	// Blocked status : dispatched, pending, cancelled
	if sc.FulfilmentStatus != "accepted" && sc.FulfilmentStatus != "preparing" && sc.FulfilmentStatus != "ready" {
		return nil, ErrShippingNotReady
	}
	
	// Label shown as the "courier" so the UI has something to display.
	provider := "Local delivery"

	// Build the new shipment row in memory.
	shipment := &models.Shipment{
		OrderID:         sc.OrderID,        // parent order
		OrderItemID:     &sc.OrderItemID,   // the specific item being delivered (pointer because the column is nullable)
		SellerID:        sc.SellerID,       // owner of the shipment
		CourierProvider: &provider,         // "Local delivery" instead of a carrier name
		DeliveryMode:    "seller_managed",  // existing enum value meaning the seller handles it
		Status:          "in_transit",      // it's on the way immediately; there is no label or pickup step
		// TrackingNumber is left nil on purpose: local delivery has no tracking.
	}

	// If the seller wrote a note, store it in the provider_metadata JSON column.
	if note := strings.TrimSpace(in.Note); note != "" { // ignore blank or whitespace-only notes
		meta, _ := json.Marshal(map[string]string{"note": note}) // produces {"note":"..."}; marshaling a string map can't fail
		shipment.ProviderMetadata = meta
	}


	// INSERT the shipment into marketplace.shipments.
	// This fills in shipment.ID, CreatedAt, etc.
	if err := s.shipments.Create(ctx, shipment); err != nil {
		return nil, err
	}

	// UPDATE order_items.fulfilment_status = 'dispatched'.
	// This is also what stops a second "start" call: the status check above
	// will now fail with ErrShippingNotReady.
	if err := s.shipments.MarkOrderItemDispatched(ctx, sc.OrderItemID); err != nil {
		return nil, err
	}
	return shipment, nil // the handler returns this as JSON with status 201
}


// CompleteLocalDelivery is called when the seller has handed the item to the customer.
func (s *ShippingService) CompleteLocalDelivery(ctx context.Context, sellerID, orderItemID string) (*models.Shipment, error) {
	// Same ownership lookup and error mapping as above.
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

	// Safety check: this endpoint may only complete *local* deliveries.
	// If the item was shipped with a courier (manual or Shippo), reject it,
	// so a seller can't mark a courier parcel as delivered through this route.
	if shipment.DeliveryMode != "seller_managed" {
		return nil, ErrInvalidInput
	}

	now := time.Now().UTC() // one timestamp, reused for the DB and the response

	// One transaction that marks the shipment, the item, and possibly the order as delivered.
	if err := s.shipments.MarkLocalDelivered(ctx, shipment.ID, sc.OrderItemID, sc.OrderID, now); err != nil {
		return nil, err
	}

	// Update the in-memory struct so the response matches the DB,
	// without querying again.
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
	RecipientID string           `json:"recipient_id"`
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

// QuotedShopDelivery is all courier options for one shop's parcel.
type QuotedShopDelivery struct {
	ShopID           string                 `json:"shop_id"`
	ShopName         string                 `json:"shop_name"`
	ShipmentObjectID string                 `json:"shipment_object_id"`
	Options          []QuotedDeliveryOption `json:"options"`
}

// QuotedShipment is the recommended service for one shop (compat + place-order echo).
type QuotedShipment struct {
	ShopID      string `json:"shop_id"`
	ShopName    string `json:"shop_name"`
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

// QuoteDelivery prices delivery for a cart so the customer sees a real total
// before paying.
//
// One parcel per shop: a cart can span several shops, each dispatching from
// its own address. Parcel size comes from product.parcel when set, else default.
// Anything that cannot be quoted is reported rather than guessed at.
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
	if to.Street1 == "" || to.City == "" || to.CountryISO == "" || to.Name == "" {
		return nil, fmt.Errorf("%w: the recipient needs a street, city and country before delivery can be priced", ErrShippingAddress)
	}

	// Group cart lines by shop and gather parcels for merging.
	type shopCart struct {
		shopID  uuid.UUID
		parcels []ParcelInput
	}
	byShop := map[uuid.UUID]*shopCart{}
	shopIDs := []uuid.UUID{}
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
		sc.parcels = append(sc.parcels, parcelFromCheckoutProduct(product, qty))
	}

	quote := &DeliveryQuote{
		Shops:     []QuotedShopDelivery{},
		Shipments: []QuotedShipment{},
		Complete:  true,
	}
	if !s.shippo.Enabled() {
		quote.Complete = false
		quote.Unquoted = append(quote.Unquoted, "shipping provider not configured")
		return quote, nil
	}

	froms, err := s.shipments.ShipFromForShops(ctx, shopIDs)
	if err != nil {
		return nil, err
	}

	deliverBy := parseDeliveryDate(in.DeliveryDate)
	toAddr := ShippoAddressInput{
		Name: to.Name, Street1: to.Street1, Street2: to.Street2, City: to.City,
		State: to.Region, Zip: to.PostalCode, Country: normalizeCountryISO(to.CountryISO),
		Phone: to.Phone, Email: to.Email, IsResidential: true,
	}

	for _, shopID := range shopIDs {
		from, ok := froms[shopID]
		if !ok || from.Street1 == "" || from.City == "" || from.CountryISO == "" {
			quote.Complete = false
			quote.Unquoted = append(quote.Unquoted, "a shop has no dispatch address set")
			continue
		}
		fromAddr := ShippoAddressInput{
			Name: from.Name, Street1: from.Street1, Street2: from.Street2, City: from.City,
			State: from.Region, Zip: from.PostalCode, Country: normalizeCountryISO(from.CountryISO),
			Phone: from.Phone, Email: from.Email,
		}

		parcel := mergeParcels(byShop[shopID].parcels)
		shipment, err := s.shippo.CreateShipment(ctx, fromAddr, toAddr, parcelToShippo(parcel), "")
		if err != nil || len(shipment.Rates) == 0 {
			quote.Complete = false
			quote.Unquoted = append(quote.Unquoted, fmt.Sprintf("%s: no carrier available for this route", from.Name))
			continue
		}

		rates := mapShippoRates(shipment.Rates)
		best, missed := pickBestRate(rates, deliverBy)
		if best == nil {
			quote.Complete = false
			quote.Unquoted = append(quote.Unquoted, fmt.Sprintf("%s: no usable rate", from.Name))
			continue
		}

		shopQuote := QuotedShopDelivery{
			ShopID:           shopID.String(),
			ShopName:         from.Name,
			ShipmentObjectID: shipment.ObjectID,
			Options:          make([]QuotedDeliveryOption, 0, len(rates)),
		}
		for _, r := range rates {
			amount, err := rateAmountMinor(r.Amount)
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
				Recommended:      r.ObjectID == best.ObjectID,
				RateObjectID:     r.ObjectID,
				ShipmentObjectID: shipment.ObjectID,
			}
			shopQuote.Options = append(shopQuote.Options, opt)
		}
		if len(shopQuote.Options) == 0 {
			quote.Complete = false
			quote.Unquoted = append(quote.Unquoted, fmt.Sprintf("%s: unreadable rates", from.Name))
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
		quote.Shipments = append(quote.Shipments, QuotedShipment{
			ShopID:             shopID.String(),
			ShopName:           from.Name,
			Provider:           best.Provider,
			ServiceName:        best.ServiceName,
			Amount:             bestAmount,
			Currency:           best.Currency,
			EstimatedDays:      best.EstimatedDays,
			DaysAvailable:      best.EstimatedDays,
			MissesDeliveryDate: missed,
			RateObjectID:       best.ObjectID,
			ShipmentObjectID:   shipment.ObjectID,
		})
		quote.Amount += bestAmount
		if quote.Currency == "" {
			quote.Currency = best.Currency
		}
	}
	return quote, nil
}

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
	if quantity > 1 {
		if w, err := strconv.ParseFloat(out.Weight, 64); err == nil {
			out.Weight = strconv.FormatFloat(w*float64(quantity), 'f', 3, 64)
		}
	}
	return out
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
