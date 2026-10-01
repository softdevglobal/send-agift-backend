package handlers

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"

	"myapp/internal/middleware"
	"myapp/internal/services"
	"myapp/internal/utils"
)

// ShippingHandler prices delivery zones at checkout and records shop hand-over.
type ShippingHandler struct {
	shipping *services.ShippingService
}

func NewShippingHandler(shipping *services.ShippingService) *ShippingHandler {
	return &ShippingHandler{shipping: shipping}
}

var (
	shipOpenStatuses     = []string{"accepted", "preparing", "ready"}
	shipDispatchedStatus = []string{"dispatched"}
)

func (h *ShippingHandler) parcelOrderItemID(r *http.Request, sellerID string, prefer []string) (string, error) {
	return h.shipping.ResolveShopParcelItem(
		r.Context(), sellerID, chi.URLParam(r, "orderID"), chi.URLParam(r, "shopID"), prefer...,
	)
}

// QuoteDelivery handles POST /customers/me/shipping/quote.
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

// StartLocalDelivery handles POST .../shipping/local.
func (h *ShippingHandler) StartLocalDelivery(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	orderItemID, err := h.parcelOrderItemID(r, sellerID, shipOpenStatuses)
	if err != nil {
		h.writeError(w, err, "could not start local delivery")
		return
	}

	var req services.LocalDeliveryInput
	_ = json.NewDecoder(r.Body).Decode(&req)

	shipment, err := h.shipping.StartLocalDelivery(r.Context(), sellerID, orderItemID, req)
	if err != nil {
		h.writeError(w, err, "could not start local delivery")
		return
	}
	utils.JSON(w, http.StatusCreated, shipment)
}

// CompleteLocalDelivery handles POST .../shipping/local/delivered.
func (h *ShippingHandler) CompleteLocalDelivery(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	orderItemID, err := h.parcelOrderItemID(r, sellerID, shipDispatchedStatus)
	if err != nil {
		h.writeError(w, err, "could not complete local delivery")
		return
	}

	shipment, err := h.shipping.CompleteLocalDelivery(r.Context(), sellerID, orderItemID)
	if err != nil {
		h.writeError(w, err, "could not complete local delivery")
		return
	}
	utils.JSON(w, http.StatusOK, shipment)
}

func (h *ShippingHandler) writeError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, services.ErrShippingNotReady):
		msg := err.Error()
		if msg == "" || msg == services.ErrShippingNotReady.Error() {
			msg = "order item is not ready for shipping"
		}
		utils.Error(w, http.StatusConflict, msg)
	case errors.Is(err, services.ErrShippingAddress):
		utils.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, services.ErrOrderNotFound):
		utils.Error(w, http.StatusNotFound, "order item not found")
	case errors.Is(err, services.ErrOutsideDeliveryZone):
		utils.Error(w, http.StatusConflict, err.Error())
	case errors.Is(err, services.ErrInvalidInput):
		msg := err.Error()
		if msg == "" || msg == services.ErrInvalidInput.Error() {
			msg = "invalid request"
		}
		utils.Error(w, http.StatusBadRequest, msg)
	default:
		log.Printf("shipping handler error: %v", err)
		utils.Error(w, http.StatusInternalServerError, fallback)
	}
}
