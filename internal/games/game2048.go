package games

import (
	"errors"
	"fmt"
)

// Move directions. These exact lowercase strings are the wire format between
// the app and this backend.
const (
	MoveUp    = "up"
	MoveDown  = "down"
	MoveLeft  = "left"
	MoveRight = "right"
)

// ErrInvalidMove is returned when a submitted move log contains something the
// engine cannot interpret.
var ErrInvalidMove = errors.New("invalid move")

// Config holds the rules both the server and the client run. It is stored per
// game version and snapshotted onto each session, so editing a version can
// never change how an already-started game is scored.
type Config struct {
	BoardSize         int `json:"board_size"`
	StartTiles        int `json:"start_tiles"`
	SpawnFourPercent  int `json:"spawn_four_percent"`
	WinTile           int `json:"win_tile"`
	MaxMoves          int `json:"max_moves"`
	MinMsPerMove      int `json:"min_ms_per_move"`
	SessionTTLSeconds int `json:"session_ttl_seconds"`
}

// DefaultConfig mirrors the 1.0.0 version seeded by migration 000027.
func DefaultConfig() Config {
	return Config{
		BoardSize:         4,
		StartTiles:        2,
		SpawnFourPercent:  10,
		WinTile:           2048,
		MaxMoves:          5000,
		MinMsPerMove:      40,
		SessionTTLSeconds: 3600,
	}
}

// withDefaults fills zero values so a partially-populated config from the
// database still produces a playable game rather than a divide-by-zero board.
func (c Config) withDefaults() Config {
	d := DefaultConfig()
	if c.BoardSize <= 0 {
		c.BoardSize = d.BoardSize
	}
	if c.StartTiles <= 0 {
		c.StartTiles = d.StartTiles
	}
	if c.SpawnFourPercent <= 0 {
		c.SpawnFourPercent = d.SpawnFourPercent
	}
	if c.WinTile <= 0 {
		c.WinTile = d.WinTile
	}
	if c.MaxMoves <= 0 {
		c.MaxMoves = d.MaxMoves
	}
	if c.SessionTTLSeconds <= 0 {
		c.SessionTTLSeconds = d.SessionTTLSeconds
	}
	return c
}

// Game2048 is the deterministic 2048 engine.
//
// The Dart implementation in the mobile app must match this file move for
// move: same slide order, same merge rule, same spawn order (position drawn
// before value). Any divergence shows up as a score mismatch on submit.
type Game2048 struct {
	cfg   Config
	rng   *DeterministicRNG
	board []int // flat, row-major: board[row*size+col]
	score int64
}

// NewGame2048 builds a fresh game from a server seed and places the starting
// tiles, exactly as the client does when it receives the same seed.
func NewGame2048(seed string, cfg Config) (*Game2048, error) {
	state, err := ParseSeed(seed)
	if err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()

	g := &Game2048{
		cfg:   cfg,
		rng:   NewRNG(state),
		board: make([]int, cfg.BoardSize*cfg.BoardSize),
	}
	for i := 0; i < cfg.StartTiles; i++ {
		g.spawn()
	}
	return g, nil
}

// Score is the running score: the sum of every merged tile value.
func (g *Game2048) Score() int64 { return g.score }

// Board returns a copy of the flat row-major board.
func (g *Game2048) Board() []int {
	out := make([]int, len(g.board))
	copy(out, g.board)
	return out
}

// HighestTile is the largest tile currently on the board.
func (g *Game2048) HighestTile() int {
	best := 0
	for _, v := range g.board {
		if v > best {
			best = v
		}
	}
	return best
}

// Won reports whether the win tile has been reached.
func (g *Game2048) Won() bool { return g.HighestTile() >= g.cfg.WinTile }

// spawn places one new tile on a random empty cell.
//
// Order matters and is part of the wire contract: the position is drawn FIRST,
// then the value. Swapping these two lines in either implementation desyncs
// every game.
func (g *Game2048) spawn() {
	empties := make([]int, 0, len(g.board))
	for i, v := range g.board {
		if v == 0 {
			empties = append(empties, i)
		}
	}
	if len(empties) == 0 {
		return
	}
	cell := empties[g.rng.NextInt(len(empties))]
	value := 2
	if g.rng.NextInt(100) < g.cfg.SpawnFourPercent {
		value = 4
	}
	g.board[cell] = value
}

// line reads one row or column in the direction of travel, so every direction
// can reuse the same "slide towards index 0" logic.
func (g *Game2048) line(dir string, index int) []int {
	size := g.cfg.BoardSize
	out := make([]int, size)
	for i := 0; i < size; i++ {
		switch dir {
		case MoveLeft:
			out[i] = g.board[index*size+i]
		case MoveRight:
			out[i] = g.board[index*size+(size-1-i)]
		case MoveUp:
			out[i] = g.board[i*size+index]
		case MoveDown:
			out[i] = g.board[(size-1-i)*size+index]
		}
	}
	return out
}

