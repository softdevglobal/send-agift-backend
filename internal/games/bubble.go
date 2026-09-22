package games

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// BubbleSlug identifies Bubble Shooter in the catalog and the engine registry.
const BubbleSlug = "bubble-shooter"

// BubbleSwapMove is the log entry for exchanging the two queued colours.
const BubbleSwapMove = "s"

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

// Bubble flight is simulated in fixed-point units rather than with any
// trigonometry, so the Go server and the Dart client walk a shot along
// exactly the same path. A float would be free to differ in its last bit
// between the two and land the bubble in a different cell.
const (
	bubbleScale = 1000 // fixed-point units per cell
	bubbleStep  = 100  // units travelled per simulation step
	// The steepest shot allowed, as sideways units per 1000 units of rise —
	// about 76 degrees off vertical. Flatter than this and a shot can ping
	// between the walls almost indefinitely.
	bubbleMaxAim = 4000
	// A shot cannot take longer than crossing the board many times over.
	bubbleMaxSteps = 20000
)

// BubbleGame is Bubble Shooter: the player aims the loaded colour anywhere
// across the board and fires, and the bubble flies until it hits the wall of
// bubbles or the ceiling, bouncing off the sides on the way. Landing it
// against enough of its own colour pops the whole connected cluster, and
// anything left unsupported above the pop falls too — which is where the big
// chains come from. A bubble reaching the floor row ends the round.
//
// Two colours are queued at a time and the player may swap them, so a shot
// that has nowhere useful to go is a choice rather than a dead end.
//
// The starting wall and every queued colour come from the seed, so the server
// replays the same board the player was aiming at. The Dart implementation in
// the mobile app mirrors this file.
type BubbleGame struct {
	cfg   BubbleConfig
	rng   *DeterministicRNG
	grid  [][]int // grid[row][col]; -1 is empty, row 0 is the ceiling
	next  int
	after int

	shots     int
	swaps     int
	pops      int
	bestCombo int
	score     int64
	over      bool
}

// NewBubbleGame builds the opening wall and queues the first two colours.
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
	g.after = g.rng.NextInt(cfg.Colors)
	return g, nil
}

func (g *BubbleGame) Score() int64  { return g.score }
func (g *BubbleGame) Next() int     { return g.next }
func (g *BubbleGame) After() int    { return g.after }
func (g *BubbleGame) Pops() int     { return g.pops }
func (g *BubbleGame) Shots() int    { return g.shots }
func (g *BubbleGame) Over() bool    { return g.over }
func (g *BubbleGame) Grid() [][]int { return g.grid }

// At is the colour in a cell, or -1 when it is empty.
func (g *BubbleGame) At(row, col int) int {
	if row < 0 || row >= g.cfg.Rows || col < 0 || col >= g.cfg.Columns {
		return -1
	}
	return g.grid[row][col]
}

// ClampAim holds an aim inside what the engine will accept, so the client and
// the server agree on what a wild drag means rather than one rejecting it.
func ClampAim(dx int) int {
	if dx > bubbleMaxAim {
		return bubbleMaxAim
	}
	if dx < -bubbleMaxAim {
		return -bubbleMaxAim
	}
	return dx
}

