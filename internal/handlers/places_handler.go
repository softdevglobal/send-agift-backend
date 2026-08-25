package handlers

import (
	"errors"
	"net/http"

	"myapp/internal/services"
	"myapp/internal/utils"
)

// PlacesHandler exposes Google Places lookups to the frontend without ever
// handing out the API key.
type PlacesHandler struct {
	places *services.PlacesService
}

func NewPlacesHandler(places *services.PlacesService) *PlacesHandler {
	return &PlacesHandler{places: places}
}

// Autocomplete handles GET /places/autocomplete?input=&session=&country=&language=
func (h *PlacesHandler) Autocomplete(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	input := query.Get("input")
	if input == "" {
		input = query.Get("q")
	}

	suggestions, err := h.places.Autocomplete(r.Context(), services.AutocompleteParams{
		Input:        input,
		SessionToken: query.Get("session"),
		CountryCode:  query.Get("country"),
		Language:     query.Get("language"),
		Types:        query.Get("types"),
	})
	if err != nil {
		utils.Error(w, http.StatusBadGateway, "could not search places")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]any{"suggestions": suggestions})
}

// Details handles GET /places/details?place_id=&session=&language=
func (h *PlacesHandler) Details(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	placeID := query.Get("place_id")
	if placeID == "" {
		utils.Error(w, http.StatusBadRequest, "place_id is required")
		return
	}

	details, err := h.places.Details(r.Context(), placeID, query.Get("session"), query.Get("language"))
	if err != nil {
		if errors.Is(err, services.ErrPlaceNotFound) {
			utils.Error(w, http.StatusNotFound, "place not found")
			return
		}
		utils.Error(w, http.StatusBadGateway, "could not load place details")
		return
	}
	utils.JSON(w, http.StatusOK, details)
}
