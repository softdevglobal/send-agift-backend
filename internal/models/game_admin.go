package models

import (
	"time"

	"github.com/google/uuid"
)

// AdminPlayer identifies a player to a superadmin. Unlike the public boards
// it carries the full name and email — admins need to know who is winning.
type AdminPlayer struct {
	Kind        string     `json:"kind"` // customer or guest
	CustomerID  *uuid.UUID `json:"customer_id,omitempty"`
	Name        string     `json:"name"`
	Email       *string    `json:"email,omitempty"`
	CountryName *string    `json:"country_name,omitempty"`
}

// AdminGameSummary is one game in the superadmin games overview.
type AdminGameSummary struct {
	Slug         string       `json:"slug"`
	Name         string       `json:"name"`
	GameType     string       `json:"game_type"`
	Status       string       `json:"status"`
	Version      string       `json:"version"`
	Playable     bool         `json:"playable"`
	Plays        int          `json:"plays"`
	Scores       int          `json:"scores"`
	Players      int          `json:"players"`
	UnderReview  int          `json:"under_review"`
	Rejected     int          `json:"rejected"`
	Competitions int          `json:"competitions"`
	TopScore     *int64       `json:"top_score,omitempty"`
	TopPlayer    *AdminPlayer `json:"top_player,omitempty"`
	LastPlayedAt *time.Time   `json:"last_played_at,omitempty"`
}

// AdminLeaderboardRow is one player on a game's full practice board.
type AdminLeaderboardRow struct {
	Rank         int         `json:"rank"`
	Player       AdminPlayer `json:"player"`
	BestScore    int64       `json:"best_score"`
	DurationMs   int64       `json:"duration_ms"`
	Plays        int         `json:"plays"`
	AchievedAt   time.Time   `json:"achieved_at"`
	LastPlayedAt time.Time   `json:"last_played_at"`
}

// AdminGameLeaderboard is a game's summary plus its full board.
type AdminGameLeaderboard struct {
	Game    AdminGameSummary      `json:"game"`
	Entries []AdminLeaderboardRow `json:"entries"`
}

// AdminGameScore is one recorded practice score, as a superadmin sees it —
// including the anti-cheat verdict and why.
type AdminGameScore struct {
	SessionID        uuid.UUID        `json:"session_id"`
	Player           AdminPlayer      `json:"player"`
	Score            int64            `json:"score"`
	ClientScore      *int64           `json:"client_score,omitempty"`
	MovesCount       int              `json:"moves_count"`
	DurationMs       int64            `json:"duration_ms"`
	ValidationStatus string           `json:"validation_status"`
	ReviewReason     *string          `json:"review_reason,omitempty"`
	Stats            map[string]int64 `json:"stats"`
	CreatedAt        time.Time        `json:"created_at"`
}