// Trace flies a shot aimed at dx sideways per 1000 units of rise, and reports
// the cell it comes to rest in. ok is false when the shot has nowhere to land,
// which means the wall is already against the floor.
//
// The ball is followed as a point: the last empty cell it passed through is
// where it sticks when the next one is occupied. Every step is integer
// arithmetic, so the client's preview is the same flight the server replays.
func (g *BubbleGame) Trace(dx int) (row, col int, ok bool) {
	dx = ClampAim(dx)
	const dy = -bubbleScale

	m := dx
	if m < 0 {
		m = -m
	}
	if bubbleScale > m {
		m = bubbleScale
	}
	sx := dx * bubbleStep / m
	sy := dy * bubbleStep / m

	width := g.cfg.Columns * bubbleScale
	x := width / 2
	y := g.cfg.Rows * bubbleScale

	lastRow, lastCol, haveLast := -1, -1, false
	for step := 0; step < bubbleMaxSteps; step++ {
		x += sx
		y += sy

		// Bounce off the sides, the way a real shot banks into a gap.
		for x < 0 || x >= width {
			if x < 0 {
				x = -x
			}
			if x >= width {
				x = 2*(width-1) - x
			}
			sx = -sx
		}

		if y <= 0 {
			// Reached the ceiling: it hangs from the top row.
			c := x / bubbleScale
			if c < 0 {
				c = 0
			}
			if c >= g.cfg.Columns {
				c = g.cfg.Columns - 1
			}
			if g.grid[0][c] < 0 {
				return 0, c, true
			}
			if haveLast {
				return lastRow, lastCol, true
			}
			return 0, 0, false
		}

		r := y / bubbleScale
		c := x / bubbleScale
		if r < 0 || r >= g.cfg.Rows || c < 0 || c >= g.cfg.Columns {
			continue
		}
		if g.grid[r][c] >= 0 {
			if haveLast {
				return lastRow, lastCol, true
			}
			// The very first cell the shot entered is taken: there is no
			// room left on the board at all.
			return 0, 0, false
		}
		lastRow, lastCol, haveLast = r, c, true
	}

	if haveLast {
		return lastRow, lastCol, true
	}
	return 0, 0, false
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

// Swap exchanges the loaded colour with the one behind it. It draws nothing
// new from the seed, so it can never be used to fish for a better colour —
// it only lets the player choose which of the two to spend now.
func (g *BubbleGame) Swap() error {
	switch {
	case g.over:
		return fmt.Errorf("%w: swap after the wall reached the floor", ErrInvalidMove)
	case g.swaps >= g.cfg.MaxShots:
		return fmt.Errorf("%w: too many swaps", ErrInvalidMove)
	}
	g.next, g.after = g.after, g.next
	g.swaps++
	return nil
}

// Shoot fires the loaded colour at an aim of dx sideways per 1000 units of
// rise: 0 is straight up, negative leans left, positive right.
func (g *BubbleGame) Shoot(dx int) (int, error) {
	switch {
	case g.over:
		return 0, fmt.Errorf("%w: shot after the wall reached the floor", ErrInvalidMove)
	case g.shots >= g.cfg.MaxShots:
		return 0, fmt.Errorf("%w: the %d shot limit is spent", ErrInvalidMove, g.cfg.MaxShots)
	}

	row, col, ok := g.Trace(dx)
	g.shots++
	if !ok {
		// Nowhere left for it to land: the wall has won.
		g.over = true
		return 0, nil
	}

	g.grid[row][col] = g.next

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

	// A bubble on the floor row means the wall has come all the way down.
	for c := 0; c < g.cfg.Columns; c++ {
		if g.grid[g.cfg.Rows-1][c] >= 0 {
			g.over = true
			break
		}
	}

	g.next = g.after
	g.after = g.rng.NextInt(g.cfg.Colors)
	return popped, nil
}

// ReplayBubble replays a Bubble Shooter log. Each entry is either the aim of
// a shot — sideways units per 1000 of rise — or "s" for a swap of the two
// queued colours.
func ReplayBubble(seed string, cfg BubbleConfig, moves []string) (*Result, error) {
	cfg = cfg.withDefaults()
	g, err := NewBubbleGame(seed, cfg)
	if err != nil {
		return nil, err
	}
	for i, m := range moves {
		if m == BubbleSwapMove {
			if err := g.Swap(); err != nil {
				return nil, fmt.Errorf("move %d: %w", i, err)
			}
			continue
		}
		dx, err := strconv.Atoi(m)
		if err != nil {
			return nil, fmt.Errorf("move %d: %w: %q is not an aim", i, ErrInvalidMove, m)
		}
		if dx < -bubbleMaxAim || dx > bubbleMaxAim {
			return nil, fmt.Errorf("move %d: %w: aim %d is off the board", i, ErrInvalidMove, dx)
		}
		if _, err := g.Shoot(dx); err != nil {
			return nil, fmt.Errorf("move %d: %w", i, err)
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
