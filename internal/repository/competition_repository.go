package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"myapp/internal/models"
)

var (
	ErrCompetitionNotFound     = errors.New("competition not found")
	ErrCompetitionStateChanged = errors.New("competition changed state")
	ErrAttemptLimitReached     = errors.New("attempt limit reached")
	ErrAttemptConflict         = errors.New("another attempt is starting")
	ErrAttemptNotFound         = errors.New("attempt not found")
	ErrSubmissionNotFound      = errors.New("score submission not found")
	ErrWinnerNotFound          = errors.New("winner not found")
	ErrWinnerStateChanged      = errors.New("winner changed state")
	ErrClaimNotFound           = errors.New("prize claim not found")
	ErrClaimStateChanged       = errors.New("prize claim changed state")
	ErrReserveLocked           = errors.New("prize reserve can no longer change")
)

// CompetitionRepository persists competitions and everything hanging off
// them. Every multi-step change runs in one transaction together with its
// audit-log row, so an admin action is either fully applied and recorded, or
// not applied at all.
type CompetitionRepository struct {
	db *pgxpool.Pool
}

func NewCompetitionRepository(db *pgxpool.Pool) *CompetitionRepository {
	return &CompetitionRepository{db: db}
}

type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type scanner interface {
	Scan(dest ...any) error
}

func (r *CompetitionRepository) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// insertAudit appends one row to admin.audit_log.
func insertAudit(ctx context.Context, q querier, e models.AuditEntry) error {
	toJSON := func(v any) ([]byte, error) {
		if v == nil {
			return nil, nil
		}
		return json.Marshal(v)
	}
	before, err := toJSON(e.Before)
	if err != nil {
		return err
	}
	after, err := toJSON(e.After)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `
		insert into admin.audit_log
			(actor_type, actor_id, action, entity_type, entity_id, before_data, after_data, reason, ip_address, user_agent)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9::inet, $10)`,
		e.ActorType, e.ActorID, e.Action, e.EntityType, e.EntityID, before, after, e.Reason,
		cleanIP(e.IPAddress), e.UserAgent)
	return err
}

// cleanIP strips a port and drops anything that is not an IP, so a malformed
// header can never make an audit write fail.
func cleanIP(raw *string) *string {
	if raw == nil || *raw == "" {
		return nil
	}
	host := *raw
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if net.ParseIP(host) == nil {
		return nil
	}
	return &host
}

// ─── Competitions ─────────────────────────────────────────────────────────

const competitionSelect = `
	select c.id, c.country_id, co.iso_code, co.name,
	       c.game_version_id, v.status, g.slug, g.name, v.version, v.config,
	       c.title, c.status, c.starts_at, c.ends_at, c.timezone, c.server_seed,
	       c.points_per_attempt, c.max_attempts_per_customer, c.min_age,
	       c.requires_identity_verification, c.number_of_winners,
	       c.prize_description, c.prize_value_amount, c.prize_currency,
	       c.official_rules, c.official_rules_media_id,
	       c.cancel_reason, c.cancel_note, c.cancelled_at, c.frozen_at, c.finalised_at,
	       c.created_by_admin_id, c.created_at, c.updated_at
	from competition.competitions c
	inner join core.countries co on co.id = c.country_id
	inner join competition.game_versions v on v.id = c.game_version_id
	inner join competition.games g on g.id = v.game_id`

