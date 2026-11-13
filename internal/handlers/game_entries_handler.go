package handlers

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"myapp/internal/services"
	"myapp/internal/utils"
)

// GameEntriesHandler serves customers' game entries and state, sellers'
// advertising packages, and the admin controls for both.
type GameEntriesHandler struct {
	entries *services.GameEntriesService
	ads     *services.AdvertisingService
}

func NewGameEntriesHandler(entries *services.GameEntriesService, ads *services.AdvertisingService) *GameEntriesHandler {
	return &GameEntriesHandler{entries: entries, ads: ads}
}

func (h *GameEntriesHandler) writeError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, services.ErrCustomerRequired):
		utils.Error(w, http.StatusUnauthorized, "unauthorized")
	case errors.Is(err, services.ErrInvalidEntries), errors.Is(err, services.ErrInvalidState),
		errors.Is(err, services.ErrInvalidAdPackage), errors.Is(err, services.ErrInvalidAdPurchase):
		utils.JSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "code": "INVALID_REQUEST"})
	case errors.Is(err, services.ErrStateNotFound), errors.Is(err, services.ErrJurisdictionNotFound),
		errors.Is(err, services.ErrShortfallNotFound), errors.Is(err, services.ErrAdPackageNotFound),
		errors.Is(err, services.ErrAdPurchaseNotFound):
		utils.Error(w, http.StatusNotFound, err.Error())
	case errors.Is(err, services.ErrAdPackageState), errors.Is(err, services.ErrAdPurchaseState):
		utils.JSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "STATE_CONFLICT"})
	case errors.Is(err, services.ErrAdTestPaymentsOff):
		utils.Error(w, http.StatusForbidden, err.Error())
	case errors.Is(err, services.ErrFundingLocked):
		utils.JSON(w, http.StatusLocked, map[string]any{"error": err.Error(), "code": "FUNDING_LOCKED"})
	default:
		log.Printf("game entries: %s: %v", fallback, err)
		utils.Error(w, http.StatusInternalServerError, fallback)
	}
}

func decode(w http.ResponseWriter, r *http.Request, into any) bool {
	if err := json.NewDecoder(r.Body).Decode(into); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

func (h *GameEntriesHandler) reply(w http.ResponseWriter, out any, err error, fallback string) {
	if err != nil {
		h.writeError(w, err, fallback)
		return
	}
	utils.JSON(w, http.StatusOK, out)
}

// ─── Public ───────────────────────────────────────────────────────────────

// States handles GET /countries/{countryID}/states.
func (h *GameEntriesHandler) States(w http.ResponseWriter, r *http.Request) {
	list, err := h.entries.States(r.Context(), chi.URLParam(r, "countryID"))
	if err != nil {
		h.writeError(w, err, "could not list states")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]any{"items": list})
}

// ResolveState handles POST /states/resolve: { country_id, place_id } for a
// state picked from Google, answered with our state to send as state_id.
func (h *GameEntriesHandler) ResolveState(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CountryID string `json:"country_id"`
		PlaceID   string `json:"place_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	out, err := h.entries.ResolveState(r.Context(), in.CountryID, in.PlaceID)
	h.reply(w, out, err, "could not look up that state")
}

// ─── Customer ─────────────────────────────────────────────────────────────

// Wallet handles GET /customers/me/game-entries.
func (h *GameEntriesHandler) Wallet(w http.ResponseWriter, r *http.Request) {
	id, ok := customerUUID(w, r)
	if !ok {
		return
	}
	out, err := h.entries.Wallet(r.Context(), id)
	h.reply(w, out, err, "could not read game entries")
}

// Offers handles POST /customers/me/game-entries/offers: what each product
// in a cart would earn this customer.
func (h *GameEntriesHandler) Offers(w http.ResponseWriter, r *http.Request) {
	id, ok := customerUUID(w, r)
	if !ok {
		return
	}
	var in struct {
		ProductIDs []string `json:"product_ids"`
	}
	if !decode(w, r, &in) {
		return
	}
	out, err := h.entries.Offers(r.Context(), id, in.ProductIDs)
	h.reply(w, out, err, "could not work out game entries")
}

// ClaimFree handles POST /customers/me/game-entries/free.
func (h *GameEntriesHandler) ClaimFree(w http.ResponseWriter, r *http.Request) {
	id, ok := customerUUID(w, r)
	if !ok {
		return
	}
	out, err := h.entries.ClaimFree(r.Context(), id)
	h.reply(w, out, err, "could not claim free entries")
}

// MyState handles GET /customers/me/state.
func (h *GameEntriesHandler) MyState(w http.ResponseWriter, r *http.Request) {
	id, ok := customerUUID(w, r)
	if !ok {
		return
	}
	out, err := h.entries.CustomerState(r.Context(), id)
	h.reply(w, out, err, "could not read your state")
}

// SetMyState handles PUT /customers/me/state.
func (h *GameEntriesHandler) SetMyState(w http.ResponseWriter, r *http.Request) {
	id, ok := customerUUID(w, r)
	if !ok {
		return
	}
	var in struct {
		StateID string `json:"state_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	out, err := h.entries.SetCustomerState(r.Context(), id, in.StateID)
	h.reply(w, out, err, "could not save your state")
}

