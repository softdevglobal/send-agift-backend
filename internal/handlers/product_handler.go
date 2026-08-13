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

type ProductHandler struct {
	products *services.ProductService
}

func NewProductHandler(products *services.ProductService) *ProductHandler {
	return &ProductHandler{products: products}
}

func (h *ProductHandler) ListByShop(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	shopID := chi.URLParam(r, "shopID")
	items, err := h.products.ListByShop(r.Context(), sellerID, shopID)
	if err != nil {
		h.writeError(w, err, "could not list products")
		return
	}
	utils.JSON(w, http.StatusOK, items)
}

func (h *ProductHandler) Create(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	shopID := chi.URLParam(r, "shopID")
	var req services.ProductInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	details, err := h.products.Create(r.Context(), sellerID, shopID, req)
	if err != nil {
		h.writeError(w, err, "could not create product")
		return
	}
	utils.JSON(w, http.StatusCreated, details)
}

func (h *ProductHandler) Get(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	productID := chi.URLParam(r, "id")
	details, err := h.products.Get(r.Context(), sellerID, productID)
	if err != nil {
		h.writeError(w, err, "could not get product")
		return
	}
	utils.JSON(w, http.StatusOK, details)
}

func (h *ProductHandler) Update(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	productID := chi.URLParam(r, "id")
	var req services.ProductInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	product, err := h.products.Update(r.Context(), sellerID, productID, req)
	if err != nil {
		h.writeError(w, err, "could not update product")
		return
	}
	utils.JSON(w, http.StatusOK, product)
}

func (h *ProductHandler) Delete(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	productID := chi.URLParam(r, "id")
	if err := h.products.Delete(r.Context(), sellerID, productID); err != nil {
		h.writeError(w, err, "could not delete product")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"message": "product deleted"})
}

func (h *ProductHandler) GetInventory(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	productID := chi.URLParam(r, "id")
	inv, err := h.products.GetInventory(r.Context(), sellerID, productID)
	if err != nil {
		h.writeError(w, err, "could not get inventory")
		return
	}
	utils.JSON(w, http.StatusOK, inv)
}

func (h *ProductHandler) UpdateInventory(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	productID := chi.URLParam(r, "id")
	var req services.InventoryInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	inv, err := h.products.UpdateInventory(r.Context(), sellerID, productID, req)
	if err != nil {
		h.writeError(w, err, "could not update inventory")
		return
	}
	utils.JSON(w, http.StatusOK, inv)
}

func (h *ProductHandler) writeError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, services.ErrInvalidProduct):
		utils.Error(w, http.StatusBadRequest, "name, currency required; status must be draft|published|paused|rejected; visibility personal|corporate|both; amounts >= 0")
	case errors.Is(err, services.ErrInvalidCurrency):
		utils.Error(w, http.StatusBadRequest, "currency must be a known ISO currency code")
	case errors.Is(err, services.ErrInvalidInventory):
		utils.Error(w, http.StatusBadRequest, "qty fields must be >= 0; unavailable_dates must be YYYY-MM-DD")
	case errors.Is(err, services.ErrProductConflict):
		utils.Error(w, http.StatusConflict, "product slug already exists for this shop")
	case errors.Is(err, services.ErrShopNotFound):
		utils.Error(w, http.StatusNotFound, "shop not found")
	case errors.Is(err, services.ErrProductNotFound):
		utils.Error(w, http.StatusNotFound, "product not found")
	case errors.Is(err, services.ErrInventoryNotFound):
		utils.Error(w, http.StatusNotFound, "inventory not found")
	default:
		log.Printf("product handler error: %v", err)
		utils.Error(w, http.StatusInternalServerError, fallback)
	}
}
