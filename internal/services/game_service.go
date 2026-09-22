package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"myapp/internal/games"
	"myapp/internal/models"
	"myapp/internal/repository"
)

var (
	ErrGameNotFound         = errors.New("game not found")
	ErrGameSessionNotFound  = errors.New("game session not found")
	ErrGameSessionExpired   = errors.New("game session expired")
	ErrGameSessionForbidden = errors.New("game session belongs to another player")
	ErrInvalidMoveLog       = errors.New("invalid move log")
)

const (
	defaultLeaderboardLimit = 20
	maxLeaderboardLimit     = 100
)

// GameService owns skill-game play: handing out seeded sessions and turning a
// submitted move log into an authoritative score.
//
// The rule that shapes this whole file: the client is never trusted with a
// score. It reports what it did (the moves), and the server works out what
// that was worth by replaying the game itself with that game's engine.
type GameService struct {
	games        *repository.GameRepository
	competitions *CompetitionService
}

func NewGameService(gameRepo *repository.GameRepository) *GameService {
	return &GameService{games: gameRepo}
}

// UseCompetitions routes official competition sessions to the competition
// service when they are submitted. Without it, official sessions cannot be
// scored.
func (s *GameService) UseCompetitions(c *CompetitionService) {
	s.competitions = c
}

// ListGames returns every playable game for the app's game collection.
// A catalog row with no replay engine behind it cannot be scored, so it is
// hidden rather than offered and then failing on submit.
func (s *GameService) ListGames(ctx context.Context) ([]models.GameView, error) {
	all, err := s.games.ListPlayable(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]models.GameView, 0, len(all))
	for _, g := range all {
		if _, ok := games.EngineFor(g.Slug); ok {
			out = append(out, g)
		}
	}
	return out, nil
}

// GetGame returns one playable game and the rules its current version uses.
func (s *GameService) GetGame(ctx context.Context, slug string) (*models.GameView, error) {
	pg, err := s.playable(ctx, slug)
	if err != nil {
		return nil, err
	}
	return &models.GameView{
		Slug:        pg.Game.Slug,
		Name:        pg.Game.Name,
		Description: pg.Game.Description,
		GameType:    pg.Game.GameType,
		Version:     pg.Version.Version,
		Config:      pg.Version.Config,
	}, nil
}

// playable loads a game that exists in the catalog AND has an engine.
func (s *GameService) playable(ctx context.Context, slug string) (*repository.PlayableGame, error) {
	if _, ok := games.EngineFor(slug); !ok {
		return nil, ErrGameNotFound
	}
	pg, err := s.games.GetPlayableBySlug(ctx, slug)
	if err != nil {
		return nil, mapGameRepoErr(err)
	}
	return pg, nil
}

// maxSessionLevel bounds how far a player can scale a session up, so a
// crafted level value can't deal an absurd board.
const maxSessionLevel = 8

// StartSession opens a play and hands the client its seed.
//
// The seed is generated here, server-side, so a player cannot keep restarting
// until they are dealt an easy board, and so this backend can reproduce the
// exact same game when the score comes back.
//
// level lets a game with level progression (currently Memory Match) ask for
// a harder board than its base config. The scaled config is computed once,
// here, and stored on the session itself — replay just reads it back, so a
// level is never something the server has to remember separately.
func (s *GameService) StartSession(ctx context.Context, slug string, actor SocialActor, level int) (*models.GameSessionView, error) {
	identity, err := actor.toIdentity()
	if err != nil {
		return nil, err
	}

	pg, err := s.playable(ctx, slug)
	if err != nil {
		return nil, err
	}

	if level < 1 {
		level = 1
	}
	if level > maxSessionLevel {
		level = maxSessionLevel
	}
	config, err := scaleConfigForLevel(slug, pg.Version.Config, level)
	if err != nil {
		return nil, err
	}

	seed, err := games.NewSeed()
	if err != nil {
		return nil, err
	}

	ttl := time.Duration(games.SessionTTLSeconds(config)) * time.Second
	session, err := s.games.CreateSession(ctx, repository.CreateSessionInput{
		GameVersionID: pg.Version.ID,
		Identity:      identity,
		Mode:          "practice", // official mode arrives with competitions
		ServerSeed:    seed,
		Config:        config,
		ExpiresAt:     time.Now().Add(ttl),
	})
	if err != nil {
		return nil, err
	}

	return &models.GameSessionView{
		SessionID: session.ID,
		GameSlug:  pg.Game.Slug,
		Version:   pg.Version.Version,
		Mode:      session.Mode,
		Seed:      session.ServerSeed,
		Config:    session.Config,
		StartedAt: session.StartedAt,
		ExpiresAt: session.ExpiresAt,
	}, nil
}

// SubmitScoreInput is what the app sends when a game ends.
// Note there is no trusted score field: ClientScore is only kept as a signal
// that the app and the server disagree, which means tampering or a version
// mismatch.
type SubmitScoreInput struct {
	Moves       []string
	ClientScore *int64
}

