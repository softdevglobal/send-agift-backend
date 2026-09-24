package games

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// SnakeSlug identifies Snake in the catalog and the engine registry.
const SnakeSlug = "snake"

// SnakeConfig is the Snake rule set, versioned like every other game.
type SnakeConfig struct {
	GridSize         int `json:"grid_size"`
	StartLength      int `json:"start_length"`
	TickMs           int `json:"tick_ms"`
	MinTickMs        int `json:"min_tick_ms"`
	SpeedupMsPerFood int `json:"speedup_ms_per_food"`
	// SpeedupEveryTicks is how many ticks pass before the snake quickens by
	// a millisecond on its own. Zero turns the drift off and leaves the pace
	// to the gifts alone.
	SpeedupEveryTicks int `json:"speedup_every_ticks"`
	PointsPerFood     int `json:"points_per_food"`
	MaxTicks          int `json:"max_ticks"`
	SessionTTLSeconds int `json:"session_ttl_seconds"`
}

// DefaultSnakeConfig mirrors the 1.0.0 version seeded by migration 000028.
func DefaultSnakeConfig() SnakeConfig {
	return SnakeConfig{
		GridSize:    15,
		StartLength: 3,
		// A round opens at a stroll, slow enough to place the first few
		// turns without hurrying. Scoring is what winds it up — every gift
		// takes a good bite out of the tick — with a slight drift underneath
		// so a long round still tightens when the player is not finding any.
		TickMs:            300,
		MinTickMs:         70,
		SpeedupMsPerFood:  12,
		SpeedupEveryTicks: 45,
		PointsPerFood:     10,
		MaxTicks:          20000,
		SessionTTLSeconds: defaultSessionTTLSeconds,
	}
}

func (c SnakeConfig) withDefaults() SnakeConfig {
	d := DefaultSnakeConfig()
	if c.GridSize < 5 {
		c.GridSize = d.GridSize
	}
	if c.StartLength <= 0 {
		c.StartLength = d.StartLength
	}
	// The starting body lies left of centre; it has to fit on the board.
	if maxLen := c.GridSize/2 + 1; c.StartLength > maxLen {
		c.StartLength = maxLen
	}
	if c.TickMs <= 0 {
		c.TickMs = d.TickMs
	}
	if c.MinTickMs <= 0 {
		c.MinTickMs = d.MinTickMs
	}
	if c.SpeedupMsPerFood < 0 {
		c.SpeedupMsPerFood = 0
	}
	if c.SpeedupEveryTicks < 0 {
		c.SpeedupEveryTicks = 0
	}
	if c.PointsPerFood <= 0 {
		c.PointsPerFood = d.PointsPerFood
	}
	if c.MaxTicks <= 0 {
		c.MaxTicks = d.MaxTicks
	}
	return c
}

// SnakeGame is a tick-driven Snake.
//
// Why ticks: a real-time game scored on frames would reward whoever has the
// smoothest phone, and could not be replayed exactly. Here the board only
// changes on numbered ticks, the client logs which tick each turn landed on,
// and the server replays the same ticks — so the outcome depends on the
// player's decisions, not their hardware.
//
// The Dart implementation in the mobile app mirrors this file exactly.
type SnakeGame struct {
	cfg       SnakeConfig
	rng       *DeterministicRNG
	body      []int // head first; cell = y*size + x
	heading   string
	food      int // -1 once the board is full
	foods     int
	score     int64
	ticks     int
	elapsedMs int64
	dead      bool
	won       bool
}

// NewSnakeGame lays the snake out left of centre facing right and places the
// first food from the seed.
func NewSnakeGame(seed string, cfg SnakeConfig) (*SnakeGame, error) {
	state, err := ParseSeed(seed)
	if err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()

	g := &SnakeGame{cfg: cfg, rng: NewRNG(state), heading: MoveRight}
	c := cfg.GridSize / 2
	for i := 0; i < cfg.StartLength; i++ {
		g.body = append(g.body, c*cfg.GridSize+(c-i))
	}
	g.spawnFood()
	return g, nil
}

func (g *SnakeGame) Score() int64 { return g.score }
func (g *SnakeGame) Over() bool   { return g.dead || g.won }
func (g *SnakeGame) Food() int    { return g.food }
func (g *SnakeGame) Ticks() int   { return g.ticks }

// Body returns a copy of the snake, head first.
func (g *SnakeGame) Body() []int {
	out := make([]int, len(g.body))
	copy(out, g.body)
	return out
}

// TickIntervalMs is how long the next tick lasts.
//
// The snake starts at a walk and quickens two ways: every gift eaten takes a
// few milliseconds off, and time itself takes one off every so often. Both
// run down to the same floor, so a round that goes on long enough ends up at
// full pelt whether or not the player is finding gifts.
func (g *SnakeGame) TickIntervalMs() int {
	ms := g.cfg.TickMs - g.cfg.SpeedupMsPerFood*g.foods
	if g.cfg.SpeedupEveryTicks > 0 {
		ms -= g.ticks / g.cfg.SpeedupEveryTicks
	}
	if ms < g.cfg.MinTickMs {
		ms = g.cfg.MinTickMs
	}
	return ms
}

