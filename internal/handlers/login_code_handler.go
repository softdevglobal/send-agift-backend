package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"myapp/internal/middleware"
	"myapp/internal/services"
	"myapp/internal/utils"
)

type LoginCodeHandler struct {
	codes *services.LoginCodeService
}

func NewLoginCodeHandler(codes *services.LoginCodeService) *LoginCodeHandler {
	return &LoginCodeHandler{codes: codes}
}

type loginCodeRequest struct {
	Channel     string `json:"channel"` // email or phone
	Destination string `json:"destination"` // email address or phone number
	Code        string `json:"code"` // verification code
}

type phoneCodeRequest struct {
	Phone string `json:"phone"`
	Code  string `json:"code"`
}

// RequestCode handles POST /customers/login/code. The answer is the same
// whether or not an account exists.
func (h *LoginCodeHandler) RequestCode(w http.ResponseWriter, r *http.Request) {
	var req loginCodeRequest // request body
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return // return error if request body is invalid
	}
	if err := h.codes.RequestLoginCode(r.Context(), req.Channel, req.Destination); err != nil {
		writeLoginCodeError(w, err, "could not send a code") // return error if code could not be sent
		return // return error if code could not be sent
	}
	utils.JSON(w, http.StatusAccepted, map[string]string{"message": "If that is yours, a code is on its way."}) // return success message
}

// VerifyCode handles POST /customers/login/code/verify.
func (h *LoginCodeHandler) VerifyCode(w http.ResponseWriter, r *http.Request) {
	var req loginCodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	res, err := h.codes.VerifyLoginCode(r.Context(), req.Channel, req.Destination, req.Code)
	if err != nil {
		writeLoginCodeError(w, err, "could not check the code")
		return
	}
	utils.JSON(w, http.StatusOK, res)
}

// RequestPhoneCode handles POST /customers/me/phone/code.
func (h *LoginCodeHandler) RequestPhoneCode(w http.ResponseWriter, r *http.Request) {
	customerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	var req phoneCodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.codes.RequestPhoneVerification(r.Context(), customerID, req.Phone); err != nil {
		writeLoginCodeError(w, err, "could not send a code")
		return
	}
	utils.JSON(w, http.StatusAccepted, map[string]string{"message": "Code sent."})
}

// VerifyPhone handles POST /customers/me/phone/verify.
func (h *LoginCodeHandler) VerifyPhone(w http.ResponseWriter, r *http.Request) {
	customerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	var req phoneCodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.codes.ConfirmPhoneVerification(r.Context(), customerID, req.Phone, req.Code); err != nil {
		writeLoginCodeError(w, err, "could not verify the phone")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"message": "Phone verified."})
}

func writeLoginCodeError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, services.ErrLoginChannel), errors.Is(err, services.ErrInvalidPhone):
		utils.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, services.ErrInvalidInput):
		utils.Error(w, http.StatusBadRequest, "enter a valid email")
	case errors.Is(err, services.ErrLoginCodeWait), errors.Is(err, services.ErrLoginCodeTooMany),
		errors.Is(err, services.ErrLoginCodeLocked):
		utils.Error(w, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, services.ErrLoginCodeWrong), errors.Is(err, services.ErrLoginCodeExpired):
		utils.Error(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, services.ErrPhoneTaken):
		utils.Error(w, http.StatusConflict, err.Error())
	default:
		utils.Error(w, http.StatusInternalServerError, fallback)
	}
}
