package handlers

import (
	"net/http"

	"myapp/internal/middleware"
	"myapp/internal/utils"
)

type AdminHandler struct{}

func NewAdminHandler() *AdminHandler {
	return &AdminHandler{}
}

// Me returns the authenticated admin id from the JWT (for verifying auth works).
func (h *AdminHandler) Me(w http.ResponseWriter, r *http.Request) {
	adminID, _ := r.Context().Value(middleware.AdminIDContextKey).(string)
	if adminID == "" {
		utils.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"admin_id": adminID})
}
