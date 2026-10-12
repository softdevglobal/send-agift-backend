package handlers

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"

	"myapp/internal/middleware"
	"myapp/internal/repository"
	"myapp/internal/services"
	"myapp/internal/utils"
)

// StripeHandler serves paying for an order, seller payout onboarding, and
// Stripe's two webhooks.
type StripeHandler struct {
	stripe *services.StripeService
}

func NewStripeHandler(stripe *services.StripeService) *StripeHandler {
	return &StripeHandler{stripe: stripe}
}

// PayOrder handles POST /customers/me/orders/{id}/pay. The answer carries
// the PaymentIntent client secret for Stripe's payment sheet.
func (h *StripeHandler) PayOrder(w http.ResponseWriter, r *http.Request) {
	customerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	p, err := h.stripe.PayOrder(r.Context(), customerID, chi.URLParam(r, "id"))
	if err != nil {
		h.writeError(w, err, "could not start the payment")
		return
	}
	utils.JSON(w, http.StatusOK, p)
}

// SyncPayment handles POST /customers/me/orders/{id}/payment/sync. The
// customer's page calls it on coming back from Stripe, so the order turns
// paid even if Stripe's webhook has not arrived. It answers with the status.
func (h *StripeHandler) SyncPayment(w http.ResponseWriter, r *http.Request) {
	customerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	status, err := h.stripe.SyncOrderPayment(r.Context(), customerID, chi.URLParam(r, "id"))
	if err != nil {
		h.writeError(w, err, "could not check the payment")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]any{
		"status": status,
		"paid":   status != "pending_payment" && status != "draft",
	})
}

// SellerStatus handles GET /sellers/me/stripe.
func (h *StripeHandler) SellerStatus(w http.ResponseWriter, r *http.Request) {
	st, err := h.stripe.SellerStatus(r.Context(), sellerID(r))
	if err != nil {
		h.writeError(w, err, "could not read the payout account")
		return
	}
	utils.JSON(w, http.StatusOK, st)
}

// PayoutHistory handles GET /sellers/me/payouts: the payout account, totals
// and the latest settlements.
func (h *StripeHandler) PayoutHistory(w http.ResponseWriter, r *http.Request) {
	out, err := h.stripe.SellerPayoutHistory(r.Context(), sellerID(r))
	if err != nil {
		h.writeError(w, err, "could not read your payouts")
		return
	}
	utils.JSON(w, http.StatusOK, out)
}

// SellerConnect handles POST /sellers/me/stripe/onboarding: a link to
// Stripe's hosted page for adding bank and identity details.
func (h *StripeHandler) SellerConnect(w http.ResponseWriter, r *http.Request) {
	link, err := h.stripe.ConnectOnboarding(r.Context(), sellerID(r))
	if err != nil {
		h.writeError(w, err, "could not start payout setup")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"url": link})
}

// PlatformWebhook handles POST /payments/stripe/webhook (events from the
// platform account).
func (h *StripeHandler) PlatformWebhook(w http.ResponseWriter, r *http.Request) {
	h.webhook(w, r, h.stripe.HandlePlatformWebhook)
}

// ConnectWebhook handles POST /payments/stripe/connect-webhook (events from
// connected accounts).
func (h *StripeHandler) ConnectWebhook(w http.ResponseWriter, r *http.Request) {
	h.webhook(w, r, h.stripe.HandleConnectWebhook)
}

// webhook reads the raw body (the signature covers it byte for byte) and
// answers 200 once the event is applied or already was. Anything else lets
// Stripe retry.
func (h *StripeHandler) webhook(w http.ResponseWriter, r *http.Request, handle func(ctx context.Context, body []byte, signature string) error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		utils.Error(w, http.StatusBadRequest, "could not read body")
		return
	}
	if err := handle(r.Context(), body, r.Header.Get("Stripe-Signature")); err != nil {
		switch {
		case errors.Is(err, services.ErrStripeSignature):
			utils.Error(w, http.StatusBadRequest, "invalid signature")
		case errors.Is(err, services.ErrStripeNotConfigured):
			utils.Error(w, http.StatusServiceUnavailable, "stripe webhook is not configured")
		default:
			log.Printf("stripe webhook: %v", err)
			utils.Error(w, http.StatusInternalServerError, "could not apply the event")
		}
		return
	}
	utils.JSON(w, http.StatusOK, map[string]bool{"received": true})
}

func (h *StripeHandler) writeError(w http.ResponseWriter, err error, fallback string) {
	var apiErr *services.StripeAPIError
	switch {
	case errors.Is(err, services.ErrStripeNotConfigured):
		utils.Error(w, http.StatusServiceUnavailable, "card payments are not available yet")
	case errors.Is(err, repository.ErrStripeOrderNotFound), errors.Is(err, repository.ErrStripeSellerNotFound):
		utils.Error(w, http.StatusNotFound, "not found")
	case errors.Is(err, services.ErrOrderAlreadyPaid), errors.Is(err, services.ErrOrderNotPayable):
		utils.Error(w, http.StatusConflict, err.Error())
	case errors.As(err, &apiErr) && apiErr.Code == "country_unsupported":
		log.Printf("stripe: %s: %v", fallback, err)
		utils.Error(w, http.StatusUnprocessableEntity,
			"Payouts through Stripe are not available in your country yet. We will let you know when they are.")
	case errors.As(err, &apiErr):
		log.Printf("stripe: %s: %v", fallback, err)
		utils.Error(w, http.StatusBadGateway, fallback+": "+apiErr.Message)
	default:
		log.Printf("stripe: %s: %v", fallback, err)
		utils.Error(w, http.StatusInternalServerError, fallback)
	}
}
