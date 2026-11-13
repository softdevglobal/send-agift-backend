package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"myapp/internal/models"
	"myapp/internal/repository"
)

// Game entries, the customer's state, and the switches that turn games and
// entry earning on for a country, a state and a game.

var (
	ErrInvalidEntries       = errors.New("invalid game entries request")
	ErrInvalidState         = errors.New("invalid state")
	ErrStateNotFound        = errors.New("state not found")
	ErrJurisdictionNotFound = errors.New("no games settings for that place")
	ErrShortfallNotFound    = errors.New("review case not found, or already resolved")
)

func invalidEntries(msg string) error { return fmt.Errorf("%w: %s", ErrInvalidEntries, msg) }
func invalidState(msg string) error   { return fmt.Errorf("%w: %s", ErrInvalidState, msg) }

// GameEntriesService is the customer's entries and where games are on.
type GameEntriesService struct {
	repo *repository.GameEntriesRepository
	// stateCooldown is how long a customer waits before changing the state
	// they gave.
	stateCooldown time.Duration
}

func NewGameEntriesService(repo *repository.GameEntriesRepository, stateCooldownDays int) *GameEntriesService {
	if stateCooldownDays <= 0 {
		stateCooldownDays = 90
	}
	return &GameEntriesService{repo: repo, stateCooldown: time.Duration(stateCooldownDays) * 24 * time.Hour}
}

func mapEntriesErr(err error) error {
	var blocked *repository.StateChangeBlockedError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, repository.ErrEntriesCustomerNotFound):
		return ErrCustomerRequired
	case errors.Is(err, repository.ErrStateNotFound):
		return ErrStateNotFound
	case errors.Is(err, repository.ErrJurisdictionNotFound):
		return ErrJurisdictionNotFound
	case errors.Is(err, repository.ErrStateDuplicate), errors.Is(err, repository.ErrStateInUse),
		errors.Is(err, repository.ErrStateWrongCountry), errors.Is(err, repository.ErrStateNeedsPhone):
		return invalidState(err.Error())
	case errors.As(err, &blocked):
		return invalidState(blocked.Error())
	}
	return err
}

// ─── Customer ─────────────────────────────────────────────────────────────

// Wallet is the customer's entries, history, and whether they can earn and
// play where they live.
func (s *GameEntriesService) Wallet(ctx context.Context, customerID uuid.UUID) (*models.GameEntriesWallet, error) {
	w, err := s.repo.Wallet(ctx, customerID, 100)
	if err != nil {
		return nil, err
	}
	if w.Earn, err = s.repo.Gate(ctx, customerID, "earn", nil); err != nil {
		return nil, mapEntriesErr(err)
	}
	if w.Play, err = s.repo.Gate(ctx, customerID, "play", nil); err != nil {
		return nil, mapEntriesErr(err)
	}
	return w, nil
}

// EntryOffers is what the cart or checkout shows: the entries each product
// would earn this customer if bought now, calculated on the server.
type EntryOffers struct {
	Offers  []repository.EntryOffer `json:"offers"`
	Total   int                     `json:"total_game_entries"`
	Allowed bool                    `json:"allowed"`
	Message string                  `json:"message,omitempty"`
}

