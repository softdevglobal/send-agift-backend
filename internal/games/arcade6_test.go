package games

import (
	"errors"
	"strconv"
	"testing"
)

// The six games added by migration 000032. Each one has to be deterministic
// from its seed, reject a log that could not have been played, and score the
// same way twice — those are the properties the whole server-side replay
// rests on.

const arcadeSeed = "1a2b3c4d"

func TestMemoryDealIsDeterministic(t *testing.T) {
	a, err := NewMemoryGame(arcadeSeed, MemoryConfig{})
	if err != nil {
		t.Fatalf("deal: %v", err)
	}
	b, _ := NewMemoryGame(arcadeSeed, MemoryConfig{})
	for i, card := range a.Cards() {
		if b.Cards()[i] != card {
			t.Fatalf("card %d differs between deals: %d vs %d", i, card, b.Cards()[i])
		}
	}

	// Every face value must appear exactly twice, or pairs are unwinnable.
	counts := map[int]int{}
	for _, card := range a.Cards() {
		counts[card]++
	}
	for face, n := range counts {
		if n != 2 {
			t.Fatalf("face %d appears %d times, want 2", face, n)
		}
	}
}

func TestMemoryPerfectRunClearsTheGrid(t *testing.T) {
	g, _ := NewMemoryGame(arcadeSeed, MemoryConfig{})
	// Play it with full knowledge: pair every face in turn.
	seen := map[int][]int{}
	for i, face := range g.Cards() {
		seen[face] = append(seen[face], i)
	}
	moves := []string{}
	for face := 0; face < len(seen); face++ {
		pair := seen[face]
		moves = append(moves, strconv.Itoa(pair[0]), strconv.Itoa(pair[1]))
	}

	result, err := ReplayMemory(arcadeSeed, MemoryConfig{}, moves)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !result.Won {
		t.Fatal("a perfect run should clear the grid")
	}
	if result.Stats["matches"] != 8 {
		t.Fatalf("matches = %d, want 8", result.Stats["matches"])
	}
	// Eight matches in a row: the streak bonus should beat eight flat matches.
	if result.Score <= 8*20 {
		t.Fatalf("score %d should exceed a flat 160 once the streak pays", result.Score)
	}
}

func TestMemoryRejectsAlreadyMatchedCard(t *testing.T) {
	g, _ := NewMemoryGame(arcadeSeed, MemoryConfig{})
	seen := map[int][]int{}
	for i, face := range g.Cards() {
		seen[face] = append(seen[face], i)
	}
	pair := seen[0]
	moves := []string{
		strconv.Itoa(pair[0]), strconv.Itoa(pair[1]), // match it
		strconv.Itoa(pair[0]), // flip it again
	}
	if _, err := ReplayMemory(arcadeSeed, MemoryConfig{}, moves); !errors.Is(err, ErrInvalidMove) {
		t.Fatalf("want ErrInvalidMove for a matched card, got %v", err)
	}
}

func TestWhackScheduleIsDeterministicAndOrdered(t *testing.T) {
	a, err := NewWhackGame(arcadeSeed, WhackConfig{})
	if err != nil {
		t.Fatalf("schedule: %v", err)
	}
	b, _ := NewWhackGame(arcadeSeed, WhackConfig{})
	for i, mole := range a.Moles() {
		other := b.Moles()[i]
		if mole != other {
			t.Fatalf("mole %d differs: %+v vs %+v", i, mole, other)
		}
		if mole.Down <= mole.Up {
			t.Fatalf("mole %d is never up: %+v", i, mole)
		}
		if i > 0 && mole.Up < a.Moles()[i-1].Down {
			t.Fatalf("mole %d overlaps the one before it", i)
		}
	}
}

