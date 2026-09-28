package games

import (
	"errors"
	"fmt"
	"testing"
)

// fruitMove is one blade piece in the wire format.
func fruitMove(tick, stroke, x1, y1, x2, y2 int) string {
	return fmt.Sprintf("%d:%d:%d:%d:%d:%d", tick, stroke, x1, y1, x2, y2)
}

// fruitBot cuts every fruit at the top of its flight with a short horizontal
// stroke, and leaves every bomb alone. Fruit cut close together share a
// stroke, so a volley becomes a combo.
func fruitBot(g *FruitGame, limit int) []string {
	moves := []string{}
	stroke, last := 0, -1000
	for _, t := range g.Throws() {
		if t.Bomb {
			continue
		}
		tick := t.Enter + (t.Exit-t.Enter)/2
		if tick-last > g.cfg.ComboWindowTicks {
			stroke++
		}
		last = tick
		x, y := t.Pos(tick)
		moves = append(moves, fruitMove(tick, stroke, x-20, y, x+20, y))
		if len(moves) == limit {
			break
		}
	}
	return moves
}

func TestFruitScheduleIsOrderedAndOnTheBoard(t *testing.T) {
	g, err := NewFruitGame(arcadeSeed, FruitConfig{})
	if err != nil {
		t.Fatalf("schedule: %v", err)
	}
	cfg := DefaultFruitConfig()
	bombs, prev := 0, -1
	for i, th := range g.Throws() {
		if th.Enter < prev {
			t.Fatalf("throw %d enters at %d, before %d", i, th.Enter, prev)
		}
		prev = th.Enter
		if th.Bomb {
			bombs++
		}
		for tick := th.Enter; tick < th.Exit; tick++ {
			x, y := th.Pos(tick)
			if x < 0 || x > cfg.Width || y > cfg.MaxPeak {
				t.Fatalf("throw %d leaves the board at tick %d: (%d,%d)", i, tick, x, y)
			}
		}
	}
	if bombs == 0 {
		t.Fatal("the schedule should contain bombs")
	}
	if len(g.Throws()) <= cfg.Volleys {
		t.Fatal("volleys should grow past a single throw")
	}
}

func TestFruitBotClearsTheRound(t *testing.T) {
	g, _ := NewFruitGame(arcadeSeed, FruitConfig{})
	moves := fruitBot(g, -1)
	result, err := ReplayFruit(arcadeSeed, FruitConfig{}, moves)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if result.GameOver {
		t.Fatal("cutting every fruit and no bomb should not end the round early")
	}
	if result.Stats["fruits"] != int64(len(moves)) {
		t.Fatalf("cut %d of %d fruits", result.Stats["fruits"], len(moves))
	}
	if result.Stats["best_combo"] < 3 {
		t.Fatalf("best combo %d: a volley cut in one stroke should chain", result.Stats["best_combo"])
	}
	flat := int64(len(moves)) * 10
	if result.Score <= flat {
		t.Fatalf("score %d should beat a flat %d once combos pay", result.Score, flat)
	}
}

func TestFruitGolden(t *testing.T) {
	// Pinned in test/arcade6_golden_test.dart on the app side.
	g, _ := NewFruitGame("5f3a91c2", FruitConfig{})
	moves := fruitBot(g, 20)
	result, err := ReplayFruit("5f3a91c2", FruitConfig{}, moves)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if result.Score != 225 || result.Stats["fruits"] != 20 || result.Stats["best_combo"] != 2 {
		t.Fatalf("moves=%q score=%d fruits=%d best_combo=%d", moves, result.Score, result.Stats["fruits"], result.Stats["best_combo"])
	}
}

func TestFruitBombEndsTheRound(t *testing.T) {
	g, _ := NewFruitGame(arcadeSeed, FruitConfig{})
	for _, th := range g.Throws() {
		if !th.Bomb {
			continue
		}
		tick := th.Enter + (th.Exit-th.Enter)/2
		// Keep every life by cutting the fruit that tops out before the bomb.
		moves := []string{}
		for _, m := range fruitBot(g, -1) {
			var at int
			fmt.Sscanf(m, "%d:", &at)
			if at < tick {
				moves = append(moves, m)
			}
		}
		x, y := th.Pos(tick)
		moves = append(moves, fruitMove(tick, 999, x-5, y-5, x+5, y+5))
		result, err := ReplayFruit(arcadeSeed, FruitConfig{}, moves)
		if err != nil {
			t.Fatalf("replay: %v", err)
		}
		if !result.GameOver {
			t.Fatal("cutting a bomb must end the round")
		}
		if result.Stats["fruits"] != int64(len(moves)-1) {
			t.Fatalf("a bomb is not a fruit: %d", result.Stats["fruits"])
		}
		return
	}
	t.Fatal("no bomb in the schedule")
}

func TestFruitDroppingEveryLifeEndsTheRound(t *testing.T) {
	g, _ := NewFruitGame(arcadeSeed, FruitConfig{})
	// Touch nothing, then try to swipe once the third fruit has fallen.
	fallen, at := 0, 0
	for _, th := range g.Throws() {
		if !th.Bomb {
			fallen++
			if fallen == 3 {
				at = th.Exit
				break
			}
		}
	}
	_, err := ReplayFruit(arcadeSeed, FruitConfig{}, []string{fruitMove(at, 1, 0, 0, 10, 10)})
	if !errors.Is(err, ErrInvalidMove) {
		t.Fatalf("want ErrInvalidMove after the last life, got %v", err)
	}
	result, err := ReplayFruit(arcadeSeed, FruitConfig{}, nil)
	if err != nil || !result.GameOver {
		t.Fatalf("a round with every fruit dropped is over: %v %v", result, err)
	}
}

func TestFruitRejectsBadSwipes(t *testing.T) {
	for _, moves := range [][]string{
		{"10:1:0:0:5"},
		{fruitMove(10, 1, 0, 0, 5000, 0)},
		{fruitMove(20, 1, 0, 0, 1, 1), fruitMove(10, 1, 0, 0, 1, 1)},
		{fruitMove(10, 2, 0, 0, 1, 1), fruitMove(10, 1, 0, 0, 1, 1)},
	} {
		if _, err := ReplayFruit(arcadeSeed, FruitConfig{}, moves); !errors.Is(err, ErrInvalidMove) {
			t.Fatalf("%q: want ErrInvalidMove, got %v", moves, err)
		}
	}
}

func TestSegmentHits(t *testing.T) {
	cases := []struct {
		x1, y1, x2, y2, cx, cy, r int
		want                      bool
	}{
		{0, 0, 100, 0, 50, 10, 10, true},
		{0, 0, 100, 0, 50, 11, 10, false},
		{0, 0, 100, 0, 110, 0, 10, true},
		{0, 0, 100, 0, 111, 0, 10, false},
		{0, 0, 0, 0, 3, 4, 5, true},
		{0, 0, 100, 100, 50, 60, 8, true},
	}
	for _, c := range cases {
		if got := segmentHits(c.x1, c.y1, c.x2, c.y2, c.cx, c.cy, c.r); got != c.want {
			t.Fatalf("%+v: got %v", c, got)
		}
	}
}
