package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"myapp/internal/services"
	"myapp/internal/utils"
)

type AuthHandler struct {
	auth *services.AuthService
}

func NewAuthHandler(auth *services.AuthService) *AuthHandler {
	return &AuthHandler{auth: auth}
}

type bootstrapRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

// Bootstrap creates the very first superadmin (HTTP layer only).
func (h *AuthHandler) Bootstrap(w http.ResponseWriter, r *http.Request) {
	var req bootstrapRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	admin, err := h.auth.Bootstrap(r.Context(), services.BootstrapInput{
		Email:           req.Email,
		Password:        req.Password,
		DisplayName:     req.DisplayName,
		BootstrapSecret: r.Header.Get("X-Bootstrap-Secret"),
	})
	if err != nil {
		switch {
		case errors.Is(err, services.ErrBootstrapForbidden):
			utils.Error(w, http.StatusForbidden, "bootstrap already completed")
		case errors.Is(err, services.ErrInvalidInput):
			utils.Error(w, http.StatusBadRequest, "email required, password must be at least 8 characters")
		default:
			utils.Error(w, http.StatusInternalServerError, "could not create admin")
		}
		return
	}

	utils.JSON(w, http.StatusCreated, map[string]interface{}{
		"message": "superadmin created",
		"id":      admin.ID,
	})
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Login verifies email + password, then returns a signed Bearer JWT (HTTP layer only).
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	result, err := h.auth.Login(r.Context(), services.LoginInput{
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		switch {
		case errors.Is(err, services.ErrInvalidCredentials):
			utils.Error(w, http.StatusUnauthorized, "invalid email or password")
		default:
			utils.Error(w, http.StatusInternalServerError, "login failed")
		}
		return
	}

	utils.JSON(w, http.StatusOK, result)
}
