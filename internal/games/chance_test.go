package games

import (
	"encoding/json"
	"testing"
)

func withDraws(t *testing.T, values ...int64) {
	t.Helper()
	orig := SecureIntn
	i := 0
	SecureIntn = func(n int64) (int64, error) {
		if i < len(values) {
			v := values[i] % n
			i++
			return v, nil
		}
		return 0, nil
	}
	t.Cleanup(func() { SecureIntn = orig })
}

func TestSecureIntnStaysInRange(t *testing.T) {
	seen := map[int64]bool{}
	for i := 0; i < 2000; i++ {
		v, err := SecureIntn(5)
		if err != nil || v < 0 || v >= 5 {
			t.Fatalf("SecureIntn(5) = %d, %v", v, err)
		}
		seen[v] = true
	}
	if len(seen) != 5 {
		t.Fatalf("2000 draws should hit all five values, saw %v", seen)
	}
	if _, err := SecureIntn(0); err == nil {
		t.Fatal("n = 0 must be an error")
	}
}

func TestDrawInstantWinsOnlyOnZero(t *testing.T) {
	withDraws(t, 0)
	res, err := DrawInstant(MechanicSpin, 10, json.RawMessage(`{"segments": 6}`))
	if err != nil || !res.Won || *res.Segment != 0 || res.Segments != 6 {
		t.Fatalf("a zero draw wins and lands on the jackpot segment: %+v %v", res, err)
	}
	withDraws(t, 7, 3)
	res, _ = DrawInstant(MechanicSpin, 10, nil)
	if res.Won || *res.Segment == 0 || res.Draw != 7 {
		t.Fatalf("a losing spin never lands on the jackpot: %+v", res)
	}
	if _, err := DrawInstant(MechanicInstant, 1, nil); err == nil {
		t.Fatal("odds below 2 are refused")
	}
}

func TestScratchCardMatchesTheOutcome(t *testing.T) {
	count := func(cells []string) int {
		n := 0
		for _, c := range cells {
			if c == "jackpot" {
				n++
			}
		}
		return n
	}
	for i := 0; i < 200; i++ {
		win, _ := DrawInstant(MechanicScratch, 2, nil)
		if got := count(win.Cells); win.Won != (got == 3) || got > 3 {
			t.Fatalf("won=%v but %d jackpots: %v", win.Won, got, win.Cells)
		}
	}
}

func TestRunDrawPicksDistinctEligibleWinners(t *testing.T) {
	entries := []DrawEntry{
		{PlayID: "p1", CustomerID: "ann"}, {PlayID: "p2", CustomerID: "ann"},
		{PlayID: "p3", CustomerID: "bob"}, {PlayID: "p4", CustomerID: "cat"},
	}
	// Draw p2 (ann), then p1 (ann again, skipped), then bob, who is ineligible, then cat.
	withDraws(t, 1, 0, 1, 0)
	picks, randoms, err := RunDraw(entries, 2, func(c string) (bool, string) {
		if c == "bob" {
			return false, "not verified"
		}
		return true, ""
	})
	if err != nil {
		t.Fatal(err)
	}
	var winners []string
	for _, p := range picks {
		if p.Outcome == "winner" {
			winners = append(winners, p.CustomerID)
		}
	}
	if len(winners) != 2 || winners[0] == winners[1] {
		t.Fatalf("want two distinct winners, got %v (picks %+v)", winners, picks)
	}
	if len(randoms) != len(picks) {
		t.Fatal("every pick must record the random value that made it")
	}
	for _, w := range winners {
		if w == "bob" {
			t.Fatal("an ineligible customer cannot win")
		}
	}
}
