package models

import (
	"time"

	"github.com/google/uuid"

	"myapp/internal/games"
)

// Game maps to competition.games — the catalog entry for one skill game.
type Game struct {
	ID          uuid.UUID `json:"id"`
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	Description *string   `json:"description,omitempty"`
	GameType    string    `json:"game_type"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// GameVersion maps to competition.game_versions. Config carries the rules the
// server enforces and the client renders, so both run identical mechanics.
type GameVersion struct {
	ID         uuid.UUID    `json:"id"`
	GameID     uuid.UUID    `json:"game_id"`
	Version    string       `json:"version"`
	Config     games.Config `json:"config"`
	Status     string       `json:"status"`
	ApprovedAt *time.Time   `json:"approved_at,omitempty"`
	CreatedAt  time.Time    `json:"created_at"`
}

// GameSession maps to competition.game_sessions — one play.
// CustomerID / GuestToken are never exposed in JSON; they exist only to prove
// that the account submitting a score is the one that started the session.
type GameSession struct {
	ID            uuid.UUID    `json:"id"`
	GameVersionID uuid.UUID    `json:"game_version_id"`
	CustomerID    *uuid.UUID   `json:"-"`
	GuestToken    *string      `json:"-"`
	Mode          string       `json:"mode"`
	CompetitionID *uuid.UUID   `json:"-"`
	ServerSeed    string       `json:"server_seed"`
	Config        games.Config `json:"config"`
	Status        string       `json:"status"`
	StartedAt     time.Time    `json:"started_at"`
	ExpiresAt     time.Time    `json:"expires_at"`
	SubmittedAt   *time.Time   `json:"submitted_at,omitempty"`
}

// GameScore maps to competition.game_scores — the server-computed result.
type GameScore struct {
	SessionID        uuid.UUID  `json:"session_id"`
	GameVersionID    uuid.UUID  `json:"game_version_id"`
	CustomerID       *uuid.UUID `json:"-"`
	GuestToken       *string    `json:"-"`
	Score            int64      `json:"score"`
	ClientScore      *int64     `json:"-"` // cheat signal only; never shown
	HighestTile      int        `json:"highest_tile"`
	MovesCount       int        `json:"moves_count"`
	DurationMs       int64      `json:"duration_ms"`
	MoveLogHash      string     `json:"-"`
	ValidationStatus string     `json:"validation_status"`
	ReviewReason     *string    `json:"-"` // internal; not leaked to cheaters
	CreatedAt        time.Time  `json:"created_at"`
}

// GameView is the public catalog payload.
type GameView struct {
	Slug        string       `json:"slug"`
	Name        string       `json:"name"`
	Description *string      `json:"description,omitempty"`
	GameType    string       `json:"game_type"`
	Version     string       `json:"version"`
	Config      games.Config `json:"config"`
}

// GameSessionView is handed to the client when a play starts. The seed is what
// lets the app generate the very same tiles this backend will replay.
type GameSessionView struct {
	SessionID uuid.UUID    `json:"session_id"`
	GameSlug  string       `json:"game_slug"`
	Version   string       `json:"version"`
	Mode      string       `json:"mode"`
	Seed      string       `json:"seed"`
	Config    games.Config `json:"config"`
	StartedAt time.Time    `json:"started_at"`
	ExpiresAt time.Time    `json:"expires_at"`
}

// GameScoreView is the result of a submission.
// Score is the server's number, never the client's.
type GameScoreView struct {
	SessionID      uuid.UUID `json:"session_id"`
	Score          int64     `json:"score"`
	HighestTile    int       `json:"highest_tile"`
	MovesCount     int       `json:"moves_count"`
	Won            bool      `json:"won"`
	GameOver       bool      `json:"game_over"`
	PersonalBest   int64     `json:"personal_best"`
	IsPersonalBest bool      `json:"is_personal_best"`
	// Accepted is false when the score was held for review, which happens when
	// the submission looks tampered with.
	Accepted bool `json:"accepted"`
}

// LeaderboardEntry is one public row. No customer ids or guest tokens.
type LeaderboardEntry struct {
	Rank        int       `json:"rank"`
	DisplayName string    `json:"display_name"`
	Score       int64     `json:"score"`
	HighestTile int       `json:"highest_tile"`
	AchievedAt  time.Time `json:"achieved_at"`
}

// LeaderboardView is the board plus the caller's own best, when known.
type LeaderboardView struct {
	GameSlug string             `json:"game_slug"`
	Version  string             `json:"version"`
	Entries  []LeaderboardEntry `json:"entries"`
	MyBest   *int64             `json:"my_best,omitempty"`
}
