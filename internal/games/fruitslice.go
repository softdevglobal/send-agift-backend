package games

import (
	"encoding/json"
	"fmt"
)

// FruitSlug identifies Fruit Slice in the catalog and the engine registry.
const FruitSlug = "fruit-slice"

// FruitKinds is how many fruits the board knows how to draw.
const FruitKinds = 6

// fruitKindScale is each fruit's size against the base radius, in percent:
// watermelon, orange, apple, lemon, coconut, plum. The hit circle is the
// drawn circle, so a watermelon really is easier to catch than a plum.
var fruitKindScale = [FruitKinds]int{128, 100, 100, 94, 110, 88}

// FruitConfig is the Fruit Slice rule set, versioned like every game.
//
// Everything is measured on a Width x Height field with y pointing up from
// the bottom edge, in whole units, so the app and the server agree on every
// position exactly.
type FruitConfig struct {
	TickMs           int `json:"tick_ms"`
	Width            int `json:"width"`
	Height           int `json:"height"`
	Volleys          int `json:"volleys"`
	StartFlightTicks int `json:"start_flight_ticks"`
	MinFlightTicks   int `json:"min_flight_ticks"`
	FlightStepTicks  int `json:"flight_step_ticks"`
	StaggerTicks     int `json:"stagger_ticks"`
	GapTicks         int `json:"gap_ticks"`
	MaxVolley        int `json:"max_volley"`
	VolleyGrowEvery  int `json:"volley_grow_every"`
	BombFromVolley   int `json:"bomb_from_volley"`
	BombChance       int `json:"bomb_chance"`
	FruitRadius      int `json:"fruit_radius"`
	BombRadius       int `json:"bomb_radius"`
	MinPeak          int `json:"min_peak"`
	MaxPeak          int `json:"max_peak"`
	Margin           int `json:"margin"`
	Drift            int `json:"drift"`
	Lives            int `json:"lives"`
	PointsPerFruit   int `json:"points_per_fruit"`
	ComboBonus       int `json:"combo_bonus"`
	ComboWindowTicks int `json:"combo_window_ticks"`

	SessionTTLSeconds int `json:"session_ttl_seconds"`
}

// DefaultFruitConfig mirrors the version set by migration 000042, which
// replaced the original lane game with free swiping.
func DefaultFruitConfig() FruitConfig {
	return FruitConfig{
		TickMs:            20,
		Width:             1000,
		Height:            1800,
		Volleys:           50,
		StartFlightTicks:  120,
		MinFlightTicks:    80,
		FlightStepTicks:   1,
		StaggerTicks:      8,
		GapTicks:          20,
		MaxVolley:         5,
		VolleyGrowEvery:   6,
		BombFromVolley:    3,
		BombChance:        3,
		FruitRadius:       86,
		BombRadius:        76,
		MinPeak:           1250,
		MaxPeak:           1650,
		Margin:            150,
		Drift:             250,
		Lives:             3,
		PointsPerFruit:    10,
		ComboBonus:        5,
		ComboWindowTicks:  15,
		SessionTTLSeconds: defaultSessionTTLSeconds,
	}
}

func (c FruitConfig) withDefaults() FruitConfig {
	d := DefaultFruitConfig()
	fill := func(v *int, def int) {
		if *v <= 0 {
			*v = def
		}
	}
	fill(&c.TickMs, d.TickMs)
	fill(&c.Width, d.Width)
	fill(&c.Height, d.Height)
	fill(&c.Volleys, d.Volleys)
	fill(&c.StartFlightTicks, d.StartFlightTicks)
	fill(&c.MinFlightTicks, d.MinFlightTicks)
	fill(&c.FlightStepTicks, d.FlightStepTicks)
	fill(&c.StaggerTicks, d.StaggerTicks)
	fill(&c.GapTicks, d.GapTicks)
	fill(&c.MaxVolley, d.MaxVolley)
	fill(&c.VolleyGrowEvery, d.VolleyGrowEvery)
	fill(&c.BombFromVolley, d.BombFromVolley)
	fill(&c.BombChance, d.BombChance)
	fill(&c.FruitRadius, d.FruitRadius)
	fill(&c.BombRadius, d.BombRadius)
	fill(&c.MinPeak, d.MinPeak)
	fill(&c.MaxPeak, d.MaxPeak)
	fill(&c.Margin, d.Margin)
	fill(&c.Drift, d.Drift)
	fill(&c.Lives, d.Lives)
	fill(&c.PointsPerFruit, d.PointsPerFruit)
	fill(&c.ComboBonus, d.ComboBonus)
	fill(&c.ComboWindowTicks, d.ComboWindowTicks)
	fill(&c.SessionTTLSeconds, d.SessionTTLSeconds)
	if c.MaxPeak < c.MinPeak {
		c.MaxPeak = c.MinPeak
	}
	if 2*c.Margin >= c.Width {
		c.Margin = c.Width / 4
	}
	return c
}

