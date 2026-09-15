package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
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
// server enforces and the client renders. Its shape is game-specific, so it is
// kept raw here and decoded by that game's engine.
type GameVersion struct {
	ID         uuid.UUID       `json:"id"`
	GameID     uuid.UUID       `json:"game_id"`
	Version    string          `json:"version"`
	Config     json.RawMessage `json:"config"`
	Status     string          `json:"status"`
	ApprovedAt *time.Time      `json:"approved_at,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
}

// GameSession maps to competition.game_sessions — one play.
// CustomerID / GuestToken are never exposed in JSON; they exist only to prove
// that the account submitting a score is the one that started the session.
type GameSession struct {
	ID            uuid.UUID       `json:"id"`
	GameVersionID uuid.UUID       `json:"game_version_id"`
	GameSlug      string          `json:"-"` // picks the replay engine
	CustomerID    *uuid.UUID      `json:"-"`
	GuestToken    *string         `json:"-"`
	Mode          string          `json:"mode"`
	CompetitionID *uuid.UUID      `json:"-"`
	ServerSeed    string          `json:"server_seed"`
	Config        json.RawMessage `json:"config"`
	Status        string          `json:"status"`
	StartedAt     time.Time       `json:"started_at"`
	ExpiresAt     time.Time       `json:"expires_at"`
	SubmittedAt   *time.Time      `json:"submitted_at,omitempty"`
}

// GameScore maps to competition.game_scores — the server-computed result.
type GameScore struct {
	SessionID        uuid.UUID        `json:"session_id"`
	GameVersionID    uuid.UUID        `json:"game_version_id"`
	CustomerID       *uuid.UUID       `json:"-"`
	GuestToken       *string          `json:"-"`
	Score            int64            `json:"score"`
	ClientScore      *int64           `json:"-"` // cheat signal only; never shown
	HighestTile      int              `json:"highest_tile"`
	MovesCount       int              `json:"moves_count"`
	DurationMs       int64            `json:"duration_ms"`
	MoveLogHash      string           `json:"-"`
	ValidationStatus string           `json:"validation_status"`
	ReviewReason     *string          `json:"-"` // internal; not leaked to cheaters
	Stats            map[string]int64 `json:"stats"`
	CreatedAt        time.Time        `json:"created_at"`
}

// GameView is the public catalog payload.
type GameView struct {
	Slug        string          `json:"slug"`
	Name        string          `json:"name"`
	Description *string         `json:"description,omitempty"`
	GameType    string          `json:"game_type"`
	Version     string          `json:"version"`
	Config      json.RawMessage `json:"config"`
}

// GameSessionView is handed to the client when a play starts. The seed is what
// lets the app generate the very same board this backend will replay.
type GameSessionView struct {
	SessionID uuid.UUID       `json:"session_id"`
	GameSlug  string          `json:"game_slug"`
	Version   string          `json:"version"`
	Mode      string          `json:"mode"`
	Seed      string          `json:"seed"`
	Config    json.RawMessage `json:"config"`
	StartedAt time.Time       `json:"started_at"`
	ExpiresAt time.Time       `json:"expires_at"`
}

// GameScoreView is the result of a submission.
// Score is the server's number, never the client's.
type GameScoreView struct {
	SessionID      uuid.UUID        `json:"session_id"`
	Score          int64            `json:"score"`
	HighestTile    int              `json:"highest_tile"`
	MovesCount     int              `json:"moves_count"`
	Won            bool             `json:"won"`
	GameOver       bool             `json:"game_over"`
	PersonalBest   int64            `json:"personal_best"`
	IsPersonalBest bool             `json:"is_personal_best"`
	Stats          map[string]int64 `json:"stats"`
	// Accepted is false when the score was held for review, which happens when
	// the submission looks tampered with.
	Accepted bool `json:"accepted"`
	// Set for official competition attempts only.
	CompetitionID     *uuid.UUID `json:"competition_id,omitempty"`
	Rank              *int       `json:"rank,omitempty"`
	AttemptsRemaining *int       `json:"attempts_remaining,omitempty"`
}

// Leaderboard entry statuses (§16). Until a competition is finalised every
// score is provisional; practice scores are verified as soon as they replay.
const (
	EntryProvisional = "provisional"
	EntryVerified    = "verified"
	EntryUnderReview = "under_review"
)

// LeaderboardEntry is one public row (§16). No customer ids or guest tokens,
// and names are shortened to first name plus surname initial.
type LeaderboardEntry struct {
	Rank        int       `json:"rank"`
	DisplayName string    `json:"display_name"`
	CountryCode string    `json:"country_code,omitempty"`
	CountryName string    `json:"country_name,omitempty"`
	Score       int64     `json:"score"`
	DurationMs  int64     `json:"duration_ms"`
	AchievedAt  time.Time `json:"achieved_at"`
	Status      string    `json:"status"`
	IsMe        bool      `json:"is_me"`
}

// LeaderboardView is a practice board plus the caller's own row, when known —
// even if they rank outside the top of the board.
type LeaderboardView struct {
	GameSlug     string             `json:"game_slug"`
	Version      string             `json:"version"`
	Entries      []LeaderboardEntry `json:"entries"`
	Me           *LeaderboardEntry  `json:"me,omitempty"`
	MyBest       *int64             `json:"my_best,omitempty"`
	TotalPlayers int                `json:"total_players"`
}
