package handlers

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"myapp/internal/services"
	"myapp/internal/utils"
)

// SocialAuthHandler serves customer sign-in with Google and Facebook.
type SocialAuthHandler struct {
	social *services.SocialAuthService
}

func NewSocialAuthHandler(social *services.SocialAuthService) *SocialAuthHandler {
	return &SocialAuthHandler{social: social}
}

// SignIn handles POST /auth/social: { provider, token, token_type }.
func (h *SocialAuthHandler) SignIn(w http.ResponseWriter, r *http.Request) {
	var req services.SocialSignInInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	result, err := h.social.SignIn(r.Context(), req)
	if err != nil {
		h.writeError(w, err)
		return
	}
	utils.JSON(w, http.StatusOK, result)
}

// Complete handles POST /auth/social/complete: the country and phone a new
// social customer needs before their account is made.
func (h *SocialAuthHandler) Complete(w http.ResponseWriter, r *http.Request) {
	var req services.SocialCompleteInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	result, err := h.social.Complete(r.Context(), req)
	if err != nil {
		h.writeError(w, err)
		return
	}
	utils.JSON(w, http.StatusCreated, result)
}

func (h *SocialAuthHandler) writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, services.ErrSocialNotConfigured):
		writeCodedError(w, http.StatusServiceUnavailable, "provider_unavailable", err.Error())
	case errors.Is(err, services.ErrSocialInvalidToken):
		writeCodedError(w, http.StatusUnauthorized, "invalid_token", err.Error())
	case errors.Is(err, services.ErrSocialSignupExpired):
		writeCodedError(w, http.StatusUnauthorized, "signup_expired", err.Error())
	case errors.Is(err, services.ErrSocialNoEmail):
		writeCodedError(w, http.StatusUnprocessableEntity, "no_email", err.Error())
	case errors.Is(err, services.ErrSocialEmailInUse):
		writeCodedError(w, http.StatusConflict, "email_in_use", err.Error())
	case errors.Is(err, services.ErrCustomerConflict):
		writeCodedError(w, http.StatusConflict, "email_in_use", "email already registered")
	case errors.Is(err, services.ErrInvalidCountry):
		utils.Error(w, http.StatusBadRequest, "invalid country_id")
	case errors.Is(err, services.ErrCustomerRegistrationDisabled):
		utils.Error(w, http.StatusForbidden, "customer registration is disabled for this country")
	case errors.Is(err, services.ErrInvalidInput):
		utils.Error(w, http.StatusBadRequest, "provider, token and phone number are required")
	default:
		log.Printf("social auth error: %v", err)
		utils.Error(w, http.StatusBadGateway, "Sign-in failed. Please try again.")
	}
}
