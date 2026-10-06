package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"myapp/internal/middleware"
	"myapp/internal/services"
	"myapp/internal/utils"
)

const passwordCodeSent = "If an account uses this email, we sent a code."

func writePasswordResetError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, services.ErrPasswordCodeWrong),
		errors.Is(err, services.ErrPasswordCodeExpired),
		errors.Is(err, services.ErrPasswordCodeLocked),
		errors.Is(err, services.ErrPasswordCodeWait),
		errors.Is(err, services.ErrPasswordUnchanged):
		utils.Error(w, http.StatusBadRequest, err.Error())
		return true
	case errors.Is(err, services.ErrInvalidInput):
		utils.Error(w, http.StatusBadRequest, "password must be at least 8 characters and not a temporary password")
		return true
	default:
		return false
	}
}

func (h *CustomerHandler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.customers.SendForgotPasswordCode(r.Context(), req.Email); err != nil {
		if writePasswordResetError(w, err) {
			return
		}
		h.writeCustomerError(w, err, "could not send a code")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"message": passwordCodeSent})
}

func (h *CustomerHandler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Code     string `json:"code"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.customers.ResetForgottenPassword(r.Context(), req.Email, req.Code, req.Password); err != nil {
		if writePasswordResetError(w, err) {
			return
		}
		h.writeCustomerError(w, err, "could not reset password")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"message": "password changed"})
}

func (h *CustomerHandler) SendPasswordCode(w http.ResponseWriter, r *http.Request) {
	customerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	if err := h.customers.SendProfilePasswordCode(r.Context(), customerID); err != nil {
		if writePasswordResetError(w, err) {
			return
		}
		h.writeCustomerError(w, err, "could not send a code")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"message": "We sent a code to your email."})
}

func (h *CustomerHandler) ResetProfilePassword(w http.ResponseWriter, r *http.Request) {
	customerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	var req struct {
		Code        string `json:"code"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.customers.ResetProfilePassword(r.Context(), customerID, req.Code, req.NewPassword); err != nil {
		if writePasswordResetError(w, err) {
			return
		}
		h.writeCustomerError(w, err, "could not change password")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"message": "password changed"})
}

func (h *SellerHandler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.sellers.SendForgotPasswordCode(r.Context(), req.Email); err != nil {
		if writePasswordResetError(w, err) {
			return
		}
		h.writeError(w, err, "could not send a code")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"message": passwordCodeSent})
}

func (h *SellerHandler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Code     string `json:"code"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.sellers.ResetForgottenPassword(r.Context(), req.Email, req.Code, req.Password); err != nil {
		if writePasswordResetError(w, err) {
			return
		}
		h.writeError(w, err, "could not reset password")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"message": "password changed"})
}

func (h *SellerHandler) SendPasswordCode(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	if err := h.sellers.SendProfilePasswordCode(r.Context(), sellerID); err != nil {
		if writePasswordResetError(w, err) {
			return
		}
		h.writeError(w, err, "could not send a code")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"message": "We sent a code to your email."})
}

func (h *SellerHandler) ResetProfilePassword(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	var req struct {
		Code        string `json:"code"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.sellers.ResetProfilePassword(r.Context(), sellerID, req.Code, req.NewPassword); err != nil {
		if writePasswordResetError(w, err) {
			return
		}
		h.writeError(w, err, "could not change password")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"message": "password changed"})
}
