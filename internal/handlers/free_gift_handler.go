package handlers

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"myapp/internal/middleware"
	"myapp/internal/services"
	"myapp/internal/utils"
)

// FreeGiftHandler serves the first free gift program: the public product
// list, the customer's status and send, the seller's budget and products,
// and the admin list and stop switch.
type FreeGiftHandler struct {
	gifts *services.FreeGiftService
}

func NewFreeGiftHandler(gifts *services.FreeGiftService) *FreeGiftHandler {
	return &FreeGiftHandler{gifts: gifts}
}

// RequireEnabled answers FREE_GIFT_DISABLED on every free gift route while
// FREE_GIFT_ENABLED is off.
func (h *FreeGiftHandler) RequireEnabled(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h == nil || !h.gifts.Enabled() {
			writeCodedError(w, http.StatusNotFound, "FREE_GIFT_DISABLED", services.ErrFreeGiftDisabled.Error())
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ListProducts handles GET /free-gifts/products?country=.
func (h *FreeGiftHandler) ListProducts(w http.ResponseWriter, r *http.Request) {
	items, err := h.gifts.Available(r.Context(), r.URL.Query().Get("country"))
	if err != nil {
		h.writeError(w, err, "could not list free gift products")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// Status handles GET /customers/me/free-gift?recipient_id=.
func (h *FreeGiftHandler) Status(w http.ResponseWriter, r *http.Request) {
	customerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	status, err := h.gifts.Status(r.Context(), customerID, r.URL.Query().Get("recipient_id"))
	if err != nil {
		h.writeError(w, err, "could not read free gift status")
		return
	}
	utils.JSON(w, http.StatusOK, status)
}

// Send handles POST /customers/me/free-gifts. The Idempotency-Key header is
// required; a retry with the same key returns the first order.
func (h *FreeGiftHandler) Send(w http.ResponseWriter, r *http.Request) {
	customerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	var in services.FreeGiftSendInput
	if !decodeBody(w, r, &in) {
		return
	}
	details, err := h.gifts.Send(r.Context(), customerID, strings.TrimSpace(r.Header.Get(IdempotencyKeyHeader)), in)
	if err != nil {
		h.writeError(w, err, "could not send free gift")
		return
	}
	utils.JSON(w, http.StatusCreated, details)
}

// Program handles GET /sellers/me/free-gift-program.
func (h *FreeGiftHandler) Program(w http.ResponseWriter, r *http.Request) {
	program, err := h.gifts.Program(r.Context(), sellerID(r))
	if err != nil {
		h.writeError(w, err, "could not read free gift program")
		return
	}
	utils.JSON(w, http.StatusOK, program)
}

// SetProgram handles PUT /sellers/me/free-gift-program.
func (h *FreeGiftHandler) SetProgram(w http.ResponseWriter, r *http.Request) {
	var in services.FreeGiftProgramInput
	if !decodeBody(w, r, &in) {
		return
	}
	program, err := h.gifts.SetProgram(r.Context(), sellerID(r), in)
	if err != nil {
		h.writeError(w, err, "could not save free gift program")
		return
	}
	utils.JSON(w, http.StatusOK, program)
}

// SellerProducts handles GET /sellers/me/free-gift-products.
func (h *FreeGiftHandler) SellerProducts(w http.ResponseWriter, r *http.Request) {
	items, err := h.gifts.Products(r.Context(), sellerID(r))
	if err != nil {
		h.writeError(w, err, "could not list free gift products")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// SetProduct handles PUT /sellers/me/free-gift-products/{productID}.
func (h *FreeGiftHandler) SetProduct(w http.ResponseWriter, r *http.Request) {
	productID, ok := uuidParam(w, r, "productID", "product not found")
	if !ok {
		return
	}
	var in services.FreeGiftProductInput
	if r.ContentLength != 0 && !decodeBody(w, r, &in) {
		return
	}
	product, err := h.gifts.SetProduct(r.Context(), sellerID(r), productID, in)
	if err != nil {
		h.writeError(w, err, "could not save free gift product")
		return
	}
	utils.JSON(w, http.StatusOK, product)
}

// RemoveProduct handles DELETE /sellers/me/free-gift-products/{productID}.
func (h *FreeGiftHandler) RemoveProduct(w http.ResponseWriter, r *http.Request) {
	productID, ok := uuidParam(w, r, "productID", "product not found")
	if !ok {
		return
	}
	if err := h.gifts.RemoveProduct(r.Context(), sellerID(r), productID); err != nil {
		h.writeError(w, err, "could not remove free gift product")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// AdminList handles GET /admin/free-gifts.
func (h *FreeGiftHandler) AdminList(w http.ResponseWriter, r *http.Request) {
	items, err := h.gifts.AdminPrograms(r.Context())
	if err != nil {
		h.writeError(w, err, "could not list free gift programs")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// AdminSetSeller handles PATCH /admin/free-gifts/sellers/{sellerID} with
// {"stopped": true | false}: the kill switch for one seller's program.
func (h *FreeGiftHandler) AdminSetSeller(w http.ResponseWriter, r *http.Request) {
	id, ok := uuidParam(w, r, "sellerID", "free gift program not found")
	if !ok {
		return
	}
	var in struct {
		Stopped *bool `json:"stopped"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Stopped == nil {
		utils.Error(w, http.StatusBadRequest, "stopped must be true or false")
		return
	}
	program, err := h.gifts.AdminSetStopped(r.Context(), id, *in.Stopped)
	if err != nil {
		h.writeError(w, err, "could not change free gift program")
		return
	}
	utils.JSON(w, http.StatusOK, program)
}

func (h *FreeGiftHandler) writeError(w http.ResponseWriter, err error, fallback string) {
	coded := func(status int, code string) { writeCodedError(w, status, code, err.Error()) }
	switch {
	case errors.Is(err, services.ErrFreeGiftDisabled):
		coded(http.StatusNotFound, "FREE_GIFT_DISABLED")
	case errors.Is(err, services.ErrVerifyEmailRequired):
		coded(http.StatusForbidden, "VERIFY_EMAIL_REQUIRED")
	case errors.Is(err, services.ErrVerifyPhoneRequired):
		coded(http.StatusForbidden, "VERIFY_PHONE_REQUIRED")
	case errors.Is(err, services.ErrRedemptionLocked):
		coded(http.StatusLocked, "REDEMPTION_LOCKED")
	case errors.Is(err, services.ErrFreeGiftAlreadyUsed):
		writeCodedError(w, http.StatusConflict, "ALREADY_USED", "you have already sent your free gift")
	case errors.Is(err, services.ErrFreeGiftRecipientUsed):
		// Nothing about who sent it or when.
		writeCodedError(w, http.StatusConflict, "RECIPIENT_ALREADY_USED", "this recipient has already received a free gift")
	case errors.Is(err, services.ErrFreeGiftRecipientContact):
		coded(http.StatusUnprocessableEntity, "RECIPIENT_CONTACT_REQUIRED")
	case errors.Is(err, services.ErrFreeGiftSelf):
		coded(http.StatusUnprocessableEntity, "SELF_GIFT")
	case errors.Is(err, services.ErrFreeGiftUnavailable):
		writeCodedError(w, http.StatusConflict, "FREE_GIFT_UNAVAILABLE", "this gift is no longer available as a free gift")
	case errors.Is(err, services.ErrFreeGiftOutOfStock):
		writeCodedError(w, http.StatusConflict, "OUT_OF_STOCK", "this gift is out of stock")
	case errors.Is(err, services.ErrDeliveryDate):
		coded(http.StatusUnprocessableEntity, "DELIVERY_DATE_UNAVAILABLE")
	case errors.Is(err, services.ErrIdempotencyMismatch):
		coded(http.StatusUnprocessableEntity, "IDEMPOTENCY_MISMATCH")
	case errors.Is(err, services.ErrIdempotencyKeyRequired):
		coded(http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED")
	case errors.Is(err, services.ErrOutsideDeliveryZone):
		coded(http.StatusUnprocessableEntity, "OUTSIDE_DELIVERY_ZONE")
	case errors.Is(err, services.ErrInvalidCountry):
		writeCodedError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid country_id")
	case errors.Is(err, services.ErrInvalidOrder):
		writeCodedError(w, http.StatusBadRequest, "INVALID_REQUEST", "product_id, recipient_id, country_id and delivery_date (YYYY-MM-DD) are required")
	case errors.Is(err, services.ErrRecipientNotFound):
		writeCodedError(w, http.StatusNotFound, "RECIPIENT_NOT_FOUND", "recipient not found")
	case errors.Is(err, services.ErrCustomerNotFound), errors.Is(err, services.ErrSellerNotFound):
		writeCodedError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")

	// Seller and admin program management.
	case errors.Is(err, services.ErrFreeGiftInput):
		coded(http.StatusBadRequest, "INVALID_REQUEST")
	case errors.Is(err, services.ErrFreeGiftProgramNotFound):
		writeCodedError(w, http.StatusConflict, "PROGRAM_REQUIRED", "set your free gift budget before choosing products")
	case errors.Is(err, services.ErrFreeGiftProductNotFound):
		writeCodedError(w, http.StatusNotFound, "PRODUCT_NOT_FOUND", "product not found")
	case errors.Is(err, services.ErrFreeGiftTermsRequired):
		writeCodedError(w, http.StatusUnprocessableEntity, "TERMS_REQUIRED", "accept the free gift program terms to set a budget")
	case errors.Is(err, services.ErrFreeGiftProgramStopped):
		writeCodedError(w, http.StatusConflict, "PROGRAM_STOPPED", "your free gift program was stopped by SendAGift. Contact support")
	case errors.Is(err, services.ErrFreeGiftBudgetBelowCommitted):
		writeCodedError(w, http.StatusUnprocessableEntity, "BUDGET_BELOW_COMMITTED", "the budget cannot be lower than what is already held and spent on free gifts")
	case errors.Is(err, services.ErrFreeGiftCurrencyLocked):
		writeCodedError(w, http.StatusConflict, "CURRENCY_LOCKED", "the currency cannot change while the program has products or gifts. Remove the products first")
	case errors.Is(err, services.ErrFreeGiftProductNotEligible):
		writeCodedError(w, http.StatusUnprocessableEntity, "PRODUCT_NOT_ELIGIBLE", strings.ReplaceAll(err.Error(), "\n", ": "))
	case errors.Is(err, services.ErrFreeGiftCapBelowCount):
		writeCodedError(w, http.StatusUnprocessableEntity, "CAP_BELOW_COUNT", "the gift cap cannot be lower than the gifts already sent")
	default:
		log.Printf("free gift handler: %s: %v", fallback, err)
		writeCodedError(w, http.StatusInternalServerError, "INTERNAL", fallback)
	}
}
