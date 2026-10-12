package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"myapp/internal/middleware"
	"myapp/internal/services"
	"myapp/internal/utils"
)

// emailChanger is what the handler needs from the customer or seller service.
type emailChanger interface {
	RequestEmailChange(ctx context.Context, id, newEmail, currentPassword, ip string) error
	ConfirmEmailChange(ctx context.Context, id, code, ip string) (*services.EmailChangeResult, error)
	RevertEmailChange(ctx context.Context, rawToken string) error
}

// EmailChangeHandler serves changing a login email and undoing it, for
// customers and for sellers.
type EmailChangeHandler struct {
	accounts emailChanger
}

func NewEmailChangeHandler(customers *services.CustomerService) *EmailChangeHandler {
	return &EmailChangeHandler{accounts: customers}
}

func NewSellerEmailChangeHandler(sellers *services.SellerService) *EmailChangeHandler {
	return &EmailChangeHandler{accounts: sellers}
}

type emailChangeRequest struct {
	NewEmail        string `json:"new_email"`
	CurrentPassword string `json:"current_password"`
}

type emailChangeConfirm struct {
	Code string `json:"code"`
}

type emailUndoRequest struct {
	Token string `json:"token"`
}

// Request handles POST /customers/me/email/change. The answer is the same
// whether or not the new address can be used.
func (h *EmailChangeHandler) Request(w http.ResponseWriter, r *http.Request) {
	customerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	var req emailChangeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.accounts.RequestEmailChange(r.Context(), customerID, req.NewEmail, req.CurrentPassword, requestIP(r)); err != nil {
		h.writeError(w, err, "could not start the email change")
		return
	}
	utils.JSON(w, http.StatusAccepted, map[string]string{
		"message": "If that address can be used, a code is on its way.",
	})
}

// Confirm handles POST /customers/me/email/confirm. It answers with a new
// token, because every older one stops working.
func (h *EmailChangeHandler) Confirm(w http.ResponseWriter, r *http.Request) {
	customerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	var req emailChangeConfirm
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	res, err := h.accounts.ConfirmEmailChange(r.Context(), customerID, req.Code, requestIP(r))
	if err != nil {
		h.writeError(w, err, "could not change the email")
		return
	}
	utils.JSON(w, http.StatusOK, res)
}

// Undo handles POST /customers/email/undo. The token comes from the alert
// sent to the old address, so no session is needed.
func (h *EmailChangeHandler) Undo(w http.ResponseWriter, r *http.Request) {
	var req emailUndoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Token) == "" {
		utils.Error(w, http.StatusBadRequest, "token is required")
		return
	}
	if err := h.accounts.RevertEmailChange(r.Context(), req.Token); err != nil {
		h.writeError(w, err, "could not undo the email change")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{
		"message": "Your old email is back. Reset your password to sign in.",
	})
}

// requestIP is the caller's address. chi's RealIP middleware has already put
// the forwarded address in RemoteAddr.
func requestIP(r *http.Request) string {
	return r.RemoteAddr
}

func (h *EmailChangeHandler) writeError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, services.ErrInvalidInput):
		utils.Error(w, http.StatusBadRequest, "enter a valid new email")
	case errors.Is(err, services.ErrPasswordChangeRequired):
		utils.Error(w, http.StatusForbidden, err.Error())
	case errors.Is(err, services.ErrWrongPassword):
		utils.Error(w, http.StatusForbidden, err.Error())
	case errors.Is(err, services.ErrEmailChangeLimit), errors.Is(err, services.ErrEmailChangeWait),
		errors.Is(err, services.ErrPasswordCodeLocked):
		utils.Error(w, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, services.ErrPasswordCodeWrong), errors.Is(err, services.ErrPasswordCodeExpired),
		errors.Is(err, services.ErrUndoInvalid):
		utils.Error(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, services.ErrEmailChangeTaken):
		utils.Error(w, http.StatusConflict, err.Error())
	case errors.Is(err, services.ErrCustomerNotFound), errors.Is(err, services.ErrSellerNotFound):
		utils.Error(w, http.StatusNotFound, "account not found")
	default:
		log.Printf("email change: %s: %v", fallback, err)
		utils.Error(w, http.StatusInternalServerError, fallback)
	}
}
