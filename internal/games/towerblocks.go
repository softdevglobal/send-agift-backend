package games

import (
	"encoding/json"
	"fmt"
)

// TowerBlocksSlug identifies Tower Blocks in the catalog and the registry.
const TowerBlocksSlug = "tower-blocks"

// TowerBlocksConfig is the Tower Blocks rule set, versioned like every game.
type TowerBlocksConfig struct {
	Columns           int `json:"columns"`
	Rows              int `json:"rows"`
	MaxWidth          int `json:"max_width"`
	PointsPerPiece    int `json:"points_per_piece"`
	PointsPerRow      int `json:"points_per_row"`
	MultiRowBonus     int `json:"multi_row_bonus"`
	MaxPieces         int `json:"max_pieces"`
	MinMsPerPiece     int `json:"min_ms_per_piece"`
	SessionTTLSeconds int `json:"session_ttl_seconds"`
}

// DefaultTowerBlocksConfig mirrors the 1.0.0 version seeded by migration 000032.
func DefaultTowerBlocksConfig() TowerBlocksConfig {
	return TowerBlocksConfig{
		Columns:           6,
		Rows:              12,
		MaxWidth:          3,
		PointsPerPiece:    4,
		PointsPerRow:      40,
		MultiRowBonus:     30,
		MaxPieces:         300,
		MinMsPerPiece:     260,
		SessionTTLSeconds: defaultSessionTTLSeconds,
	}
}

func (c TowerBlocksConfig) withDefaults() TowerBlocksConfig {
	d := DefaultTowerBlocksConfig()
	fill := func(v *int, def int) {
		if *v <= 0 {
			*v = def
		}
	}
	fill(&c.Columns, d.Columns)
	fill(&c.Rows, d.Rows)
	fill(&c.MaxWidth, d.MaxWidth)
	fill(&c.PointsPerPiece, d.PointsPerPiece)
	fill(&c.PointsPerRow, d.PointsPerRow)
	fill(&c.MultiRowBonus, d.MultiRowBonus)
	fill(&c.MaxPieces, d.MaxPieces)
	fill(&c.MinMsPerPiece, d.MinMsPerPiece)
	fill(&c.SessionTTLSeconds, d.SessionTTLSeconds)
	if c.MaxWidth > c.Columns {
		c.MaxWidth = c.Columns
	}
	return c
}

// TowerBlocksGame is Tower Blocks: slabs of one to three cells drop into a
// well, and the player picks the column each lands in. A slab rests on the
// tallest column it spans, so an uneven stack wastes space underneath it;
// filling a row across the whole well clears it and pulls everything down.
// Stacking past the top ends the round.
//
// Every slab's width comes from the seed, so the server replays the same
// sequence the player was handed. The Dart implementation in the mobile app
// mirrors this file.
type TowerBlocksGame struct {
	cfg    TowerBlocksConfig
	rng    *DeterministicRNG
	grid   [][]bool // grid[row][col]; row 0 is the floor
	width  int      // the slab waiting to drop
	pieces int

	rows      int
	bestCombo int
	score     int64
	over      bool
}

// NewTowerBlocksGame empties the well and queues the first slab.
func NewTowerBlocksGame(seed string, cfg TowerBlocksConfig) (*TowerBlocksGame, error) {
	state, err := ParseSeed(seed)
	if err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()

	g := &TowerBlocksGame{cfg: cfg, rng: NewRNG(state)}
	g.grid = make([][]bool, cfg.Rows)
	for r := range g.grid {
		g.grid[r] = make([]bool, cfg.Columns)
	}
	g.width = g.rng.NextInt(cfg.MaxWidth) + 1
	return g, nil
}

func (g *TowerBlocksGame) Score() int64   { return g.score }
func (g *TowerBlocksGame) Width() int     { return g.width }
func (g *TowerBlocksGame) Rows() int      { return g.rows }
func (g *TowerBlocksGame) Pieces() int    { return g.pieces }
func (g *TowerBlocksGame) Over() bool     { return g.over }
func (g *TowerBlocksGame) Grid() [][]bool { return g.grid }

// Filled reports whether a cell holds part of a slab.
func (g *TowerBlocksGame) Filled(row, col int) bool {
	if row < 0 || row >= g.cfg.Rows || col < 0 || col >= g.cfg.Columns {
		return false
	}
	return g.grid[row][col]
}

