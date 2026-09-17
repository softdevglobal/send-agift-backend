package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
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

// ShippingRatesResult is returned by GetRates for the seller to pick a carrier rate.
type ShippingRatesResult struct {
	ShipmentObjectID     string       `json:"shipment_object_id"`               // Shippo shipment id
	CustomsDeclarationID string       `json:"customs_declaration_id,omitempty"` // set for international
	International        bool         `json:"international"`
	Rates                []ShippoRate `json:"rates"`
}

// BuyLabelInput is the body for purchasing a label from a previously quoted rate.
type BuyLabelInput struct {
	RateObjectID   string `json:"rate_object_id"`
	Provider       string `json:"provider"`
	IdempotencyKey string `json:"idempotency_key"`
}

// GetRates creates a Shippo shipment (with customs if international), returns carrier rates,
// and upserts a pending row on marketplace.shipments with parcel_details / customs_declaration.
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

	// Prefer posted body; reuse parcel/customs already stored on a pending shipment if omitted.
	shippingIn, err := mergeShippingInput(posted, sc.StoredParcel, sc.StoredCustoms)
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
	var parcelJSON, customsJSON json.RawMessage
	if parcelBytes, err := json.Marshal(parcel); err == nil {
		parcelJSON = parcelBytes
	}

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

	// Persist quote so BuyLabel can update this same pending row.
	ratesMeta, _ := json.Marshal(map[string]any{"rates": shippoShipment.Rates})
	providerShipmentID := shippoShipment.ObjectID
	quote := &models.Shipment{
		OrderID:            sc.OrderID,
		OrderItemID:        &sc.OrderItemID,
		SellerID:           sc.SellerID,
		DeliveryMode:       "courier",
		Status:             "pending",
		IsInternational:    international,
		ParcelDetails:      parcelJSON,
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
		ShipmentObjectID:     shippoShipment.ObjectID,
		CustomsDeclarationID: customsDeclarationID,
		International:        international,
		Rates:                mapShippoRates(shippoShipment.Rates),
	}, nil
}

// BuyLabel purchases a Shippo label for rate_object_id, uploads the PDF to S3,
// and updates the pending shipment to status=label_created.
// IdempotencyKey prevents buying twice if the seller retries the same request.
func (s *ShippingService) BuyLabel(ctx context.Context, sellerID, orderItemID string, in BuyLabelInput) (*models.Shipment, error) {
	if !s.shippo.Enabled() {
		return nil, ErrShippingNotConfigured
	}
	if strings.TrimSpace(in.RateObjectID) == "" || strings.TrimSpace(in.IdempotencyKey) == "" {
		return nil, ErrInvalidInput
	}

	// Return cached shipment if this idempotency_key was already completed.
	scope := repository.ShipmentLabelScope()
	if cached, skip, err := s.idempotency.Acquire(ctx, scope, in.IdempotencyKey); err != nil {
		return nil, err
	} else if skip {
		var shipment models.Shipment
		if err := json.Unmarshal(cached, &shipment); err != nil {
			return nil, err
		}
		return &shipment, nil
	}

	sc, err := s.shipments.GetShippingContext(ctx, sellerID, orderItemID)
	if err != nil {
		return nil, err
	}

	// Buy label from Shippo using the rate chosen by the seller.
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

	resp, _ := json.Marshal(shipment)
	_ = s.idempotency.Complete(ctx, scope, in.IdempotencyKey, resp)
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

// QuotedShipment is the chosen service for one shop's parcel.
type QuotedShipment struct {
	ShopID      string `json:"shop_id"`
	ShopName    string `json:"shop_name"`
	Provider    string `json:"provider"`
	ServiceName string `json:"service_name"`
	// Minor units, in Currency — matches how every other amount is carried.
	Amount        int    `json:"amount"`
	Currency      string `json:"currency"`
	EstimatedDays int    `json:"estimated_days"`
	// True when nothing quoted could make the requested date, so the cheapest
	// available service was chosen instead.
	MissesDeliveryDate bool `json:"misses_delivery_date"`
}

// DeliveryQuote is the whole cart's delivery cost.
type DeliveryQuote struct {
	Shipments []QuotedShipment `json:"shipments"`
	// Sum of Shipments, in Currency.
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
// its own address. Anything that cannot be quoted — Shippo off, no carrier for
// the lane, a shop with no address — is reported rather than guessed at, and
// the caller decides whether to let the order through with delivery arranged
// later.
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

	// Group the cart by shop: one quoted parcel each.
	shopOf := map[uuid.UUID]uuid.UUID{} // productID → shopID
	shopIDs := []uuid.UUID{}
	seen := map[uuid.UUID]bool{}
	for _, line := range in.Items {
		product, err := s.orders.GetCheckoutProduct(ctx, strings.TrimSpace(line.ProductID))
		if err != nil {
			return nil, ErrInvalidInput
		}
		shopID, err := uuid.Parse(product.ShopID)
		if err != nil {
			return nil, ErrInvalidInput
		}
		productID, err := uuid.Parse(product.ID)
		if err != nil {
			return nil, ErrInvalidInput
		}
		shopOf[productID] = shopID
		if !seen[shopID] {
			seen[shopID] = true
			shopIDs = append(shopIDs, shopID)
		}
	}

	quote := &DeliveryQuote{Shipments: []QuotedShipment{}, Complete: true}
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

		// No per-product dimensions exist (migration 000021 removed them), so
		// every parcel is quoted at the standard box. Real weights would need
		// that column back.
		shipment, err := s.shippo.CreateShipment(ctx, fromAddr, toAddr, parcelToShippo(defaultDomesticParcel()), "")
		if err != nil || len(shipment.Rates) == 0 {
			quote.Complete = false
			quote.Unquoted = append(quote.Unquoted, fmt.Sprintf("%s: no carrier available for this route", from.Name))
			continue
		}

		best, missed := pickBestRate(mapShippoRates(shipment.Rates), deliverBy)
		if best == nil {
			quote.Complete = false
			quote.Unquoted = append(quote.Unquoted, fmt.Sprintf("%s: no usable rate", from.Name))
			continue
		}
		amount, err := rateAmountMinor(best.Amount)
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
			Amount:             amount,
			Currency:           best.Currency,
			EstimatedDays:      best.EstimatedDays,
			MissesDeliveryDate: missed,
		})
		quote.Amount += amount
		if quote.Currency == "" {
			quote.Currency = best.Currency
		}
	}
	return quote, nil
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
