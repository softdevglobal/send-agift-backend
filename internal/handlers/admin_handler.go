package handlers

import (
	"errors" // errors is used to handle errors
	"net/http" // net/http is used to create and manage HTTP servers and clients

	"myapp/internal/middleware" // middleware is used to handle the middleware of the application
	"myapp/internal/services" // services is used to handle the services of the application
	"myapp/internal/utils" // utils is used to handle the utilities of the application
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
