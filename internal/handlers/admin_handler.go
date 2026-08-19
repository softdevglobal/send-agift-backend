package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"myapp/internal/middleware"
	"myapp/internal/services"
	"myapp/internal/utils"
)

// AdminHandler is a struct that contains the admin service
type AdminHandler struct {
	admins *services.AdminService
}

func NewAdminHandler(admins *services.AdminService) *AdminHandler {
	return &AdminHandler{admins: admins}
}

// Me returns the authenticated admin profile (HTTP layer only).
func (h *AdminHandler) Me(w http.ResponseWriter, r *http.Request) {
	adminID, _ := r.Context().Value(middleware.AdminIDContextKey).(string)

	admin, err := h.admins.GetByID(r.Context(), adminID)
	if err != nil {
		if errors.Is(err, services.ErrUnauthorized) {
			utils.Error(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		utils.Error(w, http.StatusInternalServerError, "could not load admin")
		return
	}

	utils.JSON(w, http.StatusOK, admin)
}

// UpdateMe updates the authenticated admin profile (display_name, image_url).
func (h *AdminHandler) UpdateMe(w http.ResponseWriter, r *http.Request) {
	adminID, _ := r.Context().Value(middleware.AdminIDContextKey).(string)
	var req services.AdminUpdateInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	admin, err := h.admins.Update(r.Context(), adminID, req)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrUnauthorized):
			utils.Error(w, http.StatusUnauthorized, "unauthorized")
		case errors.Is(err, services.ErrAdminNotFound):
			utils.Error(w, http.StatusNotFound, "admin not found")
		default:
			utils.Error(w, http.StatusInternalServerError, "could not update admin")
		}
		return
	}
	utils.JSON(w, http.StatusOK, admin)
}
