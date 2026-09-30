package handlers

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"myapp/internal/services"
	"myapp/internal/utils"
)

// AvailabilityHandler answers the Find gifts search: which gifts can be
// delivered to a point by a day, using each shop's delivery zones.
type AvailabilityHandler struct {
	availability *services.AvailabilityService
}

func NewAvailabilityHandler(availability *services.AvailabilityService) *AvailabilityHandler {
	return &AvailabilityHandler{availability: availability}
}

// Search handles GET /availability.
// Query: latitude, longitude, optional delivery_date (yyyy-mm-dd), optional customer_type.
func (h *AvailabilityHandler) Search(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	lat, lng, ok := parseCoordinates(query.Get("latitude"), query.Get("longitude"))
	if !ok {
		utils.Error(w, http.StatusBadRequest, "latitude and longitude are required")
		return
	}
	deliveryDate := query.Get("delivery_date")
	if deliveryDate != "" {
		day, err := time.Parse("2006-01-02", deliveryDate)
		if err != nil {
			utils.Error(w, http.StatusBadRequest, "delivery_date must be yyyy-mm-dd")
			return
		}
		today := time.Now().Format("2006-01-02")
		if day.Format("2006-01-02") < today {
			utils.Error(w, http.StatusBadRequest, "delivery_date must be today or later")
			return
		}
	}

	result, err := h.availability.Search(r.Context(), services.GiftAvailabilityQuery{
		Latitude:     lat,
		Longitude:    lng,
		DeliveryDate: deliveryDate,
		CustomerType: query.Get("customer_type"),
	})
	if err != nil {
		if errors.Is(err, services.ErrInvalidCustomerType) {
			utils.Error(w, http.StatusBadRequest, "customer_type must be personal or corporate")
			return
		}
		log.Printf("gift availability error: %v", err)
		utils.Error(w, http.StatusInternalServerError, "could not check gift availability")
		return
	}
	utils.JSON(w, http.StatusOK, result)
}

func parseCoordinates(latRaw, lngRaw string) (float64, float64, bool) {
	if latRaw == "" || lngRaw == "" {
		return 0, 0, false
	}
	lat, errLat := strconv.ParseFloat(latRaw, 64)
	lng, errLng := strconv.ParseFloat(lngRaw, 64)
	if errLat != nil || errLng != nil || lat < -90 || lat > 90 || lng < -180 || lng > 180 {
		return 0, 0, false
	}
	return lat, lng, true
}
