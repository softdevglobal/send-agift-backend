package games

import (
	"encoding/json"
	"fmt"
)

// HillSlug identifies Hill Rider in the catalog and the engine registry.
const HillSlug = "hill-rider"

// Hill Rider pedal states, as logged.
const (
	HillGas     = "g"
	HillBrake   = "b"
	HillNeutral = "n"
)

// HillConfig is the Hill Rider rule set, versioned like every game.
type HillConfig struct {
	TickMs            int `json:"tick_ms"`
	KnotSpacing       int `json:"knot_spacing"`
	Knots             int `json:"knots"`
	HillAmp           int `json:"hill_amp"`
	HillRamp          int `json:"hill_ramp"`
	StartFuel         int `json:"start_fuel"`
	FuelCanEvery      int `json:"fuel_can_every"`
	Engine            int `json:"engine"`
	Brake             int `json:"brake"`
	SlopeGravity      int `json:"slope_gravity"`
	AirGravity        int `json:"air_gravity"`
	Friction          int `json:"friction"`
	MaxSpeed          int `json:"max_speed"`
	LaunchK           int `json:"launch_k"`
	CrashSlope        int `json:"crash_slope"`
	MaxTicks          int `json:"max_ticks"`
	SessionTTLSeconds int `json:"session_ttl_seconds"`
}

// DefaultHillConfig mirrors the 1.0.0 version seeded by migration 000031,
// made steeper and less forgiving by migration 000041.
func DefaultHillConfig() HillConfig {
	return HillConfig{
		TickMs:      20,
		KnotSpacing: 200,
		Knots:       600,
		// The biggest rise or fall between two knots, and how fast the
		// course grows to it from the flat start. Past 200 a climb is
		// steeper than the engine alone can pull, so the tallest hills need
		// a run-up.
		HillAmp:           220,
		HillRamp:          7,
		StartFuel:         800,
		FuelCanEvery:      16,
		Engine:            4,
		Brake:             4,
		SlopeGravity:      4,
		AirGravity:        3,
		Friction:          1,
		MaxSpeed:          150,
		LaunchK:           1000000,
		CrashSlope:        130,
		MaxTicks:          30000,
		SessionTTLSeconds: defaultSessionTTLSeconds,
	}
}

func (c HillConfig) withDefaults() HillConfig {
	d := DefaultHillConfig()
	fill := func(v *int, def int) {
		if *v <= 0 {
			*v = def
		}
	}
	fill(&c.TickMs, d.TickMs)
	fill(&c.KnotSpacing, d.KnotSpacing)
	fill(&c.HillAmp, d.HillAmp)
	fill(&c.HillRamp, d.HillRamp)
	fill(&c.StartFuel, d.StartFuel)
	fill(&c.FuelCanEvery, d.FuelCanEvery)
	fill(&c.Engine, d.Engine)
	fill(&c.Brake, d.Brake)
	fill(&c.SlopeGravity, d.SlopeGravity)
	fill(&c.AirGravity, d.AirGravity)
	fill(&c.Friction, d.Friction)
	fill(&c.MaxSpeed, d.MaxSpeed)
	fill(&c.LaunchK, d.LaunchK)
	fill(&c.CrashSlope, d.CrashSlope)
	fill(&c.MaxTicks, d.MaxTicks)
	if c.Knots < 10 {
		c.Knots = d.Knots
	}
	return c
}

// HillGame is Hill Rider: drive as far as you can over rolling hills.
//
// The car moves along the ground under engine, brake, gravity on the slope
// and friction. Crest a hill too fast and it takes off; land at an angle
// that does not match the ground and it crashes. Gas burns fuel, and fuel
// cans along the way refill the tank.
//
// The hills are drawn from the seed and visible ahead of the car, and every
// quantity — position, speed, height — is a whole number stepped on fixed
// ticks, so the server replays the drive exactly. Positions and speeds are in
// sixteenths of a unit; terrain heights are in whole units.
//
// The Dart implementation in the mobile app mirrors this file exactly.
type HillGame struct {
	cfg      HillConfig
	heights  []int
	x, y     int
	v, vy    int
	airborne bool
	input    string
	fuel     int
	nextCan  int
	cans     int
	maxX     int
	airTicks int
	ticks    int
	crashed  bool
	finished bool
}

// NewHillGame lays out the hills from the seed.
func NewHillGame(seed string, cfg HillConfig) (*HillGame, error) {
	state, err := ParseSeed(seed)
	if err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()
	rng := NewRNG(state)
	g := &HillGame{cfg: cfg, input: HillNeutral, fuel: cfg.StartFuel}
	g.heights = make([]int, cfg.Knots)
	for i := 3; i < cfg.Knots; i++ {
		amp := 24 + i*cfg.HillRamp
		if amp > cfg.HillAmp {
			amp = cfg.HillAmp
		}
		delta := rng.NextInt(2*amp+1) - amp
		if absInt(g.heights[i-1]+delta) > 1200 {
			delta = -delta
		}
		g.heights[i] = g.heights[i-1] + delta
	}
	return g, nil
}

func (g *HillGame) segment(x int) int {
	i := x / 16 / g.cfg.KnotSpacing
	if i > g.cfg.Knots-2 {
		i = g.cfg.Knots - 2
	}
	return i
}

func (g *HillGame) slope(i int) int { return g.heights[i+1] - g.heights[i] }

// HeightAt is the ground height, in units, at a position in units.
func (g *HillGame) HeightAt(xu int) int {
	s := g.cfg.KnotSpacing
	i := xu / s
	if i >= g.cfg.Knots-1 {
		return g.heights[g.cfg.Knots-1]
	}
	return g.heights[i] + (g.heights[i+1]-g.heights[i])*(xu-i*s)/s
}

