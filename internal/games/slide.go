package games

import (
	"encoding/json"
	"fmt"
)

// SlideSlug identifies the sliding puzzle in the catalog and the registry.
const SlideSlug = "slide-puzzle"

// SlideConfig is the sliding-puzzle rule set.
type SlideConfig struct {
	Size              int `json:"size"`
	ShuffleMoves      int `json:"shuffle_moves"`
	SolveBase         int `json:"solve_base"`
	MovePenalty       int `json:"move_penalty"`
	SolvedMinScore    int `json:"solved_min_score"`
	MinMsPerMove      int `json:"min_ms_per_move"`
	MaxMoves          int `json:"max_moves"`
	SessionTTLSeconds int `json:"session_ttl_seconds"`
}

// DefaultSlideConfig mirrors the 1.0.0 version seeded by migration 000028.
func DefaultSlideConfig() SlideConfig {
	return SlideConfig{
		Size:              3,
		ShuffleMoves:      80,
		SolveBase:         5000,
		MovePenalty:       20,
		SolvedMinScore:    1000,
		MinMsPerMove:      80,
		MaxMoves:          3000,
		SessionTTLSeconds: defaultSessionTTLSeconds,
	}
}

func (c SlideConfig) withDefaults() SlideConfig {
	d := DefaultSlideConfig()
	if c.Size < 2 {
		c.Size = d.Size
	}
	if c.ShuffleMoves <= 0 {
		c.ShuffleMoves = d.ShuffleMoves
	}
	if c.SolveBase <= 0 {
		c.SolveBase = d.SolveBase
	}
	if c.MovePenalty < 0 {
		c.MovePenalty = 0
	}
	if c.SolvedMinScore < 0 {
		c.SolvedMinScore = 0
	}
	if c.MinMsPerMove < 0 {
		c.MinMsPerMove = 0
	}
	if c.MaxMoves <= 0 {
		c.MaxMoves = d.MaxMoves
	}
	return c
}

// SlidePuzzle is the classic sliding-tile puzzle.
//
// It is perfect-information: every tile is visible from the first move, so the
// result depends only on the player's reasoning. The scramble is produced by
// playing legal moves backwards from the solved board, which guarantees every
// seed is solvable.
//
// Moves name the direction a TILE travels into the gap: "up" slides the tile
// below the gap upwards. The Dart implementation mirrors this file exactly.
type SlidePuzzle struct {
	cfg   SlideConfig
	board []int // row-major; 0 is the gap
	blank int
	moves int
}

// NewSlidePuzzle builds the scrambled board for a seed.
func NewSlidePuzzle(seed string, cfg SlideConfig) (*SlidePuzzle, error) {
	state, err := ParseSeed(seed)
	if err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()

	n := cfg.Size * cfg.Size
	p := &SlidePuzzle{cfg: cfg, board: make([]int, n), blank: n - 1}
	for i := 0; i < n-1; i++ {
		p.board[i] = i + 1
	}

	rng := NewRNG(state)
	prev := ""
	// Keep going past shuffle_moves if the scramble happened to land solved.
	for i := 0; i < cfg.ShuffleMoves || p.Solved(); i++ {
		legal := make([]string, 0, 4)
		for _, d := range []string{MoveUp, MoveDown, MoveLeft, MoveRight} {
			// Never undo the previous scramble move; it wastes the step.
			if d != opposite(prev) && p.canMove(d) {
				legal = append(legal, d)
			}
		}
		d := legal[rng.NextInt(len(legal))]
		p.slide(d)
		prev = d
	}
	return p, nil
}

// Board returns a copy of the flat row-major board.
func (p *SlidePuzzle) Board() []int {
	out := make([]int, len(p.board))
	copy(out, p.board)
	return out
}

func (p *SlidePuzzle) Moves() int { return p.moves }

// Solved reports whether every tile is home and the gap is last.
func (p *SlidePuzzle) Solved() bool {
	last := len(p.board) - 1
	for i := 0; i < last; i++ {
		if p.board[i] != i+1 {
			return false
		}
	}
	return p.board[last] == 0
}

// TilesInPlace counts tiles already in their home position.
func (p *SlidePuzzle) TilesInPlace() int {
	count := 0
	for i := 0; i < len(p.board)-1; i++ {
		if p.board[i] == i+1 {
			count++
		}
	}
	return count
}

// Score only rewards a solved board: fewer moves, more points.
func (p *SlidePuzzle) Score() int64 {
	if !p.Solved() {
		return 0
	}
	s := p.cfg.SolveBase - p.cfg.MovePenalty*p.moves
	if s < p.cfg.SolvedMinScore {
		s = p.cfg.SolvedMinScore
	}
	return int64(s)
}

// tileFor is the cell holding the tile that would travel in dir, or -1.
func (p *SlidePuzzle) tileFor(dir string) int {
	size := p.cfg.Size
	dx, dy := delta(dir)
	// The tile sits on the far side of the gap from where it is heading.
	tx, ty := p.blank%size-dx, p.blank/size-dy
	if tx < 0 || ty < 0 || tx >= size || ty >= size {
		return -1
	}
	return ty*size + tx
}

func (p *SlidePuzzle) canMove(dir string) bool { return p.tileFor(dir) >= 0 }

func (p *SlidePuzzle) slide(dir string) {
	t := p.tileFor(dir)
	p.board[p.blank] = p.board[t]
	p.board[t] = 0
	p.blank = t
}

// Move slides one tile. Unlike 2048, an impossible move is never something an
// honest client sends — it only offers tiles next to the gap — so it is an
// error rather than a no-op.
func (p *SlidePuzzle) Move(dir string) error {
	if !isDirection(dir) {
		return fmt.Errorf("%w: %q", ErrInvalidMove, dir)
	}
	if p.Solved() {
		return fmt.Errorf("%w: move after the puzzle was solved", ErrInvalidMove)
	}
	if !p.canMove(dir) {
		return fmt.Errorf("%w: no tile can slide %s", ErrInvalidMove, dir)
	}
	p.slide(dir)
	p.moves++
	return nil
}

// ReplaySlide replays a sliding-puzzle move log.
func ReplaySlide(seed string, cfg SlideConfig, moves []string) (*Result, error) {
	cfg = cfg.withDefaults()
	if len(moves) > cfg.MaxMoves {
		return nil, fmt.Errorf("%w: move log has %d moves, limit is %d", ErrInvalidMove, len(moves), cfg.MaxMoves)
	}

	p, err := NewSlidePuzzle(seed, cfg)
	if err != nil {
		return nil, err
	}
	for i, m := range moves {
		if err := p.Move(m); err != nil {
			return nil, fmt.Errorf("move %d: %w", i, err)
		}
	}

	solved := p.Solved()
	return &Result{
		Score:         p.Score(),
		MovesUsed:     p.moves,
		Won:           solved,
		GameOver:      solved,
		MinDurationMs: int64(p.moves) * int64(cfg.MinMsPerMove),
		Stats: map[string]int64{
			"moves":          int64(p.moves),
			"tiles_in_place": int64(p.TilesInPlace()),
		},
	}, nil
}

type slideEngine struct{}

func (slideEngine) Replay(seed string, raw json.RawMessage, moves []string) (*Result, error) {
	var cfg SlideConfig
	if err := decodeConfig(raw, &cfg); err != nil {
		return nil, err
	}
	return ReplaySlide(seed, cfg, moves)
}
