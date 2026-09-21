package games

import (
	"encoding/json"
	"fmt"
)

// BubbleSlug identifies Bubble Shooter in the catalog and the engine registry.
const BubbleSlug = "bubble-shooter"

// BubbleConfig is the Bubble Shooter rule set, versioned like every game.
type BubbleConfig struct {
	Columns           int `json:"columns"`
	Rows              int `json:"rows"`
	StartRows         int `json:"start_rows"`
	Colors            int `json:"colors"`
	MinCluster        int `json:"min_cluster"`
	PointsPerBubble   int `json:"points_per_bubble"`
	ComboBonus        int `json:"combo_bonus"`
	MaxShots          int `json:"max_shots"`
	MinMsPerShot      int `json:"min_ms_per_shot"`
	SessionTTLSeconds int `json:"session_ttl_seconds"`
}

// DefaultBubbleConfig mirrors the 1.0.0 version seeded by migration 000032.
func DefaultBubbleConfig() BubbleConfig {
	return BubbleConfig{
		Columns:           7,
		Rows:              11,
		StartRows:         4,
		Colors:            4,
		MinCluster:        3,
		PointsPerBubble:   10,
		ComboBonus:        5,
		MaxShots:          200,
		MinMsPerShot:      220,
		SessionTTLSeconds: defaultSessionTTLSeconds,
	}
}

func (c BubbleConfig) withDefaults() BubbleConfig {
	d := DefaultBubbleConfig()
	fill := func(v *int, def int) {
		if *v <= 0 {
			*v = def
		}
	}
	fill(&c.Columns, d.Columns)
	fill(&c.Rows, d.Rows)
	fill(&c.StartRows, d.StartRows)
	fill(&c.Colors, d.Colors)
	fill(&c.MinCluster, d.MinCluster)
	fill(&c.PointsPerBubble, d.PointsPerBubble)
	fill(&c.ComboBonus, d.ComboBonus)
	fill(&c.MaxShots, d.MaxShots)
	fill(&c.MinMsPerShot, d.MinMsPerShot)
	fill(&c.SessionTTLSeconds, d.SessionTTLSeconds)
	if c.StartRows >= c.Rows {
		c.StartRows = c.Rows - 1
	}
	return c
}

// BubbleGame is Bubble Shooter: the player fires the queued colour up a
// column, where it sticks under the bubbles already there. Landing it against
// enough of its own colour pops the whole connected cluster, and anything left
// unsupported above the pop falls too — which is where the big chains come
// from. The ceiling creeps down every time a shot fails to pop anything, and
// reaching the floor ends the round.
//
// The starting wall and every queued colour come from the seed, so the server
// replays the same board the player was aiming at. The Dart implementation in
// the mobile app mirrors this file.
type BubbleGame struct {
	cfg  BubbleConfig
	rng  *DeterministicRNG
	grid [][]int // grid[row][col]; -1 is empty, row 0 is the ceiling
	next int

	shots     int
	pops      int
	bestCombo int
	score     int64
	over      bool
}

// NewBubbleGame builds the opening wall and queues the first colour.
func NewBubbleGame(seed string, cfg BubbleConfig) (*BubbleGame, error) {
	state, err := ParseSeed(seed)
	if err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()

	g := &BubbleGame{cfg: cfg, rng: NewRNG(state)}
	g.grid = make([][]int, cfg.Rows)
	for r := range g.grid {
		g.grid[r] = make([]int, cfg.Columns)
		for c := range g.grid[r] {
			g.grid[r][c] = -1
		}
	}
	for r := 0; r < cfg.StartRows; r++ {
		for c := 0; c < cfg.Columns; c++ {
			g.grid[r][c] = g.rng.NextInt(cfg.Colors)
		}
	}
	g.next = g.rng.NextInt(cfg.Colors)
	return g, nil
}

func (g *BubbleGame) Score() int64  { return g.score }
func (g *BubbleGame) Next() int     { return g.next }
func (g *BubbleGame) Pops() int     { return g.pops }
func (g *BubbleGame) Over() bool    { return g.over }
func (g *BubbleGame) Grid() [][]int { return g.grid }

// At is the colour in a cell, or -1 when it is empty.
func (g *BubbleGame) At(row, col int) int {
	if row < 0 || row >= g.cfg.Rows || col < 0 || col >= g.cfg.Columns {
		return -1
	}
	return g.grid[row][col]
}

// landingRow is where a shot up a column comes to rest: directly under the
// lowest bubble in that column, or on the floor when the column is clear.
func (g *BubbleGame) landingRow(col int) int {
	for r := g.cfg.Rows - 1; r >= 0; r-- {
		if g.grid[r][col] >= 0 {
			return r + 1
		}
	}
	return 0
}

