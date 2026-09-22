package handlers

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"myapp/internal/middleware"
	"myapp/internal/services"
	"myapp/internal/utils"
)

// CompetitionHandler serves skill competitions: the public list, detail and
// live leaderboard, official attempts and prize claims for customers, and the
// admin lifecycle from draft to winners.
//
// Official scores are submitted through the normal game submit endpoint;
// the session knows it belongs to a competition.
type CompetitionHandler struct {
	competitions *services.CompetitionService
}

func NewCompetitionHandler(competitionService *services.CompetitionService) *CompetitionHandler {
	return &CompetitionHandler{competitions: competitionService}
}

func competitionID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	return uuidParam(w, r, "id", "competition not found")
}

func uuidParam(w http.ResponseWriter, r *http.Request, name, notFound string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		utils.Error(w, http.StatusNotFound, notFound)
		return uuid.Nil, false
	}
	return id, true
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

// adminActor reads the admin id set by RequireAuth, plus request details for
// the audit log.
func adminActor(w http.ResponseWriter, r *http.Request) (services.AdminActor, bool) {
	raw, _ := r.Context().Value(middleware.AdminIDContextKey).(string)
	id, err := uuid.Parse(raw)
	if err != nil {
		utils.Error(w, http.StatusUnauthorized, "unauthorized")
		return services.AdminActor{}, false
	}
	return services.AdminActor{ID: id, IP: r.RemoteAddr, UserAgent: r.UserAgent()}, true
}

// ─── Public and customer ──────────────────────────────────────────────────

// List handles GET /competitions.
func (h *CompetitionHandler) List(w http.ResponseWriter, r *http.Request) {
	list, err := h.competitions.ListCompetitions(r.Context(), actorFromContext(r))
	if err != nil {
		h.writeError(w, err, "could not list competitions")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]any{"items": list})
}

// Get handles GET /competitions/{id}.
func (h *CompetitionHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := competitionID(w, r)
	if !ok {
		return
	}
	view, err := h.competitions.GetCompetition(r.Context(), id, actorFromContext(r))
	if err != nil {
		h.writeError(w, err, "could not get competition")
		return
	}
	utils.JSON(w, http.StatusOK, view)
}

// Leaderboard handles GET /competitions/{id}/leaderboard.
func (h *CompetitionHandler) Leaderboard(w http.ResponseWriter, r *http.Request) {
	id, ok := competitionID(w, r)
	if !ok {
		return
	}
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			utils.Error(w, http.StatusBadRequest, "limit must be a positive number")
			return
		}
		limit = n
	}
	board, err := h.competitions.Leaderboard(r.Context(), id, actorFromContext(r), limit)
	if err != nil {
		h.writeError(w, err, "could not get leaderboard")
		return
	}
	utils.JSON(w, http.StatusOK, board)
}

// StartAttempt handles POST /competitions/{id}/attempts.
func (h *CompetitionHandler) StartAttempt(w http.ResponseWriter, r *http.Request) {
	id, ok := competitionID(w, r)
	if !ok {
		return
	}
	view, err := h.competitions.StartAttempt(r.Context(), id, actorFromContext(r))
	if err != nil {
		h.writeError(w, err, "could not start attempt")
		return
	}
	utils.JSON(w, http.StatusCreated, view)
}

// ClaimPrize handles POST /competitions/{id}/claim.
func (h *CompetitionHandler) ClaimPrize(w http.ResponseWriter, r *http.Request) {
	id, ok := competitionID(w, r)
	if !ok {
		return
	}
	var in services.ClaimPrizeInput
	if !decodeBody(w, r, &in) {
		return
	}
	claim, err := h.competitions.ClaimPrize(r.Context(), id, actorFromContext(r), in)
	if err != nil {
		h.writeError(w, err, "could not claim prize")
		return
	}
	utils.JSON(w, http.StatusOK, claim)
}

// ─── Admin ────────────────────────────────────────────────────────────────

// AdminList handles GET /admin/competitions.
func (h *CompetitionHandler) AdminList(w http.ResponseWriter, r *http.Request) {
	list, err := h.competitions.AdminList(r.Context())
	if err != nil {
		h.writeError(w, err, "could not list competitions")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]any{"items": list})
}

// AdminGet handles GET /admin/competitions/{id}.
func (h *CompetitionHandler) AdminGet(w http.ResponseWriter, r *http.Request) {
	id, ok := competitionID(w, r)
	if !ok {
		return
	}
	view, err := h.competitions.AdminGet(r.Context(), id)
	if err != nil {
		h.writeError(w, err, "could not get competition")
		return
	}
	utils.JSON(w, http.StatusOK, view)
}

// Create handles POST /admin/competitions.
func (h *CompetitionHandler) Create(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	var in services.CompetitionInput
	if !decodeBody(w, r, &in) {
		return
	}
	view, err := h.competitions.CreateCompetition(r.Context(), admin, in)
	if err != nil {
		h.writeError(w, err, "could not create competition")
		return
	}
	utils.JSON(w, http.StatusCreated, view)
}

