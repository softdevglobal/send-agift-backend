package handlers

import (
	"errors" // errors is used to handle errors
	"net/http" // net/http is used to create and manage HTTP servers and clients

	"myapp/internal/middleware" // middleware is used to handle the middleware of the application
	"myapp/internal/services" // services is used to handle the services of the application
	"myapp/internal/utils" // utils is used to handle the utilities of the application
)

// AdminHandler is a struct that contains the admin service
type AdminHandler struct { // AdminHandler is a struct that contains the admin service
	admins *services.AdminService // admins is a pointer to the admin service
}

func NewAdminHandler(admins *services.AdminService) *AdminHandler { // NewAdminHandler is a function that creates a new admin handler
	return &AdminHandler{admins: admins} // returns a new admin handler
}

// Me returns the authenticated admin profile (HTTP layer only).
func (h *AdminHandler) Me(w http.ResponseWriter, r *http.Request) {
	adminID, _ := r.Context().Value(middleware.AdminIDContextKey).(string) // adminID is the ID of the admin

	admin, err := h.admins.GetByID(r.Context(), adminID) // admin is the admin profile
	if err != nil {
		if errors.Is(err, services.ErrUnauthorized) { // if the admin is unauthorized
			utils.Error(w, http.StatusUnauthorized, "unauthorized") // returns an unauthorized error
			return
		}
		utils.Error(w, http.StatusInternalServerError, "could not load admin") // returns an internal server error
		return
	}

	utils.JSON(w, http.StatusOK, admin) // returns the admin profile
}