func TestWhackHittingEveryMoleBeatsSpamming(t *testing.T) {
	g, _ := NewWhackGame(arcadeSeed, WhackConfig{})
	clean := []string{}
	for _, mole := range g.Moles() {
		clean = append(clean, strconv.Itoa(mole.Up)+":"+strconv.Itoa(mole.Hole))
	}
	good, err := ReplayWhack(arcadeSeed, WhackConfig{}, clean)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if good.Stats["hits"] != int64(len(clean)) {
		t.Fatalf("hits = %d, want %d", good.Stats["hits"], len(clean))
	}

	// The same taps with a wild swing in the middle must score less: the miss
	// breaks the streak the bonus was building on. (A miss *before* any streak
	// is free — the score floors at zero rather than going negative.)
	half := len(clean) / 2
	gapTick := g.Moles()[half].Down // between two moles, so nothing is up
	noisy := make([]string, 0, len(clean)+1)
	noisy = append(noisy, clean[:half+1]...)
	noisy = append(noisy, strconv.Itoa(gapTick)+":0")
	noisy = append(noisy, clean[half+1:]...)
	bad, err := ReplayWhack(arcadeSeed, WhackConfig{}, noisy)
	if err != nil {
		t.Fatalf("replay noisy: %v", err)
	}
	if bad.Score >= good.Score {
		t.Fatalf("spamming scored %d, clean play %d — spam must not pay", bad.Score, good.Score)
	}
}

func TestWhackRejectsOutOfOrderTaps(t *testing.T) {
	g, _ := NewWhackGame(arcadeSeed, WhackConfig{})
	second := g.Moles()[1]
	moves := []string{
		strconv.Itoa(second.Up) + ":" + strconv.Itoa(second.Hole),
		"1:0", // back in time
	}
	if _, err := ReplayWhack(arcadeSeed, WhackConfig{}, moves); !errors.Is(err, ErrInvalidMove) {
		t.Fatalf("want ErrInvalidMove for a rewound tap, got %v", err)
	}
}

func TestBubblePopsClearAndCascade(t *testing.T) {
	cfg := BubbleConfig{}
	a, err := NewBubbleGame(arcadeSeed, cfg)
	if err != nil {
		t.Fatalf("deal: %v", err)
	}
	b, _ := NewBubbleGame(arcadeSeed, cfg)
	for r := range a.Grid() {
		for c := range a.Grid()[r] {
			if a.At(r, c) != b.At(r, c) {
				t.Fatalf("wall differs at %d,%d", r, c)
			}
		}
	}

	// Firing into every column in turn must never panic and must stay scoreable.
	moves := []string{}
	for i := 0; i < 20; i++ {
		moves = append(moves, strconv.Itoa(i%7))
	}
	result, err := ReplayBubble(arcadeSeed, cfg, moves)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if result.Score < 0 {
		t.Fatalf("score went negative: %d", result.Score)
	}
	if result.Stats["shots"] != 20 {
		t.Fatalf("shots = %d, want 20", result.Stats["shots"])
	}
}

func TestBubbleRejectsOffBoardColumn(t *testing.T) {
	if _, err := ReplayBubble(arcadeSeed, BubbleConfig{}, []string{"99"}); !errors.Is(err, ErrInvalidMove) {
		t.Fatalf("want ErrInvalidMove off the board, got %v", err)
	}
}

func TestTowerBlocksClearsAFullRow(t *testing.T) {
	// A well six wide, every slab forced to one cell, filled left to right.
	cfg := TowerBlocksConfig{Columns: 6, Rows: 12, MaxWidth: 1}
	moves := []string{"0", "1", "2", "3", "4", "5"}
	result, err := ReplayTowerBlocks(arcadeSeed, cfg, moves)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if result.Stats["rows"] != 1 {
		t.Fatalf("rows = %d, want 1 after filling the floor", result.Stats["rows"])
	}
	if result.Stats["placed"] != 6 {
		t.Fatalf("placed = %d, want 6", result.Stats["placed"])
	}
}

func TestTowerBlocksRejectsASlabThatDoesNotFit(t *testing.T) {
	cfg := TowerBlocksConfig{Columns: 6, Rows: 12, MaxWidth: 3}
	g, _ := NewTowerBlocksGame(arcadeSeed, cfg)
	// The rightmost legal start is Columns-width; one past it must be refused.
	tooFar := strconv.Itoa(g.MaxColumn() + 1)
	if _, err := ReplayTowerBlocks(arcadeSeed, cfg, []string{tooFar}); !errors.Is(err, ErrInvalidMove) {
		t.Fatalf("want ErrInvalidMove for an overhanging slab, got %v", err)
	}
}

