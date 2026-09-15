package games

import (
	"encoding/json"
	"fmt"
)

// ArcherySlug identifies Archery in the catalog and the engine registry.
const ArcherySlug = "archery"

// ArcheryConfig is the Archery rule set, versioned like every game.
type ArcheryConfig struct {
	TickMs            int `json:"tick_ms"`
	Arrows            int `json:"arrows"`
	RingWidth         int `json:"ring_width"`
	MaxWind           int `json:"max_wind"`
	SwayAmplitude     int `json:"sway_amplitude"`
	SwayPeriodX       int `json:"sway_period_x"`
	SwayPeriodY       int `json:"sway_period_y"`
	ReloadTicks       int `json:"reload_ticks"`
	MaxTicks          int `json:"max_ticks"`
	SessionTTLSeconds int `json:"session_ttl_seconds"`
}

// DefaultArcheryConfig mirrors the 1.0.0 version seeded by migration 000030.
func DefaultArcheryConfig() ArcheryConfig {
	return ArcheryConfig{
		TickMs:            30,
		Arrows:            10,
		RingWidth:         10,
		MaxWind:           25,
		SwayAmplitude:     16,
		SwayPeriodX:       46,
		SwayPeriodY:       64,
		ReloadTicks:       30,
		MaxTicks:          20000,
		SessionTTLSeconds: defaultSessionTTLSeconds,
	}
}

func (c ArcheryConfig) withDefaults() ArcheryConfig {
	d := DefaultArcheryConfig()
	fill := func(v *int, def int) {
		if *v <= 0 {
			*v = def
		}
	}
	fill(&c.TickMs, d.TickMs)
	fill(&c.Arrows, d.Arrows)
	fill(&c.RingWidth, d.RingWidth)
	fill(&c.MaxWind, d.MaxWind)
	fill(&c.SwayAmplitude, d.SwayAmplitude)
	fill(&c.SwayPeriodX, d.SwayPeriodX)
	fill(&c.SwayPeriodY, d.SwayPeriodY)
	fill(&c.ReloadTicks, d.ReloadTicks)
	fill(&c.MaxTicks, d.MaxTicks)
	c.SwayPeriodX = evenAtLeast(c.SwayPeriodX, 4)
	c.SwayPeriodY = evenAtLeast(c.SwayPeriodY, 4)
	return c
}

// ArcheryAimLimit bounds how far off the target centre an aim may be.
const ArcheryAimLimit = 150

// ArcheryArrow is the outcome of one arrow.
type ArcheryArrow struct {
	ImpactX, ImpactY int
	Points           int
	X                bool // inside the inner ten
}

// ArcheryGame is ten arrows at a target. The player aims by dragging and
// shoots by letting go. Two things push the arrow off the aim:
//
//   - the sight sways in a fixed figure — a pure function of the tick — so
//     releasing at a steady moment is the skill;
//   - wind blows each arrow sideways. It is drawn from the seed and shown on
//     screen before the shot, so everyone can compensate; with a shared
//     competition seed everyone faces the same wind.
//
// The Dart implementation in the mobile app mirrors this file exactly.
type ArcheryGame struct {
	cfg      ArcheryConfig
	rng      *DeterministicRNG
	wind     int
	arrows   int
	tens     int
	xs       int
	score    int64
	lastTick int
}

// NewArcheryGame draws the first arrow's wind from the seed.
func NewArcheryGame(seed string, cfg ArcheryConfig) (*ArcheryGame, error) {
	state, err := ParseSeed(seed)
	if err != nil {
		return nil, err
	}
	g := &ArcheryGame{cfg: cfg.withDefaults(), rng: NewRNG(state), lastTick: -1}
	g.wind = g.drawWind()
	return g, nil
}

func (g *ArcheryGame) drawWind() int {
	return g.rng.NextInt(2*g.cfg.MaxWind+1) - g.cfg.MaxWind
}