// Update handles PUT /admin/competitions/{id}.
func (h *CompetitionHandler) Update(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	id, ok := competitionID(w, r)
	if !ok {
		return
	}
	var in services.CompetitionInput
	if !decodeBody(w, r, &in) {
		return
	}
	view, err := h.competitions.UpdateCompetition(r.Context(), admin, id, in)
	if err != nil {
		h.writeError(w, err, "could not update competition")
		return
	}
	utils.JSON(w, http.StatusOK, view)
}

// SetReserve handles PUT /admin/competitions/{id}/prize-reserve.
func (h *CompetitionHandler) SetReserve(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	id, ok := competitionID(w, r)
	if !ok {
		return
	}
	var in services.ReserveInput
	if !decodeBody(w, r, &in) {
		return
	}
	reserve, err := h.competitions.SetReserve(r.Context(), admin, id, in)
	if err != nil {
		h.writeError(w, err, "could not set prize reserve")
		return
	}
	utils.JSON(w, http.StatusOK, reserve)
}

// FundReserve handles POST /admin/competitions/{id}/prize-reserve/fund.
func (h *CompetitionHandler) FundReserve(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	id, ok := competitionID(w, r)
	if !ok {
		return
	}
	var in struct {
		EvidenceReference string `json:"evidence_reference"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	reserve, err := h.competitions.FundReserve(r.Context(), admin, id, in.EvidenceReference)
	if err != nil {
		h.writeError(w, err, "could not fund prize reserve")
		return
	}
	utils.JSON(w, http.StatusOK, reserve)
}

// adminAction runs a body-less admin action that returns the competition.
func (h *CompetitionHandler) adminAction(fn func(*services.CompetitionService, *http.Request, services.AdminActor, uuid.UUID) (any, error), fallback string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		admin, ok := adminActor(w, r)
		if !ok {
			return
		}
		id, ok := competitionID(w, r)
		if !ok {
			return
		}
		out, err := fn(h.competitions, r, admin, id)
		if err != nil {
			h.writeError(w, err, fallback)
			return
		}
		if out == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		utils.JSON(w, http.StatusOK, out)
	}
}

// Schedule handles POST /admin/competitions/{id}/schedule.
func (h *CompetitionHandler) Schedule() http.HandlerFunc {
	return h.adminAction(func(s *services.CompetitionService, r *http.Request, a services.AdminActor, id uuid.UUID) (any, error) {
		return s.ScheduleCompetition(r.Context(), a, id)
	}, "could not schedule competition")
}

// Freeze handles POST /admin/competitions/{id}/freeze.
func (h *CompetitionHandler) Freeze() http.HandlerFunc {
	return h.adminAction(func(s *services.CompetitionService, r *http.Request, a services.AdminActor, id uuid.UUID) (any, error) {
		return s.FreezeCompetition(r.Context(), a, id)
	}, "could not freeze competition")
}

// Cancel handles POST /admin/competitions/{id}/cancel.
func (h *CompetitionHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	var in services.CancelInput
	if !decodeBody(w, r, &in) {
		return
	}
	h.adminAction(func(s *services.CompetitionService, r *http.Request, a services.AdminActor, id uuid.UUID) (any, error) {
		return s.CancelCompetition(r.Context(), a, id, in)
	}, "could not cancel competition")(w, r)
}

// Finalise handles POST /admin/competitions/{id}/finalise. The body is
// optional and carries a playoff result when a tie decides a prize.
func (h *CompetitionHandler) Finalise(w http.ResponseWriter, r *http.Request) {
	var in services.FinaliseInput
	if r.ContentLength != 0 && !decodeBody(w, r, &in) {
		return
	}
	h.adminAction(func(s *services.CompetitionService, r *http.Request, a services.AdminActor, id uuid.UUID) (any, error) {
		return s.FinaliseCompetition(r.Context(), a, id, in)
	}, "could not finalise competition")(w, r)
}

// ReviewQueue handles GET /admin/competitions/{id}/review-queue.
func (h *CompetitionHandler) ReviewQueue(w http.ResponseWriter, r *http.Request) {
	id, ok := competitionID(w, r)
	if !ok {
		return
	}
	items, err := h.competitions.ReviewQueue(r.Context(), id)
	if err != nil {
		h.writeError(w, err, "could not list review queue")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// Review handles POST /admin/competitions/{id}/submissions/{submissionID}/review.
func (h *CompetitionHandler) Review(w http.ResponseWriter, r *http.Request) {
	submissionID, ok := uuidParam(w, r, "submissionID", "score submission not found")
	if !ok {
		return
	}
	var in services.ReviewInput
	if !decodeBody(w, r, &in) {
		return
	}
	h.adminAction(func(s *services.CompetitionService, r *http.Request, a services.AdminActor, id uuid.UUID) (any, error) {
		return nil, s.ReviewSubmission(r.Context(), a, id, submissionID, in)
	}, "could not review score")(w, r)
}

// AdminLeaderboard handles GET /admin/competitions/{id}/leaderboard.
func (h *CompetitionHandler) AdminLeaderboard(w http.ResponseWriter, r *http.Request) {
	id, ok := competitionID(w, r)
	if !ok {
		return
	}
	rows, err := h.competitions.AdminLeaderboard(r.Context(), id)
	if err != nil {
		h.writeError(w, err, "could not get leaderboard")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]any{"items": rows})
}

// Winners handles GET /admin/competitions/{id}/winners.
func (h *CompetitionHandler) Winners(w http.ResponseWriter, r *http.Request) {
	id, ok := competitionID(w, r)
	if !ok {
		return
	}
	winners, err := h.competitions.AdminWinners(r.Context(), id)
	if err != nil {
		h.writeError(w, err, "could not list winners")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]any{"items": winners})
}

// winnerAction runs an action on one winner.
func (h *CompetitionHandler) winnerAction(w http.ResponseWriter, r *http.Request, fn func(services.AdminActor, uuid.UUID, uuid.UUID) error, fallback string) {
	winnerID, ok := uuidParam(w, r, "winnerID", "winner not found")
	if !ok {
		return
	}
	h.adminAction(func(_ *services.CompetitionService, _ *http.Request, a services.AdminActor, id uuid.UUID) (any, error) {
		return nil, fn(a, id, winnerID)
	}, fallback)(w, r)
}

// ValidateWinner handles POST /admin/competitions/{id}/winners/{winnerID}/validate.
func (h *CompetitionHandler) ValidateWinner(w http.ResponseWriter, r *http.Request) {
	h.winnerAction(w, r, func(a services.AdminActor, id, winnerID uuid.UUID) error {
		return h.competitions.ValidateWinner(r.Context(), a, id, winnerID)
	}, "could not validate winner")
}

// DisqualifyWinner handles POST /admin/competitions/{id}/winners/{winnerID}/disqualify.
func (h *CompetitionHandler) DisqualifyWinner(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reason string `json:"reason"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	h.winnerAction(w, r, func(a services.AdminActor, id, winnerID uuid.UUID) error {
		return h.competitions.DisqualifyWinner(r.Context(), a, id, winnerID, in.Reason)
	}, "could not disqualify winner")
}

// MarkUnclaimed handles POST /admin/competitions/{id}/winners/{winnerID}/unclaimed.
func (h *CompetitionHandler) MarkUnclaimed(w http.ResponseWriter, r *http.Request) {
	h.winnerAction(w, r, func(a services.AdminActor, id, winnerID uuid.UUID) error {
		return h.competitions.MarkUnclaimed(r.Context(), a, id, winnerID)
	}, "could not mark winner unclaimed")
}

// AdvanceClaim handles POST /admin/competitions/{id}/claims/{claimID}/{verify|fulfil}.
func (h *CompetitionHandler) AdvanceClaim(to string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claimID, ok := uuidParam(w, r, "claimID", "prize claim not found")
		if !ok {
			return
		}
		h.adminAction(func(s *services.CompetitionService, r *http.Request, a services.AdminActor, id uuid.UUID) (any, error) {
			return s.AdvanceClaim(r.Context(), a, id, claimID, to)
		}, "could not update prize claim")(w, r)
	}
}

