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
	pairs := DefaultMemoryConfig().Pairs
	if int(result.Stats["matches"]) != pairs {
		t.Fatalf("matches = %d, want %d", result.Stats["matches"], pairs)
	}
	// A run of matches in a row: the streak bonus should beat flat matches.
	flat := int64(pairs) * int64(DefaultMemoryConfig().PointsPerMatch)
	if result.Score <= flat {
		t.Fatalf("score %d should exceed a flat %d once the streak pays", result.Score, flat)
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

func TestBubbleDealIsDeterministic(t *testing.T) {
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
	if a.Next() != b.Next() || a.After() != b.After() {
		t.Fatal("the queued colours differ between two deals of one seed")
	}
}

func TestBubbleShotsLandInEmptyCells(t *testing.T) {
	cfg := BubbleConfig{}
	g, err := NewBubbleGame(arcadeSeed, cfg)
	if err != nil {
		t.Fatalf("deal: %v", err)
	}
	// Sweep the whole range of aims. Every one has to come to rest somewhere
	// empty — a shot landing on top of another bubble would overwrite it.
	for dx := -bubbleMaxAim; dx <= bubbleMaxAim; dx += 137 {
		row, col, ok := g.Trace(dx)
		if !ok {
			t.Fatalf("aim %d found nowhere to land on an open board", dx)
		}
		if g.At(row, col) >= 0 {
			t.Fatalf("aim %d landed on an occupied cell %d,%d", dx, row, col)
		}
	}
}

func TestBubbleAimingReachesDifferentColumns(t *testing.T) {
	// Aiming has to actually steer the shot. If every aim came to rest in the
	// same column the game would be a tap in disguise, which is the whole
	// complaint that angled shooting was meant to answer.
	g, err := NewBubbleGame(arcadeSeed, BubbleConfig{})
	if err != nil {
		t.Fatalf("deal: %v", err)
	}
	landed := map[int]bool{}
	for dx := -bubbleMaxAim; dx <= bubbleMaxAim; dx += 100 {
		if _, col, ok := g.Trace(dx); ok {
			landed[col] = true
		}
	}
	if len(landed) < 3 {
		t.Fatalf("aiming only ever reached %d column(s): %v", len(landed), landed)
	}
}

func TestBubbleAimIsClampedNotRejected(t *testing.T) {
	g, _ := NewBubbleGame(arcadeSeed, BubbleConfig{})
	_, wild, okWild := g.Trace(bubbleMaxAim * 10)
	_, edge, okEdge := g.Trace(bubbleMaxAim)
	if !okWild || !okEdge || wild != edge {
		t.Fatalf("an aim past the limit should fly like the limit: %d vs %d", wild, edge)
	}
}

func TestBubblePlayIsScoreable(t *testing.T) {
	cfg := BubbleConfig{}
	// Spread the aims about so the wall is not simply stacked into one
	// column, which would end the round before the log runs out.
	aims := []int{0, 900, -900, 1800, -1800, 2700, -2700, 3600, -3600, 450}
	moves := []string{}
	for i := 0; i < 20; i++ {
		moves = append(moves, strconv.Itoa(aims[i%len(aims)]))
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

func TestBubbleSwapExchangesTheQueue(t *testing.T) {
	g, err := NewBubbleGame(arcadeSeed, BubbleConfig{})
	if err != nil {
		t.Fatalf("deal: %v", err)
	}
	first, second := g.Next(), g.After()
	if err := g.Swap(); err != nil {
		t.Fatalf("swap: %v", err)
	}
	// A swap must only exchange the two — drawing a fresh colour here would
	// let a player keep swapping until the seed handed them one they liked.
	if g.Next() != second || g.After() != first {
		t.Fatalf("swap gave %d,%d, want %d,%d", g.Next(), g.After(), second, first)
	}
}

func TestBubbleSwapFiresTheOtherColour(t *testing.T) {
	cfg := BubbleConfig{}
	g, _ := NewBubbleGame(arcadeSeed, cfg)
	second := g.After()
	if err := g.Swap(); err != nil {
		t.Fatalf("swap: %v", err)
	}
	row, col, ok := g.Trace(0)
	if !ok {
		t.Fatal("nowhere to land on an open board")
	}
	if _, err := g.Shoot(0); err != nil {
		t.Fatalf("shoot: %v", err)
	}
	if got := g.At(row, col); got != second {
		t.Fatalf("fired colour %d, want the swapped-in %d", got, second)
	}
}

func TestBubbleSwapReplaysFromTheLog(t *testing.T) {
	cfg := BubbleConfig{}
	withSwap, err := ReplayBubble(arcadeSeed, cfg, []string{BubbleSwapMove, "0"})
	if err != nil {
		t.Fatalf("replay with swap: %v", err)
	}
	plain, err := ReplayBubble(arcadeSeed, cfg, []string{"0"})
	if err != nil {
		t.Fatalf("replay without swap: %v", err)
	}
	// The swap is a move in its own right, so the counts differ even when
	// the shot that follows it is the same.
	if withSwap.MovesUsed != 2 || plain.MovesUsed != 1 {
		t.Fatalf("moves used %d and %d, want 2 and 1", withSwap.MovesUsed, plain.MovesUsed)
	}
}

func TestBubbleRejectsAnImpossibleAim(t *testing.T) {
	if _, err := ReplayBubble(arcadeSeed, BubbleConfig{}, []string{"99999"}); !errors.Is(err, ErrInvalidMove) {
		t.Fatalf("want ErrInvalidMove for an aim off the board, got %v", err)
	}
	if _, err := ReplayBubble(arcadeSeed, BubbleConfig{}, []string{"sideways"}); !errors.Is(err, ErrInvalidMove) {
		t.Fatalf("want ErrInvalidMove for a nonsense move, got %v", err)
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
	// Climb by always hopping to the lane the next ledge is actually in,
	// driving the real game rather than a second copy of its rules — a model
	// of the climb here would only have to be kept in step with the engine.
	moves := []string{}
	for i := 0; i < 12 && !(g.Fell() || g.Topped()); i++ {
		next := g.PlatformAt(g.Height() + 1)
		// The main ledge is always within one lane by construction; the alt
		// one may not be, so follow the main.
		moves = append(moves, strconv.Itoa(next.Lane))
		if _, err := g.Hop(next.Lane); err != nil {
			t.Fatalf("hop %d: %v", i, err)
		}
	}

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

func TestDoodleSpringLiftsClearOfTheLedgesAbove(t *testing.T) {
	cfg := DefaultDoodleConfig()
	g, err := NewDoodleGame(arcadeSeed, cfg)
	if err != nil {
		t.Fatalf("tower: %v", err)
	}

	// Climb until the next ledge up is a spring.
	for i := 0; i < 200 && !(g.Fell() || g.Topped()); i++ {
		next := g.PlatformAt(g.Height() + 1)
		if next.Spring {
			break
		}
		if _, err := g.Hop(next.Lane); err != nil {
			t.Fatalf("hop %d: %v", i, err)
		}
	}
	next := g.PlatformAt(g.Height() + 1)
	if !next.Spring || g.Fell() || g.Topped() {
		t.Skip("no spring came up on this seed within the climb")
	}

	before := g.Height()
	springs := g.Springs()
	if _, err := g.Hop(next.Lane); err != nil {
		t.Fatalf("spring hop: %v", err)
	}

	// The whole point of a spring is that the ledges it clears never have to
	// be landed on, so the height has to jump by the full lift.
	if got := g.Height() - before; got != cfg.SpringLift {
		t.Fatalf("a spring lifted %d ledges, want %d", got, cfg.SpringLift)
	}
	if g.Springs() != springs+1 {
		t.Fatalf("springs = %d, want %d", g.Springs(), springs+1)
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
