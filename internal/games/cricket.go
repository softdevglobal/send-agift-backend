package games

import (
	"encoding/json"
	"fmt"
)

// CricketSlug identifies Cricket in the catalog and the engine registry.
const CricketSlug = "cricket"

// CricketConfig is the Cricket rule set, versioned like every game.
type CricketConfig struct {
	TickMs            int `json:"tick_ms"`
	Balls             int `json:"balls"`
	Wickets           int `json:"wickets"`
	BallCycleTicks    int `json:"ball_cycle_ticks"`
	RunupTicks        int `json:"runup_ticks"`
	MinTravelTicks    int `json:"min_travel_ticks"`
	MaxTravelTicks    int `json:"max_travel_ticks"`
	PerfectWindow     int `json:"perfect_window"`
	GoodWindow        int `json:"good_window"`
	EdgeWindow        int `json:"edge_window"`
	EarlyTicks        int `json:"early_ticks"`
	LateTicks         int `json:"late_ticks"`
	Fielders          int `json:"fielders"`
	FielderReach      int `json:"fielder_reach"`
	MaxAngle          int `json:"max_angle"`
	SessionTTLSeconds int `json:"session_ttl_seconds"`
}

// DefaultCricketConfig mirrors the 1.0.0 version seeded by migration 000031.
func DefaultCricketConfig() CricketConfig {
	return CricketConfig{
		TickMs:            20,
		Balls:             12,
		Wickets:           3,
		BallCycleTicks:    160,
		RunupTicks:        45,
		MinTravelTicks:    38,
		MaxTravelTicks:    62,
		PerfectWindow:     1,
		GoodWindow:        3,
		EdgeWindow:        6,
		EarlyTicks:        12,
		LateTicks:         8,
		Fielders:          5,
		FielderReach:      10,
		MaxAngle:          80,
		SessionTTLSeconds: defaultSessionTTLSeconds,
	}
}

func (c CricketConfig) withDefaults() CricketConfig {
	d := DefaultCricketConfig()
	fill := func(v *int, def int) {
		if *v <= 0 {
			*v = def
		}
	}
	fill(&c.TickMs, d.TickMs)
	fill(&c.Balls, d.Balls)
	fill(&c.Wickets, d.Wickets)
	fill(&c.BallCycleTicks, d.BallCycleTicks)
	fill(&c.RunupTicks, d.RunupTicks)
	fill(&c.MinTravelTicks, d.MinTravelTicks)
	fill(&c.MaxTravelTicks, d.MaxTravelTicks)
	fill(&c.PerfectWindow, d.PerfectWindow)
	fill(&c.GoodWindow, d.GoodWindow)
	fill(&c.EdgeWindow, d.EdgeWindow)
	fill(&c.EarlyTicks, d.EarlyTicks)
	fill(&c.LateTicks, d.LateTicks)
	fill(&c.Fielders, d.Fielders)
	fill(&c.FielderReach, d.FielderReach)
	fill(&c.MaxAngle, d.MaxAngle)
	if c.MaxTravelTicks < c.MinTravelTicks {
		c.MaxTravelTicks = c.MinTravelTicks
	}
	// Every ball must be over before the next one starts.
	if need := c.RunupTicks + c.MaxTravelTicks + c.LateTicks + 10; c.BallCycleTicks < need {
		c.BallCycleTicks = need
	}
	return c
}

// CricketBall is one delivery: how long it takes to reach the bat, and its
// line (-1 outside off, 0 at the stumps, 1 down leg).
type CricketBall struct {
	Travel int
	Line   int
}

// CricketOutcome is what happened to one ball.
type CricketOutcome struct {
	Ball    int
	Runs    int
	Wicket  bool
	Timing  int // 3 perfect, 2 good, 1 edge, 0 missed or no shot
	Blocked bool
}

// CricketGame is a batting challenge: twelve balls, three wickets.
//
// Every delivery — its pace and line — and where the fielders stand are
// drawn from the seed before a ball is bowled and shown on screen, so with a
// shared competition seed everyone faces the same bowling. The batter taps to
// swing: timing against the ball's arrival decides how well it is struck,
// and where the tap lands aims the shot, which a fielder may cut off.
//
// Deliveries run on a fixed schedule of ticks, so a ball the batter lets go
// resolves the same way whenever it is replayed.
//
// The Dart implementation in the mobile app mirrors this file exactly.
type CricketGame struct {
	cfg      CricketConfig
	balls    []CricketBall
	fielders [][]int // per over
	next     int     // the next ball to resolve
	runs     int
	fours    int
	sixes    int
	wickets  int
	swings   int
	lastTick int
}

// NewCricketGame draws every delivery and field setting up front.
func NewCricketGame(seed string, cfg CricketConfig) (*CricketGame, error) {
	state, err := ParseSeed(seed)
	if err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()
	rng := NewRNG(state)
	g := &CricketGame{cfg: cfg, lastTick: -1}
	for k := 0; k < cfg.Balls; k++ {
		travel := cfg.MinTravelTicks + rng.NextInt(cfg.MaxTravelTicks-cfg.MinTravelTicks+1)
		line := rng.NextInt(3) - 1
		g.balls = append(g.balls, CricketBall{Travel: travel, Line: line})
	}
	overs := (cfg.Balls + 5) / 6
	for o := 0; o < overs; o++ {
		field := make([]int, cfg.Fielders)
		for f := range field {
			field[f] = rng.NextInt(2*cfg.MaxAngle+1) - cfg.MaxAngle
		}
		g.fielders = append(g.fielders, field)
	}
	return g, nil
}