func (g *ArcheryGame) Score() int64 { return g.score }
func (g *ArcheryGame) Wind() int    { return g.wind }
func (g *ArcheryGame) Done() bool   { return g.arrows >= g.cfg.Arrows }

// Sway is how far the sight has drifted from the aim at a tick.
func (g *ArcheryGame) Sway(tick int) (int, int) {
	a := g.cfg.SwayAmplitude
	return triangle(tick, g.cfg.SwayPeriodX, a),
		triangle(tick+g.cfg.SwayPeriodY/4, g.cfg.SwayPeriodY, a)
}

// ArcheryPoints scores an impact: 10 in the centre ring down to 1 in the
// outer one, 0 off the target. Squared distances keep it integer-only.
func ArcheryPoints(x, y, ringWidth int) int {
	d2 := x*x + y*y
	for r := 1; r <= 10; r++ {
		limit := r * ringWidth
		if d2 <= limit*limit {
			return 11 - r
		}
	}
	return 0
}

// CanShoot reports whether an arrow is nocked at a tick.
func (g *ArcheryGame) CanShoot(tick int) bool {
	if g.Done() || tick < 0 || tick > g.cfg.MaxTicks {
		return false
	}
	return g.lastTick < 0 || tick >= g.lastTick+g.cfg.ReloadTicks
}

// Shoot looses an arrow aimed at (aimX, aimY) at a tick.
func (g *ArcheryGame) Shoot(tick, aimX, aimY int) (ArcheryArrow, error) {
	if !g.CanShoot(tick) {
		return ArcheryArrow{}, fmt.Errorf("%w: no arrow ready at tick %d", ErrInvalidMove, tick)
	}
	if absInt(aimX) > ArcheryAimLimit || absInt(aimY) > ArcheryAimLimit {
		return ArcheryArrow{}, fmt.Errorf("%w: aim (%d,%d) out of range", ErrInvalidMove, aimX, aimY)
	}
	sx, sy := g.Sway(tick)
	arrow := ArcheryArrow{ImpactX: aimX + sx + g.wind, ImpactY: aimY + sy}
	arrow.Points = ArcheryPoints(arrow.ImpactX, arrow.ImpactY, g.cfg.RingWidth)
	half := g.cfg.RingWidth / 2
	arrow.X = arrow.ImpactX*arrow.ImpactX+arrow.ImpactY*arrow.ImpactY <= half*half

	g.score += int64(arrow.Points)
	if arrow.Points == 10 {
		g.tens++
	}
	if arrow.X {
		g.xs++
	}
	g.arrows++
	g.lastTick = tick
	g.wind = g.drawWind()
	return arrow, nil
}

// ReplayArchery replays an Archery log: one "<tick>:<aimX>:<aimY>" per arrow.
func ReplayArchery(seed string, cfg ArcheryConfig, moves []string) (*Result, error) {
	cfg = cfg.withDefaults()
	g, err := NewArcheryGame(seed, cfg)
	if err != nil {
		return nil, err
	}
	for i, m := range moves {
		v, err := parseTickEntry(m, 3)
		if err != nil {
			return nil, err
		}
		if _, err := g.Shoot(v[0], v[1], v[2]); err != nil {
			return nil, fmt.Errorf("arrow %d: %w", i, err)
		}
	}
	return &Result{
		Score:         g.score,
		MovesUsed:     g.arrows,
		GameOver:      g.Done(),
		MinDurationMs: tickFloorMs(g.lastTick, cfg.TickMs),
		Stats: map[string]int64{
			"arrows": int64(g.arrows),
			"tens":   int64(g.tens),
			"xs":     int64(g.xs),
		},
	}, nil
}

type archeryEngine struct{}

func (archeryEngine) Replay(seed string, raw json.RawMessage, moves []string) (*Result, error) {
	var cfg ArcheryConfig
	if err := decodeConfig(raw, &cfg); err != nil {
		return nil, err
	}
	return ReplayArchery(seed, cfg, moves)
}
