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

type SellerHandler struct {
	sellers *services.SellerService
}

func NewSellerHandler(sellers *services.SellerService) *SellerHandler {
	return &SellerHandler{sellers: sellers}
}

type sellerRegisterRequest struct {
	CountryID   string                        `json:"country_id"`
	SellerType  string                        `json:"seller_type"`
	LegalName   string                        `json:"legal_name"`
	TradingName *string                       `json:"trading_name"`
	Email       string                        `json:"email"`
	Password    string                        `json:"password"`
	Phone       *string                       `json:"phone"`
	Addresses   []services.SellerAddressInput `json:"addresses"`
	Shop        *services.ShopInput           `json:"shop"`
}

type sellerUpdateRequest struct {
	CountryID   string  `json:"country_id"`
	SellerType  string  `json:"seller_type"`
	LegalName   string  `json:"legal_name"`
	TradingName *string `json:"trading_name"`
	Phone       *string `json:"phone"`
}

func (h *SellerHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req sellerRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	details, err := h.sellers.Register(r.Context(), services.SellerRegisterInput{
		CountryID:   req.CountryID,
		SellerType:  req.SellerType,
		LegalName:   req.LegalName,
		TradingName: req.TradingName,
		Email:       req.Email,
		Password:    req.Password,
		Phone:       req.Phone,
		Addresses:   req.Addresses,
		Shop:        req.Shop,
	})
	if err != nil {
		h.writeError(w, err, "could not register seller")
		return
	}
	utils.JSON(w, http.StatusCreated, details)
}

func (h *SellerHandler) Me(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	details, err := h.sellers.GetDetails(r.Context(), sellerID)
	if err != nil {
		h.writeError(w, err, "could not load seller")
		return
	}
	utils.JSON(w, http.StatusOK, details)
}

func (h *SellerHandler) UpdateMe(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	var req sellerUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	seller, err := h.sellers.Update(r.Context(), sellerID, services.SellerUpdateInput{
		CountryID:   req.CountryID,
		SellerType:  req.SellerType,
		LegalName:   req.LegalName,
		TradingName: req.TradingName,
		Phone:       req.Phone,
	})
	if err != nil {
		h.writeError(w, err, "could not update seller")
		return
	}
	utils.JSON(w, http.StatusOK, seller)
}

func (h *SellerHandler) DeleteMe(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	if err := h.sellers.Delete(r.Context(), sellerID); err != nil {
		h.writeError(w, err, "could not delete seller")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"message": "seller deleted"})
}

func (h *SellerHandler) AddAddress(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	var req services.SellerAddressInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	addr, err := h.sellers.AddAddress(r.Context(), sellerID, req)
	if err != nil {
		h.writeError(w, err, "could not add address")
		return
	}
	utils.JSON(w, http.StatusCreated, addr)
}

func (h *SellerHandler) DeleteAddress(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	addressID := chi.URLParam(r, "id")
	if err := h.sellers.DeleteAddress(r.Context(), sellerID, addressID); err != nil {
		h.writeError(w, err, "could not delete address")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"message": "address deleted"})
}

func (h *SellerHandler) CreateShop(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	var req services.ShopInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	shop, err := h.sellers.CreateShop(r.Context(), sellerID, req)
	if err != nil {
		h.writeError(w, err, "could not create shop")
		return
	}
	utils.JSON(w, http.StatusCreated, shop)
}

func (h *SellerHandler) UpdateShop(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	shopID := chi.URLParam(r, "id")
	var req services.ShopInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	shop, err := h.sellers.UpdateShop(r.Context(), sellerID, shopID, req)
	if err != nil {
		h.writeError(w, err, "could not update shop")
		return
	}
	utils.JSON(w, http.StatusOK, shop)
}

func (h *SellerHandler) DeleteShop(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	shopID := chi.URLParam(r, "id")
	if err := h.sellers.DeleteShop(r.Context(), sellerID, shopID); err != nil {
		h.writeError(w, err, "could not delete shop")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"message": "shop deleted"})
}

func (h *SellerHandler) writeError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, services.ErrInvalidInput):
		utils.Error(w, http.StatusBadRequest, "legal_name, email required; password must be at least 8 characters")
	case errors.Is(err, services.ErrInvalidCountry):
		utils.Error(w, http.StatusBadRequest, "invalid country_id")
	case errors.Is(err, services.ErrInvalidAddress):
		utils.Error(w, http.StatusBadRequest, "address requires country_id, line1, city; address_type must be pickup|return|both")
	case errors.Is(err, services.ErrInvalidShop):
		utils.Error(w, http.StatusBadRequest, "shop name is required")
	case errors.Is(err, services.ErrSellerConflict):
		utils.Error(w, http.StatusConflict, "email already registered")
	case errors.Is(err, services.ErrShopConflict):
		utils.Error(w, http.StatusConflict, "shop slug already exists")
	case errors.Is(err, services.ErrSellerNotFound):
		utils.Error(w, http.StatusNotFound, "seller not found")
	case errors.Is(err, services.ErrShopNotFound):
		utils.Error(w, http.StatusNotFound, "shop not found")
	case errors.Is(err, services.ErrSellerAddrNotFound):
		utils.Error(w, http.StatusNotFound, "address not found")
	default:
		log.Printf("seller handler error: %v", err)
		utils.Error(w, http.StatusInternalServerError, fallback)
	}
}