// cluster is every cell of one colour reachable from a starting cell.
func (g *BubbleGame) cluster(row, col int) [][2]int {
	color := g.At(row, col)
	if color < 0 {
		return nil
	}
	seen := map[[2]int]bool{{row, col}: true}
	queue := [][2]int{{row, col}}
	out := [][2]int{{row, col}}
	for len(queue) > 0 {
		cell := queue[0]
		queue = queue[1:]
		for _, step := range [][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
			nr, nc := cell[0]+step[0], cell[1]+step[1]
			key := [2]int{nr, nc}
			if seen[key] || g.At(nr, nc) != color {
				continue
			}
			seen[key] = true
			queue = append(queue, key)
			out = append(out, key)
		}
	}
	return out
}

// dropFloaters clears anything no longer hanging from the ceiling. Walking
// down from row 0 is what makes a pop cascade instead of leaving islands.
func (g *BubbleGame) dropFloaters() int {
	attached := make([][]bool, g.cfg.Rows)
	for r := range attached {
		attached[r] = make([]bool, g.cfg.Columns)
	}
	queue := make([][2]int, 0, g.cfg.Columns)
	for c := 0; c < g.cfg.Columns; c++ {
		if g.grid[0][c] >= 0 {
			attached[0][c] = true
			queue = append(queue, [2]int{0, c})
		}
	}
	for len(queue) > 0 {
		cell := queue[0]
		queue = queue[1:]
		for _, step := range [][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
			nr, nc := cell[0]+step[0], cell[1]+step[1]
			if nr < 0 || nr >= g.cfg.Rows || nc < 0 || nc >= g.cfg.Columns {
				continue
			}
			if attached[nr][nc] || g.grid[nr][nc] < 0 {
				continue
			}
			attached[nr][nc] = true
			queue = append(queue, [2]int{nr, nc})
		}
	}

	dropped := 0
	for r := 0; r < g.cfg.Rows; r++ {
		for c := 0; c < g.cfg.Columns; c++ {
			if g.grid[r][c] >= 0 && !attached[r][c] {
				g.grid[r][c] = -1
				dropped++
			}
		}
	}
	return dropped
}

// Shoot fires the queued colour up a column.
func (g *BubbleGame) Shoot(col int) (int, error) {
	switch {
	case g.over:
		return 0, fmt.Errorf("%w: shot after the wall reached the floor", ErrInvalidMove)
	case col < 0 || col >= g.cfg.Columns:
		return 0, fmt.Errorf("%w: column %d is off the board", ErrInvalidMove, col)
	case g.shots >= g.cfg.MaxShots:
		return 0, fmt.Errorf("%w: the %d shot limit is spent", ErrInvalidMove, g.cfg.MaxShots)
	}

	row := g.landingRow(col)
	if row >= g.cfg.Rows {
		// The column is full to the floor: the wall has won.
		g.over = true
		g.shots++
		return 0, nil
	}

	g.grid[row][col] = g.next
	g.shots++

	popped := 0
	if group := g.cluster(row, col); len(group) >= g.cfg.MinCluster {
		for _, cell := range group {
			g.grid[cell[0]][cell[1]] = -1
		}
		popped = len(group) + g.dropFloaters()
		g.pops += popped
		combo := popped / g.cfg.MinCluster
		if combo > g.bestCombo {
			g.bestCombo = combo
		}
		g.score += int64(popped*g.cfg.PointsPerBubble + g.cfg.ComboBonus*combo)
	}

	if g.landingRow(col) >= g.cfg.Rows {
		g.over = true
	}
	g.next = g.rng.NextInt(g.cfg.Colors)
	return popped, nil
}

// ReplayBubble replays a Bubble Shooter log: the column of every shot.
func ReplayBubble(seed string, cfg BubbleConfig, moves []string) (*Result, error) {
	cfg = cfg.withDefaults()
	g, err := NewBubbleGame(seed, cfg)
	if err != nil {
		return nil, err
	}
	for i, m := range moves {
		fields, err := parseTickEntry(m, 1)
		if err != nil {
			return nil, fmt.Errorf("shot %d: %w", i, err)
		}
		if _, err := g.Shoot(fields[0]); err != nil {
			return nil, fmt.Errorf("shot %d: %w", i, err)
		}
	}
	return &Result{
		Score:         g.score,
		MovesUsed:     len(moves),
		GameOver:      g.over,
		MinDurationMs: int64(g.shots) * int64(cfg.MinMsPerShot),
		Stats: map[string]int64{
			"pops":       int64(g.pops),
			"shots":      int64(g.shots),
			"best_combo": int64(g.bestCombo),
		},
	}, nil
}

type bubbleEngine struct{}

func (bubbleEngine) Replay(seed string, raw json.RawMessage, moves []string) (*Result, error) {
	var cfg BubbleConfig
	if err := decodeConfig(raw, &cfg); err != nil {
		return nil, err
	}
	return ReplayBubble(seed, cfg, moves)
}
