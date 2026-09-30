package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"myapp/internal/models"
	"myapp/internal/services"
	"myapp/internal/utils"
)

// AvailabilityHandler is the HTTP layer for catalog delivery checks.
type AvailabilityHandler struct {
	availability *services.AvailabilityService
}

func NewAvailabilityHandler(availability *services.AvailabilityService) *AvailabilityHandler {
	return &AvailabilityHandler{availability: availability}
}

// Check handles POST /shipping/availability.
// One published product per shop is used as the parcel. A shop that can arrive
// by the date is returned with available=true, and the catalog shows all of its gifts.
func (h *AvailabilityHandler) Check(w http.ResponseWriter, r *http.Request) {
	var in models.AvailabilityInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	result, err := h.availability.Check(r.Context(), in)
	if err != nil {
		if errors.Is(err, services.ErrAvailabilityDestination) || errors.Is(err, services.ErrAvailabilityDate) {
			utils.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		utils.Error(w, http.StatusInternalServerError, "could not check delivery availability")
		return
	}
	utils.JSON(w, http.StatusOK, result)
}