func (s *GameEntriesService) Offers(ctx context.Context, customerID uuid.UUID, productIDs []string) (*EntryOffers, error) {
	if len(productIDs) > 100 {
		return nil, invalidEntries("at most 100 products")
	}
	ids := make([]uuid.UUID, 0, len(productIDs))
	seen := map[uuid.UUID]bool{}
	for _, raw := range productIDs {
		id, err := uuid.Parse(strings.TrimSpace(raw))
		if err != nil {
			return nil, invalidEntries("product_ids must be product IDs")
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	offers, gate, err := s.repo.Offers(ctx, customerID, ids)
	if err != nil {
		return nil, mapEntriesErr(err)
	}
	out := &EntryOffers{Offers: offers, Allowed: gate.Allowed, Message: gate.Message}
	for _, o := range offers {
		out.Total += o.Entries
	}
	return out, nil
}

// FreeClaim is the result of asking for the day's free entries.
type FreeClaim struct {
	Granted int    `json:"granted"`
	Balance int64  `json:"balance"`
	Message string `json:"message"`
}

// ClaimFree gives the customer the day's free entries, where their region
// offers a no-purchase route.
func (s *GameEntriesService) ClaimFree(ctx context.Context, customerID uuid.UUID) (*FreeClaim, error) {
	granted, gate, err := s.repo.ClaimFree(ctx, customerID)
	if err != nil {
		return nil, mapEntriesErr(err)
	}
	out := &FreeClaim{Granted: granted}
	switch {
	case !gate.Allowed:
		out.Message = gate.Message
	case gate.FreeEntriesPerDay <= 0:
		out.Message = "Free entries are not offered in your region."
	case granted == 0:
		out.Message = "You have already had today's free entries. Come back tomorrow."
	default:
		out.Message = fmt.Sprintf("%d free game entries added.", granted)
	}
	if out.Balance, err = s.repo.Balance(ctx, customerID); err != nil {
		return nil, err
	}
	return out, nil
}

// States lists the active states of a country, for sign-up and profile.
func (s *GameEntriesService) States(ctx context.Context, countryID string) ([]models.State, error) {
	id, err := uuid.Parse(strings.TrimSpace(countryID))
	if err != nil {
		return nil, invalidState("country_id is required")
	}
	return s.repo.States(ctx, id, true)
}

// CustomerState is the customer's state, whether one is still needed, and
// when it can next be changed.
func (s *GameEntriesService) CustomerState(ctx context.Context, customerID uuid.UUID) (*repository.CustomerState, error) {
	st, err := s.repo.CustomerState(ctx, customerID, s.stateCooldown)
	return st, mapEntriesErr(err)
}

// SetCustomerState records the customer's state. It is what they tell us,
// never a guess from where they connect from.
func (s *GameEntriesService) SetCustomerState(ctx context.Context, customerID uuid.UUID, stateID string) (*repository.CustomerState, error) {
	id, err := uuid.Parse(strings.TrimSpace(stateID))
	if err != nil {
		return nil, invalidState("state_id is required")
	}
	if err := s.repo.SetCustomerState(ctx, customerID, id, s.stateCooldown); err != nil {
		return nil, mapEntriesErr(err)
	}
	return s.CustomerState(ctx, customerID)
}

// RequireStateForSignup checks the state given at sign-up: required when
// the country has states, and one of that country's. It returns the state
// to store, or nil for a country without states.
func (s *GameEntriesService) RequireStateForSignup(ctx context.Context, countryID uuid.UUID, stateID string) (*uuid.UUID, error) {
	has, err := s.repo.CountryHasStates(ctx, countryID)
	if err != nil {
		return nil, err
	}
	stateID = strings.TrimSpace(stateID)
	if !has {
		return nil, nil
	}
	if stateID == "" {
		return nil, invalidState("choose your state or region")
	}
	id, err := uuid.Parse(stateID)
	if err != nil {
		return nil, invalidState("state_id is not a state")
	}
	if err := s.repo.StateOfCountry(ctx, id, countryID); err != nil {
		return nil, mapEntriesErr(err)
	}
	return &id, nil
}

// ─── Admin: states ────────────────────────────────────────────────────────

// StateInput is a state as an admin writes it.
type StateInput struct {
	CountryID string `json:"country_id"`
	Code      string `json:"code"`
	Name      string `json:"name"`
	IsActive  *bool  `json:"is_active"`
}

func (a AdminActor) entriesAudit(action, entity string, id *uuid.UUID, reason *string) models.AuditEntry {
	entry := a.cpAudit(action, entity)
	entry.EntityID, entry.Reason = id, reason
	return entry
}

func (s *GameEntriesService) AdminStates(ctx context.Context, countryID string) ([]models.State, error) {
	id, err := uuid.Parse(strings.TrimSpace(countryID))
	if err != nil {
		return nil, invalidState("country_id is required")
	}
	return s.repo.States(ctx, id, false)
}

func (s *GameEntriesService) SaveState(ctx context.Context, admin AdminActor, id *uuid.UUID, in StateInput) (*models.State, error) {
	st := &models.State{
		Code: strings.ToUpper(strings.TrimSpace(in.Code)), Name: strings.TrimSpace(in.Name), IsActive: true,
	}
	if in.IsActive != nil {
		st.IsActive = *in.IsActive
	}
	if id == nil {
		country, err := uuid.Parse(strings.TrimSpace(in.CountryID))
		if err != nil {
			return nil, invalidState("country_id is required")
		}
		st.CountryID = country
	}
	switch {
	case st.Name == "" || len(st.Name) > 80:
		return nil, invalidState("name must be 1 to 80 characters")
	case len(st.Code) < 1 || len(st.Code) > 10:
		return nil, invalidState("code must be 1 to 10 letters, digits or hyphens")
	}
	for _, r := range st.Code {
		if !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '-' {
			return nil, invalidState("code must be 1 to 10 letters, digits or hyphens")
		}
	}
	audit := admin.entriesAudit("states.saved", "state", id, nil)
	audit.After = st
	out, err := s.repo.SaveState(ctx, id, st, audit)
	return out, mapEntriesErr(err)
}

func (s *GameEntriesService) DeleteState(ctx context.Context, admin AdminActor, id uuid.UUID) error {
	return mapEntriesErr(s.repo.DeleteState(ctx, id, admin.entriesAudit("states.deleted", "state", &id, nil)))
}

// ─── Admin: where games are on ────────────────────────────────────────────

// GamesControl is the admin's view of every switch.
type GamesControl struct {
	Program       *models.EntryProgram  `json:"program"`
	Jurisdictions []models.Jurisdiction `json:"jurisdictions"`
}

func (s *GameEntriesService) Control(ctx context.Context) (*GamesControl, error) {
	out := &GamesControl{}
	var err error
	if out.Program, err = s.repo.Program(ctx); err != nil {
		return nil, err
	}
	if out.Jurisdictions, err = s.repo.Jurisdictions(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// SetProgram turns the whole programme on or off.
func (s *GameEntriesService) SetProgram(ctx context.Context, admin AdminActor, enabled bool, reason string) (*GamesControl, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, invalidEntries("a reason is required")
	}
	audit := admin.entriesAudit("games.program_set", "entry_program", nil, &reason)
	audit.After = map[string]any{"enabled": enabled}
	if err := s.repo.SetProgram(ctx, enabled, admin.ID, audit); err != nil {
		return nil, err
	}
	return s.Control(ctx)
}

// StopProgram is the global emergency stop.
func (s *GameEntriesService) StopProgram(ctx context.Context, admin AdminActor, reason string) (*GamesControl, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, invalidEntries("a reason is required")
	}
	if err := s.repo.StopProgram(ctx, reason, admin.ID,
		admin.entriesAudit("games.program_stopped", "entry_program", nil, &reason)); err != nil {
		return nil, err
	}
	return s.Control(ctx)
}

// JurisdictionInput is the games settings of a country or one of its
// states. GameIDs are the games switched on there.
type JurisdictionInput struct {
	CountryID          string   `json:"country_id"`
	StateID            *string  `json:"state_id"`
	EarningEnabled     bool     `json:"earning_enabled"`
	PlayEnabled        bool     `json:"play_enabled"`
	ChanceGamesEnabled bool     `json:"chance_games_enabled"`
	MinAge             int      `json:"min_age"`
	OfficialRules      *string  `json:"official_rules"`
	CustomerDailyCap   *int     `json:"customer_daily_cap"`
	ProductDailyCap    *int     `json:"product_daily_cap"`
	FreeEntriesPerDay  int      `json:"free_entries_per_day"`
	GameIDs            []string `json:"game_ids"`
}

// SaveJurisdiction writes a place's settings. A state must be set for a
// country that has states: approving a country does not approve its states.
func (s *GameEntriesService) SaveJurisdiction(ctx context.Context, admin AdminActor, in JurisdictionInput) (*models.Jurisdiction, error) {
	country, err := uuid.Parse(strings.TrimSpace(in.CountryID))
	if err != nil {
		return nil, invalidEntries("country_id is required")
	}
	j := &models.Jurisdiction{
		CountryID: country, EarningEnabled: in.EarningEnabled, PlayEnabled: in.PlayEnabled,
		ChanceGamesEnabled: in.ChanceGamesEnabled, MinAge: in.MinAge,
		CustomerDailyCap: in.CustomerDailyCap, ProductDailyCap: in.ProductDailyCap,
		FreeEntriesPerDay: in.FreeEntriesPerDay,
	}
	if j.MinAge == 0 {
		j.MinAge = 18
	}
	if in.StateID != nil && strings.TrimSpace(*in.StateID) != "" {
		id, err := uuid.Parse(strings.TrimSpace(*in.StateID))
		if err != nil {
			return nil, invalidEntries("state_id is not a state")
		}
		j.StateID = &id
	}
	has, err := s.repo.CountryHasStates(ctx, country)
	if err != nil {
		return nil, err
	}
	if in.OfficialRules != nil {
		if r := strings.TrimSpace(*in.OfficialRules); r != "" {
			j.OfficialRules = &r
		}
	}
	switch {
	case has && j.StateID == nil:
		return nil, invalidEntries("this country has states: switch games on state by state")
	case j.MinAge < 13 || j.MinAge > 25:
		return nil, invalidEntries("min_age must be 13 to 25")
	case (j.EarningEnabled || j.PlayEnabled) && j.OfficialRules == nil:
		return nil, invalidEntries("official rules are required before earning or play is switched on")
	case j.OfficialRules != nil && len(*j.OfficialRules) > 20000:
		return nil, invalidEntries("official rules are too long")
	case j.CustomerDailyCap != nil && *j.CustomerDailyCap < 1:
		return nil, invalidEntries("customer_daily_cap must be positive, or left empty for no cap")
	case j.ProductDailyCap != nil && *j.ProductDailyCap < 1:
		return nil, invalidEntries("product_daily_cap must be positive, or left empty for no cap")
	case j.FreeEntriesPerDay < 0 || j.FreeEntriesPerDay > 100:
		return nil, invalidEntries("free_entries_per_day must be 0 to 100")
	}
	for _, raw := range in.GameIDs {
		id, err := uuid.Parse(strings.TrimSpace(raw))
		if err != nil {
			return nil, invalidEntries("game_ids must be game IDs")
		}
		j.GameIDs = append(j.GameIDs, id)
	}
	audit := admin.entriesAudit("games.jurisdiction_saved", "jurisdiction", nil, nil)
	audit.After = in
	out, err := s.repo.SaveJurisdiction(ctx, j, admin.ID, audit)
	return out, mapEntriesErr(err)
}

// StopJurisdiction is the emergency stop for one place; stop false lifts it.
func (s *GameEntriesService) StopJurisdiction(ctx context.Context, admin AdminActor, id uuid.UUID, stop bool, reason string) (*GamesControl, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, invalidEntries("a reason is required")
	}
	action := "games.jurisdiction_stop_lifted"
	if stop {
		action = "games.jurisdiction_stopped"
	}
	if err := s.repo.StopJurisdiction(ctx, id, stop, reason, admin.ID,
		admin.entriesAudit(action, "jurisdiction", &id, &reason)); err != nil {
		return nil, mapEntriesErr(err)
	}
	return s.Control(ctx)
}

// ─── Admin: customers, review and legacy ──────────────────────────────────

func (s *GameEntriesService) SearchCustomers(ctx context.Context, query string) ([]models.CustomerEntriesSummary, error) {
	query = strings.TrimSpace(query)
	if len(query) > 120 {
		query = query[:120]
	}
	return s.repo.SearchCustomers(ctx, query, 200)
}

func (s *GameEntriesService) CustomerWallet(ctx context.Context, customerID uuid.UUID) (*models.GameEntriesWallet, error) {
	return s.repo.Wallet(ctx, customerID, 200)
}

// AdjustEntriesInput is an admin adding or removing entries. The key makes
// a repeated request apply once.
type AdjustEntriesInput struct {
	Amount         int64  `json:"amount"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotency_key"`
}

// Adjust adds (positive) or removes (negative) a customer's entries, with a
// reason, audited. Used for support corrections and audited free grants.
func (s *GameEntriesService) Adjust(ctx context.Context, admin AdminActor, customerID uuid.UUID, in AdjustEntriesInput) (*models.GameEntriesWallet, error) {
	reason := strings.TrimSpace(in.Reason)
	key := strings.TrimSpace(in.IdempotencyKey)
	switch {
	case in.Amount == 0:
		return nil, invalidEntries("amount cannot be zero")
	case in.Amount > 100_000 || in.Amount < -100_000:
		return nil, invalidEntries("amount is out of range")
	case reason == "":
		return nil, invalidEntries("a reason is required")
	case key == "" || len(key) > 128:
		return nil, invalidEntries("idempotency_key of 1 to 128 characters is required")
	}
	entryType := models.EntryAdminGrant
	if in.Amount < 0 {
		entryType = models.EntryAdminDeduction
	}
	audit := admin.entriesAudit("entries.adjusted", "customer", &customerID, &reason)
	audit.After = map[string]any{"amount": in.Amount}
	adminID := admin.ID
	_, err := s.repo.Adjust(ctx, repository.EntriesChange{
		CustomerID: customerID, EntryType: entryType, Delta: in.Amount,
		IdempotencyKey: "admin-adjust:" + key, Reason: &reason, ActorType: "admin", ActorID: &adminID,
	}, audit)
	switch {
	case errors.Is(err, repository.ErrEntriesInsufficient):
		return nil, invalidEntries("the customer does not hold that many game entries")
	case errors.Is(err, repository.ErrEntriesDuplicate):
		// The same request again: nothing more to do.
	case err != nil:
		return nil, mapEntriesErr(err)
	}
	return s.repo.Wallet(ctx, customerID, 200)
}

func (s *GameEntriesService) Shortfalls(ctx context.Context, status string) ([]models.EntryShortfall, error) {
	if status != "" && status != "open" && status != "resolved" {
		return nil, invalidEntries("status must be open or resolved")
	}
	return s.repo.ListShortfalls(ctx, status, 200)
}

func (s *GameEntriesService) ResolveShortfall(ctx context.Context, admin AdminActor, id uuid.UUID, resolution string) error {
	resolution = strings.TrimSpace(resolution)
	if resolution == "" {
		return invalidEntries("say what was decided")
	}
	err := s.repo.ResolveShortfall(ctx, id, admin.ID, resolution,
		admin.entriesAudit("entries.shortfall_resolved", "entry_shortfall", &id, &resolution))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrShortfallNotFound
	}
	return err
}

// LegacyPoints is what customers and sellers still hold in the retired
// points system, awaiting the owner's decision.
func (s *GameEntriesService) LegacyPoints(ctx context.Context) (*models.LegacyPoints, error) {
	return s.repo.LegacyBalances(ctx, 500)
}

// RunSettleLoop keeps entries in line with orders and ends packages whose
// time has run out, on one server at a time.
func (s *GameEntriesService) RunSettleLoop(ctx context.Context, every time.Duration, exclusive Exclusive) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := exclusive.run(ctx, func(ctx context.Context) error {
				res, err := s.repo.Settle(ctx, 200)
				if res != nil && res.Released+res.Reversed+res.PackagesExpired+res.Granted > 0 {
					log.Printf("game entries: %+v", *res)
				}
				return err
			}); err != nil {
				log.Printf("game entries: %v", err)
			}
		}
	}
}