// ─── Seller ───────────────────────────────────────────────────────────────

// Advertising handles GET /sellers/me/advertising.
func (h *GameEntriesHandler) Advertising(w http.ResponseWriter, r *http.Request) {
	out, err := h.ads.Dashboard(r.Context(), sellerID(r))
	h.reply(w, out, err, "could not read advertising")
}

// Buy handles POST /sellers/me/advertising/purchases.
func (h *GameEntriesHandler) Buy(w http.ResponseWriter, r *http.Request) {
	var in services.BuyInput
	if !decode(w, r, &in) {
		return
	}
	if key := r.Header.Get("Idempotency-Key"); key != "" && in.IdempotencyKey == "" {
		in.IdempotencyKey = key
	}
	out, err := h.ads.Buy(r.Context(), sellerID(r), in)
	h.reply(w, out, err, "could not start the purchase")
}

func (h *GameEntriesHandler) purchaseAction(fn func(r *http.Request, id uuid.UUID) (any, error), fallback string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := uuidParam(w, r, "purchaseID", "purchase not found")
		if !ok {
			return
		}
		out, err := fn(r, id)
		h.reply(w, out, err, fallback)
	}
}

// CancelPurchase handles POST /sellers/me/advertising/purchases/{purchaseID}/cancel.
func (h *GameEntriesHandler) CancelPurchase() http.HandlerFunc {
	return h.purchaseAction(func(r *http.Request, id uuid.UUID) (any, error) {
		return h.ads.Cancel(r.Context(), sellerID(r), id)
	}, "could not cancel the purchase")
}

// SyncPurchase handles POST /sellers/me/advertising/purchases/{purchaseID}/sync.
func (h *GameEntriesHandler) SyncPurchase() http.HandlerFunc {
	return h.purchaseAction(func(r *http.Request, id uuid.UUID) (any, error) {
		return h.ads.Sync(r.Context(), sellerID(r), id)
	}, "could not check the payment")
}

// TestPay handles POST /sellers/me/advertising/purchases/{purchaseID}/test-payment.
func (h *GameEntriesHandler) TestPay() http.HandlerFunc {
	return h.purchaseAction(func(r *http.Request, id uuid.UUID) (any, error) {
		return h.ads.TestPay(r.Context(), sellerID(r), id)
	}, "could not confirm the test payment")
}

// SetPromotions handles PUT /sellers/me/advertising/purchases/{purchaseID}/promotions.
func (h *GameEntriesHandler) SetPromotions(w http.ResponseWriter, r *http.Request) {
	id, ok := uuidParam(w, r, "purchaseID", "purchase not found")
	if !ok {
		return
	}
	var in struct {
		Promotions []services.PromotionInput `json:"promotions"`
	}
	if !decode(w, r, &in) {
		return
	}
	out, err := h.ads.SetPromotions(r.Context(), sellerID(r), id, in.Promotions)
	h.reply(w, out, err, "could not save the promoted products")
}

// ReadReminders handles POST /sellers/me/advertising/reminders/read.
func (h *GameEntriesHandler) ReadReminders(w http.ResponseWriter, r *http.Request) {
	err := h.ads.MarkRemindersRead(r.Context(), sellerID(r))
	h.reply(w, map[string]any{"ok": true}, err, "could not mark reminders read")
}

// ─── Admin: advertising ───────────────────────────────────────────────────

