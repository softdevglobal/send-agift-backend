package games

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Result is the authoritative outcome of replaying one round of any game.
// The service stores this, never anything the client claims.
type Result struct {
	Score     int64
	MovesUsed int
	NoOpMoves int
	Won       bool
	GameOver  bool
	// MinDurationMs is the fastest a person could have produced this round.
	// A submission that arrives sooner than this was not played by hand.
	MinDurationMs int64
	// Stats are game-specific details for the result screen, e.g. the highest
	// 2048 tile or the snake's final length.
	Stats map[string]int64
}

// Engine replays a submitted move log from the server-issued seed.
//
// Every game has to be deterministic from its seed and fully replayable, or
// it cannot be scored server-side and does not belong in this registry.
type Engine interface {
	Replay(seed string, config json.RawMessage, moves []string) (*Result, error)
}

var engines = map[string]Engine{
	Slug2048:       engine2048{},
	SnakeSlug:      snakeEngine{},
	SlideSlug:      slideEngine{},
	BasketballSlug: basketballEngine{},
	StackSlug:      stackEngine{},
	ArcherySlug:    archeryEngine{},
	BlockBlastSlug: blockBlastEngine{},
	CricketSlug:    cricketEngine{},
	SlingSlug:      slingEngine{},
	HillSlug:       hillEngine{},
}

// EngineFor returns the replay engine for a game slug.
func EngineFor(slug string) (Engine, bool) {
	e, ok := engines[slug]
	return e, ok
}

const defaultSessionTTLSeconds = 3600

// SessionTTLSeconds reads the session_ttl_seconds field every game config
// carries, so the service can open a session without knowing the game.
func SessionTTLSeconds(config json.RawMessage) int {
	var base struct {
		SessionTTLSeconds int `json:"session_ttl_seconds"`
	}
	_ = json.Unmarshal(config, &base)
	if base.SessionTTLSeconds <= 0 {
		return defaultSessionTTLSeconds
	}
	return base.SessionTTLSeconds
}

func decodeConfig(raw json.RawMessage, into any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("invalid game config: %w", err)
	}
	return nil
}

// opposite is the reverse of a direction; "" for anything else.
func opposite(dir string) string {
	switch dir {
	case MoveUp:
		return MoveDown
	case MoveDown:
		return MoveUp
	case MoveLeft:
		return MoveRight
	case MoveRight:
		return MoveLeft
	}
	return ""
}

// delta is the column/row step for a direction.
func delta(dir string) (dx, dy int) {
	switch dir {
	case MoveUp:
		return 0, -1
	case MoveDown:
		return 0, 1
	case MoveLeft:
		return -1, 0
	case MoveRight:
		return 1, 0
	}
	return 0, 0
}

func isDirection(dir string) bool { return opposite(dir) != "" }

// triangle is a whole-number triangle wave: it starts at -amp when u = 0,
// climbs to +amp at half the period and falls back again. Anything that moves
// on its own in a game (a hoop, a sliding block, a swaying sight) follows it.
//
// Integer-only on purpose: floating point can round differently between Go
// and Dart (Go may fuse multiply-adds on some CPUs), and the client and the
// server must agree on every position exactly. u must be non-negative and
// period even, so every division here is of non-negative numbers — Go and
// Dart truncate those identically.
func triangle(u, period, amp int) int {
	half := period / 2
	m := u % period
	if m <= half {
		return -amp + 2*amp*m/half
	}
	return amp - 2*amp*(m-half)/half
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// evenAtLeast returns v rounded up to an even number, and no smaller than
// min. Wave periods must be even so a half period is a whole tick count.
func evenAtLeast(v, min int) int {
	if v < min {
		v = min
	}
	if v%2 != 0 {
		v++
	}
	return v
}

// parseTickEntry splits "<tick>:<a>:<b>..." into integers. The tick must be
// non-negative; the other fields may be negative.
func parseTickEntry(entry string, fields int) ([]int, error) {
	parts := strings.Split(entry, ":")
	if len(parts) != fields {
		return nil, fmt.Errorf("%w: %q should have %d parts", ErrInvalidMove, entry, fields)
	}
	out := make([]int, fields)
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("%w: %q is not a number in %q", ErrInvalidMove, p, entry)
		}
		out[i] = v
	}
	if out[0] < 0 {
		return nil, fmt.Errorf("%w: negative tick in %q", ErrInvalidMove, entry)
	}
	return out, nil
}

// tickFloorMs is the least real time a round that reached lastTick can have
// taken. Client clocks only ever run late, so the 20% allowance absorbs a
// janky frame without letting a sped-up client through.
func tickFloorMs(lastTick, tickMs int) int64 {
	if lastTick <= 0 {
		return 0
	}
	return int64(lastTick) * int64(tickMs) * 8 / 10
}

// ─── 2048 adapter ─────────────────────────────────────────────────────────

const Slug2048 = "2048"

type engine2048 struct{}

func (engine2048) Replay(seed string, raw json.RawMessage, moves []string) (*Result, error) {
	var cfg Config2048
	if err := decodeConfig(raw, &cfg); err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()

	r, err := Replay2048(seed, cfg, moves)
	if err != nil {
		return nil, err
	}
	return &Result{
		Score:         r.Score,
		MovesUsed:     r.MovesUsed,
		NoOpMoves:     r.NoOpMoves,
		Won:           r.Won,
		GameOver:      r.GameOver,
		MinDurationMs: int64(r.MovesUsed) * int64(cfg.MinMsPerMove),
		Stats:         map[string]int64{"highest_tile": int64(r.HighestTile)},
	}, nil
}
