package games

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestSlideScrambleIsSeededAndValid(t *testing.T) {
	a, err := NewSlidePuzzle("a1b2c3d4", DefaultSlideConfig())
	if err != nil {
		t.Fatalf("new puzzle: %v", err)
	}
	b, _ := NewSlidePuzzle("a1b2c3d4", DefaultSlideConfig())
	if !reflect.DeepEqual(a.Board(), b.Board()) {
		t.Fatalf("same seed scrambled differently: %v vs %v", a.Board(), b.Board())
	}
	if a.Solved() {
		t.Fatal("a scramble must never start solved")
	}
	if a.Moves() != 0 {
		t.Errorf("scrambling counted %d player moves", a.Moves())
	}

	got := a.Board()
	sort.Ints(got)
	for i, v := range got {
		if v != i {
			t.Fatalf("board is not a permutation of 0..8: %v", a.Board())
		}
	}
}

// "up" means the tile below the gap slides up into it.
func TestSlideMoveNamesTheTileDirection(t *testing.T) {
	p, _ := NewSlidePuzzle("00000001", DefaultSlideConfig())
	p.board = []int{1, 2, 3, 4, 0, 5, 7, 8, 6}
	p.blank = 4

	if err := p.Move(MoveUp); err != nil {
		t.Fatalf("move up: %v", err)
	}
	want := []int{1, 2, 3, 4, 8, 5, 7, 0, 6}
	if !reflect.DeepEqual(p.Board(), want) {
		t.Fatalf("after up = %v, want %v", p.Board(), want)
	}
}

func TestSlideRejectsImpossibleMoves(t *testing.T) {
	p, _ := NewSlidePuzzle("00000001", DefaultSlideConfig())
	// Unsolved, gap in the bottom-right corner: nothing is below or right of
	// it, so "up" and "left" have no tile to move.
	p.board = []int{1, 2, 3, 4, 5, 6, 8, 7, 0}
	p.blank = 8

	if err := p.Move(MoveUp); err == nil {
		t.Error("no tile below the gap, but up was accepted")
	}
	if err := p.Move("diagonal"); err == nil {
		t.Error("unknown direction was accepted")
	}
}

func TestSlideUnsolvedScoresNothing(t *testing.T) {
	res, err := ReplaySlide("00000001", DefaultSlideConfig(), nil)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if res.Score != 0 || res.Won {
		t.Errorf("an untouched scramble scored %d (won=%v)", res.Score, res.Won)
	}
}

func TestSlideScoreFloorsAtTheMinimum(t *testing.T) {
	p, _ := NewSlidePuzzle("00000001", DefaultSlideConfig())
	p.board = []int{1, 2, 3, 4, 5, 6, 7, 8, 0}
	p.blank = 8
	p.moves = 10000
	if got := p.Score(); got != int64(DefaultSlideConfig().SolvedMinScore) {
		t.Errorf("slow solve scored %d, want the floor %d", got, DefaultSlideConfig().SolvedMinScore)
	}
}

func TestReplaySlideLimits(t *testing.T) {
	cfg := DefaultSlideConfig()
	cfg.MaxMoves = 2
	if _, err := ReplaySlide("00000001", cfg, []string{"up", "down", "up"}); err == nil {
		t.Error("expected the move limit to be enforced")
	}
}

// The Flutter engine asserts the same scramble and solution in
// send-agift-mobile/test/slide_puzzle_test.dart.
func TestSlideCrossLanguageGolden(t *testing.T) {
	p, _ := NewSlidePuzzle("cafebabe", DefaultSlideConfig())
	if want := []int{1, 3, 6, 5, 0, 7, 2, 4, 8}; !reflect.DeepEqual(p.Board(), want) {
		t.Fatalf("scramble = %v, want %v", p.Board(), want)
	}

	// Breadth-first optimal solution for this scramble.
	solution := strings.Split("left,down,right,right,up,up,left,down,right,down,left,up,right,up,left,left", ",")
	res, err := ReplaySlide("cafebabe", DefaultSlideConfig(), solution)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !res.Won || res.Score != 4680 || res.MovesUsed != 16 {
		t.Errorf("won %v score %d moves %d, want true / 4680 / 16", res.Won, res.Score, res.MovesUsed)
	}
}
