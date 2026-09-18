package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"myapp/internal/middleware"
	"myapp/internal/repository"
	"myapp/internal/services"
	"myapp/internal/utils"
)

// ShippingHandler is the HTTP layer for Shippo rates, labels, and webhooks.
// It only parses the request and maps service errors to status codes.
type ShippingHandler struct {
	shipping *services.ShippingService
}

func NewShippingHandler(shipping *services.ShippingService) *ShippingHandler {
	return &ShippingHandler{shipping: shipping}
}

// GetRates handles POST .../shipping/rates.
// Optional body: { parcel, customs_declaration }.
// Domestic may omit body; international must send both parcel and customs.
func (h *ShippingHandler) GetRates(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	orderItemID := chi.URLParam(r, "orderItemID")

	var req services.ShippingShipmentInput
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			utils.Error(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}

	rates, err := h.shipping.GetRates(r.Context(), sellerID, orderItemID, req)
	if err != nil {
		h.writeError(w, err, "could not get shipping rates")
		return
	}
	utils.JSON(w, http.StatusOK, rates)
}

// buyLabelRequest is the body for POST .../shipping/labels.
type buyLabelRequest struct {
	RateObjectID        string `json:"rate_object_id"`         // from latest rates response rates[].object_id
	Provider            string `json:"provider"`               // e.g. USPS, DHL Express
	IdempotencyKey      string `json:"idempotency_key"`        // unique per purchase; reuse returns cached shipment
	UseCustomerSelected bool   `json:"use_customer_selected"`  // buy checkout courier from latest rates (ids change each rates call)
}

// BuyLabel handles POST .../shipping/labels.
// Purchases a Shippo label for a rate returned earlier by GetRates.
func (h *ShippingHandler) BuyLabel(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	orderItemID := chi.URLParam(r, "orderItemID")

	var req buyLabelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	shipment, err := h.shipping.BuyLabel(r.Context(), sellerID, orderItemID, services.BuyLabelInput{
		RateObjectID:        req.RateObjectID,
		Provider:            req.Provider,
		IdempotencyKey:      req.IdempotencyKey,
		UseCustomerSelected: req.UseCustomerSelected,
	})
	if err != nil {
		h.writeError(w, err, "could not buy shipping label")
		return
	}
	utils.JSON(w, http.StatusCreated, shipment)
}

// MarkShippedManually handles POST .../shipping/manual.
// Fallback for a lane Shippo's connected carriers cannot quote: the seller
// records their own courier and tracking number, and the order item moves
// straight to dispatched — no label, no rate.
func (h *ShippingHandler) MarkShippedManually(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	orderItemID := chi.URLParam(r, "orderItemID")

	var req services.ManualShipmentInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	shipment, err := h.shipping.MarkShippedManually(r.Context(), sellerID, orderItemID, req)
	if err != nil {
		h.writeError(w, err, "could not record the shipment")
		return
	}
	utils.JSON(w, http.StatusCreated, shipment)
}

// LabelURL handles GET /sellers/me/order-items/{orderItemID}/shipping/label.
// Returns a short-lived download link for the label PDF already bought.
func (h *ShippingHandler) LabelURL(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	orderItemID := chi.URLParam(r, "orderItemID")

	link, err := h.shipping.LabelURL(r.Context(), sellerID, orderItemID)
	if err != nil {
		h.writeError(w, err, "could not get the shipping label")
		return
	}
	utils.JSON(w, http.StatusOK, link)
}

// QuoteDelivery handles POST /customers/me/shipping/quote.
// Prices delivery for a cart so checkout can show a real total before paying.
func (h *ShippingHandler) QuoteDelivery(w http.ResponseWriter, r *http.Request) {
	customerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)

	var req services.DeliveryQuoteInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	quote, err := h.shipping.QuoteDelivery(r.Context(), customerID, req)
	if err != nil {
		h.writeError(w, err, "could not price delivery")
		return
	}
	utils.JSON(w, http.StatusOK, quote)
}

// ShippoWebhook handles POST /webhooks/shippo/tracking.
// Updates marketplace.shipments tracking status; marks order delivered when applicable.
func (h *ShippingHandler) ShippoWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.shipping.HandleTrackingWebhook(r.Context(), body); err != nil {
		h.writeError(w, err, "could not process webhook")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// POST /sellers/me/order-items/{orderItemID}/shipping/local
func (h *ShippingHandler) StartLocalDelivery(w http.ResponseWriter, r *http.Request) {
	// The seller's ID is placed in the context by the auth middleware (from the JWT).
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)

	// Read {orderItemID} from the URL path.
	orderItemID := chi.URLParam(r, "orderItemID")

	var req services.LocalDeliveryInput
	// Decode the optional body. The error is ignored on purpose, so an empty
	// body still works (Note just stays "").
	// Side effect: malformed JSON is also silently ignored.
	_ = json.NewDecoder(r.Body).Decode(&req)

	shipment, err := h.shipping.StartLocalDelivery(r.Context(), sellerID, orderItemID, req)
	if err != nil {
		// Your existing helper maps service errors to status codes
		// (e.g. ErrOrderNotFound → 404, ErrShippingNotReady → 409/400).
		h.writeError(w, err, "could not start local delivery")
		return
	}

	utils.JSON(w, http.StatusCreated, shipment) // 201, because a new shipment was created
}

// POST /sellers/me/order-items/{orderItemID}/shipping/local/delivered
func (h *ShippingHandler) CompleteLocalDelivery(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string) // from the JWT
	orderItemID := chi.URLParam(r, "orderItemID")                         // from the URL
	// No body to read for this endpoint.

	shipment, err := h.shipping.CompleteLocalDelivery(r.Context(), sellerID, orderItemID)
	if err != nil {
		h.writeError(w, err, "could not complete local delivery")
		return
	}

	utils.JSON(w, http.StatusOK, shipment) // 200, because an existing shipment was updated
}

// writeError maps known shipping service errors to HTTP status codes.
func (h *ShippingHandler) writeError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, services.ErrShippingNotConfigured):
		utils.Error(w, http.StatusServiceUnavailable, "shipping provider not configured")
	case errors.Is(err, services.ErrShippingNotReady):
		utils.Error(w, http.StatusConflict, "order item is not ready for shipping")
	case errors.Is(err, services.ErrShippingLabelNotFound):
		utils.Error(w, http.StatusNotFound, "no shipping label has been bought for this order item")
	case errors.Is(err, services.ErrShippingAddress):
		utils.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, services.ErrOrderNotFound):
		utils.Error(w, http.StatusNotFound, "order item not found")
	case errors.Is(err, services.ErrShippingProvider):
		log.Printf("shipping provider error: %v", err)
		utils.Error(w, http.StatusBadGateway, strings.TrimPrefix(err.Error(), "shipping provider error: "))
	case errors.Is(err, services.ErrShippingCustomsRequired):
		utils.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, services.ErrCourierChangeRequiresChat):
		utils.Error(w, http.StatusConflict, err.Error())
	case errors.Is(err, repository.ErrIdempotencyConflict):
		utils.Error(w, http.StatusConflict, err.Error())
	case errors.Is(err, services.ErrInvalidInput):
		msg := err.Error()
		if msg == "" || msg == services.ErrInvalidInput.Error() {
			msg = "invalid request"
		}
		utils.Error(w, http.StatusBadRequest, msg)
	default:
		log.Printf("shipping handler error: %v", err)
		utils.Error(w, http.StatusInternalServerError, err.Error())
	}
}
