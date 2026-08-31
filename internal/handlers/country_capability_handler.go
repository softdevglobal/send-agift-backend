package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"myapp/internal/services"
	"myapp/internal/utils"
)

type CountryCapabilityHandler struct {
	capabilities *services.CountryCapabilityService
}

func NewCountryCapabilityHandler(capabilities *services.CountryCapabilityService) *CountryCapabilityHandler {
	return &CountryCapabilityHandler{capabilities: capabilities}
}

type countryCapabilityRequest struct {
	CustomerRegistrationEnabled  bool `json:"customer_registration_enabled"`
	SellerRegistrationEnabled    bool `json:"seller_registration_enabled"`
	SellerPayoutsEnabled         bool `json:"seller_payouts_enabled"`
	DomesticDeliveryEnabled      bool `json:"domestic_delivery_enabled"`
	InternationalDeliveryEnabled bool `json:"international_delivery_enabled"`
	MembershipsEnabled           bool `json:"memberships_enabled"`
	PointsEarningEnabled         bool `json:"points_earning_enabled"`
	PointsUsageEnabled           bool `json:"points_usage_enabled"`
	SkillCompetitionsEnabled     bool `json:"skill_competitions_enabled"`
	AppStoreAvailable            bool `json:"app_store_available"`
}

func (h *CountryCapabilityHandler) toInput(req countryCapabilityRequest) services.CountryCapabilityInput {
	return services.CountryCapabilityInput{
		CustomerRegistrationEnabled:  req.CustomerRegistrationEnabled,
		SellerRegistrationEnabled:    req.SellerRegistrationEnabled,
		SellerPayoutsEnabled:         req.SellerPayoutsEnabled,
		DomesticDeliveryEnabled:      req.DomesticDeliveryEnabled,
		InternationalDeliveryEnabled: req.InternationalDeliveryEnabled,
		MembershipsEnabled:           req.MembershipsEnabled,
		PointsEarningEnabled:         req.PointsEarningEnabled,
		PointsUsageEnabled:           req.PointsUsageEnabled,
		SkillCompetitionsEnabled:     req.SkillCompetitionsEnabled,
		AppStoreAvailable:            req.AppStoreAvailable,
	}
}

func (h *CountryCapabilityHandler) List(w http.ResponseWriter, r *http.Request) {
	items, err := h.capabilities.List(r.Context())
	if err != nil {
		utils.Error(w, http.StatusInternalServerError, "could not list country capabilities")
		return
	}
	utils.JSON(w, http.StatusOK, items)
}

func (h *CountryCapabilityHandler) GetByCountryID(w http.ResponseWriter, r *http.Request) {
	countryID := chi.URLParam(r, "id")
	item, err := h.capabilities.GetByCountryID(r.Context(), countryID)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrCountryCapabilityNotFound):
			utils.Error(w, http.StatusNotFound, "country capability not found")
		case errors.Is(err, services.ErrInvalidInput):
			utils.Error(w, http.StatusBadRequest, "invalid country id")
		case errors.Is(err, services.ErrCountryNotFound):
			utils.Error(w, http.StatusNotFound, "country not found")
		default:
			utils.Error(w, http.StatusInternalServerError, "could not get country capability")
		}
		return
	}
	utils.JSON(w, http.StatusOK, item)
}

func (h *CountryCapabilityHandler) Create(w http.ResponseWriter, r *http.Request) {
	countryID := chi.URLParam(r, "id")
	var req countryCapabilityRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	item, err := h.capabilities.Create(r.Context(), countryID, h.toInput(req))
	if err != nil {
		switch {
		case errors.Is(err, services.ErrInvalidInput):
			utils.Error(w, http.StatusBadRequest, "invalid country id")
		case errors.Is(err, services.ErrCountryNotFound):
			utils.Error(w, http.StatusNotFound, "country not found")
		case errors.Is(err, services.ErrCountryCapabilityConflict):
			utils.Error(w, http.StatusConflict, "country capability already exists for this country")
		default:
			utils.Error(w, http.StatusInternalServerError, "could not create country capability")
		}
		return
	}
	utils.JSON(w, http.StatusCreated, item)
}

func (h *CountryCapabilityHandler) Update(w http.ResponseWriter, r *http.Request) {
	countryID := chi.URLParam(r, "id")
	var req countryCapabilityRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	item, err := h.capabilities.Update(r.Context(), countryID, h.toInput(req))
	if err != nil {
		switch {
		case errors.Is(err, services.ErrCountryCapabilityNotFound):
			utils.Error(w, http.StatusNotFound, "country capability not found")
		case errors.Is(err, services.ErrInvalidInput):
			utils.Error(w, http.StatusBadRequest, "invalid country id")
		case errors.Is(err, services.ErrCountryNotFound):
			utils.Error(w, http.StatusNotFound, "country not found")
		default:
			utils.Error(w, http.StatusInternalServerError, "could not update country capability")
		}
		return
	}
	utils.JSON(w, http.StatusOK, item)
}

func (h *CountryCapabilityHandler) Delete(w http.ResponseWriter, r *http.Request) {
	countryID := chi.URLParam(r, "id")
	if err := h.capabilities.Delete(r.Context(), countryID); err != nil {
		switch {
		case errors.Is(err, services.ErrCountryCapabilityNotFound):
			utils.Error(w, http.StatusNotFound, "country capability not found")
		case errors.Is(err, services.ErrInvalidInput):
			utils.Error(w, http.StatusBadRequest, "invalid country id")
		case errors.Is(err, services.ErrCountryNotFound):
			utils.Error(w, http.StatusNotFound, "country not found")
		default:
			utils.Error(w, http.StatusInternalServerError, "could not delete country capability")
		}
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"message": "country capability deleted"})
}