// AdminPackages handles GET /admin/advertising/packages.
func (h *GameEntriesHandler) AdminPackages(w http.ResponseWriter, r *http.Request) {
	list, err := h.ads.AdminPackages(r.Context())
	h.reply(w, map[string]any{"items": list}, err, "could not list packages")
}

// CreatePackage handles POST /admin/advertising/packages.
func (h *GameEntriesHandler) CreatePackage(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	var in services.AdPackageInput
	if !decode(w, r, &in) {
		return
	}
	out, err := h.ads.CreatePackage(r.Context(), admin, in)
	h.reply(w, out, err, "could not create the package")
}

// UpdatePackage handles PUT /admin/advertising/packages/{packageID}.
func (h *GameEntriesHandler) UpdatePackage(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "packageID", "package not found")
	if !ok {
		return
	}
	var in services.AdPackageInput
	if !decode(w, r, &in) {
		return
	}
	out, err := h.ads.UpdatePackage(r.Context(), admin, id, in)
	h.reply(w, out, err, "could not update the package")
}

// PackageAction handles POST /admin/advertising/packages/{packageID}/{action}:
// publish, retire or new-version.
func (h *GameEntriesHandler) PackageAction(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "packageID", "package not found")
	if !ok {
		return
	}
	out, err := h.ads.PackageAction(r.Context(), admin, id, chi.URLParam(r, "action"))
	h.reply(w, out, err, "could not change the package")
}

// DeletePackage handles DELETE /admin/advertising/packages/{packageID}.
func (h *GameEntriesHandler) DeletePackage(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "packageID", "package not found")
	if !ok {
		return
	}
	err := h.ads.DeletePackage(r.Context(), admin, id)
	h.reply(w, map[string]any{"ok": true}, err, "could not delete the package")
}

// AdminPurchases handles GET /admin/advertising/purchases.
func (h *GameEntriesHandler) AdminPurchases(w http.ResponseWriter, r *http.Request) {
	list, err := h.ads.AdminPurchases(r.Context(), r.URL.Query().Get("status"))
	h.reply(w, map[string]any{"items": list}, err, "could not list purchases")
}

// ConfirmPurchase handles POST /admin/advertising/purchases/{purchaseID}/confirm.
func (h *GameEntriesHandler) ConfirmPurchase(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "purchaseID", "purchase not found")
	if !ok {
		return
	}
	var in struct {
		Reference string `json:"reference"`
	}
	if !decode(w, r, &in) {
		return
	}
	out, err := h.ads.AdminConfirmPurchase(r.Context(), admin, id, in.Reference)
	h.reply(w, out, err, "could not confirm the purchase")
}

// ResendReminder handles POST /admin/advertising/purchases/{purchaseID}/reminder.
func (h *GameEntriesHandler) ResendReminder(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "purchaseID", "purchase not found")
	if !ok {
		return
	}
	out, err := h.ads.ResendReminder(r.Context(), admin, id)
	h.reply(w, out, err, "could not resend the reminder")
}

// ─── Admin: games, states and entries ─────────────────────────────────────

// Control handles GET /admin/games-control.
func (h *GameEntriesHandler) Control(w http.ResponseWriter, r *http.Request) {
	out, err := h.entries.Control(r.Context())
	h.reply(w, out, err, "could not read the games controls")
}

type reasonBody struct {
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason"`
}

// SetProgram handles PUT /admin/games-control/program.
func (h *GameEntriesHandler) SetProgram(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	var in reasonBody
	if !decode(w, r, &in) {
		return
	}
	out, err := h.entries.SetProgram(r.Context(), admin, in.Enabled, in.Reason)
	h.reply(w, out, err, "could not change the programme")
}

// StopProgram handles POST /admin/games-control/program/stop. It needs no
// password re-entry: an emergency stop must be quick.
func (h *GameEntriesHandler) StopProgram(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	var in reasonBody
	if !decode(w, r, &in) {
		return
	}
	out, err := h.entries.StopProgram(r.Context(), admin, in.Reason)
	h.reply(w, out, err, "could not stop the programme")
}

// SaveJurisdiction handles PUT /admin/games-control/jurisdictions.
func (h *GameEntriesHandler) SaveJurisdiction(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	var in services.JurisdictionInput
	if !decode(w, r, &in) {
		return
	}
	out, err := h.entries.SaveJurisdiction(r.Context(), admin, in)
	h.reply(w, out, err, "could not save the settings")
}

