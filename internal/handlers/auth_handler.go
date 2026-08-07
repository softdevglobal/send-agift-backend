package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"time"

	"myapp/internal/models"
	"myapp/internal/repository"
	"myapp/internal/utils"
)

type AuthHandler struct {
	admins    *repository.AdminRepository
	jwtSecret string
}

func NewAuthHandler(admins *repository.AdminRepository, jwtSecret string) *AuthHandler {
	return &AuthHandler{admins: admins, jwtSecret: jwtSecret}
}

type bootstrapRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

// Bootstrap creates the very first superadmin.
// Allowed when the table is empty (count == 0), OR when the caller sends a
// matching X-Bootstrap-Secret header — lets you re-seed a fresh environment
// without leaving this endpoint open forever.
func (h *AuthHandler) Bootstrap(w http.ResponseWriter, r *http.Request) {
	count, err := h.admins.CountAdmins(r.Context())
	if err != nil {
		utils.Error(w, http.StatusInternalServerError, "could not check admin count")
		return
	}

	expectedSecret := os.Getenv("BOOTSTRAP_SECRET")
	sentSecret := r.Header.Get("X-Bootstrap-Secret")

	if count > 0 && (expectedSecret == "" || sentSecret != expectedSecret) {
		utils.Error(w, http.StatusForbidden, "bootstrap already completed")
		return
	}

	var req bootstrapRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Email == "" || len(req.Password) < 8 {
		utils.Error(w, http.StatusBadRequest, "email required, password must be at least 8 characters")
		return
	}

	hash, err := utils.HashPassword(req.Password)
	if err != nil {
		utils.Error(w, http.StatusInternalServerError, "could not hash password")
		return
	}

	admin := &models.Admin{
		Email:        req.Email,
		PasswordHash: hash,
		DisplayName:  req.DisplayName,
		MFARequired:  true,
	}
	if err := h.admins.CreateAdmin(r.Context(), admin); err != nil {
		utils.Error(w, http.StatusInternalServerError, "could not create admin")
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

// Login verifies email + password, then returns a signed Bearer JWT.
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	admin, err := h.admins.GetByEmail(r.Context(), req.Email)
	if err != nil {
		if errors.Is(err, repository.ErrAdminNotFound) {
			utils.Error(w, http.StatusUnauthorized, "invalid email or password")
			return
		}
		utils.Error(w, http.StatusInternalServerError, "login failed")
		return
	}

	if !utils.CheckPassword(req.Password, admin.PasswordHash) {
		utils.Error(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	token, err := utils.GenerateJWT(admin.ID.String(), admin.Email, "admin", h.jwtSecret, 24*time.Hour)
	if err != nil {
		utils.Error(w, http.StatusInternalServerError, "could not generate token")
		return
	}

	utils.JSON(w, http.StatusOK, map[string]string{"token": token})
}