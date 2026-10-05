package handlers

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"myapp/internal/middleware"
	"myapp/internal/services"
	"myapp/internal/utils"
)

// VerificationHandler serves seller email confirmation and the admin seller
// review queue.
type VerificationHandler struct {
	verification *services.SellerVerificationService
}

func NewVerificationHandler(verification *services.SellerVerificationService) *VerificationHandler {
	return &VerificationHandler{verification: verification}
}

type verifyEmailRequest struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

// VerifyEmail handles POST /sellers/verify-email: a right code confirms the
// seller's email and signs them in.
func (h *VerificationHandler) VerifyEmail(w http.ResponseWriter, r *http.Request) {
	var req verifyEmailRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	result, err := h.verification.VerifyEmail(r.Context(), req.Email, req.Code)
	if err != nil {
		h.writeError(w, err, "could not verify email")
		return
	}
	utils.JSON(w, http.StatusOK, result)
}

// ResendCode handles POST /sellers/verify-email/resend.
func (h *VerificationHandler) ResendCode(w http.ResponseWriter, r *http.Request) {
	var req verifyEmailRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.verification.ResendCode(r.Context(), req.Email); err != nil {
		h.writeError(w, err, "could not send a new code")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"message": "if that email belongs to a seller account, a new code is on its way"})
}

// AdminList handles GET /admin/sellers?status=&q=&limit=&offset=.
func (h *VerificationHandler) AdminList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	list, err := h.verification.AdminList(r.Context(), q.Get("status"), q.Get("q"), limit, offset)
	if err != nil {
		h.writeError(w, err, "could not list sellers")
		return
	}
	utils.JSON(w, http.StatusOK, list)
}

// AdminGet handles GET /admin/sellers/{id}.
func (h *VerificationHandler) AdminGet(w http.ResponseWriter, r *http.Request) {
	details, err := h.verification.AdminGet(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		h.writeError(w, err, "could not load seller")
		return
	}
	utils.JSON(w, http.StatusOK, details)
}

// AdminReview handles PATCH /admin/sellers/{id}/verification.
func (h *VerificationHandler) AdminReview(w http.ResponseWriter, r *http.Request) {
	adminID, _ := r.Context().Value(middleware.AdminIDContextKey).(string)
	var req services.SellerReviewInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	seller, err := h.verification.AdminReview(r.Context(), adminID, chi.URLParam(r, "id"), req)
	if err != nil {
		h.writeError(w, err, "could not review seller")
		return
	}
	utils.JSON(w, http.StatusOK, seller)
}

// AttachDocument handles PUT /sellers/me/application/document: the seller's
// uploaded business registration evidence.
func (h *VerificationHandler) AttachDocument(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	var req services.SellerDocumentInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.verification.AttachDocument(r.Context(), sellerID, req); err != nil {
		h.writeError(w, err, "could not save the document")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"message": "document saved"})
}

// AdminDocument handles GET /admin/sellers/{id}/document: a short-lived link
// to the seller's business document.
func (h *VerificationHandler) AdminDocument(w http.ResponseWriter, r *http.Request) {
	url, err := h.verification.AdminDocumentURL(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		h.writeError(w, err, "could not open the document")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"url": url})
}

// RequireApprovedSeller lets a request through only for a seller an admin
// has approved. Use after RequireAuth and RequireRole("seller").
func (h *VerificationHandler) RequireApprovedSeller(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
		status, err := h.verification.VerificationStatus(r.Context(), sellerID)
		if err != nil {
			h.writeError(w, err, "could not check seller account")
			return
		}
		if status != "verified" {
			writeCodedError(w, http.StatusForbidden, "seller_not_approved",
				"Your seller account is waiting for approval. You can open shops and list gifts once it's active")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *VerificationHandler) writeError(w http.ResponseWriter, err error, fallback string) {
	var cooldown *services.EmailCodeCooldownError
	switch {
	case errors.As(err, &cooldown):
		w.Header().Set("Retry-After", strconv.Itoa(int(cooldown.RetryAfter.Seconds())+1))
		writeCodedError(w, http.StatusTooManyRequests, "code_cooldown", cooldown.Error())
	case errors.Is(err, services.ErrEmailCodeInvalid):
		writeCodedError(w, http.StatusBadRequest, "code_invalid", "That code isn't right. Check the email and try again")
	case errors.Is(err, services.ErrEmailCodeExpired):
		writeCodedError(w, http.StatusBadRequest, "code_expired", "That code has expired. Ask for a new one")
	case errors.Is(err, services.ErrEmailCodeLocked):
		writeCodedError(w, http.StatusBadRequest, "code_locked", "Too many wrong codes. Ask for a new one")
	case errors.Is(err, services.ErrEmailAlreadyVerified):
		writeCodedError(w, http.StatusConflict, "already_verified", "This email is already confirmed. Sign in to continue")
	case errors.Is(err, services.ErrInvalidSellerReview):
		utils.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, services.ErrSellerEmailUnconfirmed):
		utils.Error(w, http.StatusConflict, err.Error())
	case errors.Is(err, services.ErrInvalidInput):
		utils.Error(w, http.StatusBadRequest, "status must be unverified, pending, verified or rejected")
	case errors.Is(err, services.ErrSellerNotFound):
		utils.Error(w, http.StatusNotFound, "seller not found")
	case errors.Is(err, services.ErrInvalidSellerDocument):
		utils.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, services.ErrNoSellerApplication), errors.Is(err, services.ErrNoSellerDocument):
		utils.Error(w, http.StatusNotFound, err.Error())
	case errors.Is(err, services.ErrDocumentsUnavailable):
		utils.Error(w, http.StatusServiceUnavailable, err.Error())
	default:
		log.Printf("verification handler error: %v", err)
		utils.Error(w, http.StatusInternalServerError, fallback)
	}
}

// writeCodedError is an error with a machine-readable code beside the
// message, for errors a client reacts to rather than just shows.
func writeCodedError(w http.ResponseWriter, status int, code, message string) {
	utils.JSON(w, status, map[string]string{"error": message, "code": code})
}