// writeLine puts a processed line back where it came from.
func (g *Game2048) writeLine(dir string, index int, values []int) {
	size := g.cfg.BoardSize
	for i := 0; i < size; i++ {
		switch dir {
		case MoveLeft:
			g.board[index*size+i] = values[i]
		case MoveRight:
			g.board[index*size+(size-1-i)] = values[i]
		case MoveUp:
			g.board[i*size+index] = values[i]
		case MoveDown:
			g.board[(size-1-i)*size+index] = values[i]
		}
	}
}

// collapse slides a single line towards index 0 and merges equal neighbours.
// Each tile may merge at most once per move, and merging is resolved from the
// leading edge inwards — the standard 2048 rule.
func collapse(in []int, score *int64) []int {
	size := len(in)

	// 1. Drop the gaps.
	packed := make([]int, 0, size)
	for _, v := range in {
		if v != 0 {
			packed = append(packed, v)
		}
	}

	// 2. Merge equal neighbours once, moving away from the leading edge.
	merged := make([]int, 0, size)
	for i := 0; i < len(packed); i++ {
		if i+1 < len(packed) && packed[i] == packed[i+1] {
			sum := packed[i] * 2
			merged = append(merged, sum)
			*score += int64(sum)
			i++ // the consumed neighbour cannot merge again this move
			continue
		}
		merged = append(merged, packed[i])
	}

	// 3. Pad back to full width.
	for len(merged) < size {
		merged = append(merged, 0)
	}
	return merged
}

// Move applies one swipe and reports whether the board actually changed.
// A new tile is spawned only when something moved, which is what keeps the
// client and server RNG streams aligned.
func (g *Game2048) Move(dir string) (bool, error) {
	switch dir {
	case MoveUp, MoveDown, MoveLeft, MoveRight:
	default:
		return false, fmt.Errorf("%w: %q", ErrInvalidMove, dir)
	}

	changed := false
	for i := 0; i < g.cfg.BoardSize; i++ {
		before := g.line(dir, i)
		after := collapse(before, &g.score)
		for j := range before {
			if before[j] != after[j] {
				changed = true
				break
			}
		}
		g.writeLine(dir, i, after)
	}

	if changed {
		g.spawn()
	}
	return changed, nil
}

// HasMoves reports whether any legal move remains: either an empty cell, or
// two equal neighbours that could still merge.
func (g *Game2048) HasMoves() bool {
	size := g.cfg.BoardSize
	for i, v := range g.board {
		if v == 0 {
			return true
		}
		row, col := i/size, i%size
		if col+1 < size && g.board[i+1] == v {
			return true
		}
		if row+1 < size && g.board[i+size] == v {
			return true
		}
	}
	return false
}

// ReplayResult is what the server derives by re-playing a submitted move log.
// Score here is the only score that counts; whatever the client claimed is
// treated as an unverified hint.
type ReplayResult struct {
	Score       int64
	HighestTile int
	MovesUsed   int // moves that actually changed the board
	NoOpMoves   int // moves that changed nothing; a divergence signal
	Won         bool
	GameOver    bool
	FinalBoard  []int
}

// Replay re-runs a whole game from its seed and move log and returns the
// authoritative outcome.
//
// This is the anti-cheat core: the client never sends a score we trust, it
// sends the moves it made, and we compute the result ourselves. A modified app
// cannot invent a score without also inventing a move sequence that genuinely
// produces it under these rules.
func Replay(seed string, cfg Config, moves []string) (*ReplayResult, error) {
	cfg = cfg.withDefaults()

	if len(moves) > cfg.MaxMoves {
		return nil, fmt.Errorf("%w: move log has %d moves, limit is %d", ErrInvalidMove, len(moves), cfg.MaxMoves)
	}

	g, err := NewGame2048(seed, cfg)
	if err != nil {
		return nil, err
	}

	res := &ReplayResult{}
	for i, m := range moves {
		// Moves sent after the board is already dead cannot have happened.
		if !g.HasMoves() {
			return nil, fmt.Errorf("%w: move %d played after game over", ErrInvalidMove, i)
		}
		changed, err := g.Move(m)
		if err != nil {
			return nil, fmt.Errorf("move %d: %w", i, err)
		}
		if changed {
			res.MovesUsed++
		} else {
			// The real client's board would not have changed either, so it had
			// no reason to send this. Harmless to score, but worth counting.
			res.NoOpMoves++
		}
	}

	res.Score = g.Score()
	res.HighestTile = g.HighestTile()
	res.Won = g.Won()
	res.GameOver = !g.HasMoves()
	res.FinalBoard = g.Board()
	return res, nil
}
