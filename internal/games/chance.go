package games

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
)

// Chance mechanics (Progressive Prize spec §2, §7 "Random outcomes").
//
// Unlike the skill games, nothing about a chance play is replayed from moves:
// the server decides the outcome the moment the play is made, with a
// cryptographically secure generator, and keeps the draw with the play so it
// can be audited. The app only reveals the result. The wheel, the scratch
// card and the chests are presentation, not input.

const (
	MechanicSpin     = "spin"
	MechanicScratch  = "scratch"
	MechanicTreasure = "treasure"
	MechanicInstant  = "instant"
	MechanicDraw     = "draw"
)

// ChanceAlgorithm names how every chance value is drawn, for the audit
// record.
const ChanceAlgorithm = "crypto/rand uniform integer (Go math/big rand.Int)"

var chanceGames = map[string]string{
	"spin-wheel":    MechanicSpin,
	"scratch-card":  MechanicScratch,
	"treasure-hunt": MechanicTreasure,
	"instant-win":   MechanicInstant,
	"prize-draw":    MechanicDraw,
}

// ChanceMechanic reports whether a game slug is a chance mechanic, and which.
func ChanceMechanic(slug string) (string, bool) {
	m, ok := chanceGames[slug]
	return m, ok
}

// SecureIntn returns a uniformly random integer in [0, n) from the operating
// system's secure generator. A variable so tests can make outcomes certain.
var SecureIntn = func(n int64) (int64, error) {
	if n <= 0 {
		return 0, fmt.Errorf("SecureIntn: n must be positive, got %d", n)
	}
	v, err := rand.Int(rand.Reader, big.NewInt(n))
	if err != nil {
		return 0, err
	}
	return v.Int64(), nil
}

// ChanceResult is one chance play's outcome and what it was drawn from. It
// is stored with the play, returned in the play receipt, and is everything
// the app needs to reveal it.
type ChanceResult struct {
	Mechanic  string `json:"mechanic"`
	Won       bool   `json:"won"`
	Odds      int64  `json:"odds"`
	Draw      int64  `json:"draw"` // the random value in [0, odds); 0 wins
	Algorithm string `json:"algorithm"`

	// Spin: the segment the wheel stops on; segment 0 is the jackpot.
	Segment  *int `json:"segment,omitempty"`
	Segments int  `json:"segments,omitempty"`
	// Scratch: the symbols under the nine panels. Three jackpots win.
	Cells []string `json:"cells,omitempty"`
	// Treasure: what the chosen chest holds.
	Contents string `json:"contents,omitempty"`
}

type chanceConfig struct {
	Segments int `json:"segments"`
	Cells    int `json:"cells"`
}

var scratchFillers = []string{"gift", "star", "heart", "diamond", "clover"}

// DrawInstant decides one instant-win play: it wins when a uniform draw in
// [0, odds) comes up 0, i.e. with probability exactly 1/odds. The reveal
// details are then made to match the outcome.
func DrawInstant(mechanic string, odds int64, config json.RawMessage) (*ChanceResult, error) {
	if odds < 2 {
		return nil, fmt.Errorf("win odds must be at least 2, got %d", odds)
	}
	draw, err := SecureIntn(odds)
	if err != nil {
		return nil, err
	}
	res := &ChanceResult{
		Mechanic: mechanic, Won: draw == 0, Odds: odds, Draw: draw, Algorithm: ChanceAlgorithm,
	}
	var cfg chanceConfig
	_ = json.Unmarshal(config, &cfg)

	switch mechanic {
	case MechanicSpin:
		segments := cfg.Segments
		if segments < 2 {
			segments = 8
		}
		segment := 0
		if !res.Won {
			n, err := SecureIntn(int64(segments - 1))
			if err != nil {
				return nil, err
			}
			segment = int(n) + 1
		}
		res.Segment, res.Segments = &segment, segments
	case MechanicScratch:
		cells, err := scratchCells(res.Won)
		if err != nil {
			return nil, err
		}
		res.Cells = cells
	case MechanicTreasure:
		res.Contents = "empty"
		if res.Won {
			res.Contents = "jackpot"
		}
	case MechanicInstant:
	default:
		return nil, fmt.Errorf("%s is not an instant-win mechanic", mechanic)
	}
	return res, nil
}