func (g *HillGame) groundY(x int) int { return g.HeightAt(x/16) * 16 }

func towardZero(v, by int) int {
	switch {
	case v > by:
		return v - by
	case v < -by:
		return v + by
	}
	return 0
}

// Over reports whether the drive has ended.
func (g *HillGame) Over() bool {
	return g.crashed || g.finished || g.ticks >= g.cfg.MaxTicks ||
		(g.fuel == 0 && g.v == 0 && !g.airborne)
}

// SetInput changes the pedals from the next tick on.
func (g *HillGame) SetInput(input string) { g.input = input }

// Step advances the drive one tick.
func (g *HillGame) Step() {
	if g.Over() {
		return
	}
	c := g.cfg
	g.ticks++

	accel := 0
	if g.input == HillGas && g.fuel > 0 {
		accel = c.Engine
		g.fuel--
	}

	if !g.airborne {
		seg := g.segment(g.x)
		s := g.slope(seg)
		g.v += accel - (c.SlopeGravity*s)/c.KnotSpacing - g.v/64
		if accel == 0 {
			g.v = towardZero(g.v, c.Friction)
		}
		if g.input == HillBrake {
			g.v = towardZero(g.v, c.Brake)
		}
		if g.v > c.MaxSpeed {
			g.v = c.MaxSpeed
		}
		if g.v < -c.MaxSpeed {
			g.v = -c.MaxSpeed
		}
		g.x += g.v
		if g.x < 0 {
			g.x, g.v = 0, 0
		}
		if next := g.segment(g.x); next > seg && g.v > 0 {
			// Cresting: if the ground falls away faster than the car can
			// follow at this speed, it leaves the ground.
			if drop := s - g.slope(next); drop > 0 && g.v*g.v*drop > c.LaunchK {
				g.airborne = true
				g.vy = g.v * s / c.KnotSpacing
			}
		}
		if !g.airborne {
			g.y = g.groundY(g.x)
		}
	} else {
		g.airTicks++
		g.vy -= c.AirGravity
		g.x += g.v
		g.y += g.vy
		if ground := g.groundY(g.x); g.y <= ground {
			v := g.v
			if v < 1 {
				v = 1
			}
			flight := g.vy * c.KnotSpacing / v
			if absInt(flight-g.slope(g.segment(g.x))) > c.CrashSlope {
				g.crashed = true
			} else {
				g.airborne = false
				g.y = ground
				g.v = g.v * 7 / 8
			}
		}
	}

	for g.x/16 >= (g.nextCan+1)*c.FuelCanEvery*c.KnotSpacing {
		g.nextCan++
		g.cans++
		g.fuel = c.StartFuel
	}
	if g.x > g.maxX {
		g.maxX = g.x
	}
	if g.x/16 >= (c.Knots-1)*c.KnotSpacing {
		g.finished = true
	}
}

// Distance is the furthest the car got, in metres (10 units a metre).
func (g *HillGame) Distance() int { return g.maxX / 16 / 10 }

// Score is the distance plus a bonus for air time.
func (g *HillGame) Score() int64 { return int64(g.Distance() + g.airTicks/5) }

// ReplayHill replays a Hill Rider log: "<tick>:<g|b|n>" pedal changes in
// increasing tick order, then exactly one "<ticks>:end".
func ReplayHill(seed string, cfg HillConfig, moves []string) (*Result, error) {
	cfg = cfg.withDefaults()
	if len(moves) == 0 {
		return nil, fmt.Errorf("%w: missing end marker", ErrInvalidMove)
	}
	end, word, err := parseSnakeEntry(moves[len(moves)-1])
	if err != nil {
		return nil, err
	}
	if word != "end" {
		return nil, fmt.Errorf("%w: last entry must be the end marker", ErrInvalidMove)
	}
	if end > cfg.MaxTicks {
		return nil, fmt.Errorf("%w: %d ticks exceeds the %d limit", ErrInvalidMove, end, cfg.MaxTicks)
	}

	type change struct {
		tick  int
		input string
	}
	changes := make([]change, 0, len(moves)-1)
	prev := -1
	for i, m := range moves[:len(moves)-1] {
		tick, input, err := parseSnakeEntry(m)
		if err != nil {
			return nil, err
		}
		if input != HillGas && input != HillBrake && input != HillNeutral {
			return nil, fmt.Errorf("%w: entry %d has pedal %q", ErrInvalidMove, i, input)
		}
		if tick <= prev || tick >= end {
			return nil, fmt.Errorf("%w: entry %d is out of tick order", ErrInvalidMove, i)
		}
		prev = tick
		changes = append(changes, change{tick, input})
	}

	g, err := NewHillGame(seed, cfg)
	if err != nil {
		return nil, err
	}
	next := 0
	for t := 0; t < end && !g.Over(); t++ {
		if next < len(changes) && changes[next].tick == t {
			g.SetInput(changes[next].input)
			next++
		}
		g.Step()
	}
	return &Result{
		Score:         g.Score(),
		MovesUsed:     g.ticks,
		GameOver:      g.Over(),
		Won:           g.finished,
		MinDurationMs: tickFloorMs(g.ticks, cfg.TickMs),
		Stats: map[string]int64{
			"distance":  int64(g.Distance()),
			"air_ticks": int64(g.airTicks),
			"fuel_cans": int64(g.cans),
		},
	}, nil
}

type hillEngine struct{}

func (hillEngine) Replay(seed string, raw json.RawMessage, moves []string) (*Result, error) {
	var cfg HillConfig
	if err := decodeConfig(raw, &cfg); err != nil {
		return nil, err
	}
	return ReplayHill(seed, cfg, moves)
}
