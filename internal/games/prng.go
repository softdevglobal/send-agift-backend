package games

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
)

// DeterministicRNG is a 32-bit linear congruential generator.
//
// Why not math/rand: the Flutter client has to produce the EXACT same number
// sequence as this backend. If it did not, the tiles the player actually saw
// would differ from the ones the server replays, and every honest score would
// be rejected. Standard library generators are not specified across languages,
// so both sides implement this same short algorithm instead.
//
// The constants are the Numerical Recipes LCG. They are chosen so the
// multiplication stays below 2^53 (1664525 * 2^32 ≈ 7.15e15), which means Dart
// on the web — where int is a float64 under the hood — still reproduces it
// exactly. Do not change these constants without shipping a new game version:
// every previously recorded session replays against them.
type DeterministicRNG struct {
	state uint32
}

const (
	lcgMultiplier = 1664525
	lcgIncrement  = 1013904223
)

// NewRNG starts a generator at the given state.
func NewRNG(seed uint32) *DeterministicRNG {
	return &DeterministicRNG{state: seed}
}

// Next advances the generator and returns the new 32-bit state.
// Dart equivalent: state = (state * 1664525 + 1013904223) & 0xFFFFFFFF;
func (r *DeterministicRNG) Next() uint32 {
	// uint32 overflow wraps, which is the same as masking with 0xFFFFFFFF.
	r.state = r.state*lcgMultiplier + lcgIncrement
	return r.state
}

// NextInt returns a value in [0, n). Modulo bias is irrelevant here because
// fairness comes from every player receiving the same seed, not from the
// distribution being perfectly uniform.
func (r *DeterministicRNG) NextInt(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.Next() % uint32(n))
}

// NewSeed generates a fresh 8-character hex seed for a new session.
// Seeds are server-generated so a client can never choose a favourable board.
func NewSeed() (string, error) {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate seed: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// ParseSeed converts the stored hex seed back into generator state.
func ParseSeed(seed string) (uint32, error) {
	v, err := strconv.ParseUint(seed, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid seed %q: %w", seed, err)
	}
	return uint32(v), nil
}