// scratchCells lays out nine panels: exactly three jackpots when the play
// won, at most two when it lost, the rest ordinary symbols.
func scratchCells(won bool) ([]string, error) {
	cells := make([]string, 9)
	for i := range cells {
		n, err := SecureIntn(int64(len(scratchFillers)))
		if err != nil {
			return nil, err
		}
		cells[i] = scratchFillers[n]
	}
	jackpots := 3
	if !won {
		n, err := SecureIntn(3) // 0, 1 or 2 near misses
		if err != nil {
			return nil, err
		}
		jackpots = int(n)
	}
	// Place the jackpots on distinct panels (a partial Fisher–Yates).
	idx := []int{0, 1, 2, 3, 4, 5, 6, 7, 8}
	for i := 0; i < jackpots; i++ {
		j, err := SecureIntn(int64(len(idx) - i))
		if err != nil {
			return nil, err
		}
		k := i + int(j)
		idx[i], idx[k] = idx[k], idx[i]
		cells[idx[i]] = "jackpot"
	}
	return cells, nil
}

// DrawEntry is one entry into a prize draw.
type DrawEntry struct {
	PlayID     string `json:"play_id"`
	CustomerID string `json:"customer_id"`
}

// DrawPick is one entry the draw selected, and whether it became a winner.
type DrawPick struct {
	Order      int    `json:"order"`
	EntryIndex int    `json:"entry_index"`
	PlayID     string `json:"play_id"`
	CustomerID string `json:"customer_id"`
	Outcome    string `json:"outcome"` // winner, skipped_repeat or skipped_ineligible
	Reason     string `json:"reason,omitempty"`
}

// RunDraw picks up to winners distinct customers from the entries, in draw
// order, sampling entries without replacement. Every random value used is
// returned so the draw can be re-checked against the stored entry list.
// eligible is asked about each newly drawn customer; an ineligible one is
// recorded and the draw moves on, never re-drawn in their favour.
func RunDraw(entries []DrawEntry, winners int, eligible func(customerID string) (bool, string)) (picks []DrawPick, randoms []int64, err error) {
	pool := make([]int, len(entries))
	for i := range pool {
		pool[i] = i
	}
	seen := map[string]bool{}
	chosen := 0
	for n := len(pool); n > 0 && chosen < winners; n-- {
		r, err := SecureIntn(int64(n))
		if err != nil {
			return nil, nil, err
		}
		randoms = append(randoms, r)
		// Swap the drawn entry to the end of the live pool.
		i := int(r)
		pool[i], pool[n-1] = pool[n-1], pool[i]
		e := entries[pool[n-1]]
		pick := DrawPick{Order: len(picks) + 1, EntryIndex: pool[n-1], PlayID: e.PlayID, CustomerID: e.CustomerID}
		switch {
		case seen[e.CustomerID]:
			pick.Outcome = "skipped_repeat"
		default:
			seen[e.CustomerID] = true
			if ok, reason := eligible(e.CustomerID); !ok {
				pick.Outcome, pick.Reason = "skipped_ineligible", reason
			} else {
				pick.Outcome = "winner"
				chosen++
			}
		}
		picks = append(picks, pick)
	}
	return picks, randoms, nil
}

// DrawEntriesHash fingerprints the ordered entry list a draw was made from,
// so the stored draw can be checked against the plays in the database.
func DrawEntriesHash(entries []DrawEntry) string {
	h := sha256.New()
	for _, e := range entries {
		fmt.Fprintf(h, "%s:%s\n", e.PlayID, e.CustomerID)
	}
	return hex.EncodeToString(h.Sum(nil))
}