// height is how many cells are stacked in a column.
func (g *TowerBlocksGame) height(col int) int {
	for r := g.cfg.Rows - 1; r >= 0; r-- {
		if g.grid[r][col] {
			return r + 1
		}
	}
	return 0
}

// MaxColumn is the rightmost column the queued slab can start at.
func (g *TowerBlocksGame) MaxColumn() int { return g.cfg.Columns - g.width }

// RestRow is the row the queued slab would land on from a starting column.
func (g *TowerBlocksGame) RestRow(col int) int {
	rest := 0
	for c := col; c < col+g.width; c++ {
		if h := g.height(c); h > rest {
			rest = h
		}
	}
	return rest
}

// Drop places the queued slab with its left edge at col.
func (g *TowerBlocksGame) Drop(col int) (int, error) {
	switch {
	case g.over:
		return 0, fmt.Errorf("%w: drop after the well overflowed", ErrInvalidMove)
	case col < 0 || col > g.MaxColumn():
		return 0, fmt.Errorf("%w: a %d-wide slab does not fit at column %d", ErrInvalidMove, g.width, col)
	case g.pieces >= g.cfg.MaxPieces:
		return 0, fmt.Errorf("%w: the %d piece limit is spent", ErrInvalidMove, g.cfg.MaxPieces)
	}

	row := g.RestRow(col)
	g.pieces++
	if row >= g.cfg.Rows {
		// Nowhere left to rest: the stack has reached the top.
		g.over = true
		return 0, nil
	}
	for c := col; c < col+g.width; c++ {
		g.grid[row][c] = true
	}
	g.score += int64(g.cfg.PointsPerPiece)

	cleared := g.clearRows()
	if cleared > 0 {
		g.rows += cleared
		if cleared > g.bestCombo {
			g.bestCombo = cleared
		}
		g.score += int64(cleared*g.cfg.PointsPerRow + g.cfg.MultiRowBonus*(cleared-1))
	}

	if g.height(0) >= g.cfg.Rows {
		g.over = true
	}
	g.width = g.rng.NextInt(g.cfg.MaxWidth) + 1
	return cleared, nil
}

// clearRows removes every full row and pulls the rest down.
func (g *TowerBlocksGame) clearRows() int {
	cleared := 0
	for r := 0; r < g.cfg.Rows; {
		full := true
		for c := 0; c < g.cfg.Columns; c++ {
			if !g.grid[r][c] {
				full = false
				break
			}
		}
		if !full {
			r++
			continue
		}
		// Shift everything above down one, and blank the top row.
		for up := r; up < g.cfg.Rows-1; up++ {
			copy(g.grid[up], g.grid[up+1])
		}
		for c := 0; c < g.cfg.Columns; c++ {
			g.grid[g.cfg.Rows-1][c] = false
		}
		cleared++
		// Do not advance r: what fell into this row may also be full.
	}
	return cleared
}

// ReplayTowerBlocks replays a Tower Blocks log: the column of every drop.
func ReplayTowerBlocks(seed string, cfg TowerBlocksConfig, moves []string) (*Result, error) {
	cfg = cfg.withDefaults()
	g, err := NewTowerBlocksGame(seed, cfg)
	if err != nil {
		return nil, err
	}
	for i, m := range moves {
		fields, err := parseTickEntry(m, 1)
		if err != nil {
			return nil, fmt.Errorf("drop %d: %w", i, err)
		}
		if _, err := g.Drop(fields[0]); err != nil {
			return nil, fmt.Errorf("drop %d: %w", i, err)
		}
	}
	return &Result{
		Score:         g.score,
		MovesUsed:     len(moves),
		GameOver:      g.over,
		MinDurationMs: int64(g.pieces) * int64(cfg.MinMsPerPiece),
		Stats: map[string]int64{
			"rows":       int64(g.rows),
			"placed":     int64(g.pieces),
			"best_combo": int64(g.bestCombo),
		},
	}, nil
}

type towerBlocksEngine struct{}

func (towerBlocksEngine) Replay(seed string, raw json.RawMessage, moves []string) (*Result, error) {
	var cfg TowerBlocksConfig
	if err := decodeConfig(raw, &cfg); err != nil {
		return nil, err
	}
	return ReplayTowerBlocks(seed, cfg, moves)
}