// FruitThrow is one thing tossed into the air: it rises from below the
// bottom edge at X0, peaks Peak units up, and falls back out at X1, taking
// Exit-Enter ticks to do it.
type FruitThrow struct {
	Enter  int
	Exit   int
	X0     int
	X1     int
	Peak   int
	Radius int
	Bomb   bool
	Kind   int // which fruit to draw; ignored for bombs
}

// Pos is where a throw is at a tick inside its flight: a straight drift
// across and a parabola up, both in whole units.
func (t FruitThrow) Pos(tick int) (x, y int) {
	f := t.Exit - t.Enter
	s := tick - t.Enter
	x = t.X0 + (t.X1-t.X0)*s/f
	y = -t.Radius + 4*(t.Peak+t.Radius)*s*(f-s)/(f*f)
	return x, y
}

// FruitGame is Fruit Slice: fruit is tossed up from below in volleys and the
// player draws a blade across the screen to cut it. Several fruit in one
// stroke pay a growing combo; a fruit that falls back uncut costs a life, and
// cutting a bomb ends the round outright.
//
// The whole schedule of throws comes from the seed and every position is
// whole-unit maths, which is what lets the server replay a round exactly.
// The Dart implementation in the mobile app mirrors this file.
type FruitGame struct {
	cfg    FruitConfig
	throws []FruitThrow
	cut    []bool

	settledTo int // throws before this index have landed or been cut
	tick      int
	stroke    int
	strokeRun int
	strokeAt  int

	sliced    int
	bestCombo int
	dropped   int
	score     int64
	over      bool
}

