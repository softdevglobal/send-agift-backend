package games

import (
	"fmt"
	"testing"
)

// ─── Block Blast ──────────────────────────────────────────────────────────

func TestBlockBlastHandIsSeeded(t *testing.T) {
	a, _ := NewBlockBlastGame("12345678", DefaultBlockBlastConfig())
	b, _ := NewBlockBlastGame("12345678", DefaultBlockBlastConfig())
	if fmt.Sprint(a.Hand()) != fmt.Sprint(b.Hand()) {
		t.Fatalf("same seed dealt %v and %v", a.Hand(), b.Hand())
	}
}

func TestBlockBlastClearsARowAndColumnTogether(t *testing.T) {
	g, _ := NewBlockBlastGame("cafebabe", DefaultBlockBlastConfig())
	n := g.cfg.BoardSize
	// Fill row 0 and column 0 except the corner, then drop a single in it.
	for i := 1; i < n; i++ {
		g.board[i] = 1
		g.board[i*n] = 1
	}
	g.hand[0] = 0 // single cell
	move, err := g.Place(0, 0, 0)
	if err != nil {
		t.Fatalf("place: %v", err)
	}
	if move.Lines != 2 {
		t.Fatalf("lines = %d, want 2", move.Lines)
	}
	for i, cell := range g.board {
		if cell != 0 {
			t.Fatalf("cell %d not cleared", i)
		}
	}
	// 1 cell + (2 lines: 10*2*3/2 = 30).
	if move.Points != 31 {
		t.Fatalf("points = %d, want 31", move.Points)
	}
}

func TestBlockBlastRejectsBadPlacements(t *testing.T) {
	cases := map[string][]string{
		"no such slot":      {"3:0:0"},
		"off the board":     {"0:7:7:"},
		"negative row":      {"0:-1:0"},
		"not three numbers": {"0:0"},
		"slot used twice":   {"0:0:0", "0:4:4"},
	}
	for name, moves := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ReplayBlockBlast("cafebabe", DefaultBlockBlastConfig(), moves); err == nil {
				t.Fatalf("expected %v to be rejected", moves)
			}
		})
	}
}

func TestBlockBlastNewHandWhenEmpty(t *testing.T) {
	g, _ := NewBlockBlastGame("cafebabe", DefaultBlockBlastConfig())
	n := g.cfg.BoardSize
	for slot := 0; slot < 3; slot++ {
		placed := false
		for r := 0; r < n && !placed; r++ {
			for c := 0; c < n && !placed; c++ {
				if g.Fits(g.hand[slot], r, c) {
					if _, err := g.Place(slot, r, c); err != nil {
						t.Fatal(err)
					}
					placed = true
				}
			}
		}
	}
	for _, p := range g.Hand() {
		if p < 0 {
			t.Fatalf("hand %v should have been refilled", g.Hand())
		}
	}
}

// ─── Cricket ──────────────────────────────────────────────────────────────

func openAngle(g *CricketGame, k int) int {
	for a := -g.cfg.MaxAngle; a <= g.cfg.MaxAngle; a++ {
		if !g.blocked(k, a) {
			return a
		}
	}
	return 0
}

func TestCricketMiddledShotIsASix(t *testing.T) {
	g, _ := NewCricketGame("cafebabe", DefaultCricketConfig())
	out, err := g.Swing(g.Arrival(0), 0)
	if err != nil {
		t.Fatalf("swing: %v", err)
	}
	if out.Runs != 6 {
		t.Fatalf("a perfectly timed shot should be six: %+v", out)
	}
}

func TestCricketGoodShotIntoAGapIsFour(t *testing.T) {
	g, _ := NewCricketGame("cafebabe", DefaultCricketConfig())
	out, err := g.Swing(g.Arrival(0)+2, openAngle(g, 0))
	if err != nil {
		t.Fatalf("swing: %v", err)
	}
	if out.Runs != 4 {
		t.Fatalf("a good shot into a gap should be four: %+v", out)
	}
}

func TestCricketBallsOnTheStumpsBowlYou(t *testing.T) {
	g, _ := NewCricketGame("cafebabe", DefaultCricketConfig())
	g.settle(g.cfg.Balls)
	onStumps := 0
	for _, b := range g.balls {
		if b.Line == 0 {
			onStumps++
		}
	}
	want := onStumps
	if want > g.cfg.Wickets {
		want = g.cfg.Wickets
	}
	if g.wickets != want || g.runs != 0 {
		t.Fatalf("leaving every ball: wickets %d runs %d, want %d / 0", g.wickets, g.runs, want)
	}
}