func (g *CricketGame) Runs() int { return g.runs }

// Over reports whether the innings is finished.
func (g *CricketGame) Over() bool {
	return g.wickets >= g.cfg.Wickets || g.next >= g.cfg.Balls
}

// Arrival is the tick ball k reaches the bat.
func (g *CricketGame) Arrival(k int) int {
	return k*g.cfg.BallCycleTicks + g.cfg.RunupTicks + g.balls[k].Travel
}

func (g *CricketGame) timing(delta int) int {
	d := absInt(delta)
	switch {
	case d <= g.cfg.PerfectWindow:
		return 3
	case d <= g.cfg.GoodWindow:
		return 2
	case d <= g.cfg.EdgeWindow:
		return 1
	}
	return 0
}

func (g *CricketGame) blocked(k, angle int) bool {
	for _, f := range g.fielders[k/6] {
		if absInt(angle-f) <= g.cfg.FielderReach {
			return true
		}
	}
	return false
}

func (g *CricketGame) resolve(k int, swung bool, tick, angle int) CricketOutcome {
	out := CricketOutcome{Ball: k}
	if swung {
		out.Timing = g.timing(tick - g.Arrival(k))
		out.Blocked = g.blocked(k, angle)
	}
	switch out.Timing {
	case 3: // middled: over the rope
		out.Runs = 6
		g.sixes++
	case 2:
		if out.Blocked {
			out.Runs = 1
		} else {
			out.Runs = 4
			g.fours++
		}
	case 1: // an edge: safe for one, unless it goes straight to a fielder
		if out.Blocked {
			out.Wicket = true
		} else {
			out.Runs = 1
		}
	default: // missed or left alone: bowled if it was on the stumps
		out.Wicket = g.balls[k].Line == 0
	}
	g.runs += out.Runs
	if out.Wicket {
		g.wickets++
	}
	g.next = k + 1
	return out
}

// settle resolves every ball before k that the batter did not play.
func (g *CricketGame) settle(k int) {
	for g.next < k && !g.Over() {
		g.resolve(g.next, false, 0, 0)
	}
}

// Swing plays a shot at a tick, aimed at an angle (negative off side,
// positive leg side, 0 straight).
func (g *CricketGame) Swing(tick, angle int) (CricketOutcome, error) {
	if absInt(angle) > g.cfg.MaxAngle {
		return CricketOutcome{}, fmt.Errorf("%w: angle %d out of range", ErrInvalidMove, angle)
	}
	k := tick / g.cfg.BallCycleTicks
	g.settle(k)
	if g.Over() {
		return CricketOutcome{}, fmt.Errorf("%w: swing after the innings ended", ErrInvalidMove)
	}
	if k != g.next {
		return CricketOutcome{}, fmt.Errorf("%w: ball %d was already played", ErrInvalidMove, k)
	}
	arrival := g.Arrival(k)
	if tick < arrival-g.cfg.EarlyTicks || tick > arrival+g.cfg.LateTicks {
		return CricketOutcome{}, fmt.Errorf("%w: swing at tick %d is nowhere near ball %d", ErrInvalidMove, tick, k)
	}
	g.swings++
	g.lastTick = tick
	return g.resolve(k, true, tick, angle), nil
}

// ReplayCricket replays a Cricket log: one "<tick>:<angle>" per swing.
func ReplayCricket(seed string, cfg CricketConfig, moves []string) (*Result, error) {
	cfg = cfg.withDefaults()
	g, err := NewCricketGame(seed, cfg)
	if err != nil {
		return nil, err
	}
	prev := -1
	for i, m := range moves {
		v, err := parseTickEntry(m, 2)
		if err != nil {
			return nil, err
		}
		if v[0] <= prev {
			return nil, fmt.Errorf("%w: swing %d is out of tick order", ErrInvalidMove, i)
		}
		prev = v[0]
		if _, err := g.Swing(v[0], v[1]); err != nil {
			return nil, fmt.Errorf("swing %d: %w", i, err)
		}
	}
	g.settle(cfg.Balls)
	return &Result{
		Score:         int64(g.runs),
		MovesUsed:     g.swings,
		GameOver:      true,
		MinDurationMs: tickFloorMs(g.lastTick, cfg.TickMs),
		Stats: map[string]int64{
			"fours":   int64(g.fours),
			"sixes":   int64(g.sixes),
			"wickets": int64(g.wickets),
		},
	}, nil
}

type cricketEngine struct{}

func (cricketEngine) Replay(seed string, raw json.RawMessage, moves []string) (*Result, error) {
	var cfg CricketConfig
	if err := decodeConfig(raw, &cfg); err != nil {
		return nil, err
	}
	return ReplayCricket(seed, cfg, moves)
}