func scanCompetition(row scanner) (*models.Competition, error) {
	var c models.Competition
	var cfg []byte
	err := row.Scan(&c.ID, &c.CountryID, &c.CountryCode, &c.CountryName,
		&c.GameVersionID, &c.GameVersionStatus, &c.GameSlug, &c.GameName, &c.GameVersion, &cfg,
		&c.Title, &c.Status, &c.StartsAt, &c.EndsAt, &c.Timezone, &c.ServerSeed,
		&c.PointsPerAttempt, &c.MaxAttemptsPerCustomer, &c.MinAge,
		&c.RequiresIdentityVerification, &c.NumberOfWinners,
		&c.PrizeDescription, &c.PrizeValueAmount, &c.PrizeCurrency,
		&c.OfficialRules, &c.OfficialRulesMediaID,
		&c.CancelReason, &c.CancelNote, &c.CancelledAt, &c.FrozenAt, &c.FinalisedAt,
		&c.CreatedByAdminID, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	c.GameConfig = json.RawMessage(cfg)
	return &c, nil
}

// SyncStatuses moves scheduled competitions to live at their start and live
// ones to closed at their end. Called before competitions are read, so the
// stored status never lags the clock for long and no background job is
// needed.
func (r *CompetitionRepository) SyncStatuses(ctx context.Context) error {
	if _, err := r.db.Exec(ctx, `
		update competition.competitions
		set status = 'closed', updated_at = now()
		where status in ('scheduled', 'live') and now() >= ends_at`); err != nil {
		return err
	}
	_, err := r.db.Exec(ctx, `
		update competition.competitions
		set status = 'live', updated_at = now()
		where status = 'scheduled' and now() >= starts_at and now() < ends_at`)
	return err
}

func (r *CompetitionRepository) GetByID(ctx context.Context, id uuid.UUID) (*models.Competition, error) {
	c, err := scanCompetition(r.db.QueryRow(ctx, competitionSelect+` where c.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCompetitionNotFound
	}
	return c, err
}

// CompetitionFilter narrows a competition list.
type CompetitionFilter struct {
	IncludeDrafts bool
	CountryID     *uuid.UUID
	Limit         int
}

// List returns competitions live first, then upcoming, then finished.
func (r *CompetitionRepository) List(ctx context.Context, f CompetitionFilter) ([]models.Competition, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	rows, err := r.db.Query(ctx, competitionSelect+`
		where ($1::bool or c.status <> 'draft')
		  and ($2::uuid is null or c.country_id = $2)
		order by case c.status
		           when 'live' then 0 when 'scheduled' then 1
		           when 'closed' then 2 when 'frozen' then 2
		           when 'finalised' then 3 when 'draft' then 4 else 5 end,
		         case when c.status in ('live', 'scheduled', 'draft') then c.starts_at end asc,
		         c.ends_at desc
		limit $3`, f.IncludeDrafts, f.CountryID, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]models.Competition, 0)
	for rows.Next() {
		c, err := scanCompetition(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (r *CompetitionRepository) Create(ctx context.Context, c *models.Competition, audit models.AuditEntry) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			insert into competition.competitions
				(country_id, game_version_id, title, status, starts_at, ends_at, timezone, server_seed,
				 points_per_attempt, max_attempts_per_customer, min_age, requires_identity_verification,
				 number_of_winners, prize_description, prize_value_amount, prize_currency, official_rules,
				 created_by_admin_id)
			values ($1, $2, $3, 'draft', $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
			returning id, status, created_at, updated_at`,
			c.CountryID, c.GameVersionID, c.Title, c.StartsAt, c.EndsAt, c.Timezone, c.ServerSeed,
			c.PointsPerAttempt, c.MaxAttemptsPerCustomer, c.MinAge, c.RequiresIdentityVerification,
			c.NumberOfWinners, c.PrizeDescription, c.PrizeValueAmount, c.PrizeCurrency, c.OfficialRules,
			c.CreatedByAdminID).
			Scan(&c.ID, &c.Status, &c.CreatedAt, &c.UpdatedAt)
		if err != nil {
			return err
		}
		audit.EntityID = &c.ID
		audit.After = c
		return insertAudit(ctx, tx, audit)
	})
}

// Update rewrites the rules while they are still editable and returns the
// competition to draft, so every schedule gate is checked again.
func (r *CompetitionRepository) Update(ctx context.Context, c *models.Competition, audit models.AuditEntry) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			update competition.competitions
			set country_id = $2, game_version_id = $3, title = $4, starts_at = $5, ends_at = $6,
			    timezone = $7, points_per_attempt = $8, max_attempts_per_customer = $9, min_age = $10,
			    requires_identity_verification = $11, number_of_winners = $12, prize_description = $13,
			    prize_value_amount = $14, prize_currency = $15, official_rules = $16,
			    status = 'draft', updated_at = now()
			where id = $1
			  and (status = 'draft' or (status = 'scheduled' and now() < starts_at))`,
			c.ID, c.CountryID, c.GameVersionID, c.Title, c.StartsAt, c.EndsAt, c.Timezone,
			c.PointsPerAttempt, c.MaxAttemptsPerCustomer, c.MinAge, c.RequiresIdentityVerification,
			c.NumberOfWinners, c.PrizeDescription, c.PrizeValueAmount, c.PrizeCurrency, c.OfficialRules)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrCompetitionStateChanged
		}
		audit.After = c
		return insertAudit(ctx, tx, audit)
	})
}

// Schedule publishes a draft. The caller has already checked every gate.
func (r *CompetitionRepository) Schedule(ctx context.Context, id uuid.UUID, audit models.AuditEntry) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			update competition.competitions
			set status = 'scheduled', updated_at = now()
			where id = $1 and status = 'draft' and ends_at > now()`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrCompetitionStateChanged
		}
		return insertAudit(ctx, tx, audit)
	})
}