// SubmitScore replays a finished game and records the result.
func (s *GameService) SubmitScore(ctx context.Context, sessionID string, actor SocialActor, in SubmitScoreInput) (*models.GameScoreView, error) {
	identity, err := actor.toIdentity()
	if err != nil {
		return nil, err
	}

	id, err := uuid.Parse(sessionID)
	if err != nil {
		return nil, ErrGameSessionNotFound
	}
	session, err := s.games.GetSession(ctx, id)
	if err != nil {
		return nil, mapGameRepoErr(err)
	}

	// The submitter must be the player who started the session.
	if !sessionBelongsTo(session, identity) {
		return nil, ErrGameSessionForbidden
	}

	// Official attempts are scored into their competition, not the practice board.
	if session.Mode == "official" {
		if s.competitions == nil {
			return nil, ErrGameSessionNotFound
		}
		return s.competitions.SubmitOfficial(ctx, session, in)
	}

	// A session already scored returns its original result, so a retried or
	// duplicated request is harmless.
	if session.Status == "submitted" {
		existing, err := s.games.GetScoreBySession(ctx, session.ID)
		if err != nil {
			return nil, mapGameRepoErr(err)
		}
		return s.scoreView(ctx, session, existing, false)
	}

	if session.Status != "active" {
		return nil, ErrGameSessionExpired
	}

	engine, ok := games.EngineFor(session.GameSlug)
	if !ok {
		return nil, ErrGameNotFound
	}

	// The clock is the server's, never the client's.
	now := time.Now()
	if now.After(session.ExpiresAt) {
		if err := s.games.ExpireSession(ctx, session.ID); err != nil {
			return nil, err
		}
		return nil, ErrGameSessionExpired
	}
	durationMs := now.Sub(session.StartedAt).Milliseconds()
	moveLogHash := hashMoves(in.Moves)

	// Replay the game from the seed this server issued. If the move log cannot
	// produce a legal game, the submission is rejected — but we still record it
	// so the attempt is preserved as evidence.
	result, replayErr := engine.Replay(session.ServerSeed, session.Config, in.Moves)
	if replayErr != nil {
		reason := replayErr.Error()
		_, saveErr := s.games.SaveScore(ctx, repository.SaveScoreInput{
			SessionID:        session.ID,
			GameVersionID:    session.GameVersionID,
			Identity:         identity,
			Score:            0,
			ClientScore:      in.ClientScore,
			MovesCount:       len(in.Moves),
			DurationMs:       durationMs,
			MoveLogHash:      moveLogHash,
			ValidationStatus: "rejected",
			ReviewReason:     &reason,
		})
		if saveErr != nil && !errors.Is(saveErr, repository.ErrGameSessionNotFound) {
			return nil, saveErr
		}
		return nil, fmt.Errorf("%w: %s", ErrInvalidMoveLog, replayErr.Error())
	}

	// Anti-cheat signals. These hold a score for review rather than deleting
	// it, so a false positive never silently loses a real player's game.
	status := "accepted"
	var reviewReason *string

	if durationMs < result.MinDurationMs {
		reason := fmt.Sprintf("played %d moves in %dms, faster than the %dms floor",
			result.MovesUsed, durationMs, result.MinDurationMs)
		status, reviewReason = "manual_review", &reason
	} else if in.ClientScore != nil && *in.ClientScore != result.Score {
		reason := fmt.Sprintf("client reported %d, server replayed %d", *in.ClientScore, result.Score)
		status, reviewReason = "manual_review", &reason
	}

	saved, err := s.games.SaveScore(ctx, repository.SaveScoreInput{
		SessionID:        session.ID,
		GameVersionID:    session.GameVersionID,
		Identity:         identity,
		Score:            result.Score,
		ClientScore:      in.ClientScore,
		HighestTile:      int(result.Stats["highest_tile"]),
		MovesCount:       result.MovesUsed,
		DurationMs:       durationMs,
		MoveLogHash:      moveLogHash,
		ValidationStatus: status,
		ReviewReason:     reviewReason,
		Stats:            result.Stats,
	})
	// Lost the race against a concurrent submit: return the stored result.
	if errors.Is(err, repository.ErrGameSessionNotFound) {
		existing, getErr := s.games.GetScoreBySession(ctx, session.ID)
		if getErr != nil {
			return nil, mapGameRepoErr(getErr)
		}
		return s.scoreView(ctx, session, existing, false)
	}
	if err != nil {
		return nil, err
	}

	view, err := s.scoreView(ctx, session, saved, true)
	if err != nil {
		return nil, err
	}
	view.Won = result.Won
	view.GameOver = result.GameOver
	return view, nil
}

