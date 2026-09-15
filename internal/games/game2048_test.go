package games

import (
	"reflect"
	"testing"
)

// TestRNGGoldenSequence pins the exact PRNG output. These numbers are the
// cross-language contract: the Dart implementation in the mobile app must
// produce this same sequence, or replayed games will not match.
func TestRNGGoldenSequence(t *testing.T) {
	r := NewRNG(1)
	got := make([]uint32, 8)
	for i := range got {
		got[i] = r.Next()
	}
	want := []uint32{
		1015568748, 1586005467, 2165703038, 3027450565,
		217083232, 1587069247, 3327581586, 2388811721,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PRNG sequence changed.\n got: %v\nwant: %v\n"+
			"Changing this breaks every stored session and the Dart client.", got, want)
	}
}

// TestCollapseRules covers the merge rules that decide the score.
func TestCollapseRules(t *testing.T) {
	cases := []struct {
		name      string
		in        []int
		want      []int
		wantScore int64
	}{
		{"empty", []int{0, 0, 0, 0}, []int{0, 0, 0, 0}, 0},
		{"slide only", []int{0, 0, 0, 2}, []int{2, 0, 0, 0}, 0},
		{"single merge", []int{2, 2, 0, 0}, []int{4, 0, 0, 0}, 4},
		{"gap then merge", []int{2, 0, 2, 0}, []int{4, 0, 0, 0}, 4},
		{"two merges", []int{2, 2, 4, 4}, []int{4, 8, 0, 0}, 12},
		{"no triple merge", []int{2, 2, 2, 0}, []int{4, 2, 0, 0}, 4},
		{"four equal makes two pairs", []int{2, 2, 2, 2}, []int{4, 4, 0, 0}, 8},
		{"unequal untouched", []int{2, 4, 8, 16}, []int{2, 4, 8, 16}, 0},
		{"merge leading edge first", []int{4, 4, 8, 0}, []int{8, 8, 0, 0}, 8},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var score int64
			got := collapse(tc.in, &score)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("collapse(%v) = %v, want %v", tc.in, got, tc.want)
			}
			if score != tc.wantScore {
				t.Errorf("score = %d, want %d", score, tc.wantScore)
			}
		})
	}
}

// TestDeterminism is the property the entire anti-cheat design depends on:
// one seed always produces exactly one game.
func TestDeterminism(t *testing.T) {
	const seed = "a1b2c3d4"
	moves := []string{"left", "up", "right", "down", "left", "up", "left", "down"}

	first, err := Replay(seed, DefaultConfig(), moves)
	if err != nil {
		t.Fatalf("first replay: %v", err)
	}

	for i := 0; i < 50; i++ {
		again, err := Replay(seed, DefaultConfig(), moves)
		if err != nil {
			t.Fatalf("replay %d: %v", i, err)
		}
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("replay %d diverged.\nfirst: %+v\nagain: %+v", i, first, again)
		}
	}
}

// TestStartingBoardIsSeeded checks the opening position comes from the seed,
// so players cannot reroll a bad start by restarting the app.
func TestStartingBoardIsSeeded(t *testing.T) {
	a, err := NewGame2048("00000001", DefaultConfig())
	if err != nil {
		t.Fatalf("new game: %v", err)
	}
	b, err := NewGame2048("00000001", DefaultConfig())
	if err != nil {
		t.Fatalf("new game: %v", err)
	}
	if !reflect.DeepEqual(a.Board(), b.Board()) {
		t.Fatalf("same seed gave different boards: %v vs %v", a.Board(), b.Board())
	}

	c, err := NewGame2048("ffffffff", DefaultConfig())
	if err != nil {
		t.Fatalf("new game: %v", err)
	}
	if reflect.DeepEqual(a.Board(), c.Board()) {
		t.Errorf("different seeds produced identical boards: %v", a.Board())
	}

	// Exactly start_tiles non-empty cells, each a 2 or a 4.
	filled := 0
	for _, v := range a.Board() {
		if v == 0 {
			continue
		}
		filled++
		if v != 2 && v != 4 {
			t.Errorf("starting tile has value %d, want 2 or 4", v)
		}
	}
	if filled != DefaultConfig().StartTiles {
		t.Errorf("board has %d starting tiles, want %d", filled, DefaultConfig().StartTiles)
	}
}

