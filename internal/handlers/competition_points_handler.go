package handlers

import (
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"myapp/internal/repository"
	"myapp/internal/services"
	"myapp/internal/utils"
)

// CompetitionPointsHandler serves memberships, the competition-points
// wallet, competition entries and the admin controls behind them.
type CompetitionPointsHandler struct {
	cp *services.CompetitionPointsService
}

func NewCompetitionPointsHandler(cp *services.CompetitionPointsService) *CompetitionPointsHandler {
	return &CompetitionPointsHandler{cp: cp}
}

// ChannelHeader is how a native app names itself. A browser sends nothing.
const ChannelHeader = "X-Client-Channel"

func customerUUID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(sellerID(r)) // the JWT subject, whatever the role
	if err != nil {
		utils.Error(w, http.StatusUnauthorized, "unauthorized")
		return uuid.Nil, false
	}
	return id, true
}

func channelOf(r *http.Request) string { return services.Channel(r.Header.Get(ChannelHeader)) }

// writeError answers with a stable code, a safe message and the next action.
func (h *CompetitionPointsHandler) writeError(w http.ResponseWriter, err error, fallback string) {
	var refusal *services.CPRefusal
	var play *services.PlayRefusal
	switch {
	case errors.As(err, &refusal):
		body := map[string]any{"error": refusal.Message, "code": refusal.Code}
		if refusal.NextAction != "" {
			body["next_action"] = refusal.NextAction
		}
		if len(refusal.Details) > 0 {
			body["details"] = refusal.Details
		}
		utils.JSON(w, refusal.Status, body)
	case errors.As(err, &play):
		status := http.StatusConflict
		if play.Code == services.PlayNotEligible {
			status = http.StatusForbidden
		}
		body := map[string]any{"error": play.Message, "code": play.Code}
		if len(play.Details) > 0 {
			body["details"] = play.Details
		}
		utils.JSON(w, status, body)
	case errors.Is(err, services.ErrInvalidCPConfig):
		utils.JSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "code": "INVALID_CONFIGURATION"})
	case errors.Is(err, services.ErrCompetitionNotFound):
		utils.Error(w, http.StatusNotFound, "competition not found")
	case errors.Is(err, repository.ErrCustomerNotFound):
		utils.Error(w, http.StatusNotFound, "customer not found")
	default:
		log.Printf("competition points: %s: %v", fallback, err)
		utils.Error(w, http.StatusInternalServerError, fallback)
	}
}

// ─── Customer ────────────────────────────────────────────────────────────

// Eligibility handles GET /customers/me/eligibility.
func (h *CompetitionPointsHandler) Eligibility(w http.ResponseWriter, r *http.Request) {
	id, ok := customerUUID(w, r)
	if !ok {
		return
	}
	out, err := h.cp.Eligibility(r.Context(), id, channelOf(r))
	if err != nil {
		h.writeError(w, err, "could not check availability")
		return
	}
	utils.JSON(w, http.StatusOK, out)
}

// Offers handles GET /customers/me/membership/offers.
func (h *CompetitionPointsHandler) Offers(w http.ResponseWriter, r *http.Request) {
	id, ok := customerUUID(w, r)
	if !ok {
		return
	}
	out, err := h.cp.Offers(r.Context(), id, channelOf(r))
	if err != nil {
		h.writeError(w, err, "could not list memberships")
		return
	}
	utils.JSON(w, http.StatusOK, out)
}

// Membership handles GET /customers/me/membership.
func (h *CompetitionPointsHandler) Membership(w http.ResponseWriter, r *http.Request) {
	id, ok := customerUUID(w, r)
	if !ok {
		return
	}
	out, err := h.cp.CurrentStatus(r.Context(), id)
	if err != nil {
		h.writeError(w, err, "could not read membership")
		return
	}
	utils.JSON(w, http.StatusOK, out)
}

// MemberDiscount handles GET /customers/me/member-discount: the product
// discount the customer gets on a new order now, for showing in the bag.
func (h *CompetitionPointsHandler) MemberDiscount(w http.ResponseWriter, r *http.Request) {
	id, ok := customerUUID(w, r)
	if !ok {
		return
	}
	out, err := h.cp.MemberDiscount(r.Context(), id)
	if err != nil {
		h.writeError(w, err, "could not read the member discount")
		return
	}
	utils.JSON(w, http.StatusOK, out)
}