func (h *GameEntriesHandler) stopJurisdiction(stop bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		admin, ok := adminActor(w, r)
		if !ok {
			return
		}
		id, ok := uuidParam(w, r, "jurisdictionID", "settings not found")
		if !ok {
			return
		}
		var in reasonBody
		if !decode(w, r, &in) {
			return
		}
		out, err := h.entries.StopJurisdiction(r.Context(), admin, id, stop, in.Reason)
		h.reply(w, out, err, "could not change the stop")
	}
}

// StopJurisdiction handles POST /admin/games-control/jurisdictions/{jurisdictionID}/stop.
func (h *GameEntriesHandler) StopJurisdiction() http.HandlerFunc { return h.stopJurisdiction(true) }

// LiftJurisdiction handles POST /admin/games-control/jurisdictions/{jurisdictionID}/lift.
func (h *GameEntriesHandler) LiftJurisdiction() http.HandlerFunc { return h.stopJurisdiction(false) }

// AdminStates handles GET /admin/countries/{countryID}/states.
func (h *GameEntriesHandler) AdminStates(w http.ResponseWriter, r *http.Request) {
	list, err := h.entries.AdminStates(r.Context(), chi.URLParam(r, "countryID"))
	h.reply(w, map[string]any{"items": list}, err, "could not list states")
}

// CreateState handles POST /admin/states.
func (h *GameEntriesHandler) CreateState(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	var in services.StateInput
	if !decode(w, r, &in) {
		return
	}
	out, err := h.entries.SaveState(r.Context(), admin, nil, in)
	h.reply(w, out, err, "could not create the state")
}

// UpdateState handles PUT /admin/states/{stateID}.
func (h *GameEntriesHandler) UpdateState(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "stateID", "state not found")
	if !ok {
		return
	}
	var in services.StateInput
	if !decode(w, r, &in) {
		return
	}
	out, err := h.entries.SaveState(r.Context(), admin, &id, in)
	h.reply(w, out, err, "could not update the state")
}

// DeleteState handles DELETE /admin/states/{stateID}.
func (h *GameEntriesHandler) DeleteState(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "stateID", "state not found")
	if !ok {
		return
	}
	err := h.entries.DeleteState(r.Context(), admin, id)
	h.reply(w, map[string]any{"ok": true}, err, "could not delete the state")
}

// SearchCustomers handles GET /admin/customers.
func (h *GameEntriesHandler) SearchCustomers(w http.ResponseWriter, r *http.Request) {
	list, err := h.entries.SearchCustomers(r.Context(), r.URL.Query().Get("q"))
	h.reply(w, map[string]any{"items": list}, err, "could not search customers")
}

// CustomerWallet handles GET /admin/customers/{customerID}/game-entries.
func (h *GameEntriesHandler) CustomerWallet(w http.ResponseWriter, r *http.Request) {
	id, ok := uuidParam(w, r, "customerID", "customer not found")
	if !ok {
		return
	}
	out, err := h.entries.CustomerWallet(r.Context(), id)
	h.reply(w, out, err, "could not read game entries")
}

// Adjust handles POST /admin/customers/{customerID}/game-entries/adjustments.
func (h *GameEntriesHandler) Adjust(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "customerID", "customer not found")
	if !ok {
		return
	}
	var in services.AdjustEntriesInput
	if !decode(w, r, &in) {
		return
	}
	out, err := h.entries.Adjust(r.Context(), admin, id, in)
	h.reply(w, out, err, "could not adjust game entries")
}

// Shortfalls handles GET /admin/game-entries/shortfalls.
func (h *GameEntriesHandler) Shortfalls(w http.ResponseWriter, r *http.Request) {
	list, err := h.entries.Shortfalls(r.Context(), r.URL.Query().Get("status"))
	h.reply(w, map[string]any{"items": list}, err, "could not list review cases")
}

// ResolveShortfall handles POST /admin/game-entries/shortfalls/{caseID}/resolve.
func (h *GameEntriesHandler) ResolveShortfall(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "caseID", "case not found")
	if !ok {
		return
	}
	var in struct {
		Resolution string `json:"resolution"`
	}
	if !decode(w, r, &in) {
		return
	}
	err := h.entries.ResolveShortfall(r.Context(), admin, id, in.Resolution)
	h.reply(w, map[string]any{"ok": true}, err, "could not resolve the case")
}
