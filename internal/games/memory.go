package games

import (
	"encoding/json"
	"fmt"
)

// MemorySlug identifies Memory Match in the catalog and the engine registry.
const MemorySlug = "memory-match"

// MemoryConfig is the Memory Match rule set, versioned like every game.
type MemoryConfig struct {
	Pairs             int `json:"pairs"`
	Columns           int `json:"columns"`
	PointsPerMatch    int `json:"points_per_match"`
	StreakBonus       int `json:"streak_bonus"`
	TurnPenalty       int `json:"turn_penalty"`
	MaxTurns          int `json:"max_turns"`
	MinMsPerFlip      int `json:"min_ms_per_flip"`
	SessionTTLSeconds int `json:"session_ttl_seconds"`
}

// DefaultMemoryConfig mirrors the 1.0.0 version seeded by migration 000032.
func DefaultMemoryConfig() MemoryConfig {
	return MemoryConfig{
		Pairs:             8,
		Columns:           4,
		PointsPerMatch:    20,
		StreakBonus:       10,
		TurnPenalty:       1,
		MaxTurns:          80,
		MinMsPerFlip:      160,
		SessionTTLSeconds: defaultSessionTTLSeconds,
	}
}

func (c MemoryConfig) withDefaults() MemoryConfig {
	d := DefaultMemoryConfig()
	fill := func(v *int, def int) {
		if *v <= 0 {
			*v = def
		}
	}
	fill(&c.Pairs, d.Pairs)
	fill(&c.Columns, d.Columns)
	fill(&c.PointsPerMatch, d.PointsPerMatch)
	fill(&c.StreakBonus, d.StreakBonus)
	fill(&c.MaxTurns, d.MaxTurns)
	fill(&c.MinMsPerFlip, d.MinMsPerFlip)
	fill(&c.SessionTTLSeconds, d.SessionTTLSeconds)
	if c.TurnPenalty < 0 {
		c.TurnPenalty = d.TurnPenalty
	}
	return c
}

// MemoryGame is Memory Match: a grid of face-down gift cards holding pairs.
// Two flips make a turn; a matching pair stays up and scores, and a run of
// consecutive matches pays a growing bonus. Every turn costs a little, so the
// score rewards remembering rather than brute-forcing the grid.
//
// The layout is dealt from the seed, so the server lays out exactly the grid
// the player saw. The Dart implementation in the mobile app mirrors this file.
type MemoryGame struct {
	cfg     MemoryConfig
	cards   []int  // card index → face value
	matched []bool // card index → already paired off
	pending int    // first card of the turn in progress, -1 when none

	turns      int
	matches    int
	streak     int
	bestStreak int
	score      int64
	flips      int
}

// NewMemoryGame deals the grid from the seed.
func NewMemoryGame(seed string, cfg MemoryConfig) (*MemoryGame, error) {
	state, err := ParseSeed(seed)
	if err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()

	count := cfg.Pairs * 2
	cards := make([]int, count)
	for i := range cards {
		cards[i] = i / 2 // two of every face value
	}
	// Fisher-Yates from the seed. Walking downwards keeps the draw order the
	// same as the Dart mirror, which matters more than the shuffle's quality.
	rng := NewRNG(state)
	for i := count - 1; i > 0; i-- {
		j := rng.NextInt(i + 1)
		cards[i], cards[j] = cards[j], cards[i]
	}

	return &MemoryGame{
		cfg:     cfg,
		cards:   cards,
		matched: make([]bool, count),
		pending: -1,
	}, nil
}

func (g *MemoryGame) Score() int64   { return g.score }
func (g *MemoryGame) Matches() int   { return g.matches }
func (g *MemoryGame) Turns() int     { return g.turns }
func (g *MemoryGame) Cards() []int   { return g.cards }
func (g *MemoryGame) Rows() int      { return len(g.cards) / g.cfg.Columns }
func (g *MemoryGame) Complete() bool { return g.matches == g.cfg.Pairs }

// Face is the value on a card, for the client to render.
func (g *MemoryGame) Face(index int) int { return g.cards[index] }

// Matched reports whether a card has already been paired off.
func (g *MemoryGame) Matched(index int) bool { return g.matched[index] }

// Pending is the card waiting for its partner, or -1 between turns.
func (g *MemoryGame) Pending() int { return g.pending }

// Flip turns one card face up. The second flip of a turn resolves it.
func (g *MemoryGame) Flip(index int) (bool, error) {
	switch {
	case g.Complete():
		return false, fmt.Errorf("%w: flip after every pair was found", ErrInvalidMove)
	case index < 0 || index >= len(g.cards):
		return false, fmt.Errorf("%w: card %d is off the grid", ErrInvalidMove, index)
	case g.matched[index]:
		return false, fmt.Errorf("%w: card %d is already matched", ErrInvalidMove, index)
	case index == g.pending:
		return false, fmt.Errorf("%w: card %d is already face up", ErrInvalidMove, index)
	case g.turns >= g.cfg.MaxTurns:
		return false, fmt.Errorf("%w: the %d turn limit is spent", ErrInvalidMove, g.cfg.MaxTurns)
	}

	g.flips++
	if g.pending < 0 {
		g.pending = index
		return false, nil
	}

	first := g.pending
	g.pending = -1
	g.turns++

	if g.cards[first] != g.cards[index] {
		g.streak = 0
		// A wrong turn costs, but never digs the score into a negative.
		if g.score >= int64(g.cfg.TurnPenalty) {
			g.score -= int64(g.cfg.TurnPenalty)
		} else {
			g.score = 0
		}
		return false, nil
	}

	g.matched[first] = true
	g.matched[index] = true
	g.matches++
	g.streak++
	if g.streak > g.bestStreak {
		g.bestStreak = g.streak
	}
	g.score += int64(g.cfg.PointsPerMatch + g.cfg.StreakBonus*(g.streak-1))
	return true, nil
}

// ReplayMemory replays a Memory Match log: the index of every card flipped.
func ReplayMemory(seed string, cfg MemoryConfig, moves []string) (*Result, error) {
	cfg = cfg.withDefaults()
	g, err := NewMemoryGame(seed, cfg)
	if err != nil {
		return nil, err
	}
	for i, m := range moves {
		fields, err := parseTickEntry(m, 1)
		if err != nil {
			return nil, fmt.Errorf("flip %d: %w", i, err)
		}
		if _, err := g.Flip(fields[0]); err != nil {
			return nil, fmt.Errorf("flip %d: %w", i, err)
		}
	}
	return &Result{
		Score:         g.score,
		MovesUsed:     len(moves),
		Won:           g.Complete(),
		GameOver:      g.Complete() || g.turns >= cfg.MaxTurns,
		MinDurationMs: int64(g.flips) * int64(cfg.MinMsPerFlip),
		Stats: map[string]int64{
			"matches":     int64(g.matches),
			"turns":       int64(g.turns),
			"best_streak": int64(g.bestStreak),
		},
	}, nil
}

type memoryEngine struct{}

func (memoryEngine) Replay(seed string, raw json.RawMessage, moves []string) (*Result, error) {
	var cfg MemoryConfig
	if err := decodeConfig(raw, &cfg); err != nil {
		return nil, err
	}
	return ReplayMemory(seed, cfg, moves)
}
