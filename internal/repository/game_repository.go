package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"myapp/internal/games"
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
// the reel social features use.
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
		order by g.name asc`)
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
		if err := json.Unmarshal(rawConfig, &gv.Config); err != nil {
			return nil, err
		}
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
	if err := json.Unmarshal(rawConfig, &pg.Version.Config); err != nil {
		return nil, err
	}
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
	Config        games.Config
	ExpiresAt     time.Time
}

// CreateSession records a new play before the first move is made.
func (r *GameRepository) CreateSession(ctx context.Context, in CreateSessionInput) (*models.GameSession, error) {
	rawConfig, err := json.Marshal(in.Config)
	if err != nil {
		return nil, err
	}

	var s models.GameSession
	var scannedConfig []byte
	err = r.db.QueryRow(ctx, `
		insert into competition.game_sessions
			(game_version_id, customer_id, guest_token, mode, server_seed, config, expires_at)
		values ($1, $2, $3, $4, $5, $6, $7)
		returning id, game_version_id, customer_id, guest_token, mode, competition_id,
		          server_seed, config, status, started_at, expires_at, submitted_at`,
		in.GameVersionID, in.Identity.CustomerID, in.Identity.GuestToken,
		in.Mode, in.ServerSeed, rawConfig, in.ExpiresAt).
		Scan(&s.ID, &s.GameVersionID, &s.CustomerID, &s.GuestToken, &s.Mode, &s.CompetitionID,
			&s.ServerSeed, &scannedConfig, &s.Status, &s.StartedAt, &s.ExpiresAt, &s.SubmittedAt)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(scannedConfig, &s.Config); err != nil {
		return nil, err
	}
	return &s, nil
}

// GetSession loads one session by id.
func (r *GameRepository) GetSession(ctx context.Context, sessionID string) (*models.GameSession, error) {
	var s models.GameSession
	var rawConfig []byte

	err := r.db.QueryRow(ctx, `
		select id, game_version_id, customer_id, guest_token, mode, competition_id,
		       server_seed, config, status, started_at, expires_at, submitted_at
		from competition.game_sessions
		where id = $1`, sessionID).
		Scan(&s.ID, &s.GameVersionID, &s.CustomerID, &s.GuestToken, &s.Mode, &s.CompetitionID,
			&s.ServerSeed, &rawConfig, &s.Status, &s.StartedAt, &s.ExpiresAt, &s.SubmittedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrGameSessionNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(rawConfig, &s.Config); err != nil {
		return nil, err
	}
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
}

// SaveScore closes the session and writes its score in one transaction.
//
// The session is only closed when it is still active, so a replayed HTTP
// request cannot score twice; the caller detects that case by the returned
// ErrGameSessionNotFound and reads back the existing score instead.
func (r *GameRepository) SaveScore(ctx context.Context, in SaveScoreInput) (*models.GameScore, error) {
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

	var sc models.GameScore
	err = tx.QueryRow(ctx, `
		insert into competition.game_scores
			(session_id, game_version_id, customer_id, guest_token, score, client_score,
			 highest_tile, moves_count, duration_ms, move_log_hash, validation_status, review_reason)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		returning session_id, game_version_id, customer_id, guest_token, score, client_score,
		          highest_tile, moves_count, duration_ms, move_log_hash, validation_status,
		          review_reason, created_at`,
		in.SessionID, in.GameVersionID, in.Identity.CustomerID, in.Identity.GuestToken,
		in.Score, in.ClientScore, in.HighestTile, in.MovesCount, in.DurationMs,
		in.MoveLogHash, in.ValidationStatus, in.ReviewReason).
		Scan(&sc.SessionID, &sc.GameVersionID, &sc.CustomerID, &sc.GuestToken, &sc.Score,
			&sc.ClientScore, &sc.HighestTile, &sc.MovesCount, &sc.DurationMs, &sc.MoveLogHash,
			&sc.ValidationStatus, &sc.ReviewReason, &sc.CreatedAt)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &sc, nil
}

// GetScoreBySession reads an already-recorded score, so a retried submit
// returns the original result instead of an error.
func (r *GameRepository) GetScoreBySession(ctx context.Context, sessionID uuid.UUID) (*models.GameScore, error) {
	var sc models.GameScore
	err := r.db.QueryRow(ctx, `
		select session_id, game_version_id, customer_id, guest_token, score, client_score,
		       highest_tile, moves_count, duration_ms, move_log_hash, validation_status,
		       review_reason, created_at
		from competition.game_scores
		where session_id = $1`, sessionID).
		Scan(&sc.SessionID, &sc.GameVersionID, &sc.CustomerID, &sc.GuestToken, &sc.Score,
			&sc.ClientScore, &sc.HighestTile, &sc.MovesCount, &sc.DurationMs, &sc.MoveLogHash,
			&sc.ValidationStatus, &sc.ReviewReason, &sc.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrGameScoreNotFound
	}
	if err != nil {
		return nil, err
	}
	return &sc, nil
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

// Leaderboard returns the best accepted score per player, highest first.
//
// DISTINCT ON keeps one row per identity so a single strong player cannot fill
// the whole board with their own repeat runs.
func (r *GameRepository) Leaderboard(ctx context.Context, versionID uuid.UUID, limit int) ([]models.LeaderboardEntry, error) {
	rows, err := r.db.Query(ctx, `
		with best as (
			select distinct on (coalesce(s.customer_id::text, s.guest_token))
			       s.customer_id,
			       s.score,
			       s.highest_tile,
			       s.created_at,
			       c.display_name
			from competition.game_scores s
			left join customer.customers c on c.id = s.customer_id
			where s.game_version_id = $1
			  and s.validation_status = 'accepted'
			order by coalesce(s.customer_id::text, s.guest_token),
			         s.score desc, s.created_at asc
		)
		select customer_id, display_name, score, highest_tile, created_at
		from best
		order by score desc, created_at asc
		limit $2`, versionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]models.LeaderboardEntry, 0, limit)
	rank := 0
	for rows.Next() {
		var (
			customerID  *uuid.UUID
			displayName *string
			e           models.LeaderboardEntry
		)
		if err := rows.Scan(&customerID, &displayName, &e.Score, &e.HighestTile, &e.AchievedAt); err != nil {
			return nil, err
		}
		rank++
		e.Rank = rank
		// Guests and customers without a name show as "Player"; identifiers are
		// never exposed on a public board.
		e.DisplayName = "Player"
		if customerID != nil && displayName != nil && *displayName != "" {
			e.DisplayName = *displayName
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
