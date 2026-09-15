package games

import (
	"encoding/json"
	"fmt"
)

// BlockBlastSlug identifies Block Blast in the catalog and the engine registry.
const BlockBlastSlug = "block-blast"

// BlockBlastConfig is the Block Blast rule set, versioned like every game.
type BlockBlastConfig struct {
	BoardSize         int `json:"board_size"`
	HandSize          int `json:"hand_size"`
	PointsPerCell     int `json:"points_per_cell"`
	PointsPerLine     int `json:"points_per_line"`
	ComboBonus        int `json:"combo_bonus"`
	MinMsPerMove      int `json:"min_ms_per_move"`
	MaxMoves          int `json:"max_moves"`
	SessionTTLSeconds int `json:"session_ttl_seconds"`
}

// DefaultBlockBlastConfig mirrors the 1.0.0 version seeded by migration 000031.
func DefaultBlockBlastConfig() BlockBlastConfig {
	return BlockBlastConfig{
		BoardSize:         8,
		HandSize:          3,
		PointsPerCell:     1,
		PointsPerLine:     10,
		ComboBonus:        5,
		MinMsPerMove:      250,
		MaxMoves:          3000,
		SessionTTLSeconds: defaultSessionTTLSeconds,
	}
}

func (c BlockBlastConfig) withDefaults() BlockBlastConfig {
	d := DefaultBlockBlastConfig()
	fill := func(v *int, def int) {
		if *v <= 0 {
			*v = def
		}
	}
	fill(&c.HandSize, d.HandSize)
	fill(&c.PointsPerCell, d.PointsPerCell)
	fill(&c.PointsPerLine, d.PointsPerLine)
	fill(&c.ComboBonus, d.ComboBonus)
	fill(&c.MinMsPerMove, d.MinMsPerMove)
	fill(&c.MaxMoves, d.MaxMoves)
	if c.BoardSize < 5 || c.BoardSize > 12 {
		c.BoardSize = d.BoardSize
	}
	return c
}

// BlockShapes is the fixed piece catalog; a piece's id is its index. Cells
// are {row, col}. The order is part of the rules — the seed picks pieces by
// index — so never reorder it without shipping a new game version.
var BlockShapes = [][][2]int{
	{{0, 0}},
	{{0, 0}, {0, 1}},
	{{0, 0}, {1, 0}},
	{{0, 0}, {0, 1}, {0, 2}},
	{{0, 0}, {1, 0}, {2, 0}},
	{{0, 0}, {0, 1}, {0, 2}, {0, 3}},
	{{0, 0}, {1, 0}, {2, 0}, {3, 0}},
	{{0, 0}, {0, 1}, {0, 2}, {0, 3}, {0, 4}},
	{{0, 0}, {1, 0}, {2, 0}, {3, 0}, {4, 0}},
	{{0, 0}, {0, 1}, {1, 0}, {1, 1}},
	{{0, 0}, {0, 1}, {0, 2}, {1, 0}, {1, 1}, {1, 2}, {2, 0}, {2, 1}, {2, 2}},
	{{0, 0}, {1, 0}, {1, 1}},
	{{0, 0}, {0, 1}, {1, 0}},
	{{0, 0}, {0, 1}, {1, 1}},
	{{0, 1}, {1, 0}, {1, 1}},
	{{0, 0}, {1, 0}, {2, 0}, {2, 1}},
	{{0, 1}, {1, 1}, {2, 1}, {2, 0}},
	{{0, 0}, {0, 1}, {0, 2}, {1, 0}},
	{{0, 0}, {0, 1}, {0, 2}, {1, 2}},
	{{0, 0}, {0, 1}, {0, 2}, {1, 1}},
	{{0, 1}, {1, 0}, {1, 1}, {1, 2}},
	{{0, 1}, {0, 2}, {1, 0}, {1, 1}},
	{{0, 0}, {0, 1}, {1, 1}, {1, 2}},
	{{0, 0}, {1, 0}, {2, 0}, {2, 1}, {2, 2}},
}

// BlockBlastMove is the outcome of one placement.
type BlockBlastMove struct {
	Points int
	Lines  int
}

// BlockBlastGame is Block Blast: place pieces from a hand of three on the
// board; a full row or column clears. When the hand is empty a new one is
// dealt from the seed — like 2048's tile spawns, identical for everyone who
// shares the seed. The game ends when nothing in the hand fits.
//
// The Dart implementation in the mobile app mirrors this file exactly.
type BlockBlastGame struct {
	cfg       BlockBlastConfig
	rng       *DeterministicRNG
	board     []int // 0 empty, otherwise piece id + 1
	hand      []int // -1 once placed
	score     int64
	lines     int
	combo     int
	bestCombo int
	placed    int
	over      bool
}

// NewBlockBlastGame deals the first hand from the seed.
func NewBlockBlastGame(seed string, cfg BlockBlastConfig) (*BlockBlastGame, error) {
	state, err := ParseSeed(seed)
	if err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()
	g := &BlockBlastGame{
		cfg:   cfg,
		rng:   NewRNG(state),
		board: make([]int, cfg.BoardSize*cfg.BoardSize),
		hand:  make([]int, cfg.HandSize),
	}
	g.deal()
	return g, nil
}

