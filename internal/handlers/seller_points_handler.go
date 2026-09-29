package handlers

import (
	"errors"
	"io"
	"log"
	"net/http"

	"myapp/internal/middleware"
	"myapp/internal/services"
	"myapp/internal/utils"
)

// SellerPointsHandler serves sellers buying points, the admin tools that
// confirm purchases, and the payment provider's webhook.
type SellerPointsHandler struct {
	points *services.SellerPointsService
}

func NewSellerPointsHandler(points *services.SellerPointsService) *SellerPointsHandler {
	return &SellerPointsHandler{points: points}
}

func sellerID(r *http.Request) string {
	id, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	return id
}

// Wallet handles GET /sellers/me/points.
func (h *SellerPointsHandler) Wallet(w http.ResponseWriter, r *http.Request) {
	wallet, err := h.points.Wallet(r.Context(), sellerID(r))
	if err != nil {
		h.writeError(w, err, "could not read points")
		return
	}
	utils.JSON(w, http.StatusOK, wallet)
}

// Purchases handles GET /sellers/me/points/purchases.
func (h *SellerPointsHandler) Purchases(w http.ResponseWriter, r *http.Request) {
	list, err := h.points.Purchases(r.Context(), sellerID(r))
	if err != nil {
		h.writeError(w, err, "could not list purchases")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]any{"items": list})
}

// CreatePurchase handles POST /sellers/me/points/purchases.
func (h *SellerPointsHandler) CreatePurchase(w http.ResponseWriter, r *http.Request) {
	var in services.PurchaseInput
	if !decodeBody(w, r, &in) {
		return
	}
	if in.IdempotencyKey == "" {
		in.IdempotencyKey = r.Header.Get(IdempotencyKeyHeader)
	}
	p, err := h.points.CreatePurchase(r.Context(), sellerID(r), in)
	if err != nil {
		h.writeError(w, err, "could not start purchase")
		return
	}
	utils.JSON(w, http.StatusCreated, p)
}

// CancelPurchase handles POST /sellers/me/points/purchases/{purchaseID}/cancel.
func (h *SellerPointsHandler) CancelPurchase(w http.ResponseWriter, r *http.Request) {
	id, ok := uuidParam(w, r, "purchaseID", "points purchase not found")
	if !ok {
		return
	}
	p, err := h.points.CancelPurchase(r.Context(), sellerID(r), id)
	if err != nil {
		h.writeError(w, err, "could not cancel purchase")
		return
	}
	utils.JSON(w, http.StatusOK, p)
}

// TestPayment handles POST /sellers/me/points/purchases/{purchaseID}/test-payment
// with {"outcome": "success" | "failure"}. Refused unless test payments are on.
func (h *SellerPointsHandler) TestPayment(w http.ResponseWriter, r *http.Request) {
	id, ok := uuidParam(w, r, "purchaseID", "points purchase not found")
	if !ok {
		return
	}
	var in struct {
		Outcome string `json:"outcome"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Outcome != "success" && in.Outcome != "failure" {
		utils.Error(w, http.StatusBadRequest, "outcome must be success or failure")
		return
	}
	p, err := h.points.TestPayment(r.Context(), sellerID(r), id, in.Outcome == "success")
	if err != nil {
		h.writeError(w, err, "could not complete test payment")
		return
	}
	utils.JSON(w, http.StatusOK, p)
}

// Webhook handles POST /payments/points/webhook, signed with
// X-Points-Signature: hex HMAC-SHA256 of the raw body.
func (h *SellerPointsHandler) Webhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		utils.Error(w, http.StatusBadRequest, "could not read body")
		return
	}
	p, err := h.points.HandleWebhook(r.Context(), body, r.Header.Get("X-Points-Signature"))
	if err != nil {
		h.writeError(w, err, "could not apply payment event")
		return
	}
	utils.JSON(w, http.StatusOK, p)
}

// AdminPurchases handles GET /admin/points/purchases?status=.
func (h *SellerPointsHandler) AdminPurchases(w http.ResponseWriter, r *http.Request) {
	list, err := h.points.AdminPurchases(r.Context(), r.URL.Query().Get("status"))
	if err != nil {
		h.writeError(w, err, "could not list purchases")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]any{"items": list, "rate": h.points.Rate()})
}

// AdminConfirm handles POST /admin/points/purchases/{purchaseID}/confirm.
func (h *SellerPointsHandler) AdminConfirm(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "purchaseID", "points purchase not found")
	if !ok {
		return
	}
	var in services.ConfirmPurchaseInput
	if r.ContentLength != 0 && !decodeBody(w, r, &in) {
		return
	}
	p, err := h.points.AdminConfirmPurchase(r.Context(), admin, id, in)
	if err != nil {
		h.writeError(w, err, "could not confirm purchase")
		return
	}
	utils.JSON(w, http.StatusOK, p)
}

// AdminFail handles POST /admin/points/purchases/{purchaseID}/fail.
func (h *SellerPointsHandler) AdminFail(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "purchaseID", "points purchase not found")
	if !ok {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	p, err := h.points.AdminFailPurchase(r.Context(), admin, id, in.Reason)
	if err != nil {
		h.writeError(w, err, "could not fail purchase")
		return
	}
	utils.JSON(w, http.StatusOK, p)
}

func (h *SellerPointsHandler) writeError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, services.ErrSellerNotFound):
		utils.Error(w, http.StatusUnauthorized, "unauthorized")
	case errors.Is(err, services.ErrInvalidPurchase):
		utils.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, services.ErrPurchaseNotFound):
		utils.Error(w, http.StatusNotFound, err.Error())
	case errors.Is(err, services.ErrPurchaseState):
		utils.Error(w, http.StatusConflict, err.Error())
	case errors.Is(err, services.ErrTestPaymentsOff):
		utils.Error(w, http.StatusForbidden, err.Error())
	case errors.Is(err, services.ErrWebhookNotConfigured):
		utils.Error(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, services.ErrWebhookSignature):
		utils.Error(w, http.StatusUnauthorized, err.Error())
	default:
		log.Printf("seller points handler: %s: %v", fallback, err)
		utils.Error(w, http.StatusInternalServerError, fallback)
	}
}