// writeError maps service errors onto HTTP status codes.
func (h *CompetitionHandler) writeError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, services.ErrCompetitionNotFound):
		utils.Error(w, http.StatusNotFound, "competition not found")
	case errors.Is(err, services.ErrWinnerNotFound), errors.Is(err, services.ErrClaimNotFound),
		errors.Is(err, services.ErrGameNotFound):
		utils.Error(w, http.StatusNotFound, err.Error())
	case errors.Is(err, services.ErrCustomerRequired):
		utils.Error(w, http.StatusUnauthorized, err.Error())
	case errors.Is(err, services.ErrNotEligible):
		utils.Error(w, http.StatusForbidden, err.Error())
	case errors.Is(err, services.ErrInvalidCompetition), errors.Is(err, services.ErrInvalidReview),
		errors.Is(err, services.ErrInvalidClaim), errors.Is(err, services.ErrInvalidReserve):
		utils.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, services.ErrCompetitionNotLive), errors.Is(err, services.ErrCompetitionClosed),
		errors.Is(err, services.ErrCompetitionLocked), errors.Is(err, services.ErrCompetitionState),
		errors.Is(err, services.ErrScheduleBlocked), errors.Is(err, services.ErrAttemptLimit),
		errors.Is(err, services.ErrAttemptBusy), errors.Is(err, services.ErrTieAtCutoff),
		errors.Is(err, services.ErrUnresolvedScores), errors.Is(err, services.ErrWinnerState),
		errors.Is(err, services.ErrClaimState), errors.Is(err, services.ErrReserveLocked):
		utils.Error(w, http.StatusConflict, err.Error())
	default:
		log.Printf("competition handler: %s: %v", fallback, err)
		utils.Error(w, http.StatusInternalServerError, fallback)
	}
}
