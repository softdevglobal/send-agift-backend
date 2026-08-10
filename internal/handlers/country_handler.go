package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"myapp/internal/services"
	"myapp/internal/utils"
)

type CountryHandler struct {
	countries *services.CountryService
}

func NewCountryHandler(countries *services.CountryService) *CountryHandler {
	return &CountryHandler{countries: countries}
}

type countryRequest struct {
	ISOCode         string `json:"iso_code"`
	Name            string `json:"name"`
	DefaultCurrency string `json:"default_currency"`
	DefaultTimezone string `json:"default_timezone"`
	Status          string `json:"status"`
}

func (h *CountryHandler) List(w http.ResponseWriter, r *http.Request) {
	items, err := h.countries.List(r.Context())
	if err != nil {
		utils.Error(w, http.StatusInternalServerError, "could not list countries")
		return
	}
	utils.JSON(w, http.StatusOK, items)
}

func (h *CountryHandler) GetByID(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	item, err := h.countries.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, services.ErrCountryNotFound) {
			utils.Error(w, http.StatusNotFound, "country not found")
			return
		}
		utils.Error(w, http.StatusInternalServerError, "could not get country")
		return
	}
	utils.JSON(w, http.StatusOK, item)
}

func (h *CountryHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req countryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	item, err := h.countries.Create(r.Context(), services.CountryInput{
		ISOCode:         req.ISOCode,
		Name:            req.Name,
		DefaultCurrency: req.DefaultCurrency,
		DefaultTimezone: req.DefaultTimezone,
		Status:          req.Status,
	})
	if err != nil {
		switch {
		case errors.Is(err, services.ErrInvalidInput):
			utils.Error(w, http.StatusBadRequest, "iso_code, name, default_currency, and default_timezone are required")
		case errors.Is(err, services.ErrInvalidISOCode):
			utils.Error(w, http.StatusBadRequest, "iso_code must be a 2-letter country code")
		case errors.Is(err, services.ErrInvalidCurrency):
			utils.Error(w, http.StatusBadRequest, "default_currency must be a known ISO currency code")
		case errors.Is(err, services.ErrCountryConflict):
			utils.Error(w, http.StatusConflict, "iso_code already exists")
		default:
			utils.Error(w, http.StatusInternalServerError, "could not create country")
		}
		return
	}
	utils.JSON(w, http.StatusCreated, item)
}

func (h *CountryHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req countryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	item, err := h.countries.Update(r.Context(), id, services.CountryInput{
		ISOCode:         req.ISOCode,
		Name:            req.Name,
		DefaultCurrency: req.DefaultCurrency,
		DefaultTimezone: req.DefaultTimezone,
		Status:          req.Status,
	})
	if err != nil {
		switch {
		case errors.Is(err, services.ErrCountryNotFound):
			utils.Error(w, http.StatusNotFound, "country not found")
		case errors.Is(err, services.ErrInvalidInput):
			utils.Error(w, http.StatusBadRequest, "iso_code, name, default_currency, and default_timezone are required")
		case errors.Is(err, services.ErrInvalidISOCode):
			utils.Error(w, http.StatusBadRequest, "iso_code must be a 2-letter country code")
		case errors.Is(err, services.ErrInvalidCurrency):
			utils.Error(w, http.StatusBadRequest, "default_currency must be a known ISO currency code")
		case errors.Is(err, services.ErrCountryConflict):
			utils.Error(w, http.StatusConflict, "iso_code already exists")
		default:
			utils.Error(w, http.StatusInternalServerError, "could not update country")
		}
		return
	}
	utils.JSON(w, http.StatusOK, item)
}

func (h *CountryHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.countries.Delete(r.Context(), id); err != nil {
		if errors.Is(err, services.ErrCountryNotFound) {
			utils.Error(w, http.StatusNotFound, "country not found")
			return
		}
		utils.Error(w, http.StatusInternalServerError, "could not delete country")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"message": "country deleted"})
}