func TestReplayCricketValidatesTheLog(t *testing.T) {
	g, _ := NewCricketGame("cafebabe", DefaultCricketConfig())
	a0 := g.Arrival(0)
	cases := map[string][]string{
		"not two numbers":     {"50"},
		"angle out of range":  {fmt.Sprintf("%d:81", a0)},
		"far too early":       {fmt.Sprintf("%d:0", a0-13)},
		"two swings one ball": {fmt.Sprintf("%d:0", a0), fmt.Sprintf("%d:0", a0+1)},
		"out of order":        {fmt.Sprintf("%d:0", a0), fmt.Sprintf("%d:0", a0-1)},
	}
	for name, moves := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ReplayCricket("cafebabe", DefaultCricketConfig(), moves); err == nil {
				t.Fatalf("expected %v to be rejected", moves)
			}
		})
	}
}

// ─── Sling Shot ───────────────────────────────────────────────────────────

func TestSlingLevelIsSeeded(t *testing.T) {
	a, _ := NewSlingGame("12345678", DefaultSlingConfig())
	b, _ := NewSlingGame("12345678", DefaultSlingConfig())
	if fmt.Sprint(a.blocks) != fmt.Sprint(b.blocks) {
		t.Fatal("same seed built different structures")
	}
	if a.targetsAlive() == 0 {
		t.Fatal("a level needs targets")
	}
}

func TestSlingUnsupportedBlocksFall(t *testing.T) {
	g, _ := NewSlingGame("cafebabe", DefaultSlingConfig())
	g.blocks = []SlingBlock{
		{ID: 0, X: 600, Y: 100, W: 50, H: 50, Kind: 'T', Alive: true},
		{ID: 1, X: 600, Y: 50, W: 50, H: 50, Kind: 'W', Alive: false},
		{ID: 2, X: 600, Y: 0, W: 50, H: 50, Kind: 'S', Alive: true},
	}
	g.collapse()
	if g.blocks[0].Y != 50 || g.blocks[0].Alive {
		t.Fatalf("the target should drop a cell and break: %+v", g.blocks[0])
	}
}

func TestSlingRejectsImpossiblePulls(t *testing.T) {
	for _, m := range []string{"0:50", "80:80", "101:0", "50:-101"} {
		if _, err := ReplaySling("cafebabe", DefaultSlingConfig(), []string{m}); err == nil {
			t.Errorf("pull %s should be rejected", m)
		}
	}
}

func TestSlingOutOfShotsEndsTheGame(t *testing.T) {
	g, _ := NewSlingGame("cafebabe", DefaultSlingConfig())
	for i := 0; i < g.cfg.ShotsPerLevel; i++ {
		// Straight up, landing back by the sling.
		if err := g.Shoot(1, 90); err != nil {
			t.Fatal(err)
		}
	}
	if !g.Over() || g.won {
		t.Fatal("three wasted shots should end the game")
	}
}

// ─── Hill Rider ───────────────────────────────────────────────────────────

func TestHillStartsFlatAndStill(t *testing.T) {
	g, _ := NewHillGame("cafebabe", DefaultHillConfig())
	if g.HeightAt(0) != 0 || g.HeightAt(400) != 0 {
		t.Fatal("the start line should be flat")
	}
	for i := 0; i < 50; i++ {
		g.Step()
	}
	if g.x != 0 {
		t.Fatalf("with no gas the car should not move: x=%d", g.x)
	}
}

func TestHillGasDrivesForwardAndBurnsFuel(t *testing.T) {
	g, _ := NewHillGame("cafebabe", DefaultHillConfig())
	g.SetInput(HillGas)
	for i := 0; i < 100; i++ {
		g.Step()
	}
	if g.x <= 0 || g.fuel != g.cfg.StartFuel-100 {
		t.Fatalf("x=%d fuel=%d", g.x, g.fuel)
	}
}

func TestReplayHillValidatesTheLog(t *testing.T) {
	cases := map[string][]string{
		"no end":        {"0:g"},
		"bad pedal":     {"0:x", "5:end"},
		"out of order":  {"3:g", "2:n", "5:end"},
		"after the end": {"9:g", "5:end"},
		"too long":      {"30001:end"},
	}
	for name, moves := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ReplayHill("cafebabe", DefaultHillConfig(), moves); err == nil {
				t.Fatalf("expected %v to be rejected", moves)
			}
		})
	}
}
