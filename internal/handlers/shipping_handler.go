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
	"myapp/internal/services"
	"myapp/internal/utils"
)

type ShippingHandler struct {
	shipping *services.ShippingService
}

func NewShippingHandler(shipping *services.ShippingService) *ShippingHandler {
	return &ShippingHandler{shipping: shipping}
}

func (h *ShippingHandler) GetRates(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	orderItemID := chi.URLParam(r, "orderItemID")
	rates, err := h.shipping.GetRates(r.Context(), sellerID, orderItemID)
	if err != nil {
		h.writeError(w, err, "could not get shipping rates")
		return
	}
	utils.JSON(w, http.StatusOK, rates)
}

type buyLabelRequest struct {
	RateObjectID   string `json:"rate_object_id"`
	Provider       string `json:"provider"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (h *ShippingHandler) BuyLabel(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	orderItemID := chi.URLParam(r, "orderItemID")
	var req buyLabelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	shipment, err := h.shipping.BuyLabel(r.Context(), sellerID, orderItemID, services.BuyLabelInput{
		RateObjectID:   req.RateObjectID,
		Provider:       req.Provider,
		IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		h.writeError(w, err, "could not buy shipping label")
		return
	}
	utils.JSON(w, http.StatusCreated, shipment)
}

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

func (h *ShippingHandler) writeError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, services.ErrShippingNotConfigured):
		utils.Error(w, http.StatusServiceUnavailable, "shipping provider not configured")
	case errors.Is(err, services.ErrShippingNotReady):
		utils.Error(w, http.StatusConflict, "order item is not ready for shipping")
	case errors.Is(err, services.ErrShippingAddress):
		utils.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, services.ErrOrderNotFound):
		utils.Error(w, http.StatusNotFound, "order item not found")
	case errors.Is(err, services.ErrShippingProvider):
		log.Printf("shipping provider error: %v", err)
		utils.Error(w, http.StatusBadGateway, strings.TrimPrefix(err.Error(), "shipping provider error: "))
	case errors.Is(err, services.ErrInvalidInput):
		utils.Error(w, http.StatusBadRequest, "invalid request")
	default:
		log.Printf("shipping handler error: %v", err)
		utils.Error(w, http.StatusInternalServerError, err.Error())
	}
}