func TestFruitBombEndsTheRound(t *testing.T) {
	g, err := NewFruitGame(arcadeSeed, FruitConfig{})
	if err != nil {
		t.Fatalf("schedule: %v", err)
	}
	var bomb FruitThrow
	found := false
	for _, throw := range g.Throws() {
		if throw.Bomb {
			bomb, found = throw, true
			break
		}
	}
	if !found {
		t.Fatal("the schedule should contain at least one bomb")
	}

	moves := []string{strconv.Itoa(bomb.Enter) + ":" + strconv.Itoa(bomb.Lane)}
	result, err := ReplayFruit(arcadeSeed, FruitConfig{}, moves)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !result.GameOver {
		t.Fatal("cutting a bomb must end the round")
	}
	if result.Stats["fruits"] != 0 {
		t.Fatalf("a bomb is not a fruit: %d", result.Stats["fruits"])
	}
}

func TestFruitStreakPaysMoreThanSingles(t *testing.T) {
	g, _ := NewFruitGame(arcadeSeed, FruitConfig{})
	run := []string{}
	for _, throw := range g.Throws() {
		if throw.Bomb {
			break
		}
		run = append(run, strconv.Itoa(throw.Enter)+":"+strconv.Itoa(throw.Lane))
	}
	if len(run) < 3 {
		t.Skip("schedule has too few fruits before the first bomb")
	}
	result, err := ReplayFruit(arcadeSeed, FruitConfig{}, run)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	flat := int64(len(run)) * 12
	if result.Score <= flat {
		t.Fatalf("score %d should exceed a flat %d once the combo pays", result.Score, flat)
	}
}

func TestDoodleClimbFollowsTheSeededTower(t *testing.T) {
	g, err := NewDoodleGame(arcadeSeed, DoodleConfig{})
	if err != nil {
		t.Fatalf("tower: %v", err)
	}
	// Climb by always hopping to the lane the next ledge is actually in.
	moves := []string{}
	height := 0
	lane := g.Lane()
	for i := 0; i < 12 && height < len(g.Platforms())-1; i++ {
		next := g.PlatformAt(height + 1)
		// The main ledge is always within one lane by construction; the alt
		// one may not be, so follow the main.
		moves = append(moves, strconv.Itoa(next.Lane))
		lane = next.Lane
		height++
		if next.Spring && height < len(g.Platforms())-1 {
			height++
			lane = g.PlatformAt(height).Lane
		}
	}
	_ = lane
	result, err := ReplayDoodle(arcadeSeed, DoodleConfig{}, moves)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if result.GameOver {
		t.Fatal("following the tower exactly should not end the climb")
	}
	if result.Stats["height"] < int64(len(moves)) {
		t.Fatalf("height %d should be at least the %d hops taken", result.Stats["height"], len(moves))
	}
}

func TestDoodleRejectsAnUnreachableLane(t *testing.T) {
	g, _ := NewDoodleGame(arcadeSeed, DoodleConfig{})
	// Two lanes away from the starting lane is out of reach by the rules.
	far := g.Lane() + 2
	if far >= 5 {
		far = g.Lane() - 2
	}
	if far < 0 {
		t.Skip("starting lane leaves no out-of-reach lane on this seed")
	}
	if _, err := ReplayDoodle(arcadeSeed, DoodleConfig{}, []string{strconv.Itoa(far)}); !errors.Is(err, ErrInvalidMove) {
		t.Fatalf("want ErrInvalidMove for an unreachable lane, got %v", err)
	}
}

func TestEveryNewGameIsRegistered(t *testing.T) {
	for _, slug := range []string{
		MemorySlug, WhackSlug, BubbleSlug, TowerBlocksSlug, FruitSlug, DoodleSlug,
	} {
		if _, ok := EngineFor(slug); !ok {
			t.Fatalf("%s is not in the engine registry", slug)
		}
	}
}
