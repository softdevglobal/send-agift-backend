package games

import (
	"encoding/json"
	"fmt"
)

// WhackSlug identifies Whack-a-Mole in the catalog and the engine registry.
const WhackSlug = "whack-a-mole"

// WhackConfig is the Whack-a-Mole rule set, versioned like every game.
type WhackConfig struct {
	TickMs            int `json:"tick_ms"`
	Holes             int `json:"holes"`
	Moles             int `json:"moles"`
	StartUpTicks      int `json:"start_up_ticks"`
	MinUpTicks        int `json:"min_up_ticks"`
	UpStepTicks       int `json:"up_step_ticks"`
	GapTicks          int `json:"gap_ticks"`
	PointsPerHit      int `json:"points_per_hit"`
	StreakBonus       int `json:"streak_bonus"`
	MissPenalty       int `json:"miss_penalty"`
	SessionTTLSeconds int `json:"session_ttl_seconds"`
}

// DefaultWhackConfig mirrors the 1.0.0 version seeded by migration 000032.
func DefaultWhackConfig() WhackConfig {
	return WhackConfig{
		TickMs:            20,
		Holes:             9,
		Moles:             40,
		StartUpTicks:      46,
		MinUpTicks:        16,
		UpStepTicks:       1,
		GapTicks:          10,
		PointsPerHit:      10,
		StreakBonus:       4,
		MissPenalty:       3,
		SessionTTLSeconds: defaultSessionTTLSeconds,
	}
}

func (c WhackConfig) withDefaults() WhackConfig {
	d := DefaultWhackConfig()
	fill := func(v *int, def int) {
		if *v <= 0 {
			*v = def
		}
	}
	fill(&c.TickMs, d.TickMs)
	fill(&c.Holes, d.Holes)
	fill(&c.Moles, d.Moles)
	fill(&c.StartUpTicks, d.StartUpTicks)
	fill(&c.MinUpTicks, d.MinUpTicks)
	fill(&c.UpStepTicks, d.UpStepTicks)
	fill(&c.GapTicks, d.GapTicks)
	fill(&c.PointsPerHit, d.PointsPerHit)
	fill(&c.StreakBonus, d.StreakBonus)
	if c.MissPenalty < 0 {
		c.MissPenalty = d.MissPenalty
	}
	fill(&c.SessionTTLSeconds, d.SessionTTLSeconds)
	return c
}

// WhackMole is one mole's appearance: which hole, and the tick window it is up.
type WhackMole struct {
	Hole int
	Up   int
	Down int
}

// WhackGame is Whack-a-Mole: moles pop from numbered holes for a window that
// shrinks as the round goes on, and the player taps them before they drop.
// Consecutive hits pay a growing bonus; tapping an empty hole costs a little,
// so spamming every hole scores worse than watching.
//
// The whole schedule is drawn from the seed up front, which is what makes the
// round replayable: the server knows exactly which mole was up at any tick.
// The Dart implementation in the mobile app mirrors this file.
type WhackGame struct {
	cfg   WhackConfig
	moles []WhackMole
	hit   []bool

	hits       int
	misses     int
	streak     int
	bestStreak int
	score      int64
	lastTick   int
}

// NewWhackGame draws the whole run of moles from the seed.
func NewWhackGame(seed string, cfg WhackConfig) (*WhackGame, error) {
	state, err := ParseSeed(seed)
	if err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()

	rng := NewRNG(state)
	moles := make([]WhackMole, 0, cfg.Moles)
	tick := cfg.GapTicks
	for i := 0; i < cfg.Moles; i++ {
		// Each mole stays up a little less than the last, to a floor.
		up := cfg.StartUpTicks - cfg.UpStepTicks*i
		if up < cfg.MinUpTicks {
			up = cfg.MinUpTicks
		}
		hole := rng.NextInt(cfg.Holes)
		moles = append(moles, WhackMole{Hole: hole, Up: tick, Down: tick + up})
		// A short, seeded pause before the next one so the rhythm varies.
		tick = tick + up + cfg.GapTicks + rng.NextInt(cfg.GapTicks)
	}

	return &WhackGame{cfg: cfg, moles: moles, hit: make([]bool, len(moles))}, nil
}

func (g *WhackGame) Score() int64       { return g.score }
func (g *WhackGame) Hits() int          { return g.hits }
func (g *WhackGame) Moles() []WhackMole { return g.moles }

// LastTick is when the final mole drops — the length of the whole round.
func (g *WhackGame) LastTick() int {
	if len(g.moles) == 0 {
		return 0
	}
	return g.moles[len(g.moles)-1].Down
}

// MoleAt is the index of the mole up at a tick, or -1 when every hole is empty.
func (g *WhackGame) MoleAt(tick int) int {
	for i, m := range g.moles {
		if tick >= m.Up && tick < m.Down {
			return i
		}
		// Moles are generated in ascending order, so nothing later can match.
		if m.Up > tick {
			break
		}
	}
	return -1
}

// Whack taps a hole at a tick.
func (g *WhackGame) Whack(tick, hole int) (bool, error) {
	switch {
	case hole < 0 || hole >= g.cfg.Holes:
		return false, fmt.Errorf("%w: hole %d does not exist", ErrInvalidMove, hole)
	case tick < g.lastTick:
		return false, fmt.Errorf("%w: tap at tick %d is out of order", ErrInvalidMove, tick)
	case tick > g.LastTick():
		return false, fmt.Errorf("%w: tick %d is after the round ended", ErrInvalidMove, tick)
	}
	g.lastTick = tick

	index := g.MoleAt(tick)
	if index < 0 || g.moles[index].Hole != hole || g.hit[index] {
		g.misses++
		g.streak = 0
		if g.score >= int64(g.cfg.MissPenalty) {
			g.score -= int64(g.cfg.MissPenalty)
		} else {
			g.score = 0
		}
		return false, nil
	}

	g.hit[index] = true
	g.hits++
	g.streak++
	if g.streak > g.bestStreak {
		g.bestStreak = g.streak
	}
	g.score += int64(g.cfg.PointsPerHit + g.cfg.StreakBonus*(g.streak-1))
	return true, nil
}

// ReplayWhack replays a Whack-a-Mole log: "<tick>:<hole>" for every tap.
func ReplayWhack(seed string, cfg WhackConfig, moves []string) (*Result, error) {
	cfg = cfg.withDefaults()
	g, err := NewWhackGame(seed, cfg)
	if err != nil {
		return nil, err
	}
	for i, m := range moves {
		fields, err := parseTickEntry(m, 2)
		if err != nil {
			return nil, fmt.Errorf("tap %d: %w", i, err)
		}
		if _, err := g.Whack(fields[0], fields[1]); err != nil {
			return nil, fmt.Errorf("tap %d: %w", i, err)
		}
	}
	return &Result{
		Score:         g.score,
		MovesUsed:     len(moves),
		GameOver:      true,
		MinDurationMs: tickFloorMs(g.lastTick, cfg.TickMs),
		Stats: map[string]int64{
			"hits":        int64(g.hits),
			"misses":      int64(g.misses),
			"best_streak": int64(g.bestStreak),
		},
	}, nil
}

type whackEngine struct{}

func (whackEngine) Replay(seed string, raw json.RawMessage, moves []string) (*Result, error) {
	var cfg WhackConfig
	if err := decodeConfig(raw, &cfg); err != nil {
		return nil, err
	}
	return ReplayWhack(seed, cfg, moves)
}
