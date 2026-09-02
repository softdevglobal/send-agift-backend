package handlers

import (
	"errors"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"

	"myapp/internal/middleware"
	"myapp/internal/models"
	"myapp/internal/services"
	"myapp/internal/utils"
)

type SellerOrderHandler struct {
	orders *services.OrderService
}

func NewSellerOrderHandler(orders *services.OrderService) *SellerOrderHandler {
	return &SellerOrderHandler{orders: orders}
}

func (h *SellerOrderHandler) ListItems(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	items, err := h.orders.ListItemsForSeller(r.Context(), sellerID)
	if err != nil {
		h.writeError(w, err, "could not list order items")
		return
	}
	if items == nil {
		items = []models.SellerOrderItemSummary{}
	}
	utils.JSON(w, http.StatusOK, items)
}

func (h *SellerOrderHandler) GetItem(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	itemID := chi.URLParam(r, "id")
	item, err := h.orders.GetItemForSeller(r.Context(), sellerID, itemID)
	if err != nil {
		h.writeError(w, err, "could not load order item")
		return
	}
	utils.JSON(w, http.StatusOK, item)
}

func (h *SellerOrderHandler) AcceptItem(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	itemID := chi.URLParam(r, "id")
	item, err := h.orders.AcceptItemForSeller(r.Context(), sellerID, itemID)
	if err != nil {
		h.writeError(w, err, "could not accept order item")
		return
	}
	utils.JSON(w, http.StatusOK, item)
}

func (h *SellerOrderHandler) writeError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, services.ErrOrderItemNotFound):
		utils.Error(w, http.StatusNotFound, "order item not found")
	case errors.Is(err, services.ErrOrderItemNotAcceptable):
		utils.Error(w, http.StatusConflict, "order item cannot be accepted in its current status")
	default:
		log.Printf("seller order handler error: %v", err)
		utils.Error(w, http.StatusInternalServerError, fallback)
	}
}
