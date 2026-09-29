package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"myapp/internal/middleware"
	"myapp/internal/services"
	"myapp/internal/utils"
)

// IdempotencyKeyHeader carries the client's key for one intended play
// (Progressive Prize spec §4.2).
const IdempotencyKeyHeader = "Idempotency-Key"

// playRequest reads the idempotency key from the header, or from the body's
// client_request_id, plus a little device context for fraud review.
func playRequest(r *http.Request) services.PlayRequest {
	var body struct {
		ClientRequestID string `json:"client_request_id"`
	}
	if r.ContentLength != 0 {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	key := strings.TrimSpace(r.Header.Get(IdempotencyKeyHeader))
	if key == "" {
		key = body.ClientRequestID
	}
	meta := map[string]any{}
	if ua := r.UserAgent(); ua != "" {
		if len(ua) > 256 {
			ua = ua[:256]
		}
		meta["user_agent"] = ua
	}
	if platform := r.Header.Get("X-App-Platform"); platform != "" && len(platform) <= 32 {
		meta["platform"] = platform
	}
	if device := middleware.DeviceID(r); device != "" {
		meta["device_id"] = device
	}
	if network := networkPrefix(r.RemoteAddr); network != "" {
		meta["network"] = network
	}
	return services.PlayRequest{ClientRequestID: key, RiskMetadata: meta}
}

// networkPrefix keeps only the network part of the caller's address (/24 for
// IPv4, /48 for IPv6): enough to see many accounts playing from one place,
// without storing anyone's exact address.
func networkPrefix(remote string) string {
	host := remote
	if h, _, err := net.SplitHostPort(remote); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	switch {
	case ip == nil:
		return ""
	case ip.To4() != nil:
		return ip.Mask(net.CIDRMask(24, 32)).String() + "/24"
	default:
		return ip.Mask(net.CIDRMask(48, 128)).String() + "/48"
	}
}

// Play handles POST /competitions/{id}/plays: one idempotent play.
func (h *CompetitionHandler) Play(w http.ResponseWriter, r *http.Request) {
	id, ok := competitionID(w, r)
	if !ok {
		return
	}
	view, err := h.competitions.Play(r.Context(), id, actorFromContext(r), playRequest(r))
	if err != nil {
		h.writePlayError(w, err)
		return
	}
	status := http.StatusCreated
	if view.Replayed {
		status = http.StatusOK
	}
	utils.JSON(w, status, view)
}

// writePlayError is writeError for plays: a refusal carries its machine code
// and details, and anything unexpected is PLAY_TRANSACTION_FAILED — the
// transaction rolled back, so nothing was charged.
func (h *CompetitionHandler) writePlayError(w http.ResponseWriter, err error) {
	var refusal *services.PlayRefusal
	if !errors.As(err, &refusal) {
		switch {
		case errors.Is(err, services.ErrCompetitionNotFound), errors.Is(err, services.ErrGameNotFound),
			errors.Is(err, services.ErrCustomerRequired):
			h.writeError(w, err, "could not play")
		default:
			log.Printf("competition play failed: %v", err)
			utils.JSON(w, http.StatusInternalServerError, map[string]any{
				"error": "the play could not be completed; you have not been charged",
				"code":  services.PlayTransactionFailed,
			})
		}
		return
	}
	status := http.StatusBadRequest
	switch refusal.Code {
	case services.PlayLimitReached, services.PlayIdempotencyConflict, services.PlayPrizeCapReached, services.PlayBusy:
		status = http.StatusConflict
	case services.PlayInsufficientPoints:
		status = http.StatusUnprocessableEntity
	case services.PlayNotEligible:
		status = http.StatusForbidden
	}
	body := map[string]any{"error": refusal.Message, "code": refusal.Code}
	for k, v := range refusal.Details {
		body[k] = v
	}
	utils.JSON(w, status, body)
}

// Events handles GET /competitions/{id}/events: a Server-Sent Events stream
// of the round's live prize (spec §5.4). It opens with a SNAPSHOT, then
// sends PRIZE_UPDATED and STATUS_CHANGED as their transactions commit.
// Clients treat it as a display optimisation and still re-read the round
// from time to time.
func (h *CompetitionHandler) Events(w http.ResponseWriter, r *http.Request) {
	id, ok := competitionID(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		utils.Error(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}
	ctx := r.Context()
	snapshot, seq, err := h.competitions.LiveStart(ctx, id)
	if err != nil {
		h.writeError(w, err, "could not open live updates")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(event string, id int64, data []byte) bool {
		if id > 0 {
			if _, err := fmt.Fprintf(w, "id: %d\n", id); err != nil {
				return false
			}
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	first, _ := json.Marshal(snapshot)
	if !send("SNAPSHOT", seq, first) {
		return
	}

	poll := time.NewTicker(1500 * time.Millisecond)
	defer poll.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-poll.C:
			events, err := h.competitions.LiveEvents(ctx, id, seq)
			if err != nil {
				if ctx.Err() == nil {
					log.Printf("competition events %s: %v", id, err)
				}
				return
			}
			for _, e := range events {
				if !send(e.Type, e.Seq, e.Payload) {
					return
				}
				seq = e.Seq
			}
		}
	}
}

// ─── Super Admin ──────────────────────────────────────────────────────────

// Pause handles POST /admin/competitions/{id}/pause.
func (h *CompetitionHandler) Pause() http.HandlerFunc {
	return h.adminAction(func(s *services.CompetitionService, r *http.Request, a services.AdminActor, id uuid.UUID) (any, error) {
		return s.PauseCompetition(r.Context(), a, id)
	}, "could not pause competition")
}

// Resume handles POST /admin/competitions/{id}/resume.
func (h *CompetitionHandler) Resume() http.HandlerFunc {
	return h.adminAction(func(s *services.CompetitionService, r *http.Request, a services.AdminActor, id uuid.UUID) (any, error) {
		return s.ResumeCompetition(r.Context(), a, id)
	}, "could not resume competition")
}

// Close handles POST /admin/competitions/{id}/close.
func (h *CompetitionHandler) Close() http.HandlerFunc {
	return h.adminAction(func(s *services.CompetitionService, r *http.Request, a services.AdminActor, id uuid.UUID) (any, error) {
		return s.CloseCompetition(r.Context(), a, id)
	}, "could not close competition")
}

// AdjustPrize handles POST /admin/competitions/{id}/prize-adjustments.
func (h *CompetitionHandler) AdjustPrize(w http.ResponseWriter, r *http.Request) {
	var in services.AdjustPrizeInput
	if !decodeBody(w, r, &in) {
		return
	}
	h.adminAction(func(s *services.CompetitionService, r *http.Request, a services.AdminActor, id uuid.UUID) (any, error) {
		return s.AdjustPrize(r.Context(), a, id, in)
	}, "could not adjust prize")(w, r)
}

// VoidPlay handles POST /admin/competitions/{id}/plays/{playID}/void.
func (h *CompetitionHandler) VoidPlay(w http.ResponseWriter, r *http.Request) {
	playID, ok := uuidParam(w, r, "playID", "play not found")
	if !ok {
		return
	}
	var in services.VoidPlayInput
	if !decodeBody(w, r, &in) {
		return
	}
	h.adminAction(func(s *services.CompetitionService, r *http.Request, a services.AdminActor, id uuid.UUID) (any, error) {
		return nil, s.VoidPlay(r.Context(), a, id, playID, in)
	}, "could not void play")(w, r)
}

// Settle handles POST /admin/competitions/{id}/settle.
func (h *CompetitionHandler) Settle(w http.ResponseWriter, r *http.Request) {
	var in services.SettleInput
	if r.ContentLength != 0 && !decodeBody(w, r, &in) {
		return
	}
	h.adminAction(func(s *services.CompetitionService, r *http.Request, a services.AdminActor, id uuid.UUID) (any, error) {
		winners, err := s.SettleWinners(r.Context(), a, id, in)
		if err != nil {
			return nil, err
		}
		return map[string]any{"items": winners}, nil
	}, "could not settle winners")(w, r)
}

// Duplicate handles POST /admin/competitions/{id}/duplicate.
func (h *CompetitionHandler) Duplicate(w http.ResponseWriter, r *http.Request) {
	var in services.DuplicateInput
	if !decodeBody(w, r, &in) {
		return
	}
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	id, ok := competitionID(w, r)
	if !ok {
		return
	}
	view, err := h.competitions.DuplicateCompetition(r.Context(), admin, id, in)
	if err != nil {
		h.writeError(w, err, "could not duplicate competition")
		return
	}
	utils.JSON(w, http.StatusCreated, view)
}

// Ledger handles GET /admin/competitions/{id}/ledger.
func (h *CompetitionHandler) Ledger(w http.ResponseWriter, r *http.Request) {
	id, ok := competitionID(w, r)
	if !ok {
		return
	}
	view, err := h.competitions.Ledger(r.Context(), id)
	if err != nil {
		h.writeError(w, err, "could not read prize ledger")
		return
	}
	utils.JSON(w, http.StatusOK, view)
}

// Plays handles GET /admin/competitions/{id}/plays.
func (h *CompetitionHandler) Plays(w http.ResponseWriter, r *http.Request) {
	id, ok := competitionID(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	plays, err := h.competitions.AdminPlays(r.Context(), id, limit)
	if err != nil {
		h.writeError(w, err, "could not list plays")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]any{"items": plays})
}

// Reconcile handles POST /admin/competitions/{id}/reconcile.
func (h *CompetitionHandler) Reconcile(w http.ResponseWriter, r *http.Request) {
	id, ok := competitionID(w, r)
	if !ok {
		return
	}
	rec, err := h.competitions.Reconcile(r.Context(), id)
	if err != nil {
		h.writeError(w, err, "could not reconcile prize")
		return
	}
	utils.JSON(w, http.StatusOK, rec)
}

// Analytics handles GET /admin/competitions/{id}/analytics.
func (h *CompetitionHandler) Analytics(w http.ResponseWriter, r *http.Request) {
	id, ok := competitionID(w, r)
	if !ok {
		return
	}
	a, err := h.competitions.Analytics(r.Context(), id)
	if err != nil {
		h.writeError(w, err, "could not load analytics")
		return
	}
	utils.JSON(w, http.StatusOK, a)
}

// ─── Points ───────────────────────────────────────────────────────────────

// PointsHandler serves the points wallet.
type PointsHandler struct {
	points *services.PointsService
}

func NewPointsHandler(points *services.PointsService) *PointsHandler {
	return &PointsHandler{points: points}
}

// MyWallet handles GET /customers/me/points.
func (h *PointsHandler) MyWallet(w http.ResponseWriter, r *http.Request) {
	wallet, err := h.points.MyWallet(r.Context(), actorFromContext(r))
	if err != nil {
		h.writeError(w, err, "could not read points")
		return
	}
	utils.JSON(w, http.StatusOK, wallet)
}

// SearchCustomers handles GET /admin/customers?q=.
func (h *PointsHandler) SearchCustomers(w http.ResponseWriter, r *http.Request) {
	list, err := h.points.SearchCustomers(r.Context(), r.URL.Query().Get("q"))
	if err != nil {
		h.writeError(w, err, "could not search customers")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]any{"items": list})
}

// CustomerWallet handles GET /admin/customers/{customerID}/points.
func (h *PointsHandler) CustomerWallet(w http.ResponseWriter, r *http.Request) {
	id, ok := uuidParam(w, r, "customerID", "customer not found")
	if !ok {
		return
	}
	wallet, err := h.points.CustomerWallet(r.Context(), id)
	if err != nil {
		h.writeError(w, err, "could not read points")
		return
	}
	utils.JSON(w, http.StatusOK, wallet)
}

// Adjust handles POST /admin/customers/{customerID}/points/adjustments.
func (h *PointsHandler) Adjust(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "customerID", "customer not found")
	if !ok {
		return
	}
	var in services.AdjustPointsInput
	if !decodeBody(w, r, &in) {
		return
	}
	if in.IdempotencyKey == "" {
		in.IdempotencyKey = r.Header.Get(IdempotencyKeyHeader)
	}
	wallet, err := h.points.AdjustPoints(r.Context(), admin, id, in)
	if err != nil {
		h.writeError(w, err, "could not adjust points")
		return
	}
	utils.JSON(w, http.StatusOK, wallet)
}

// MyEarningRule handles GET /customers/me/points/earning.
func (h *PointsHandler) MyEarningRule(w http.ResponseWriter, r *http.Request) {
	rule, err := h.points.MyEarningRule(r.Context(), actorFromContext(r))
	if err != nil {
		h.writeError(w, err, "could not read earning rule")
		return
	}
	utils.JSON(w, http.StatusOK, rule)
}

// EarningRules handles GET /admin/points/earning-rules.
func (h *PointsHandler) EarningRules(w http.ResponseWriter, r *http.Request) {
	rules, err := h.points.EarningRules(r.Context())
	if err != nil {
		h.writeError(w, err, "could not list earning rules")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]any{"items": rules})
}