// Cancel stops a competition for one of the permitted reasons (§13.9). No
// winner is declared; every attempt is voided so its points can be returned,
// and open sessions are closed.
func (r *CompetitionRepository) Cancel(ctx context.Context, id uuid.UUID, reason string, note *string, audit models.AuditEntry) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			update competition.competitions
			set status = 'cancelled', cancel_reason = $2, cancel_note = $3, cancelled_at = now(), updated_at = now()
			where id = $1 and status not in ('finalised', 'cancelled')`, id, reason, note)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrCompetitionStateChanged
		}
		if _, err := tx.Exec(ctx, `
			update competition.competition_attempts
			set status = 'voided', void_reason = 'competition_cancelled', updated_at = now()
			where competition_id = $1 and status <> 'voided'`, id); err != nil {
			return err
		}
		if err := expireSessions(ctx, tx, id); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}

func expireSessions(ctx context.Context, q querier, competitionID uuid.UUID) error {
	_, err := q.Exec(ctx, `
		update competition.game_sessions
		set status = 'expired', updated_at = now()
		where competition_id = $1 and status = 'active'`, competitionID)
	return err
}

// Freeze stops all leaderboard changes after close and records the closing
// and frozen standings (§17).
func (r *CompetitionRepository) Freeze(ctx context.Context, id uuid.UUID, snapshot []byte, adminID *uuid.UUID, audit models.AuditEntry) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			update competition.competitions
			set status = 'frozen', frozen_at = now(), updated_at = now()
			where id = $1 and status = 'closed'`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrCompetitionStateChanged
		}
		if err := expireSessions(ctx, tx, id); err != nil {
			return err
		}
		for _, kind := range []string{"close", "freeze"} {
			if _, err := tx.Exec(ctx, `
				insert into competition.leaderboard_snapshots (competition_id, snapshot_type, snapshot_data, created_by_admin_id)
				values ($1, $2, $3, $4)
				on conflict (competition_id, snapshot_type) do nothing`, id, kind, snapshot, adminID); err != nil {
				return err
			}
		}
		return insertAudit(ctx, tx, audit)
	})
}

// AdminCounts is attempts, submissions and scores awaiting review.
func (r *CompetitionRepository) AdminCounts(ctx context.Context, id uuid.UUID) (attempts, submissions, underReview int, err error) {
	err = r.db.QueryRow(ctx, `
		select
			(select count(*) from competition.competition_attempts where competition_id = $1),
			(select count(*) from competition.score_submissions where competition_id = $1),
			(select count(*) from competition.score_submissions
			  where competition_id = $1 and validation_status in ('pending', 'manual_review'))`, id).
		Scan(&attempts, &submissions, &underReview)
	return
}

// ─── Prize reserve ────────────────────────────────────────────────────────

const reserveColumns = `id, competition_id, reserve_amount, currency, funding_source, status,
	evidence_reference, funded_at, created_at, updated_at`

func scanReserve(row scanner) (*models.PrizeReserve, error) {
	var p models.PrizeReserve
	err := row.Scan(&p.ID, &p.CompetitionID, &p.ReserveAmount, &p.Currency, &p.FundingSource,
		&p.Status, &p.EvidenceReference, &p.FundedAt, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// GetReserve returns the competition's prize reserve, or nil if none is set.
func (r *CompetitionRepository) GetReserve(ctx context.Context, competitionID uuid.UUID) (*models.PrizeReserve, error) {
	p, err := scanReserve(r.db.QueryRow(ctx,
		`select `+reserveColumns+` from finance.prize_reserves where competition_id = $1`, competitionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

// UpsertReserve sets the reserve amount while it is still pending.
func (r *CompetitionRepository) UpsertReserve(ctx context.Context, p *models.PrizeReserve, audit models.AuditEntry) (*models.PrizeReserve, error) {
	var out *models.PrizeReserve
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = scanReserve(tx.QueryRow(ctx, `
			insert into finance.prize_reserves (competition_id, reserve_amount, currency, funding_source)
			values ($1, $2, $3, $4)
			on conflict (competition_id) do update
			set reserve_amount = excluded.reserve_amount, currency = excluded.currency,
			    funding_source = excluded.funding_source, updated_at = now()
			where finance.prize_reserves.status = 'pending'
			returning `+reserveColumns, p.CompetitionID, p.ReserveAmount, p.Currency, p.FundingSource))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrReserveLocked
		}
		if err != nil {
			return err
		}
		audit.After = out
		return insertAudit(ctx, tx, audit)
	})
	return out, err
}

// FundReserve records that the prize money is held, with its evidence.
func (r *CompetitionRepository) FundReserve(ctx context.Context, competitionID uuid.UUID, evidence string, audit models.AuditEntry) (*models.PrizeReserve, error) {
	var out *models.PrizeReserve
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = scanReserve(tx.QueryRow(ctx, `
			update finance.prize_reserves
			set status = 'funded', evidence_reference = $2, funded_at = now(), updated_at = now()
			where competition_id = $1 and status = 'pending'
			returning `+reserveColumns, competitionID, evidence))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrReserveLocked
		}
		if err != nil {
			return err
		}
		audit.After = out
		return insertAudit(ctx, tx, audit)
	})
	return out, err
}

// ─── Attempts ─────────────────────────────────────────────────────────────

// MyStat is a customer's attempts and best accepted score in a competition.
type MyStat struct {
	AttemptsUsed int
	BestScore    *int64
}

