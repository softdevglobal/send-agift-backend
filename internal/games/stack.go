package games

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// StackSlug identifies Stack Tower in the catalog and the engine registry.
const StackSlug = "stack-tower"

// StackConfig is the Stack Tower rule set, versioned like every game.
type StackConfig struct {
	TickMs            int `json:"tick_ms"`
	BaseSize          int `json:"base_size"`
	Travel            int `json:"travel"`
	StartPeriodTicks  int `json:"start_period_ticks"`
	MinPeriodTicks    int `json:"min_period_ticks"`
	PeriodStepTicks   int `json:"period_step_ticks"`
	PerfectTolerance  int `json:"perfect_tolerance"`
	PointsPerFloor    int `json:"points_per_floor"`
	PerfectBonus      int `json:"perfect_bonus"`
	GrowEveryPerfects int `json:"grow_every_perfects"`
	GrowAmount        int `json:"grow_amount"`
	MaxFloors         int `json:"max_floors"`
	MaxTicks          int `json:"max_ticks"`
	SessionTTLSeconds int `json:"session_ttl_seconds"`
}

// DefaultStackConfig mirrors the 1.0.0 version seeded by migration 000030.
//
// The numbers are tuned so a perfect drop is always possible: at the fastest
// period the block moves 4*travel/period ≈ 7.9 units a tick, less than the
// 9-unit perfect window, so some tick always lands inside it.
func DefaultStackConfig() StackConfig {
	return StackConfig{
		TickMs:            20,
		BaseSize:          100,
		Travel:            130,
		StartPeriodTicks:  150,
		MinPeriodTicks:    66,
		PeriodStepTicks:   4,
		PerfectTolerance:  4,
		PointsPerFloor:    10,
		PerfectBonus:      5,
		GrowEveryPerfects: 4,
		GrowAmount:        6,
		MaxFloors:         500,
		MaxTicks:          90000,
		SessionTTLSeconds: defaultSessionTTLSeconds,
	}
}

func (c StackConfig) withDefaults() StackConfig {
	d := DefaultStackConfig()
	fill := func(v *int, def int) {
		if *v <= 0 {
			*v = def
		}
	}
	fill(&c.TickMs, d.TickMs)
	fill(&c.BaseSize, d.BaseSize)
	fill(&c.Travel, d.Travel)
	fill(&c.StartPeriodTicks, d.StartPeriodTicks)
	fill(&c.MinPeriodTicks, d.MinPeriodTicks)
	fill(&c.PeriodStepTicks, d.PeriodStepTicks)
	fill(&c.PerfectTolerance, d.PerfectTolerance)
	fill(&c.PointsPerFloor, d.PointsPerFloor)
	fill(&c.PerfectBonus, d.PerfectBonus)
	fill(&c.GrowEveryPerfects, d.GrowEveryPerfects)
	fill(&c.GrowAmount, d.GrowAmount)
	fill(&c.MaxFloors, d.MaxFloors)
	fill(&c.MaxTicks, d.MaxTicks)
	c.MinPeriodTicks = evenAtLeast(c.MinPeriodTicks, 2)
	c.StartPeriodTicks = evenAtLeast(c.StartPeriodTicks, c.MinPeriodTicks)
	return c
}

// StackBlock is one floor's footprint: x0..x1 by z0..z1.
type StackBlock struct {
	X0, X1, Z0, Z1 int
}

// StackDrop is the outcome of one drop.
type StackDrop struct {
	Offset  int
	Perfect bool
	Fell    bool
	Points  int
}

// StackGame is Stack Tower: a block slides back and forth over the tower and
// the player drops it. Whatever overhangs is sliced off, so the next floor is
// smaller; a drop within the perfect window snaps into place and keeps the
// size, and a run of perfect drops grows it back. Missing the tower entirely
// ends the game.
//
// The block's position is a pure function of the tick, so the server replays
// exactly the drop the player made. Floors alternate between sliding along x
// and along z; which side each floor comes from is drawn from the seed.
//
// The Dart implementation in the mobile app mirrors this file exactly.
type StackGame struct {
	cfg        StackConfig
	rng        *DeterministicRNG
	floors     []StackBlock // floors[0] is the base
	side       int
	layerStart int
	perfects   int
	streak     int
	bestStreak int
	score      int64
	fell       bool
	lastTick   int
}

// NewStackGame lays the base and starts the first floor sliding.
func NewStackGame(seed string, cfg StackConfig) (*StackGame, error) {
	state, err := ParseSeed(seed)
	if err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()
	half := cfg.BaseSize / 2
	g := &StackGame{
		cfg:    cfg,
		rng:    NewRNG(state),
		floors: []StackBlock{{-half, cfg.BaseSize - half, -half, cfg.BaseSize - half}},
	}
	g.startFloor(0)
	return g, nil
}