// SetEarningRule handles PUT /admin/points/earning-rules/{countryID}.
func (h *PointsHandler) SetEarningRule(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	countryID, ok := uuidParam(w, r, "countryID", "country not found")
	if !ok {
		return
	}
	var in services.EarningRuleInput
	if !decodeBody(w, r, &in) {
		return
	}
	rules, err := h.points.SetEarningRule(r.Context(), admin, countryID, in)
	if err != nil {
		h.writeError(w, err, "could not save earning rule")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]any{"items": rules})
}

// RunEarning handles POST /admin/points/earning-runs.
func (h *PointsHandler) RunEarning(w http.ResponseWriter, r *http.Request) {
	res, err := h.points.RunEarning(r.Context())
	if err != nil {
		h.writeError(w, err, "could not run points earning")
		return
	}
	utils.JSON(w, http.StatusOK, res)
}

// RunDraw handles POST /admin/competitions/{id}/draw.
func (h *CompetitionHandler) RunDraw() http.HandlerFunc {
	return h.adminAction(func(s *services.CompetitionService, r *http.Request, a services.AdminActor, id uuid.UUID) (any, error) {
		return s.RunDraw(r.Context(), a, id)
	}, "could not run the draw")
}

// Draw handles GET /admin/competitions/{id}/draw.
func (h *CompetitionHandler) Draw(w http.ResponseWriter, r *http.Request) {
	id, ok := competitionID(w, r)
	if !ok {
		return
	}
	d, err := h.competitions.Draw(r.Context(), id)
	if err != nil {
		h.writeError(w, err, "could not read the draw")
		return
	}
	if d == nil {
		utils.Error(w, http.StatusNotFound, "no draw has been run for this round")
		return
	}
	utils.JSON(w, http.StatusOK, d)
}

func (h *PointsHandler) writeError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, services.ErrCustomerRequired):
		utils.Error(w, http.StatusUnauthorized, err.Error())
	case errors.Is(err, services.ErrInvalidPoints):
		utils.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, services.ErrConfigConflict):
		utils.JSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": services.PlayIdempotencyConflict})
	default:
		log.Printf("points handler: %s: %v", fallback, err)
		utils.Error(w, http.StatusInternalServerError, fallback)
	}
}