// Join handles POST /customers/me/membership/checkout
// {"offer_id": "...", "renewal_consent": true} with an Idempotency-Key.
func (h *CompetitionPointsHandler) Join(w http.ResponseWriter, r *http.Request) {
	id, ok := customerUUID(w, r)
	if !ok {
		return
	}
	var body struct {
		OfferID        string `json:"offer_id"`
		RenewalConsent bool   `json:"renewal_consent"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	offerID, err := uuid.Parse(body.OfferID)
	if err != nil {
		utils.Error(w, http.StatusBadRequest, "offer_id is required")
		return
	}
	out, err := h.cp.Join(r.Context(), id, channelOf(r), services.JoinInput{
		OfferID: offerID, RenewalConsent: body.RenewalConsent, IdempotencyKey: r.Header.Get(IdempotencyKeyHeader),
	})
	if err != nil {
		h.writeError(w, err, "could not start membership checkout")
		return
	}
	utils.JSON(w, http.StatusCreated, out)
}

// CancelMembership handles POST /customers/me/membership/cancel.
func (h *CompetitionPointsHandler) CancelMembership(w http.ResponseWriter, r *http.Request) {
	id, ok := customerUUID(w, r)
	if !ok {
		return
	}
	out, err := h.cp.Cancel(r.Context(), id)
	if err != nil {
		h.writeError(w, err, "could not cancel membership")
		return
	}
	utils.JSON(w, http.StatusOK, out)
}

// TestPay handles POST /customers/me/membership/test-payment (sandbox only).
func (h *CompetitionPointsHandler) TestPay(w http.ResponseWriter, r *http.Request) {
	id, ok := customerUUID(w, r)
	if !ok {
		return
	}
	out, err := h.cp.TestPay(r.Context(), id)
	if err != nil {
		h.writeError(w, err, "could not confirm test payment")
		return
	}
	utils.JSON(w, http.StatusOK, out)
}

// SyncMembership handles POST /customers/me/membership/sync: after Stripe's
// checkout, confirms the payment with Stripe and returns the membership.
func (h *CompetitionPointsHandler) SyncMembership(w http.ResponseWriter, r *http.Request) {
	id, ok := customerUUID(w, r)
	if !ok {
		return
	}
	out, err := h.cp.SyncMembership(r.Context(), id)
	if err != nil {
		h.writeError(w, err, "could not check the membership payment")
		return
	}
	utils.JSON(w, http.StatusOK, out)
}

// Wallet handles GET /customers/me/competition-points.
func (h *CompetitionPointsHandler) Wallet(w http.ResponseWriter, r *http.Request) {
	id, ok := customerUUID(w, r)
	if !ok {
		return
	}
	out, err := h.cp.Wallet(r.Context(), id)
	if err != nil {
		h.writeError(w, err, "could not read competition points")
		return
	}
	utils.JSON(w, http.StatusOK, out)
}

// Quote handles GET /competitions/{id}/entry-quote.
func (h *CompetitionPointsHandler) Quote(w http.ResponseWriter, r *http.Request) {
	customer, ok := customerUUID(w, r)
	if !ok {
		return
	}
	id, ok := competitionID(w, r)
	if !ok {
		return
	}
	out, err := h.cp.Quote(r.Context(), customer, channelOf(r), id)
	if err != nil {
		h.writeError(w, err, "could not quote the entry")
		return
	}
	utils.JSON(w, http.StatusOK, out)
}

// Reserve handles POST /competitions/{id}/entries {"reference": "..."} with
// an Idempotency-Key.
func (h *CompetitionPointsHandler) Reserve(w http.ResponseWriter, r *http.Request) {
	customer, ok := customerUUID(w, r)
	if !ok {
		return
	}
	id, ok := competitionID(w, r)
	if !ok {
		return
	}
	var req services.ReserveRequest
	if !decodeBody(w, r, &req) {
		return
	}
	req.ClientRequestID = r.Header.Get(IdempotencyKeyHeader)
	out, err := h.cp.Reserve(r.Context(), customer, channelOf(r), id, req)
	if err != nil {
		h.writeError(w, err, "could not reserve the entry")
		return
	}
	status := http.StatusCreated
	if out.Replayed {
		status = http.StatusOK
	}
	utils.JSON(w, status, out)
}

// StartEntry handles POST /competitions/{id}/entries/{entryID}/start.
func (h *CompetitionPointsHandler) StartEntry(w http.ResponseWriter, r *http.Request) {
	customer, ok := customerUUID(w, r)
	if !ok {
		return
	}
	id, ok := competitionID(w, r)
	if !ok {
		return
	}
	entryID, ok := uuidParam(w, r, "entryID", "entry not found")
	if !ok {
		return
	}
	var body struct {
		RiskMetadata map[string]any `json:"risk_metadata"`
	}
	if r.ContentLength > 0 && !decodeBody(w, r, &body) {
		return
	}
	view, err := h.cp.StartEntry(r.Context(), customer, id, entryID, r.Header.Get(IdempotencyKeyHeader), body.RiskMetadata)
	if err != nil {
		h.writeError(w, err, "could not start the attempt")
		return
	}
	status := http.StatusCreated
	if view.Replayed {
		status = http.StatusOK
	}
	utils.JSON(w, status, view)
}

// CancelEntry handles POST /competitions/{id}/entries/{entryID}/cancel.
func (h *CompetitionPointsHandler) CancelEntry(w http.ResponseWriter, r *http.Request) {
	customer, ok := customerUUID(w, r)
	if !ok {
		return
	}
	entryID, ok := uuidParam(w, r, "entryID", "entry not found")
	if !ok {
		return
	}
	out, err := h.cp.CancelEntry(r.Context(), customer, entryID)
	if err != nil {
		h.writeError(w, err, "could not cancel the entry")
		return
	}
	utils.JSON(w, http.StatusOK, out)
}

// ─── Admin ───────────────────────────────────────────────────────────────

func (h *CompetitionPointsHandler) list(w http.ResponseWriter, items any, err error, fallback string) {
	if err != nil {
		h.writeError(w, err, fallback)
		return
	}
	utils.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// Settings handles GET /admin/competition-points/settings.
func (h *CompetitionPointsHandler) Settings(w http.ResponseWriter, r *http.Request) {
	utils.JSON(w, http.StatusOK, h.cp.Settings())
}

// AdminOffers handles GET /admin/competition-points/offers.
func (h *CompetitionPointsHandler) AdminOffers(w http.ResponseWriter, r *http.Request) {
	items, err := h.cp.AdminOffers(r.Context())
	h.list(w, items, err, "could not list offers")
}

// AdminMembers handles GET /admin/competition-points/members?state=&tier=&country_id=&q=&limit=&offset=.
func (h *CompetitionPointsHandler) AdminMembers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := repository.MemberFilter{State: q.Get("state"), TierCode: q.Get("tier"), Query: q.Get("q")}
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	f.Offset, _ = strconv.Atoi(q.Get("offset"))
	if f.Offset < 0 {
		f.Offset = 0
	}
	if raw := q.Get("country_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			utils.Error(w, http.StatusBadRequest, "country_id is not valid")
			return
		}
		f.CountryID = &id
	}
	out, err := h.cp.AdminMembers(r.Context(), f)
	if err != nil {
		h.writeError(w, err, "could not list members")
		return
	}
	utils.JSON(w, http.StatusOK, out)
}

// CreateOffer handles POST /admin/competition-points/offers.
func (h *CompetitionPointsHandler) CreateOffer(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	var in services.OfferInput
	if !decodeBody(w, r, &in) {
		return
	}
	out, err := h.cp.CreateOffer(r.Context(), admin, in)
	if err != nil {
		h.writeError(w, err, "could not create offer")
		return
	}
	utils.JSON(w, http.StatusCreated, out)
}

// UpdateOffer handles PUT /admin/competition-points/offers/{offerID}.
func (h *CompetitionPointsHandler) UpdateOffer(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "offerID", "offer not found")
	if !ok {
		return
	}
	var in services.OfferInput
	if !decodeBody(w, r, &in) {
		return
	}
	out, err := h.cp.UpdateOffer(r.Context(), admin, id, in)
	if err != nil {
		h.writeError(w, err, "could not update offer")
		return
	}
	utils.JSON(w, http.StatusOK, out)
}

// OfferAction handles POST /admin/competition-points/offers/{offerID}/{action}.
func (h *CompetitionPointsHandler) OfferAction(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "offerID", "offer not found")
	if !ok {
		return
	}
	out, err := h.cp.OfferTransition(r.Context(), admin, id, chi.URLParam(r, "action"))
	if err != nil {
		h.writeError(w, err, "could not change offer")
		return
	}
	utils.JSON(w, http.StatusOK, out)
}

// DeleteOffer handles DELETE /admin/competition-points/offers/{offerID}.
func (h *CompetitionPointsHandler) DeleteOffer(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "offerID", "offer not found")
	if !ok {
		return
	}
	if err := h.cp.DeleteOffer(r.Context(), admin, id); err != nil {
		h.writeError(w, err, "could not delete offer")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// AdminPolicies handles GET /admin/competition-points/policies.
func (h *CompetitionPointsHandler) AdminPolicies(w http.ResponseWriter, r *http.Request) {
	items, err := h.cp.AdminPolicies(r.Context())
	h.list(w, items, err, "could not list reward policies")
}

// CreatePolicy handles POST /admin/competition-points/policies.
func (h *CompetitionPointsHandler) CreatePolicy(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	var in services.PolicyInput
	if !decodeBody(w, r, &in) {
		return
	}
	out, err := h.cp.CreatePolicy(r.Context(), admin, in)
	if err != nil {
		h.writeError(w, err, "could not create reward policy")
		return
	}
	utils.JSON(w, http.StatusCreated, out)
}

// UpdatePolicy handles PUT /admin/competition-points/policies/{policyID}.
func (h *CompetitionPointsHandler) UpdatePolicy(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "policyID", "reward policy not found")
	if !ok {
		return
	}
	var in services.PolicyInput
	if !decodeBody(w, r, &in) {
		return
	}
	out, err := h.cp.UpdatePolicy(r.Context(), admin, id, in)
	if err != nil {
		h.writeError(w, err, "could not update reward policy")
		return
	}
	utils.JSON(w, http.StatusOK, out)
}

// PolicyAction handles POST /admin/competition-points/policies/{policyID}/{action}.
func (h *CompetitionPointsHandler) PolicyAction(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "policyID", "reward policy not found")
	if !ok {
		return
	}
	out, err := h.cp.PolicyTransition(r.Context(), admin, id, chi.URLParam(r, "action"))
	if err != nil {
		h.writeError(w, err, "could not change reward policy")
		return
	}
	utils.JSON(w, http.StatusOK, out)
}

// DeletePolicy handles DELETE /admin/competition-points/policies/{policyID}.
func (h *CompetitionPointsHandler) DeletePolicy(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "policyID", "reward policy not found")
	if !ok {
		return
	}
	if err := h.cp.DeletePolicy(r.Context(), admin, id); err != nil {
		h.writeError(w, err, "could not delete reward policy")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PreviewPolicy handles POST /admin/competition-points/policies/preview
// {"policy": {...}, "eligible_minor": 5000, "member_bonus": 100, "currency": "AUD"}.
func (h *CompetitionPointsHandler) PreviewPolicy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Policy        services.PolicyInput `json:"policy"`
		EligibleMinor int64                `json:"eligible_minor"`
		MemberBonus   int64                `json:"member_bonus"`
		Currency      string               `json:"currency"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	out, err := h.cp.PreviewPolicy(body.Policy, body.EligibleMinor, body.MemberBonus, body.Currency)
	if err != nil {
		h.writeError(w, err, "could not preview")
		return
	}
	utils.JSON(w, http.StatusOK, out)
}

// AdminApprovals handles GET /admin/competition-points/approvals.
func (h *CompetitionPointsHandler) AdminApprovals(w http.ResponseWriter, r *http.Request) {
	items, err := h.cp.AdminApprovals(r.Context())
	h.list(w, items, err, "could not list approvals")
}

// RequestApproval handles POST /admin/competition-points/approvals.
func (h *CompetitionPointsHandler) RequestApproval(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	var in services.ApprovalInput
	if !decodeBody(w, r, &in) {
		return
	}
	out, err := h.cp.RequestApproval(r.Context(), admin, in)
	if err != nil {
		h.writeError(w, err, "could not record the approval request")
		return
	}
	utils.JSON(w, http.StatusCreated, out)
}

// ApprovalAction handles POST /admin/competition-points/approvals/{approvalID}/{action}
// where action is approve, reject or revoke ({"reason": "..."}).
func (h *CompetitionPointsHandler) ApprovalAction(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "approvalID", "approval not found")
	if !ok {
		return
	}
	var (
		out any
		err error
	)
	switch chi.URLParam(r, "action") {
	case "approve":
		out, err = h.cp.DecideApproval(r.Context(), admin, id, true)
	case "reject":
		out, err = h.cp.DecideApproval(r.Context(), admin, id, false)
	case "revoke":
		var body struct {
			Reason string `json:"reason"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		out, err = h.cp.RevokeApproval(r.Context(), admin, id, body.Reason)
	default:
		utils.Error(w, http.StatusNotFound, "unknown action")
		return
	}
	if err != nil {
		h.writeError(w, err, "could not change the approval")
		return
	}
	utils.JSON(w, http.StatusOK, out)
}

// AdminStops handles GET /admin/competition-points/stops.
func (h *CompetitionPointsHandler) AdminStops(w http.ResponseWriter, r *http.Request) {
	items, err := h.cp.AdminStops(r.Context())
	h.list(w, items, err, "could not list stops")
}

// ActivateStop handles POST /admin/competition-points/stops
// {"scope": "competition_entries", "country_id": null, "reason": "..."}.
func (h *CompetitionPointsHandler) ActivateStop(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	var body struct {
		Scope     string  `json:"scope"`
		CountryID *string `json:"country_id"`
		Reason    string  `json:"reason"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	out, err := h.cp.ActivateStop(r.Context(), admin, body.Scope, body.CountryID, body.Reason)
	if err != nil {
		h.writeError(w, err, "could not activate the stop")
		return
	}
	utils.JSON(w, http.StatusCreated, out)
}

// LiftStop handles POST /admin/competition-points/stops/{stopID}/lift {"reason": "..."}.
func (h *CompetitionPointsHandler) LiftStop(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "stopID", "stop not found")
	if !ok {
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	out, err := h.cp.LiftStop(r.Context(), admin, id, body.Reason)
	if err != nil {
		h.writeError(w, err, "could not lift the stop")
		return
	}
	utils.JSON(w, http.StatusOK, out)
}

// AdminCases handles GET /admin/competition-points/cases?status=open.
func (h *CompetitionPointsHandler) AdminCases(w http.ResponseWriter, r *http.Request) {
	items, err := h.cp.AdminCases(r.Context(), r.URL.Query().Get("status"))
	h.list(w, items, err, "could not list cases")
}

// ResolveCase handles POST /admin/competition-points/cases/{caseID}/resolve {"resolution": "..."}.
func (h *CompetitionPointsHandler) ResolveCase(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "caseID", "case not found")
	if !ok {
		return
	}
	var body struct {
		Resolution string `json:"resolution"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if err := h.cp.ResolveCase(r.Context(), admin, id, body.Resolution); err != nil {
		h.writeError(w, err, "could not resolve the case")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Reconcile handles GET /admin/competition-points/reconciliation.
func (h *CompetitionPointsHandler) Reconcile(w http.ResponseWriter, r *http.Request) {
	out, err := h.cp.Reconcile(r.Context())
	if err != nil {
		h.writeError(w, err, "could not reconcile")
		return
	}
	utils.JSON(w, http.StatusOK, out)
}

// RunNow handles POST /admin/competition-points/run: one pass of the job.
func (h *CompetitionPointsHandler) RunNow(w http.ResponseWriter, r *http.Request) {
	out, err := h.cp.RunOnce(r.Context())
	if err != nil {
		h.writeError(w, err, "could not run the job")
		return
	}
	utils.JSON(w, http.StatusOK, out)
}

// AdminCustomer handles GET /admin/competition-points/customers/{customerID}.
func (h *CompetitionPointsHandler) AdminCustomer(w http.ResponseWriter, r *http.Request) {
	id, ok := uuidParam(w, r, "customerID", "customer not found")
	if !ok {
		return
	}
	out, err := h.cp.AdminCustomer(r.Context(), id)
	if err != nil {
		h.writeError(w, err, "could not read the customer")
		return
	}
	utils.JSON(w, http.StatusOK, out)
}
