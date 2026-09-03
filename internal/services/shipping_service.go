package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"myapp/internal/models"
	"myapp/internal/repository"
)

var (
	ErrShippingNotConfigured = errors.New("shipping provider not configured")
	ErrShippingNotReady      = errors.New("order item is not ready for shipping")
	ErrShippingAddress       = errors.New("shipping addresses are incomplete")
	ErrShippingProvider      = errors.New("shipping provider error")
)

type ShippingService struct {
	shippo      *ShippoClient
	shipments   *repository.ShipmentRepository
	idempotency *repository.IdempotencyRepository
	media       *repository.MediaRepository
	s3          *S3Service
	labelBucket string
}

func NewShippingService(
	shippo *ShippoClient,
	shipments *repository.ShipmentRepository,
	idempotency *repository.IdempotencyRepository,
	media *repository.MediaRepository,
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
		s3:          s3,
		labelBucket: labelBucket,
	}
}

type ShippingRatesResult struct {
	ShipmentObjectID string       `json:"shipment_object_id"`
	Rates            []ShippoRate `json:"rates"`
}

type BuyLabelInput struct {
	RateObjectID   string `json:"rate_object_id"`
	Provider       string `json:"provider"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (s *ShippingService) GetRates(ctx context.Context, sellerID, orderItemID string) (*ShippingRatesResult, error) {
	if !s.shippo.Enabled() {
		return nil, ErrShippingNotConfigured
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
	from, to, err := s.toShippoAddresses(sc)
	if err != nil {
		return nil, fmt.Errorf("%w: seller ship-from and recipient ship-to addresses must include name, street, city, and country (ISO2)", ErrShippingAddress)
	}
	parcel := defaultParcel()
	shipment, err := s.shippo.CreateShipment(ctx, from, to, parcel)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrShippingProvider, err)
	}
	if len(shipment.Rates) == 0 {
		return nil, fmt.Errorf("%w: no rates returned — use valid US addresses in test mode and ensure the seller shop has an address linked", ErrShippingProvider)
	}
	return &ShippingRatesResult{
		ShipmentObjectID: shipment.ObjectID,
		Rates:            mapShippoRates(shipment.Rates),
	}, nil
}

func (s *ShippingService) BuyLabel(ctx context.Context, sellerID, orderItemID string, in BuyLabelInput) (*models.Shipment, error) {
	if !s.shippo.Enabled() {
		return nil, ErrShippingNotConfigured
	}
	if strings.TrimSpace(in.RateObjectID) == "" || strings.TrimSpace(in.IdempotencyKey) == "" {
		return nil, ErrInvalidInput
	}

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
		OrderID:             sc.OrderID,
		SellerID:            sc.SellerID,
		CourierProvider:     &provider,
		TrackingNumber:      &tracking,
		LabelMediaID:        &asset.ID,
		DeliveryMode:        "courier",
		Status:              "label_created",
		ProviderShipmentID:  &providerID,
		ProviderTrackingURL: &trackingURL,
		ProviderMetadata:    txn.Raw,
	}
	if err := s.shipments.Create(ctx, shipment); err != nil {
		return nil, err
	}
	if err := s.shipments.MarkOrderItemDispatched(ctx, sc.OrderItemID); err != nil {
		return nil, err
	}

	resp, _ := json.Marshal(shipment)
	_ = s.idempotency.Complete(ctx, scope, in.IdempotencyKey, resp)
	return shipment, nil
}

type shippoWebhookPayload struct {
	Event string `json:"event"`
	Data  struct {
		TrackingNumber string `json:"tracking_number"`
		TrackingStatus struct {
			Status      string `json:"status"`
			StatusDate  string `json:"status_date"`
		} `json:"tracking_status"`
	} `json:"data"`
}

func (s *ShippingService) HandleTrackingWebhook(ctx context.Context, body []byte) error {
	var payload shippoWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return ErrInvalidInput
	}
	if payload.Event != "track_updated" {
		return nil
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
	if status == "delivered" {
		shipment, err := s.shipments.GetByTrackingNumber(ctx, trackingNumber)
		if err != nil {
			return err
		}
		return s.shipments.MarkOrderDelivered(ctx, shipment.OrderID)
	}
	return nil
}

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
		Country: sc.FromCountryISO,
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
		Country:       sc.ToCountryISO,
		Phone:         sc.ToPhone,
		Email:         sc.ToEmail,
		IsResidential: true,
	}
	return from, to, nil
}

func defaultParcel() shippoParcelInput {
	return shippoParcelInput{
		Length: "20", Width: "15", Height: "10",
		DistanceUnit: "cm",
		Weight: "1.2", MassUnit: "kg",
	}
}

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