// NewFruitGame draws the whole run of throws from the seed.
func NewFruitGame(seed string, cfg FruitConfig) (*FruitGame, error) {
	state, err := ParseSeed(seed)
	if err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()
	rng := NewRNG(state)

	throws := make([]FruitThrow, 0, cfg.Volleys*2)
	tick := cfg.GapTicks
	for v := 0; v < cfg.Volleys; v++ {
		flight := cfg.StartFlightTicks - cfg.FlightStepTicks*v
		if flight < cfg.MinFlightTicks {
			flight = cfg.MinFlightTicks
		}
		most := 1 + v/cfg.VolleyGrowEvery
		if most > cfg.MaxVolley {
			most = cfg.MaxVolley
		}
		size := 1 + rng.NextInt(most)
		bombAt := -1
		if v >= cfg.BombFromVolley && rng.NextInt(cfg.BombChance) == 0 {
			bombAt = rng.NextInt(size)
		}
		for k := 0; k < size; k++ {
			kind := rng.NextInt(FruitKinds)
			bomb := k == bombAt
			radius := cfg.FruitRadius * fruitKindScale[kind] / 100
			if bomb {
				radius = cfg.BombRadius
			}
			x0 := cfg.Margin + rng.NextInt(cfg.Width-2*cfg.Margin+1)
			x1 := x0 + rng.NextInt(2*cfg.Drift+1) - cfg.Drift
			x1 = clampInt(x1, radius, cfg.Width-radius)
			peak := cfg.MinPeak + rng.NextInt(cfg.MaxPeak-cfg.MinPeak+1)
			enter := tick + k*cfg.StaggerTicks
			throws = append(throws, FruitThrow{
				Enter:  enter,
				Exit:   enter + flight,
				X0:     x0,
				X1:     x1,
				Peak:   peak,
				Radius: radius,
				Bomb:   bomb,
				Kind:   kind,
			})
		}
		tick += (size-1)*cfg.StaggerTicks + flight*3/4 + rng.NextInt(cfg.GapTicks)
	}
	return &FruitGame{
		cfg:      cfg,
		throws:   throws,
		cut:      make([]bool, len(throws)),
		strokeAt: -1,
	}, nil
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (g *FruitGame) Score() int64         { return g.score }
func (g *FruitGame) Sliced() int          { return g.sliced }
func (g *FruitGame) Dropped() int         { return g.dropped }
func (g *FruitGame) BestCombo() int       { return g.bestCombo }
func (g *FruitGame) Over() bool           { return g.over }
func (g *FruitGame) Throws() []FruitThrow { return g.throws }

// LastTick is when the final throw lands. The length of the whole round.
func (g *FruitGame) LastTick() int {
	last := 0
	for _, t := range g.throws {
		if t.Exit > last {
			last = t.Exit
		}
	}
	return last
}

// settle runs the clock up to tick, charging a life for every fruit that
// fell back out uncut on the way. Losing the last life ends the round.
func (g *FruitGame) settle(tick int) {
	if tick < g.tick {
		return
	}
	g.tick = tick
	// Throws land in order of Exit only within a volley, so walk from the
	// first unsettled throw and stop at the first one still in the air.
	for i := g.settledTo; i < len(g.throws); i++ {
		t := g.throws[i]
		if t.Enter > tick {
			break
		}
		if g.cut[i] || t.Exit > tick {
			continue
		}
		g.cut[i] = true // landed: nothing more can happen to it
		if !t.Bomb {
			g.dropped++
			if g.dropped >= g.cfg.Lives && !g.over {
				g.over = true
			}
		}
	}
	for g.settledTo < len(g.throws) && g.cut[g.settledTo] {
		g.settledTo++
	}
}

// segmentHits reports whether the segment (x1,y1)-(x2,y2) passes within r of
// (cx,cy). Integer-only: the squared distance is compared scaled by the
// segment's squared length rather than divided through by it.
func segmentHits(x1, y1, x2, y2, cx, cy, r int) bool {
	dx, dy := int64(x2-x1), int64(y2-y1)
	fx, fy := int64(cx-x1), int64(cy-y1)
	r2 := int64(r) * int64(r)
	len2 := dx*dx + dy*dy
	dot := fx*dx + fy*dy
	if len2 == 0 || dot <= 0 {
		return fx*fx+fy*fy <= r2
	}
	if dot >= len2 {
		ex, ey := int64(cx-x2), int64(cy-y2)
		return ex*ex+ey*ey <= r2
	}
	return (fx*fx+fy*fy)*len2-dot*dot <= r2*len2
}

// Slice draws one piece of a blade stroke at a tick and returns how many
// fruit it cut. Strokes are numbered by the client; fruit cut in the same
// stroke without a long pause between them build a combo.
func (g *FruitGame) Slice(tick, stroke, x1, y1, x2, y2 int) (int, error) {
	pad := g.cfg.Width / 2
	inside := func(x, y int) bool {
		return x >= -pad && x <= g.cfg.Width+pad && y >= -pad && y <= g.cfg.Height+pad
	}
	switch {
	case tick < g.tick:
		return 0, fmt.Errorf("%w: swipe at tick %d is out of order", ErrInvalidMove, tick)
	case stroke < g.stroke:
		return 0, fmt.Errorf("%w: stroke %d is out of order", ErrInvalidMove, stroke)
	case !inside(x1, y1) || !inside(x2, y2):
		return 0, fmt.Errorf("%w: swipe leaves the board", ErrInvalidMove)
	case tick > g.LastTick():
		return 0, fmt.Errorf("%w: tick %d is after the round ended", ErrInvalidMove, tick)
	}
	g.settle(tick)
	if g.over {
		return 0, fmt.Errorf("%w: swipe after the round ended", ErrInvalidMove)
	}
	if stroke != g.stroke {
		g.stroke = stroke
		g.strokeRun = 0
		g.strokeAt = -1
	}

	cuts := 0
	for i := g.settledTo; i < len(g.throws); i++ {
		t := g.throws[i]
		if t.Enter > tick {
			break
		}
		if g.cut[i] || tick >= t.Exit {
			continue
		}
		cx, cy := t.Pos(tick)
		if !segmentHits(x1, y1, x2, y2, cx, cy, t.Radius) {
			continue
		}
		g.cut[i] = true
		if t.Bomb {
			g.over = true
			return cuts, nil
		}
		if g.strokeAt < 0 || tick-g.strokeAt > g.cfg.ComboWindowTicks {
			g.strokeRun = 0
		}
		g.strokeRun++
		g.strokeAt = tick
		if g.strokeRun > g.bestCombo {
			g.bestCombo = g.strokeRun
		}
		g.sliced++
		g.score += int64(g.cfg.PointsPerFruit + g.cfg.ComboBonus*(g.strokeRun-1))
		cuts++
	}
	return cuts, nil
}

// ReplayFruit replays a Fruit Slice log: "<tick>:<stroke>:<x1>:<y1>:<x2>:<y2>"
// for every piece of a blade stroke that cut something.
func ReplayFruit(seed string, cfg FruitConfig, moves []string) (*Result, error) {
	cfg = cfg.withDefaults()
	g, err := NewFruitGame(seed, cfg)
	if err != nil {
		return nil, err
	}
	for i, m := range moves {
		f, err := parseTickEntry(m, 6)
		if err != nil {
			return nil, fmt.Errorf("swipe %d: %w", i, err)
		}
		if _, err := g.Slice(f[0], f[1], f[2], f[3], f[4], f[5]); err != nil {
			return nil, fmt.Errorf("swipe %d: %w", i, err)
		}
	}
	lastMove := g.tick
	g.settle(g.LastTick())
	return &Result{
		Score:         g.score,
		MovesUsed:     len(moves),
		GameOver:      g.over,
		MinDurationMs: tickFloorMs(lastMove, cfg.TickMs),
		Stats: map[string]int64{
			"fruits":     int64(g.sliced),
			"best_combo": int64(g.bestCombo),
		},
	}, nil
}

type fruitEngine struct{}

func (fruitEngine) Replay(seed string, raw json.RawMessage, moves []string) (*Result, error) {
	var cfg FruitConfig
	if err := decodeConfig(raw, &cfg); err != nil {
		return nil, err
	}
	return ReplayFruit(seed, cfg, moves)
}
