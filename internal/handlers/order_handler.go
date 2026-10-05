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

type OrderHandler struct {
	orders *services.OrderService
}

func NewOrderHandler(orders *services.OrderService) *OrderHandler {
	return &OrderHandler{orders: orders}
}

func (h *OrderHandler) Create(w http.ResponseWriter, r *http.Request) {
	customerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	var req services.OrderCreateInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	details, err := h.orders.Create(r.Context(), customerID, req)
	if err != nil {
		h.writeOrderError(w, err, "could not create order")
		return
	}
	utils.JSON(w, http.StatusCreated, details)
}

func (h *OrderHandler) List(w http.ResponseWriter, r *http.Request) {
	customerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	items, err := h.orders.List(r.Context(), customerID)
	if err != nil {
		h.writeOrderError(w, err, "could not list orders")
		return
	}
	utils.JSON(w, http.StatusOK, items)
}

// ReceivedGifts handles GET /customers/me/received-gifts: delivered gifts
// other customers sent to this one, which they can review.
func (h *OrderHandler) ReceivedGifts(w http.ResponseWriter, r *http.Request) {
	customerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	gifts, err := h.orders.ReceivedGifts(r.Context(), customerID)
	if err != nil {
		h.writeOrderError(w, err, "could not list received gifts")
		return
	}
	utils.JSON(w, http.StatusOK, gifts)
}

func (h *OrderHandler) Get(w http.ResponseWriter, r *http.Request) {
	customerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	orderID := chi.URLParam(r, "id")
	details, err := h.orders.Get(r.Context(), customerID, orderID)
	if err != nil {
		h.writeOrderError(w, err, "could not load order")
		return
	}
	utils.JSON(w, http.StatusOK, details)
}

func (h *OrderHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	customerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	orderID := chi.URLParam(r, "id")
	details, err := h.orders.Cancel(r.Context(), customerID, orderID)
	if err != nil {
		h.writeOrderError(w, err, "could not cancel order")
		return
	}
	utils.JSON(w, http.StatusOK, details)
}

func (h *OrderHandler) writeOrderError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, services.ErrCustomerNotFound):
		utils.Error(w, http.StatusNotFound, "customer not found")
	case errors.Is(err, services.ErrRecipientNotFound):
		utils.Error(w, http.StatusNotFound, "recipient not found")
	case errors.Is(err, services.ErrOrderNotFound):
		utils.Error(w, http.StatusNotFound, "order not found")
	case errors.Is(err, services.ErrInvalidCountry):
		utils.Error(w, http.StatusBadRequest, "invalid country_id")
	case errors.Is(err, services.ErrOutsideDeliveryZone):
		utils.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, services.ErrInvalidOrder):
		utils.Error(w, http.StatusBadRequest, "items required; delivery_date YYYY-MM-DD; customer_type personal or corporate; shipping_quotes[].mode courier|seller_delivery (seller_delivery needs recipient_id)")
	case errors.Is(err, services.ErrOrderProduct):
		utils.Error(w, http.StatusBadRequest, "product not found, not published, or shop is not active")
	case errors.Is(err, services.ErrOrderProductVisibility):
		utils.Error(w, http.StatusBadRequest, "product is not available for this customer_type")
	case errors.Is(err, services.ErrOrderCurrencyMix):
		utils.Error(w, http.StatusBadRequest, "all items must use the same currency")
	case errors.Is(err, services.ErrOrderNotCancellable):
		utils.Error(w, http.StatusConflict, "order cannot be cancelled in its current status")
	case errors.Is(err, services.ErrOrderRewardSpent):
		utils.Error(w, http.StatusConflict, err.Error())
	case errors.Is(err, services.ErrGiftPoints):
		utils.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, services.ErrGiftPointsBalance):
		utils.JSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "code": "INSUFFICIENT_POINTS"})
	default:
		log.Printf("order handler error: %v", err)
		utils.Error(w, http.StatusInternalServerError, fallback)
	}
}