func (g *BlockBlastGame) deal() {
	for i := range g.hand {
		g.hand[i] = g.rng.NextInt(len(BlockShapes))
	}
}

func (g *BlockBlastGame) Score() int64 { return g.score }
func (g *BlockBlastGame) Over() bool   { return g.over }

// Hand returns a copy of the hand; -1 marks a used slot.
func (g *BlockBlastGame) Hand() []int {
	out := make([]int, len(g.hand))
	copy(out, g.hand)
	return out
}

// Fits reports whether a piece can go with its top-left cell at (row, col).
func (g *BlockBlastGame) Fits(piece, row, col int) bool {
	n := g.cfg.BoardSize
	for _, cell := range BlockShapes[piece] {
		r, c := row+cell[0], col+cell[1]
		if r < 0 || c < 0 || r >= n || c >= n || g.board[r*n+c] != 0 {
			return false
		}
	}
	return true
}

func (g *BlockBlastGame) anyFits() bool {
	n := g.cfg.BoardSize
	for _, piece := range g.hand {
		if piece < 0 {
			continue
		}
		for r := 0; r < n; r++ {
			for c := 0; c < n; c++ {
				if g.Fits(piece, r, c) {
					return true
				}
			}
		}
	}
	return false
}

// Place puts the piece in a hand slot on the board.
func (g *BlockBlastGame) Place(slot, row, col int) (BlockBlastMove, error) {
	if g.over {
		return BlockBlastMove{}, fmt.Errorf("%w: placement after game over", ErrInvalidMove)
	}
	if slot < 0 || slot >= len(g.hand) || g.hand[slot] < 0 {
		return BlockBlastMove{}, fmt.Errorf("%w: slot %d has no piece", ErrInvalidMove, slot)
	}
	piece := g.hand[slot]
	if !g.Fits(piece, row, col) {
		return BlockBlastMove{}, fmt.Errorf("%w: piece %d does not fit at (%d,%d)", ErrInvalidMove, piece, row, col)
	}

	n := g.cfg.BoardSize
	for _, cell := range BlockShapes[piece] {
		g.board[(row+cell[0])*n+col+cell[1]] = piece + 1
	}
	points := len(BlockShapes[piece]) * g.cfg.PointsPerCell

	// Full rows and columns are found first and cleared together, so a
	// placement that completes a row and a column clears both.
	clear := make([]bool, n*n)
	lines := 0
	for r := 0; r < n; r++ {
		full := true
		for c := 0; c < n && full; c++ {
			full = g.board[r*n+c] != 0
		}
		if full {
			lines++
			for c := 0; c < n; c++ {
				clear[r*n+c] = true
			}
		}
	}
	for c := 0; c < n; c++ {
		full := true
		for r := 0; r < n && full; r++ {
			full = g.board[r*n+c] != 0
		}
		if full {
			lines++
			for r := 0; r < n; r++ {
				clear[r*n+c] = true
			}
		}
	}
	for i, cleared := range clear {
		if cleared {
			g.board[i] = 0
		}
	}

	if lines > 0 {
		g.combo++
		points += g.cfg.PointsPerLine * lines * (lines + 1) / 2
		if g.combo > 1 {
			points += g.cfg.ComboBonus * (g.combo - 1)
		}
		if g.combo > g.bestCombo {
			g.bestCombo = g.combo
		}
	} else {
		g.combo = 0
	}

	g.score += int64(points)
	g.lines += lines
	g.placed++
	g.hand[slot] = -1

	empty := true
	for _, p := range g.hand {
		if p >= 0 {
			empty = false
		}
	}
	if empty {
		g.deal()
	}
	if !g.anyFits() {
		g.over = true
	}
	return BlockBlastMove{Points: points, Lines: lines}, nil
}

// ReplayBlockBlast replays a Block Blast log: one "<slot>:<row>:<col>" per
// placement.
func ReplayBlockBlast(seed string, cfg BlockBlastConfig, moves []string) (*Result, error) {
	cfg = cfg.withDefaults()
	if len(moves) > cfg.MaxMoves {
		return nil, fmt.Errorf("%w: %d placements exceeds the %d limit", ErrInvalidMove, len(moves), cfg.MaxMoves)
	}
	g, err := NewBlockBlastGame(seed, cfg)
	if err != nil {
		return nil, err
	}
	for i, m := range moves {
		v, err := parseTickEntry(m, 3)
		if err != nil {
			return nil, err
		}
		if _, err := g.Place(v[0], v[1], v[2]); err != nil {
			return nil, fmt.Errorf("placement %d: %w", i, err)
		}
	}
	return &Result{
		Score:         g.score,
		MovesUsed:     g.placed,
		GameOver:      g.over,
		MinDurationMs: int64(g.placed) * int64(cfg.MinMsPerMove),
		Stats: map[string]int64{
			"lines":      int64(g.lines),
			"best_combo": int64(g.bestCombo),
			"placed":     int64(g.placed),
		},
	}, nil
}

type blockBlastEngine struct{}

func (blockBlastEngine) Replay(seed string, raw json.RawMessage, moves []string) (*Result, error) {
	var cfg BlockBlastConfig
	if err := decodeConfig(raw, &cfg); err != nil {
		return nil, err
	}
	return ReplayBlockBlast(seed, cfg, moves)
}
