package handlers

import (
	"errors"
	"net/http"

	"myapp/internal/middleware"
	"myapp/internal/services"
	"myapp/internal/utils"
)

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