// MyStats returns the customer's attempts and best score for several
// competitions in one query.
func (r *CompetitionRepository) MyStats(ctx context.Context, customerID uuid.UUID, competitionIDs []uuid.UUID) (map[uuid.UUID]MyStat, error) {
	out := make(map[uuid.UUID]MyStat, len(competitionIDs))
	if len(competitionIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.Query(ctx, `
		select a.competition_id,
		       count(*) filter (where a.status <> 'voided'),
		       max(s.score) filter (where s.validation_status = 'accepted')
		from competition.competition_attempts a
		left join competition.score_submissions s on s.attempt_id = a.id
		where a.customer_id = $1 and a.competition_id = any($2)
		group by a.competition_id`, customerID, competitionIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var st MyStat
		if err := rows.Scan(&id, &st.AttemptsUsed, &st.BestScore); err != nil {
			return nil, err
		}
		out[id] = st
	}
	return out, rows.Err()
}

// CreateAttemptInput is everything the server decides when an official
// attempt begins.
type CreateAttemptInput struct {
	CompetitionID  uuid.UUID
	CustomerID     uuid.UUID
	GameVersionID  uuid.UUID
	Seed           string
	Config         json.RawMessage
	ExpiresAt      time.Time
	MaxAttempts    int
	PointsSpent    int
	PointsLedgerID *uuid.UUID
}

// CreateAttempt opens the game session and records the attempt together.
// The attempt-number unique key means two simultaneous starts cannot both
// succeed, so the published attempt limit holds.
func (r *CompetitionRepository) CreateAttempt(ctx context.Context, in CreateAttemptInput) (*models.CompetitionAttempt, *models.GameSession, error) {
	var attempt models.CompetitionAttempt
	var session models.GameSession

	err := r.inTx(ctx, func(tx pgx.Tx) error {
		var active, total int
		if err := tx.QueryRow(ctx, `
			select count(*) filter (where status <> 'voided'), count(*)
			from competition.competition_attempts
			where competition_id = $1 and customer_id = $2`, in.CompetitionID, in.CustomerID).
			Scan(&active, &total); err != nil {
			return err
		}
		if active >= in.MaxAttempts {
			return ErrAttemptLimitReached
		}

		config := []byte(in.Config)
		if len(config) == 0 {
			config = []byte("{}")
		}
		var rawConfig []byte
		if err := tx.QueryRow(ctx, `
			insert into competition.game_sessions
				(game_version_id, customer_id, mode, competition_id, server_seed, config, expires_at)
			values ($1, $2, 'official', $3, $4, $5, $6)
			returning id, game_version_id, customer_id, guest_token, mode, competition_id,
			          server_seed, config, status, started_at, expires_at, submitted_at`,
			in.GameVersionID, in.CustomerID, in.CompetitionID, in.Seed, config, in.ExpiresAt).
			Scan(&session.ID, &session.GameVersionID, &session.CustomerID, &session.GuestToken, &session.Mode,
				&session.CompetitionID, &session.ServerSeed, &rawConfig, &session.Status, &session.StartedAt,
				&session.ExpiresAt, &session.SubmittedAt); err != nil {
			return err
		}
		session.Config = json.RawMessage(rawConfig)

		number := total + 1
		err := tx.QueryRow(ctx, `
			insert into competition.competition_attempts
				(competition_id, customer_id, session_id, points_ledger_id, points_spent,
				 attempt_number, server_seed, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8)
			returning id, competition_id, customer_id, session_id, points_ledger_id, points_spent,
			          attempt_number, status, void_reason, started_at, submitted_at`,
			in.CompetitionID, in.CustomerID, session.ID, in.PointsLedgerID, in.PointsSpent,
			number, in.Seed, fmt.Sprintf("%s:%s:%d", in.CompetitionID, in.CustomerID, number)).
			Scan(&attempt.ID, &attempt.CompetitionID, &attempt.CustomerID, &attempt.SessionID,
				&attempt.PointsLedgerID, &attempt.PointsSpent, &attempt.AttemptNumber, &attempt.Status,
				&attempt.VoidReason, &attempt.StartedAt, &attempt.SubmittedAt)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrAttemptConflict
		}
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	return &attempt, &session, nil
}

func (r *CompetitionRepository) getAttempt(ctx context.Context, where string, arg uuid.UUID) (*models.CompetitionAttempt, error) {
	var a models.CompetitionAttempt
	err := r.db.QueryRow(ctx, `
		select id, competition_id, customer_id, session_id, points_ledger_id, points_spent,
		       attempt_number, status, void_reason, started_at, submitted_at
		from competition.competition_attempts
		where `+where+` = $1`, arg).
		Scan(&a.ID, &a.CompetitionID, &a.CustomerID, &a.SessionID, &a.PointsLedgerID, &a.PointsSpent,
			&a.AttemptNumber, &a.Status, &a.VoidReason, &a.StartedAt, &a.SubmittedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAttemptNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// GetAttemptBySession finds the attempt an official session belongs to.
func (r *CompetitionRepository) GetAttemptBySession(ctx context.Context, sessionID uuid.UUID) (*models.CompetitionAttempt, error) {
	return r.getAttempt(ctx, "session_id", sessionID)
}

// GetAttemptByID loads one attempt.
func (r *CompetitionRepository) GetAttemptByID(ctx context.Context, attemptID uuid.UUID) (*models.CompetitionAttempt, error) {
	return r.getAttempt(ctx, "id", attemptID)
}

// ─── Score submissions ────────────────────────────────────────────────────

const submissionColumns = `s.id, s.competition_id, s.attempt_id, s.customer_id, s.score, s.client_score,
	s.duration_ms, s.moves_count, s.stats, s.event_log_hash, s.validation_status, s.review_reason,
	s.reviewed_by_admin_id, s.reviewed_at, s.created_at`

func scanSubmission(row scanner, extra ...any) (*models.ScoreSubmission, error) {
	var s models.ScoreSubmission
	var stats []byte
	dest := []any{&s.ID, &s.CompetitionID, &s.AttemptID, &s.CustomerID, &s.Score, &s.ClientScore,
		&s.DurationMs, &s.MovesCount, &stats, &s.EventLogHash, &s.ValidationStatus, &s.ReviewReason,
		&s.ReviewedByAdminID, &s.ReviewedAt, &s.CreatedAt}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return nil, err
	}
	s.Stats = map[string]int64{}
	if len(stats) > 0 {
		if err := json.Unmarshal(stats, &s.Stats); err != nil {
			return nil, err
		}
	}
	return &s, nil
}

// OfficialScoreInput is the replayed result of an official attempt.
type OfficialScoreInput struct {
	SessionID        uuid.UUID
	AttemptID        uuid.UUID
	CompetitionID    uuid.UUID
	CustomerID       uuid.UUID
	Score            int64
	ClientScore      *int64
	DurationMs       int64
	MovesCount       int
	Stats            map[string]int64
	EventLog         []string
	EventLogHash     string
	ValidationStatus string
	ReviewReason     *string
}

// SaveOfficialScore closes the session, writes the score submission and
// settles the attempt in one transaction. A replayed request finds the
// session already closed and gets ErrGameSessionNotFound, so a score can
// never be written twice.
func (r *CompetitionRepository) SaveOfficialScore(ctx context.Context, in OfficialScoreInput) (*models.ScoreSubmission, error) {
	stats := in.Stats
	if stats == nil {
		stats = map[string]int64{}
	}
	rawStats, err := json.Marshal(stats)
	if err != nil {
		return nil, err
	}
	moves := in.EventLog
	if moves == nil {
		moves = []string{}
	}
	rawLog, err := json.Marshal(moves)
	if err != nil {
		return nil, err
	}

	var out *models.ScoreSubmission
	err = r.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			update competition.game_sessions
			set status = 'submitted', submitted_at = now(), updated_at = now()
			where id = $1 and status = 'active'`, in.SessionID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrGameSessionNotFound
		}

		out, err = scanSubmission(tx.QueryRow(ctx, `
			insert into competition.score_submissions as s
				(competition_id, attempt_id, customer_id, score, client_score, duration_ms, moves_count,
				 stats, event_log, event_log_hash, validation_status, review_reason, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
			returning `+submissionColumns,
			in.CompetitionID, in.AttemptID, in.CustomerID, in.Score, in.ClientScore, in.DurationMs,
			in.MovesCount, rawStats, rawLog, in.EventLogHash, in.ValidationStatus, in.ReviewReason,
			"attempt:"+in.AttemptID.String()))
		if err != nil {
			return err
		}

		_, err = tx.Exec(ctx, `
			update competition.competition_attempts
			set status = case $2 when 'accepted' then 'accepted' when 'rejected' then 'rejected' else 'submitted' end,
			    submitted_at = now(), updated_at = now()
			where id = $1`, in.AttemptID, in.ValidationStatus)
		return err
	})
	return out, err
}

// GetSubmissionBySession reads back an official score, for idempotent retries.
func (r *CompetitionRepository) GetSubmissionBySession(ctx context.Context, sessionID uuid.UUID) (*models.ScoreSubmission, error) {
	s, err := scanSubmission(r.db.QueryRow(ctx, `
		select `+submissionColumns+`
		from competition.score_submissions s
		inner join competition.competition_attempts a on a.id = s.attempt_id
		where a.session_id = $1`, sessionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSubmissionNotFound
	}
	return s, err
}

// CountUnresolved is the number of scores still pending or under review.
func (r *CompetitionRepository) CountUnresolved(ctx context.Context, competitionID uuid.UUID) (int, error) {
	var n int
	err := r.db.QueryRow(ctx, `
		select count(*) from competition.score_submissions
		where competition_id = $1 and validation_status in ('pending', 'manual_review')`, competitionID).Scan(&n)
	return n, err
}

// ReviewQueue lists scores held for manual review, best first.
func (r *CompetitionRepository) ReviewQueue(ctx context.Context, competitionID uuid.UUID) ([]models.ScoreSubmission, error) {
	rows, err := r.db.Query(ctx, `
		select `+submissionColumns+`, c.display_name
		from competition.score_submissions s
		inner join customer.customers c on c.id = s.customer_id
		where s.competition_id = $1 and s.validation_status in ('pending', 'manual_review')
		order by s.score desc, s.duration_ms asc`, competitionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]models.ScoreSubmission, 0)
	for rows.Next() {
		var name *string
		s, err := scanSubmission(rows, &name)
		if err != nil {
			return nil, err
		}
		s.DisplayName = name
		out = append(out, *s)
	}
	return out, rows.Err()
}

// ReviewSubmission records an admin's verdict on a held score. Only the
// review fields change; the score itself is immutable (enforced in SQL).
func (r *CompetitionRepository) ReviewSubmission(ctx context.Context, competitionID, submissionID uuid.UUID, status string, reason *string, adminID *uuid.UUID, audit models.AuditEntry) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		var attemptID uuid.UUID
		err := tx.QueryRow(ctx, `
			update competition.score_submissions s
			set validation_status = $3, review_reason = $4, reviewed_by_admin_id = $5, reviewed_at = now()
			from competition.competitions c
			where s.id = $2 and s.competition_id = $1 and c.id = s.competition_id
			  and c.status in ('live', 'closed', 'frozen')
			  and s.validation_status in ('pending', 'manual_review', 'accepted')
			returning s.attempt_id`, competitionID, submissionID, status, reason, adminID).Scan(&attemptID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSubmissionNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			update competition.competition_attempts set status = $2, updated_at = now() where id = $1`,
			attemptID, status); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}

// RejectSubmissions marks scores that failed re-verification at finalisation.
func (r *CompetitionRepository) RejectSubmissions(ctx context.Context, competitionID uuid.UUID, ids []uuid.UUID, reason string, adminID *uuid.UUID, audit models.AuditEntry) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			update competition.score_submissions
			set validation_status = 'rejected', review_reason = $3, reviewed_by_admin_id = $4, reviewed_at = now()
			where competition_id = $1 and id = any($2)`, competitionID, ids, reason, adminID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			update competition.competition_attempts a set status = 'rejected', updated_at = now()
			from competition.score_submissions s
			where s.attempt_id = a.id and s.id = any($1)`, ids); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}

// ─── Leaderboards ─────────────────────────────────────────────────────────

// LeaderRow is one ranked player before it is shaped for the public. It is
// also the shape stored in leaderboard snapshots and shown to admins, which is
// why it carries the customer id; public views never pass it on.
type LeaderRow struct {
	Rank             int        `json:"rank"`
	CustomerID       *uuid.UUID `json:"customer_id,omitempty"`
	Player           string     `json:"-"` // customer id or guest token, for matching the caller
	DisplayName      *string    `json:"display_name,omitempty"`
	CountryCode      *string    `json:"country_code,omitempty"`
	CountryName      *string    `json:"country_name,omitempty"`
	Score            int64      `json:"score"`
	DurationMs       int64      `json:"duration_ms"`
	AchievedAt       time.Time  `json:"achieved_at"`
	ValidationStatus string     `json:"validation_status"`
}

// CompetitionLeaderboard ranks each player's best score. Ties on score are
// broken by server-measured play time (§14.4); players still level share a
// rank. Scores under review appear flagged; rejected ones do not appear.
// Returns the top rows plus the caller's own row even when it is further down.
func (r *CompetitionRepository) CompetitionLeaderboard(ctx context.Context, competitionID uuid.UUID, limit int, customerID *uuid.UUID) ([]LeaderRow, *LeaderRow, int, error) {
	rows, err := r.db.Query(ctx, `
		with best as (
			select distinct on (s.customer_id)
			       s.customer_id, s.score, s.duration_ms, s.created_at, s.validation_status
			from competition.score_submissions s
			where s.competition_id = $1 and s.validation_status in ('accepted', 'manual_review')
			order by s.customer_id, s.score desc, s.duration_ms asc, s.created_at asc
		), ranked as (
			select b.*, rank() over (order by b.score desc, b.duration_ms asc) as rnk,
			       count(*) over () as total
			from best b
		)
		select r.rnk, r.customer_id, c.display_name, co.iso_code, co.name,
		       r.score, r.duration_ms, r.created_at, r.validation_status, r.total
		from ranked r
		inner join customer.customers c on c.id = r.customer_id
		inner join core.countries co on co.id = c.country_id
		where r.rnk <= $2 or r.customer_id = $3::uuid
		order by r.rnk asc, r.created_at asc`, competitionID, limit, customerID)
	if err != nil {
		return nil, nil, 0, err
	}
	defer rows.Close()

	var top []LeaderRow
	var me *LeaderRow
	total := 0
	for rows.Next() {
		var row LeaderRow
		var id uuid.UUID
		var code, name string
		if err := rows.Scan(&row.Rank, &id, &row.DisplayName, &code, &name,
			&row.Score, &row.DurationMs, &row.AchievedAt, &row.ValidationStatus, &total); err != nil {
			return nil, nil, 0, err
		}
		row.CustomerID, row.CountryCode, row.CountryName = &id, &code, &name
		if customerID != nil && id == *customerID {
			mine := row
			me = &mine
		}
		if row.Rank <= limit {
			top = append(top, row)
		}
	}
	return top, me, total, rows.Err()
}

// RankedSubmission is a player's best accepted score with its move log, used
// to re-verify and settle winners at finalisation.
type RankedSubmission struct {
	SubmissionID uuid.UUID
	CustomerID   uuid.UUID
	Score        int64
	DurationMs   int64
	Rank         int
	EventLog     []string
}

// RankedAccepted lists each player's best accepted score in final order.
func (r *CompetitionRepository) RankedAccepted(ctx context.Context, competitionID uuid.UUID, limit int) ([]RankedSubmission, error) {
	rows, err := r.db.Query(ctx, `
		with best as (
			select distinct on (s.customer_id)
			       s.id, s.customer_id, s.score, s.duration_ms, s.created_at, s.event_log
			from competition.score_submissions s
			where s.competition_id = $1 and s.validation_status = 'accepted'
			order by s.customer_id, s.score desc, s.duration_ms asc, s.created_at asc
		)
		select b.id, b.customer_id, b.score, b.duration_ms, b.event_log,
		       rank() over (order by b.score desc, b.duration_ms asc)
		from best b
		order by b.score desc, b.duration_ms asc, b.created_at asc
		limit $2`, competitionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]RankedSubmission, 0)
	for rows.Next() {
		var s RankedSubmission
		var rawLog []byte
		if err := rows.Scan(&s.SubmissionID, &s.CustomerID, &s.Score, &s.DurationMs, &rawLog, &s.Rank); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(rawLog, &s.EventLog); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ─── Winners and claims ───────────────────────────────────────────────────

// WinnerPlan is one winner row to write at finalisation or replacement.
type WinnerPlan struct {
	CustomerID    uuid.UUID
	SubmissionID  uuid.UUID
	PrizePosition int
	Rank          int
	Status        string
	Reason        *string
}

func insertWinner(ctx context.Context, q querier, competitionID uuid.UUID, p WinnerPlan) error {
	_, err := q.Exec(ctx, `
		insert into competition.competition_winners
			(competition_id, customer_id, score_submission_id, prize_position, rank, status, status_reason)
		values ($1, $2, $3, $4, $5, $6, $7)`,
		competitionID, p.CustomerID, p.SubmissionID, p.PrizePosition, p.Rank, p.Status, p.Reason)
	return err
}

// ApplyFinalisation declares the result: winners written, final snapshot
// stored, competition finalised. Refuses if the competition left the frozen
// state in the meantime.
func (r *CompetitionRepository) ApplyFinalisation(ctx context.Context, competitionID uuid.UUID, plans []WinnerPlan, snapshot []byte, adminID *uuid.UUID, audit models.AuditEntry) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			update competition.competitions
			set status = 'finalised', finalised_at = now(), updated_at = now()
			where id = $1 and status = 'frozen'`, competitionID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrCompetitionStateChanged
		}
		for _, p := range plans {
			if err := insertWinner(ctx, tx, competitionID, p); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `
			insert into competition.leaderboard_snapshots (competition_id, snapshot_type, snapshot_data, created_by_admin_id)
			values ($1, 'final', $2, $3)`, competitionID, snapshot, adminID); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}

const winnerSelect = `
	select w.id, w.competition_id, w.customer_id, w.score_submission_id, w.prize_position, w.rank,
	       w.status, w.status_reason, w.validated_at, w.created_at, w.updated_at,
	       c.display_name, co.name, s.score, s.duration_ms, s.created_at,
	       pc.id, pc.winner_id, pc.customer_id, pc.claim_deadline_at, pc.claimed_at, pc.status,
	       pc.delivery_address_id, pc.terms_accepted_at, pc.created_at, pc.updated_at
	from competition.competition_winners w
	inner join customer.customers c on c.id = w.customer_id
	inner join core.countries co on co.id = c.country_id
	inner join competition.score_submissions s on s.id = w.score_submission_id
	left join competition.prize_claims pc on pc.winner_id = w.id`

func scanWinner(row scanner) (*models.CompetitionWinner, error) {
	var w models.CompetitionWinner
	var (
		claimID, claimWinner, claimCustomer, claimAddress *uuid.UUID
		claimDeadline, claimCreated, claimUpdated         *time.Time
		claimedAt, termsAt                                *time.Time
		claimStatus                                       *string
	)
	err := row.Scan(&w.ID, &w.CompetitionID, &w.CustomerID, &w.ScoreSubmissionID, &w.PrizePosition,
		&w.Rank, &w.Status, &w.StatusReason, &w.ValidatedAt, &w.CreatedAt, &w.UpdatedAt,
		&w.DisplayName, &w.CountryName, &w.Score, &w.DurationMs, &w.AchievedAt,
		&claimID, &claimWinner, &claimCustomer, &claimDeadline, &claimedAt, &claimStatus,
		&claimAddress, &termsAt, &claimCreated, &claimUpdated)
	if err != nil {
		return nil, err
	}
	if claimID != nil {
		w.Claim = &models.PrizeClaim{
			ID: *claimID, WinnerID: *claimWinner, CustomerID: *claimCustomer,
			ClaimDeadlineAt: *claimDeadline, ClaimedAt: claimedAt, Status: *claimStatus,
			DeliveryAddressID: claimAddress, TermsAcceptedAt: termsAt,
			CreatedAt: *claimCreated, UpdatedAt: *claimUpdated,
		}
	}
	return &w, nil
}

// Winners lists every winner row for a competition, active and historical.
func (r *CompetitionRepository) Winners(ctx context.Context, competitionID uuid.UUID) ([]models.CompetitionWinner, error) {
	rows, err := r.db.Query(ctx, winnerSelect+`
		where w.competition_id = $1
		order by w.prize_position asc, w.created_at asc`, competitionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]models.CompetitionWinner, 0)
	for rows.Next() {
		w, err := scanWinner(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

func (r *CompetitionRepository) WinnerByID(ctx context.Context, competitionID, winnerID uuid.UUID) (*models.CompetitionWinner, error) {
	w, err := scanWinner(r.db.QueryRow(ctx, winnerSelect+` where w.competition_id = $1 and w.id = $2`, competitionID, winnerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrWinnerNotFound
	}
	return w, err
}

// ValidateWinner confirms a winner and opens their 14-day claim window.
func (r *CompetitionRepository) ValidateWinner(ctx context.Context, winnerID uuid.UUID, claimDeadline time.Time, audit models.AuditEntry) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		var customerID uuid.UUID
		err := tx.QueryRow(ctx, `
			update competition.competition_winners
			set status = 'validated', validated_at = now(), updated_at = now()
			where id = $1 and status = 'pending_validation'
			returning customer_id`, winnerID).Scan(&customerID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrWinnerStateChanged
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			insert into competition.prize_claims (winner_id, customer_id, claim_deadline_at)
			values ($1, $2, $3)`, winnerID, customerID, claimDeadline); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}