// Leaderboard returns the public practice board for a game: rank, shortened
// name, country, score and time. An identified caller also gets their own
// row, even when they rank outside the top, and their best score.
func (s *GameService) Leaderboard(ctx context.Context, slug string, actor SocialActor, limit int) (*models.LeaderboardView, error) {
	pg, err := s.playable(ctx, slug)
	if err != nil {
		return nil, err
	}
	limit = clampLimit(limit)

	// Anonymous callers simply get the board with no personal row.
	var caller *repository.SocialIdentity
	if identity, err := actor.toIdentity(); err == nil {
		caller = &identity
	}

	top, mine, total, err := s.games.Leaderboard(ctx, pg.Version.ID, limit, caller)
	if err != nil {
		return nil, err
	}

	out := &models.LeaderboardView{
		GameSlug:     pg.Game.Slug,
		Version:      pg.Version.Version,
		Entries:      make([]models.LeaderboardEntry, 0, len(top)),
		TotalPlayers: total,
	}
	// Practice scores are verified the moment the server replays them.
	for _, row := range top {
		isMe := mine != nil && row.Player == mine.Player
		out.Entries = append(out.Entries, publicEntry(row, models.EntryVerified, isMe))
	}
	if mine != nil {
		entry := publicEntry(*mine, models.EntryVerified, true)
		out.Me = &entry
	}

	if caller != nil {
		best, err := s.games.PersonalBest(ctx, pg.Version.ID, *caller)
		if err != nil {
			return nil, err
		}
		out.MyBest = &best
	}
	return out, nil
}

// scoreView builds the client payload and works out whether this run beat the
// player's previous best.
func (s *GameService) scoreView(ctx context.Context, session *models.GameSession, score *models.GameScore, fresh bool) (*models.GameScoreView, error) {
	identity := repository.SocialIdentity{CustomerID: score.CustomerID, GuestToken: score.GuestToken}
	best, err := s.games.PersonalBest(ctx, session.GameVersionID, identity)
	if err != nil {
		return nil, err
	}

	stats := score.Stats
	if stats == nil {
		stats = map[string]int64{}
	}
	accepted := score.ValidationStatus == "accepted"
	return &models.GameScoreView{
		SessionID:   score.SessionID,
		Score:       score.Score,
		HighestTile: score.HighestTile,
		MovesCount:  score.MovesCount,
		Stats:       stats,
		// A run ties the record when it IS the record, so only a fresh
		// submission that matches the best counts as setting it.
		PersonalBest:   best,
		IsPersonalBest: fresh && accepted && score.Score >= best && score.Score > 0,
		Accepted:       accepted,
	}, nil
}

// sessionBelongsTo checks the submitter is the player who started the session.
func sessionBelongsTo(session *models.GameSession, identity repository.SocialIdentity) bool {
	if session.CustomerID != nil && identity.CustomerID != nil {
		return *session.CustomerID == *identity.CustomerID
	}
	if session.GuestToken != nil && identity.GuestToken != nil {
		return *session.GuestToken == *identity.GuestToken
	}
	return false
}

// hashMoves fingerprints the exact move log so the replayed input can be proven
// later without storing every move forever.
func hashMoves(moves []string) string {
	sum := sha256.Sum256([]byte(strings.Join(moves, ",")))
	return hex.EncodeToString(sum[:])
}

// scaleConfigForLevel returns the config a session should actually run with.
// Only games that define level progression change; everything else plays
// its base config regardless of what level was asked for.
func scaleConfigForLevel(slug string, base json.RawMessage, level int) (json.RawMessage, error) {
	if level > maxSessionLevel {
		level = maxSessionLevel
	}
	if slug != games.MemorySlug || level <= 1 {
		return base, nil
	}

	cfg := games.DefaultMemoryConfig()
	if err := json.Unmarshal(base, &cfg); err != nil {
		return nil, fmt.Errorf("parse memory config: %w", err)
	}

	// Half a column's worth of extra pairs a level, so the card count stays a
	// multiple of the column count and the grid never leaves a ragged row —
	// whatever the column count actually is.
	cfg.Pairs += (cfg.Columns / 2) * (level - 1)
	// The board gets bigger *and* stingier with turns: the ratio of turns to
	// pairs shrinks a little each level, down to a floor.
	ratio := 10 - (level - 1)
	if ratio < 5 {
		ratio = 5
	}
	cfg.MaxTurns = cfg.Pairs * ratio
	cfg.PointsPerMatch += 2 * (level - 1)
	cfg.StreakBonus += (level - 1)

	scaled, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal scaled memory config: %w", err)
	}
	return scaled, nil
}

// mapGameRepoErr converts storage errors into service-level ones.
func mapGameRepoErr(err error) error {
	switch {
	case errors.Is(err, repository.ErrGameNotFound), errors.Is(err, repository.ErrGameVersionNotFound):
		return ErrGameNotFound
	case errors.Is(err, repository.ErrGameSessionNotFound):
		return ErrGameSessionNotFound
	case errors.Is(err, repository.ErrGameScoreNotFound):
		return ErrGameSessionNotFound
	default:
		return err
	}
}