func (g *StackGame) startFloor(tick int) {
	g.layerStart = tick
	if g.rng.NextInt(2) == 0 {
		g.side = -1
	} else {
		g.side = 1
	}
}

func (g *StackGame) Score() int64       { return g.score }
func (g *StackGame) Floors() int        { return len(g.floors) - 1 }
func (g *StackGame) Top() StackBlock    { return g.floors[len(g.floors)-1] }
func (g *StackGame) SlidesAlongX() bool { return len(g.floors)%2 == 1 }

// Period is how many ticks the moving floor takes to slide there and back.
// Every floor is a little faster than the last, down to a floor.
func (g *StackGame) Period() int {
	p := g.cfg.StartPeriodTicks - g.cfg.PeriodStepTicks*(len(g.floors)-1)
	return evenAtLeast(p, g.cfg.MinPeriodTicks)
}

// Offset is how far the moving floor is from sitting squarely on the tower.
func (g *StackGame) Offset(tick int) int {
	u := tick - g.layerStart
	if u < 0 {
		u = 0
	}
	return g.side * triangle(u, g.Period(), g.cfg.Travel)
}

// Drop lets the moving floor go at a tick.
func (g *StackGame) Drop(tick int) (StackDrop, error) {
	switch {
	case g.fell:
		return StackDrop{}, fmt.Errorf("%w: drop after the tower fell", ErrInvalidMove)
	case tick <= g.layerStart:
		return StackDrop{}, fmt.Errorf("%w: drop at tick %d before the floor moved", ErrInvalidMove, tick)
	case tick > g.cfg.MaxTicks:
		return StackDrop{}, fmt.Errorf("%w: tick %d exceeds the %d limit", ErrInvalidMove, tick, g.cfg.MaxTicks)
	case g.Floors() >= g.cfg.MaxFloors:
		return StackDrop{}, fmt.Errorf("%w: the tower is complete", ErrInvalidMove)
	}

	offset := g.Offset(tick)
	perfect := absInt(offset) <= g.cfg.PerfectTolerance
	if perfect {
		offset = 0
	}

	top := g.Top()
	a0, a1 := top.Z0, top.Z1
	if g.SlidesAlongX() {
		a0, a1 = top.X0, top.X1
	}
	g.lastTick = tick
	drop := StackDrop{Offset: offset, Perfect: perfect}

	if (a1-a0)-absInt(offset) <= 0 {
		g.fell = true
		drop.Fell = true
		return drop, nil
	}

	n0, n1 := a0, a1
	if offset > 0 {
		n0 = a0 + offset
	} else {
		n1 = a1 + offset
	}

	bonus := 0
	if perfect {
		g.perfects++
		g.streak++
		if g.streak > g.bestStreak {
			g.bestStreak = g.streak
		}
		streak := g.streak
		if streak > 5 {
			streak = 5
		}
		bonus = g.cfg.PerfectBonus * streak
		// A run of perfect drops wins back some width, up to the base size.
		if g.streak%g.cfg.GrowEveryPerfects == 0 {
			n1 += g.cfg.GrowAmount
			if n1-n0 > g.cfg.BaseSize {
				n1 = n0 + g.cfg.BaseSize
			}
		}
	} else {
		g.streak = 0
	}

	next := top
	if g.SlidesAlongX() {
		next.X0, next.X1 = n0, n1
	} else {
		next.Z0, next.Z1 = n0, n1
	}
	drop.Points = g.cfg.PointsPerFloor + bonus
	g.score += int64(drop.Points)
	g.floors = append(g.floors, next)
	g.startFloor(tick)
	return drop, nil
}

// ReplayStack replays a Stack Tower log: the tick of every drop.
func ReplayStack(seed string, cfg StackConfig, moves []string) (*Result, error) {
	cfg = cfg.withDefaults()
	g, err := NewStackGame(seed, cfg)
	if err != nil {
		return nil, err
	}
	for i, m := range moves {
		tick, err := strconv.Atoi(m)
		if err != nil || tick < 0 {
			return nil, fmt.Errorf("%w: drop %d is not a tick: %q", ErrInvalidMove, i, m)
		}
		if _, err := g.Drop(tick); err != nil {
			return nil, fmt.Errorf("drop %d: %w", i, err)
		}
	}
	return &Result{
		Score:         g.score,
		MovesUsed:     len(moves),
		GameOver:      g.fell,
		MinDurationMs: tickFloorMs(g.lastTick, cfg.TickMs),
		Stats: map[string]int64{
			"floors":      int64(g.Floors()),
			"perfects":    int64(g.perfects),
			"best_streak": int64(g.bestStreak),
		},
	}, nil
}

type stackEngine struct{}

func (stackEngine) Replay(seed string, raw json.RawMessage, moves []string) (*Result, error) {
	var cfg StackConfig
	if err := decodeConfig(raw, &cfg); err != nil {
		return nil, err
	}
	return ReplayStack(seed, cfg, moves)
}