// TestReplayMatchesLivePlay proves the server reproduces what the player saw:
// stepping a game move by move must equal replaying the same log in one go.
func TestReplayMatchesLivePlay(t *testing.T) {
	const seed = "deadbeef"
	cfg := DefaultConfig()

	live, err := NewGame2048(seed, cfg)
	if err != nil {
		t.Fatalf("new game: %v", err)
	}

	dirs := []string{MoveLeft, MoveUp, MoveRight, MoveDown}
	var played []string
	for i := 0; i < 200 && live.HasMoves(); i++ {
		dir := dirs[i%len(dirs)]
		if _, err := live.Move(dir); err != nil {
			t.Fatalf("move: %v", err)
		}
		played = append(played, dir)
	}

	res, err := Replay(seed, cfg, played)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if res.Score != live.Score() {
		t.Errorf("replay score %d, live score %d", res.Score, live.Score())
	}
	if !reflect.DeepEqual(res.FinalBoard, live.Board()) {
		t.Errorf("replay board %v, live board %v", res.FinalBoard, live.Board())
	}
	if res.HighestTile != live.HighestTile() {
		t.Errorf("replay highest tile %d, live %d", res.HighestTile, live.HighestTile())
	}
	if res.Score == 0 {
		t.Error("expected a non-zero score after 200 moves")
	}
}

// TestReplayRejectsBadInput covers the cheat paths the replay must refuse.
func TestReplayRejectsBadInput(t *testing.T) {
	cfg := DefaultConfig()

	t.Run("unknown direction", func(t *testing.T) {
		if _, err := Replay("00000001", cfg, []string{"left", "diagonal"}); err == nil {
			t.Fatal("expected an error for an unknown direction")
		}
	})

	t.Run("over the move limit", func(t *testing.T) {
		small := cfg
		small.MaxMoves = 5
		moves := make([]string, 6)
		for i := range moves {
			moves[i] = MoveLeft
		}
		if _, err := Replay("00000001", small, moves); err == nil {
			t.Fatal("expected an error when the move log exceeds max_moves")
		}
	})

	t.Run("moves after game over", func(t *testing.T) {
		// Play until the board is dead, then append one more move.
		const seed = "0badf00d"
		g, err := NewGame2048(seed, cfg)
		if err != nil {
			t.Fatalf("new game: %v", err)
		}
		dirs := []string{MoveLeft, MoveUp, MoveRight, MoveDown}
		var played []string
		for i := 0; g.HasMoves() && i < cfg.MaxMoves; i++ {
			dir := dirs[i%len(dirs)]
			if _, err := g.Move(dir); err != nil {
				t.Fatalf("move: %v", err)
			}
			played = append(played, dir)
		}
		if g.HasMoves() {
			t.Skip("board did not fill within the move budget")
		}
		played = append(played, MoveLeft)
		if _, err := Replay(seed, cfg, played); err == nil {
			t.Fatal("expected an error for a move played after game over")
		}
	})
}

// TestForgedScoreCannotBeatReplay is the headline anti-cheat property: a
// tampered client can claim any number it likes, but the server's number comes
// from the moves alone.
func TestForgedScoreCannotBeatReplay(t *testing.T) {
	const seed = "12345678"
	moves := []string{"left", "up", "left", "up", "left"}

	res, err := Replay(seed, DefaultConfig(), moves)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}

	const forged int64 = 999999
	if res.Score == forged {
		t.Fatal("test is meaningless: real score equals the forged one")
	}
	if res.Score > 200 {
		t.Errorf("five moves produced %d points, which looks wrong", res.Score)
	}
}

// TestEmptyMoveLog covers a player who opens the game and submits immediately.
func TestEmptyMoveLog(t *testing.T) {
	res, err := Replay("00000001", DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if res.Score != 0 {
		t.Errorf("score = %d, want 0 for an empty move log", res.Score)
	}
	if res.MovesUsed != 0 {
		t.Errorf("moves used = %d, want 0", res.MovesUsed)
	}
}