// ReplaceWinner retires a winner (disqualified or unclaimed), keeping the
// original row as history, and gives the prize position to the next eligible
// player when there is one (§19.3). Replacements may include players skipped
// on the way as ineligible, recorded as disqualified.
func (r *CompetitionRepository) ReplaceWinner(ctx context.Context, competitionID, winnerID uuid.UUID, newStatus, reason, claimStatus string, fromStatuses []string, replacements []WinnerPlan, audit models.AuditEntry) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			update competition.competition_winners
			set status = $2, status_reason = $3, updated_at = now()
			where id = $1 and status = any($4)`, winnerID, newStatus, reason, fromStatuses)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrWinnerStateChanged
		}
		if _, err := tx.Exec(ctx, `
			update competition.prize_claims
			set status = $2, updated_at = now()
			where winner_id = $1 and status in ('pending', 'claimed', 'verified')`, winnerID, claimStatus); err != nil {
			return err
		}
		for _, p := range replacements {
			if err := insertWinner(ctx, tx, competitionID, p); err != nil {
				return err
			}
		}
		return insertAudit(ctx, tx, audit)
	})
}

// CustomerOwnsAddress reports whether a delivery address is the customer's.
func (r *CompetitionRepository) CustomerOwnsAddress(ctx context.Context, customerID, addressID uuid.UUID) (bool, error) {
	var ok bool
	err := r.db.QueryRow(ctx, `
		select exists (
			select 1 from customer.customer_addresses where id = $1 and customer_id = $2
		)`, addressID, customerID).Scan(&ok)
	return ok, err
}

const claimColumns = `id, winner_id, customer_id, claim_deadline_at, claimed_at, status,
	delivery_address_id, terms_accepted_at, created_at, updated_at`

func scanClaim(row scanner) (*models.PrizeClaim, error) {
	var c models.PrizeClaim
	err := row.Scan(&c.ID, &c.WinnerID, &c.CustomerID, &c.ClaimDeadlineAt, &c.ClaimedAt, &c.Status,
		&c.DeliveryAddressID, &c.TermsAcceptedAt, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// ClaimPrize records the winner accepting the prize and where it goes.
func (r *CompetitionRepository) ClaimPrize(ctx context.Context, claimID, customerID, addressID uuid.UUID, audit models.AuditEntry) (*models.PrizeClaim, error) {
	var out *models.PrizeClaim
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = scanClaim(tx.QueryRow(ctx, `
			update competition.prize_claims
			set status = 'claimed', claimed_at = now(), terms_accepted_at = now(),
			    delivery_address_id = $3, updated_at = now()
			where id = $1 and customer_id = $2 and status = 'pending' and claim_deadline_at > now()
			returning `+claimColumns, claimID, customerID, addressID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrClaimStateChanged
		}
		if err != nil {
			return err
		}
		audit.After = out
		return insertAudit(ctx, tx, audit)
	})
	return out, err
}

// UpdateClaimStatus moves a claim through verification and fulfilment.
func (r *CompetitionRepository) UpdateClaimStatus(ctx context.Context, competitionID, claimID uuid.UUID, from []string, to string, audit models.AuditEntry) (*models.PrizeClaim, error) {
	var out *models.PrizeClaim
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = scanClaim(tx.QueryRow(ctx, `
			update competition.prize_claims pc
			set status = $3, updated_at = now()
			from competition.competition_winners w
			where pc.id = $2 and w.id = pc.winner_id and w.competition_id = $1 and pc.status = any($4)
			returning pc.id, pc.winner_id, pc.customer_id, pc.claim_deadline_at, pc.claimed_at, pc.status,
			          pc.delivery_address_id, pc.terms_accepted_at, pc.created_at, pc.updated_at`,
			competitionID, claimID, to, from))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrClaimStateChanged
		}
		if err != nil {
			return err
		}
		audit.After = out
		return insertAudit(ctx, tx, audit)
	})
	return out, err
}
