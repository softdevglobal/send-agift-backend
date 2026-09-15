package services

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"myapp/internal/games"
	"myapp/internal/models"
	"myapp/internal/repository"
)

// ErrScoreNotReviewable means an admin tried to change a score that cannot
// change, such as one whose move log failed to replay.
var ErrScoreNotReviewable = errors.New("this score can no longer be reviewed")

const adminBoardLimit = 200

// AdminGames returns every game in the catalog with its activity and best
// scorer, for the superadmin console.
func (s *GameService) AdminGames(ctx context.Context) ([]models.AdminGameSummary, error) {
	list, err := s.games.AdminGameSummaries(ctx)
	if err != nil {
		return nil, err
	}
	for i := range list {
		_, list[i].Playable = games.EngineFor(list[i].Slug)
	}
	return list, nil
}

func (s *GameService) adminGame(ctx context.Context, slug string) (*models.AdminGameSummary, error) {
	list, err := s.AdminGames(ctx)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Slug == slug {
			return &list[i], nil
		}
	}
	return nil, ErrGameNotFound
}

// AdminGameLeaderboard is a game's full practice board: every player's best
// verified score, ranked by score then time, with real names.
func (s *GameService) AdminGameLeaderboard(ctx context.Context, slug string, limit int) (*models.AdminGameLeaderboard, error) {
	game, err := s.adminGame(ctx, slug)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > adminBoardLimit {
		limit = adminBoardLimit
	}
	entries, err := s.games.AdminLeaderboard(ctx, slug, limit)
	if err != nil {
		return nil, err
	}
	return &models.AdminGameLeaderboard{Game: *game, Entries: entries}, nil
}

// AdminGameScores lists a game's recent scores; status narrows it to
// accepted, rejected or manual_review.
func (s *GameService) AdminGameScores(ctx context.Context, slug, status string, limit int) ([]models.AdminGameScore, error) {
	switch status {
	case "", "accepted", "rejected", "manual_review":
	default:
		return nil, fmt.Errorf("%w: status must be accepted, rejected or manual_review", ErrInvalidReview)
	}
	if _, err := s.adminGame(ctx, slug); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > adminBoardLimit {
		limit = 50
	}
	return s.games.AdminScores(ctx, slug, status, limit)
}

// ReviewGameScore accepts or rejects a practice score held for review, or
// rejects one found to be cheated. Every verdict is audited.
func (s *GameService) ReviewGameScore(ctx context.Context, admin AdminActor, sessionID uuid.UUID, in ReviewInput) error {
	if in.Status != "accepted" && in.Status != "rejected" {
		return fmt.Errorf("%w: status must be accepted or rejected", ErrInvalidReview)
	}
	if in.Status == "rejected" && (in.Reason == nil || strings.TrimSpace(*in.Reason) == "") {
		return fmt.Errorf("%w: a reason is required to reject a score", ErrInvalidReview)
	}
	audit := admin.audit("game.score_reviewed", sessionID, in.Reason)
	audit.EntityType = "game_score"

	err := s.games.ReviewScore(ctx, sessionID, in.Status, in.Reason, audit)
	switch {
	case errors.Is(err, repository.ErrGameScoreNotFound):
		return ErrGameSessionNotFound
	case errors.Is(err, repository.ErrScoreNotReviewable):
		return ErrScoreNotReviewable
	}
	return err
}
