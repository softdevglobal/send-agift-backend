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

type CustomerHandler struct {
	customers *services.CustomerService
}

func NewCustomerHandler(customers *services.CustomerService) *CustomerHandler {
	return &CustomerHandler{customers: customers}
}

type customerRegisterRequest struct {
	CountryID    string                   `json:"country_id"`
	Email        string                   `json:"email"`
	Password     string                   `json:"password"`
	Phone        *string                  `json:"phone"`
	DisplayName  *string                  `json:"display_name"`
	CustomerType string                   `json:"customer_type"`
	DateOfBirth  string                   `json:"date_of_birth"`
	Addresses    []services.AddressInput  `json:"addresses"`
	ImageURL     *string                  `json:"image_url"`
}

type customerUpdateRequest struct {
	CountryID    string  `json:"country_id"`
	Phone        *string `json:"phone"`
	DisplayName  *string `json:"display_name"`
	CustomerType string  `json:"customer_type"`
	DateOfBirth  string  `json:"date_of_birth"`
	Status       string  `json:"status"`
	ImageURL     *string `json:"image_url"`
}

func (h *CustomerHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req customerRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	details, err := h.customers.Register(r.Context(), services.CustomerRegisterInput{
		CountryID:    req.CountryID,
		Email:        req.Email,
		Password:     req.Password,
		Phone:        req.Phone,
		DisplayName:  req.DisplayName,
		CustomerType: req.CustomerType,
		DateOfBirth:  req.DateOfBirth,
		Addresses:    req.Addresses,
		ImageURL:     req.ImageURL,
	})
	if err != nil {
		h.writeCustomerError(w, err, "could not register customer")
		return
	}
	utils.JSON(w, http.StatusCreated, details)
}

func (h *CustomerHandler) Me(w http.ResponseWriter, r *http.Request) {
	customerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	details, err := h.customers.GetDetails(r.Context(), customerID)
	if err != nil {
		h.writeCustomerError(w, err, "could not load customer")
		return
	}
	utils.JSON(w, http.StatusOK, details)
}

func (h *CustomerHandler) UpdateMe(w http.ResponseWriter, r *http.Request) {
	customerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	var req customerUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	customer, err := h.customers.Update(r.Context(), customerID, services.CustomerUpdateInput{
		CountryID:    req.CountryID,
		Phone:        req.Phone,
		DisplayName:  req.DisplayName,
		CustomerType: req.CustomerType,
		DateOfBirth:  req.DateOfBirth,
		Status:       req.Status,
		ImageURL:     req.ImageURL,
	})
	if err != nil {
		h.writeCustomerError(w, err, "could not update customer")
		return
	}
	utils.JSON(w, http.StatusOK, customer)
}

func (h *CustomerHandler) DeleteMe(w http.ResponseWriter, r *http.Request) {
	customerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	if err := h.customers.Delete(r.Context(), customerID); err != nil {
		h.writeCustomerError(w, err, "could not delete customer")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"message": "customer deleted"})
}

func (h *CustomerHandler) AddAddress(w http.ResponseWriter, r *http.Request) {
	customerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	var req services.AddressInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	addr, err := h.customers.AddAddress(r.Context(), customerID, req)
	if err != nil {
		h.writeCustomerError(w, err, "could not add address")
		return
	}
	utils.JSON(w, http.StatusCreated, addr)
}

func (h *CustomerHandler) DeleteAddress(w http.ResponseWriter, r *http.Request) {
	customerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	addressID := chi.URLParam(r, "id")
	if err := h.customers.DeleteAddress(r.Context(), customerID, addressID); err != nil {
		h.writeCustomerError(w, err, "could not delete address")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"message": "address deleted"})
}

type savedGiftRequest struct {
	ProductID string `json:"product_id"`
}

// AddSavedGift saves a product to the customer's wishlist.
// Body: { "product_id": "uuid" }
func (h *CustomerHandler) AddSavedGift(w http.ResponseWriter, r *http.Request) {
	customerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	var req savedGiftRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	gift, err := h.customers.AddSavedGift(r.Context(), customerID, req.ProductID)
	if err != nil {
		h.writeCustomerError(w, err, "could not save gift")
		return
	}
	utils.JSON(w, http.StatusCreated, gift)
}

// ListSavedGifts returns all saved gifts for the logged-in customer.
// No body.
func (h *CustomerHandler) ListSavedGifts(w http.ResponseWriter, r *http.Request) {
	customerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	items, err := h.customers.ListSavedGifts(r.Context(), customerID)
	if err != nil {
		h.writeCustomerError(w, err, "could not list saved gifts")
		return
	}
	utils.JSON(w, http.StatusOK, items)
}

// DeleteSavedGift removes one saved gift by id (must belong to this customer).
// No body — use URL {id}.
func (h *CustomerHandler) DeleteSavedGift(w http.ResponseWriter, r *http.Request) {
	customerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	savedGiftID := chi.URLParam(r, "id")
	if err := h.customers.DeleteSavedGift(r.Context(), customerID, savedGiftID); err != nil {
		h.writeCustomerError(w, err, "could not delete saved gift")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"message": "saved gift deleted"})
}

func (h *CustomerHandler) writeCustomerError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, services.ErrInvalidInput):
		utils.Error(w, http.StatusBadRequest, "email required, password must be at least 8 characters")
	case errors.Is(err, services.ErrInvalidCountry):
		utils.Error(w, http.StatusBadRequest, "invalid country_id")
	case errors.Is(err, services.ErrInvalidAddress):
		utils.Error(w, http.StatusBadRequest, "address requires country_id, line1, and city")
	case errors.Is(err, services.ErrCustomerConflict):
		utils.Error(w, http.StatusConflict, "email already registered")
	case errors.Is(err, services.ErrSavedGiftConflict):
		utils.Error(w, http.StatusConflict, "product already saved")
	case errors.Is(err, services.ErrSavedGiftProduct):
		utils.Error(w, http.StatusBadRequest, "invalid or missing product_id")
	case errors.Is(err, services.ErrCustomerNotFound):
		utils.Error(w, http.StatusNotFound, "customer not found")
	case errors.Is(err, services.ErrAddressNotFound):
		utils.Error(w, http.StatusNotFound, "address not found")
	case errors.Is(err, services.ErrSavedGiftNotFound):
		utils.Error(w, http.StatusNotFound, "saved gift not found")
	default:
		log.Printf("customer handler error: %v", err)
		utils.Error(w, http.StatusInternalServerError, fallback)
	}
}
