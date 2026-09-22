package games

import (
	"encoding/json"
	"fmt"
)

// FruitSlug identifies Fruit Slice in the catalog and the engine registry.
const FruitSlug = "fruit-slice"

// FruitConfig is the Fruit Slice rule set, versioned like every game.
type FruitConfig struct {
	TickMs            int `json:"tick_ms"`
	Lanes             int `json:"lanes"`
	Throws            int `json:"throws"`
	StartFlightTicks  int `json:"start_flight_ticks"`
	MinFlightTicks    int `json:"min_flight_ticks"`
	FlightStepTicks   int `json:"flight_step_ticks"`
	GapTicks          int `json:"gap_ticks"`
	BombEveryThrows   int `json:"bomb_every_throws"`
	PointsPerFruit    int `json:"points_per_fruit"`
	ComboBonus        int `json:"combo_bonus"`
	SessionTTLSeconds int `json:"session_ttl_seconds"`
}

// DefaultFruitConfig mirrors the 1.0.0 version seeded by migration 000032.
func DefaultFruitConfig() FruitConfig {
	return FruitConfig{
		TickMs:            20,
		Lanes:             5,
		Throws:            48,
		StartFlightTicks:  60,
		MinFlightTicks:    24,
		FlightStepTicks:   1,
		GapTicks:          14,
		BombEveryThrows:   7,
		PointsPerFruit:    12,
		ComboBonus:        6,
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
	fill(&c.Lanes, d.Lanes)
	fill(&c.Throws, d.Throws)
	fill(&c.StartFlightTicks, d.StartFlightTicks)
	fill(&c.MinFlightTicks, d.MinFlightTicks)
	fill(&c.FlightStepTicks, d.FlightStepTicks)
	fill(&c.GapTicks, d.GapTicks)
	fill(&c.BombEveryThrows, d.BombEveryThrows)
	fill(&c.PointsPerFruit, d.PointsPerFruit)
	fill(&c.ComboBonus, d.ComboBonus)
	fill(&c.SessionTTLSeconds, d.SessionTTLSeconds)
	return c
}

// FruitThrow is one item in the air: its lane, the tick window it can be cut
// in, and whether cutting it ends the round.
type FruitThrow struct {
	Lane  int
	Enter int
	Exit  int
	Bomb  bool
	Kind  int // which fruit to draw; ignored for bombs
}

// FruitGame is Fruit Slice: gifts arc up through numbered lanes and the player
// swipes the lane to cut them, with flights getting shorter as the round goes
// on. Cutting several in a row pays a growing bonus, and catching a bomb ends
// it — so the round is about what you leave alone as much as what you cut.
//
// The whole throw schedule comes from the seed, which is what lets the server
// replay it. The Dart implementation in the mobile app mirrors this file.
type FruitGame struct {
	cfg    FruitConfig
	throws []FruitThrow
	cut    []bool

	sliced     int
	streak     int
	bestStreak int
	score      int64
	lastTick   int
	over       bool
}

// NewFruitGame draws the whole run of throws from the seed.
func NewFruitGame(seed string, cfg FruitConfig) (*FruitGame, error) {
	state, err := ParseSeed(seed)
	if err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()

	rng := NewRNG(state)
	throws := make([]FruitThrow, 0, cfg.Throws)
	tick := cfg.GapTicks
	for i := 0; i < cfg.Throws; i++ {
		flight := cfg.StartFlightTicks - cfg.FlightStepTicks*i
		if flight < cfg.MinFlightTicks {
			flight = cfg.MinFlightTicks
		}
		lane := rng.NextInt(cfg.Lanes)
		kind := rng.NextInt(4)
		// Bombs arrive on a fixed cadence so the round stays fair to replay,
		// but their lane is still drawn from the seed.
		bomb := i > 0 && i%cfg.BombEveryThrows == 0
		throws = append(throws, FruitThrow{
			Lane:  lane,
			Enter: tick,
			Exit:  tick + flight,
			Bomb:  bomb,
			Kind:  kind,
		})
		tick = tick + flight + cfg.GapTicks + rng.NextInt(cfg.GapTicks)
	}
	return &FruitGame{cfg: cfg, throws: throws, cut: make([]bool, len(throws))}, nil
}

func (g *FruitGame) Score() int64         { return g.score }
func (g *FruitGame) Sliced() int          { return g.sliced }
func (g *FruitGame) Over() bool           { return g.over }
func (g *FruitGame) Throws() []FruitThrow { return g.throws }

// LastTick is when the final throw lands — the length of the whole round.
func (g *FruitGame) LastTick() int {
	if len(g.throws) == 0 {
		return 0
	}
	return g.throws[len(g.throws)-1].Exit
}

// ThrowAt is the index of the throw in a lane at a tick, or -1 when the lane
// is empty.
func (g *FruitGame) ThrowAt(tick, lane int) int {
	for i, t := range g.throws {
		if t.Enter > tick {
			break
		}
		if t.Lane == lane && tick >= t.Enter && tick < t.Exit {
			return i
		}
	}
	return -1
}

// Slice swipes a lane at a tick.
func (g *FruitGame) Slice(tick, lane int) (bool, error) {
	switch {
	case g.over:
		return false, fmt.Errorf("%w: swipe after the round ended", ErrInvalidMove)
	case lane < 0 || lane >= g.cfg.Lanes:
		return false, fmt.Errorf("%w: lane %d does not exist", ErrInvalidMove, lane)
	case tick < g.lastTick:
		return false, fmt.Errorf("%w: swipe at tick %d is out of order", ErrInvalidMove, tick)
	case tick > g.LastTick():
		return false, fmt.Errorf("%w: tick %d is after the round ended", ErrInvalidMove, tick)
	}
	g.lastTick = tick

	index := g.ThrowAt(tick, lane)
	if index < 0 || g.cut[index] {
		// A swipe through thin air just breaks the run.
		g.streak = 0
		return false, nil
	}

	g.cut[index] = true
	if g.throws[index].Bomb {
		g.over = true
		g.streak = 0
		return false, nil
	}

	g.sliced++
	g.streak++
	if g.streak > g.bestStreak {
		g.bestStreak = g.streak
	}
	g.score += int64(g.cfg.PointsPerFruit + g.cfg.ComboBonus*(g.streak-1))
	return true, nil
}

// ReplayFruit replays a Fruit Slice log: "<tick>:<lane>" for every swipe.
func ReplayFruit(seed string, cfg FruitConfig, moves []string) (*Result, error) {
	cfg = cfg.withDefaults()
	g, err := NewFruitGame(seed, cfg)
	if err != nil {
		return nil, err
	}
	for i, m := range moves {
		fields, err := parseTickEntry(m, 2)
		if err != nil {
			return nil, fmt.Errorf("swipe %d: %w", i, err)
		}
		if _, err := g.Slice(fields[0], fields[1]); err != nil {
			return nil, fmt.Errorf("swipe %d: %w", i, err)
		}
	}
	return &Result{
		Score:         g.score,
		MovesUsed:     len(moves),
		GameOver:      g.over,
		MinDurationMs: tickFloorMs(g.lastTick, cfg.TickMs),
		Stats: map[string]int64{
			"fruits":      int64(g.sliced),
			"best_streak": int64(g.bestStreak),
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