// spawnFood places food on a random empty cell, scanning cells in ascending
// order exactly as the client does.
func (g *SnakeGame) spawnFood() {
	occupied := make(map[int]bool, len(g.body))
	for _, cell := range g.body {
		occupied[cell] = true
	}
	total := g.cfg.GridSize * g.cfg.GridSize
	empties := make([]int, 0, total-len(g.body))
	for i := 0; i < total; i++ {
		if !occupied[i] {
			empties = append(empties, i)
		}
	}
	if len(empties) == 0 {
		g.food = -1
		g.won = true
		return
	}
	g.food = empties[g.rng.NextInt(len(empties))]
}

// Turn changes heading for the next tick. Reversing straight into yourself is
// ignored, as in every Snake.
func (g *SnakeGame) Turn(dir string) {
	if g.Over() || !isDirection(dir) || dir == opposite(g.heading) {
		return
	}
	g.heading = dir
}

// Step advances the board one tick.
func (g *SnakeGame) Step() {
	if g.Over() {
		return
	}
	g.ticks++
	g.elapsedMs += int64(g.TickIntervalMs())

	size := g.cfg.GridSize
	head := g.body[0]
	dx, dy := delta(g.heading)
	nx, ny := head%size+dx, head/size+dy
	if nx < 0 || ny < 0 || nx >= size || ny >= size {
		g.dead = true
		return
	}
	next := ny*size + nx
	eating := next == g.food

	// The tail moves out of the way this tick unless the snake is growing.
	limit := len(g.body)
	if !eating {
		limit--
	}
	for i := 0; i < limit; i++ {
		if g.body[i] == next {
			g.dead = true
			return
		}
	}

	g.body = append([]int{next}, g.body...)
	if eating {
		g.foods++
		g.score += int64(g.cfg.PointsPerFood)
		g.spawnFood()
	} else {
		g.body = g.body[:len(g.body)-1]
	}
}

type snakeInput struct {
	tick int
	dir  string
}

// ReplaySnake replays a Snake move log.
//
// Log format: "<tick>:<direction>" turns in strictly increasing tick order,
// followed by exactly one "<ticks>:end" giving how many ticks were played.
// The end marker is what lets the server credit food eaten after the last
// turn, and what the speed check is measured against.
func ReplaySnake(seed string, cfg SnakeConfig, moves []string) (*Result, error) {
	cfg = cfg.withDefaults()

	if len(moves) == 0 {
		return nil, fmt.Errorf("%w: missing end marker", ErrInvalidMove)
	}
	endTicks, endWord, err := parseSnakeEntry(moves[len(moves)-1])
	if err != nil {
		return nil, err
	}
	if endWord != "end" {
		return nil, fmt.Errorf("%w: last entry must be the end marker", ErrInvalidMove)
	}
	if endTicks > cfg.MaxTicks {
		return nil, fmt.Errorf("%w: %d ticks exceeds the %d limit", ErrInvalidMove, endTicks, cfg.MaxTicks)
	}

	inputs := make([]snakeInput, 0, len(moves)-1)
	prev := -1
	for i, m := range moves[:len(moves)-1] {
		tick, dir, err := parseSnakeEntry(m)
		if err != nil {
			return nil, err
		}
		if !isDirection(dir) {
			return nil, fmt.Errorf("%w: entry %d has direction %q", ErrInvalidMove, i, dir)
		}
		if tick <= prev || tick >= endTicks {
			return nil, fmt.Errorf("%w: entry %d is out of tick order", ErrInvalidMove, i)
		}
		prev = tick
		inputs = append(inputs, snakeInput{tick: tick, dir: dir})
	}

	g, err := NewSnakeGame(seed, cfg)
	if err != nil {
		return nil, err
	}
	next := 0
	for t := 0; t < endTicks && !g.Over(); t++ {
		if next < len(inputs) && inputs[next].tick == t {
			g.Turn(inputs[next].dir)
			next++
		}
		g.Step()
	}

	return &Result{
		Score:     g.score,
		MovesUsed: g.ticks,
		Won:       g.won,
		GameOver:  g.Over(),
		// Client timers only ever run late, never early; the 20% allowance
		// absorbs a janky frame without letting a sped-up client through.
		MinDurationMs: g.elapsedMs * 8 / 10,
		Stats: map[string]int64{
			"length": int64(len(g.body)),
			"food":   int64(g.foods),
			"ticks":  int64(g.ticks),
		},
	}, nil
}

func parseSnakeEntry(entry string) (int, string, error) {
	rawTick, word, ok := strings.Cut(entry, ":")
	if !ok {
		return 0, "", fmt.Errorf("%w: %q is not <tick>:<direction>", ErrInvalidMove, entry)
	}
	tick, err := strconv.Atoi(rawTick)
	if err != nil || tick < 0 {
		return 0, "", fmt.Errorf("%w: bad tick in %q", ErrInvalidMove, entry)
	}
	return tick, word, nil
}

type snakeEngine struct{}

func (snakeEngine) Replay(seed string, raw json.RawMessage, moves []string) (*Result, error) {
	var cfg SnakeConfig
	if err := decodeConfig(raw, &cfg); err != nil {
		return nil, err
	}
	return ReplaySnake(seed, cfg, moves)
}
