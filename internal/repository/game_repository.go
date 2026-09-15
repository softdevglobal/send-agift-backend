package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"myapp/internal/models"
)

var (
	ErrGameNotFound        = errors.New("game not found")
	ErrGameVersionNotFound = errors.New("approved game version not found")
	ErrGameSessionNotFound = errors.New("game session not found")
	ErrGameScoreNotFound   = errors.New("game score not found")
)

// GameRepository persists the skill-game catalog, sessions and scores under
// schema competition.
//
// Player identity reuses SocialIdentity (customer XOR guest), the same shape
// the reel social features use. Game configs are stored and returned raw:
// their shape belongs to each game's engine, not to this layer.
type GameRepository struct {
	db *pgxpool.Pool
}

func NewGameRepository(db *pgxpool.Pool) *GameRepository {
	return &GameRepository{db: db}
}

// ─── Catalog ──────────────────────────────────────────────────────────────

// ListPlayable returns approved games that have an approved version, together
// with that version. Anything still in draft stays invisible to the app.
func (r *GameRepository) ListPlayable(ctx context.Context) ([]models.GameView, error) {
	rows, err := r.db.Query(ctx, `
		select g.slug, g.name, g.description, g.game_type, v.version, v.config
		from competition.games g
		inner join competition.game_versions v
		        on v.game_id = g.id and v.status = 'approved'
		where g.status = 'approved'
		order by g.created_at asc, g.name asc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]models.GameView, 0)
	for rows.Next() {
		var gv models.GameView
		var rawConfig []byte
		if err := rows.Scan(&gv.Slug, &gv.Name, &gv.Description, &gv.GameType, &gv.Version, &rawConfig); err != nil {
			return nil, err
		}
		gv.Config = json.RawMessage(rawConfig)
		out = append(out, gv)
	}
	return out, rows.Err()
}

// PlayableGame is a game plus its approved version, resolved in one query.
type PlayableGame struct {
	Game    models.Game
	Version models.GameVersion
}

// GetPlayableBySlug loads an approved game and its approved version.
func (r *GameRepository) GetPlayableBySlug(ctx context.Context, slug string) (*PlayableGame, error) {
	var pg PlayableGame
	var rawConfig []byte

	err := r.db.QueryRow(ctx, `
		select g.id, g.slug, g.name, g.description, g.game_type, g.status, g.created_at, g.updated_at,
		       v.id, v.game_id, v.version, v.config, v.status, v.approved_at, v.created_at
		from competition.games g
		inner join competition.game_versions v
		        on v.game_id = g.id and v.status = 'approved'
		where g.slug = $1 and g.status = 'approved'`, slug).
		Scan(
			&pg.Game.ID, &pg.Game.Slug, &pg.Game.Name, &pg.Game.Description,
			&pg.Game.GameType, &pg.Game.Status, &pg.Game.CreatedAt, &pg.Game.UpdatedAt,
			&pg.Version.ID, &pg.Version.GameID, &pg.Version.Version, &rawConfig,
			&pg.Version.Status, &pg.Version.ApprovedAt, &pg.Version.CreatedAt,
		)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrGameNotFound
	}
	if err != nil {
		return nil, err
	}
	pg.Version.Config = json.RawMessage(rawConfig)
	return &pg, nil
}

// ─── Sessions ─────────────────────────────────────────────────────────────

// CreateSessionInput is everything the server decides when a play begins.
// None of it comes from the client.
type CreateSessionInput struct {
	GameVersionID uuid.UUID
	Identity      SocialIdentity
	Mode          string
	ServerSeed    string
	Config        json.RawMessage
	ExpiresAt     time.Time
}

// CreateSession records a new play before the first move is made.
func (r *GameRepository) CreateSession(ctx context.Context, in CreateSessionInput) (*models.GameSession, error) {
	config := []byte(in.Config)
	if len(config) == 0 {
		config = []byte("{}")
	}

	var s models.GameSession
	var scannedConfig []byte
	err := r.db.QueryRow(ctx, `
		insert into competition.game_sessions
			(game_version_id, customer_id, guest_token, mode, server_seed, config, expires_at)
		values ($1, $2, $3, $4, $5, $6, $7)
		returning id, game_version_id, customer_id, guest_token, mode, competition_id,
		          server_seed, config, status, started_at, expires_at, submitted_at`,
		in.GameVersionID, in.Identity.CustomerID, in.Identity.GuestToken,
		in.Mode, in.ServerSeed, config, in.ExpiresAt).
		Scan(&s.ID, &s.GameVersionID, &s.CustomerID, &s.GuestToken, &s.Mode, &s.CompetitionID,
			&s.ServerSeed, &scannedConfig, &s.Status, &s.StartedAt, &s.ExpiresAt, &s.SubmittedAt)
	if err != nil {
		return nil, err
	}
	s.Config = json.RawMessage(scannedConfig)
	return &s, nil
}

// GetSession loads one session by id, along with the slug of its game so the
// right replay engine can be chosen.
func (r *GameRepository) GetSession(ctx context.Context, sessionID uuid.UUID) (*models.GameSession, error) {
	var s models.GameSession
	var rawConfig []byte

	err := r.db.QueryRow(ctx, `
		select s.id, s.game_version_id, g.slug, s.customer_id, s.guest_token, s.mode, s.competition_id,
		       s.server_seed, s.config, s.status, s.started_at, s.expires_at, s.submitted_at
		from competition.game_sessions s
		inner join competition.game_versions v on v.id = s.game_version_id
		inner join competition.games g on g.id = v.game_id
		where s.id = $1`, sessionID).
		Scan(&s.ID, &s.GameVersionID, &s.GameSlug, &s.CustomerID, &s.GuestToken, &s.Mode, &s.CompetitionID,
			&s.ServerSeed, &rawConfig, &s.Status, &s.StartedAt, &s.ExpiresAt, &s.SubmittedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrGameSessionNotFound
	}
	if err != nil {
		return nil, err
	}
	s.Config = json.RawMessage(rawConfig)
	return &s, nil
}

// ExpireSession marks a session that ran past its deadline.
func (r *GameRepository) ExpireSession(ctx context.Context, sessionID uuid.UUID) error {
	_, err := r.db.Exec(ctx, `
		update competition.game_sessions
		set status = 'expired', updated_at = now()
		where id = $1 and status = 'active'`, sessionID)
	return err
}

// ─── Scores ───────────────────────────────────────────────────────────────

// SaveScoreInput carries the replayed, server-authoritative result.
type SaveScoreInput struct {
	SessionID        uuid.UUID
	GameVersionID    uuid.UUID
	Identity         SocialIdentity
	Score            int64
	ClientScore      *int64
	HighestTile      int
	MovesCount       int
	DurationMs       int64
	MoveLogHash      string
	ValidationStatus string
	ReviewReason     *string
	Stats            map[string]int64
}

const scoreColumns = `session_id, game_version_id, customer_id, guest_token, score, client_score,
	highest_tile, moves_count, duration_ms, move_log_hash, validation_status,
	review_reason, stats, created_at`

func scanScore(row pgx.Row) (*models.GameScore, error) {
	var sc models.GameScore
	var rawStats []byte
	if err := row.Scan(&sc.SessionID, &sc.GameVersionID, &sc.CustomerID, &sc.GuestToken, &sc.Score,
		&sc.ClientScore, &sc.HighestTile, &sc.MovesCount, &sc.DurationMs, &sc.MoveLogHash,
		&sc.ValidationStatus, &sc.ReviewReason, &rawStats, &sc.CreatedAt); err != nil {
		return nil, err
	}
	sc.Stats = map[string]int64{}
	if len(rawStats) > 0 {
		if err := json.Unmarshal(rawStats, &sc.Stats); err != nil {
			return nil, err
		}
	}
	return &sc, nil
}

// SaveScore closes the session and writes its score in one transaction.
//
// The session is only closed when it is still active, so a replayed HTTP
// request cannot score twice; the caller detects that case by the returned
// ErrGameSessionNotFound and reads back the existing score instead.
func (r *GameRepository) SaveScore(ctx context.Context, in SaveScoreInput) (*models.GameScore, error) {
	stats := in.Stats
	if stats == nil {
		stats = map[string]int64{}
	}
	rawStats, err := json.Marshal(stats)
	if err != nil {
		return nil, err
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// Claim the session. Zero rows means someone already submitted it.
	var submittedAt time.Time
	err = tx.QueryRow(ctx, `
		update competition.game_sessions
		set status = 'submitted', submitted_at = now(), updated_at = now()
		where id = $1 and status = 'active'
		returning submitted_at`, in.SessionID).Scan(&submittedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrGameSessionNotFound
	}
	if err != nil {
		return nil, err
	}

	sc, err := scanScore(tx.QueryRow(ctx, `
		insert into competition.game_scores
			(session_id, game_version_id, customer_id, guest_token, score, client_score,
			 highest_tile, moves_count, duration_ms, move_log_hash, validation_status, review_reason, stats)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		returning `+scoreColumns,
		in.SessionID, in.GameVersionID, in.Identity.CustomerID, in.Identity.GuestToken,
		in.Score, in.ClientScore, in.HighestTile, in.MovesCount, in.DurationMs,
		in.MoveLogHash, in.ValidationStatus, in.ReviewReason, rawStats))
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return sc, nil
}

// GetScoreBySession reads an already-recorded score, so a retried submit
// returns the original result instead of an error.
func (r *GameRepository) GetScoreBySession(ctx context.Context, sessionID uuid.UUID) (*models.GameScore, error) {
	sc, err := scanScore(r.db.QueryRow(ctx, `
		select `+scoreColumns+`
		from competition.game_scores
		where session_id = $1`, sessionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrGameScoreNotFound
	}
	return sc, err
}

// PersonalBest returns the caller's highest accepted score for a version.
// Zero means they have not scored yet.
func (r *GameRepository) PersonalBest(ctx context.Context, versionID uuid.UUID, identity SocialIdentity) (int64, error) {
	var best *int64
	err := r.db.QueryRow(ctx, `
		select max(score)
		from competition.game_scores
		where game_version_id = $1
		  and validation_status = 'accepted'
		  and (
		        ($2::uuid is not null and customer_id = $2)
		     or ($3::text is not null and guest_token = $3)
		  )`, versionID, identity.CustomerID, identity.GuestToken).Scan(&best)
	if err != nil {
		return 0, err
	}
	if best == nil {
		return 0, nil
	}
	return *best, nil
}

// identityKey is the one string that identifies a player on a practice board:
// the customer id, or the guest token for guests.
func identityKey(identity SocialIdentity) *string {
	switch {
	case identity.CustomerID != nil:
		key := identity.CustomerID.String()
		return &key
	case identity.GuestToken != nil:
		return identity.GuestToken
	}
	return nil
}

// Leaderboard ranks the best accepted score per player. Score ties are broken
// by play time, never by who submitted first (§14.4); players still level
// share a rank. Returns the top rows, the caller's own row even when it is
// further down, and the number of ranked players.
//
// DISTINCT ON keeps one row per identity so a single strong player cannot fill
// the whole board with their own repeat runs.
func (r *GameRepository) Leaderboard(ctx context.Context, versionID uuid.UUID, limit int, caller *SocialIdentity) ([]LeaderRow, *LeaderRow, int, error) {
	var me *string
	if caller != nil {
		me = identityKey(*caller)
	}
	rows, err := r.db.Query(ctx, `
		with best as (
			select distinct on (coalesce(s.customer_id::text, s.guest_token))
			       coalesce(s.customer_id::text, s.guest_token) as player,
			       s.customer_id, s.score, s.duration_ms, s.created_at
			from competition.game_scores s
			where s.game_version_id = $1
			  and s.validation_status = 'accepted'
			order by coalesce(s.customer_id::text, s.guest_token),
			         s.score desc, s.duration_ms asc, s.created_at asc
		), ranked as (
			select b.*, rank() over (order by b.score desc, b.duration_ms asc) as rnk,
			       count(*) over () as total
			from best b
		)
		select r.rnk, r.player, r.customer_id, c.display_name, co.iso_code, co.name,
		       r.score, r.duration_ms, r.created_at, r.total
		from ranked r
		left join customer.customers c on c.id = r.customer_id
		left join core.countries co on co.id = c.country_id
		where r.rnk <= $2 or r.player = $3::text
		order by r.rnk asc, r.created_at asc`, versionID, limit, me)
	if err != nil {
		return nil, nil, 0, err
	}
	defer rows.Close()

	top := make([]LeaderRow, 0, limit)
	var mine *LeaderRow
	total := 0
	for rows.Next() {
		row := LeaderRow{ValidationStatus: "accepted"}
		if err := rows.Scan(&row.Rank, &row.Player, &row.CustomerID, &row.DisplayName,
			&row.CountryCode, &row.CountryName, &row.Score, &row.DurationMs, &row.AchievedAt, &total); err != nil {
			return nil, nil, 0, err
		}
		if me != nil && row.Player == *me {
			copied := row
			mine = &copied
		}
		// Shared ranks can push past the limit; the board stays the size asked for.
		if row.Rank <= limit && len(top) < limit {
			top = append(top, row)
		}
	}
	return top, mine, total, rows.Err()
}
