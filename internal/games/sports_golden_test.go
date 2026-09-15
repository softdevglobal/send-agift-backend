package games

import (
	"strings"
	"testing"
)

// The logs below were produced by bots on seed "cafebabe". The Flutter
// engines replay the same logs in send-agift-mobile/test/*_game_test.dart and
// must land on the same numbers — if either side drifts, both fail.

var basketballGoldenLog = strings.Split("5:0:40,32:0:85,59:0:75,86:0:55,113:9:40,140:-13:85,167:8:70,194:11:60,221:-10:40,248:-16:85,275:0:70,302:34:55,329:-3:45,356:-28:85,383:-3:70,410:-27:55,437:72:40,464:-33:90,491:3:70,518:25:55,545:-20:40,572:15:85,599:5:70,626:5:55,653:17:40,680:-35:85,707:52:70,734:-70:60,761:-8:40,788:40:85,815:-58:70,842:65:55,869:-47:45,896:-46:85", ",")

func TestBasketballCrossLanguageGolden(t *testing.T) {
	res, err := ReplayBasketball("cafebabe", DefaultBasketballConfig(), basketballGoldenLog)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if res.Score != 124 {
		t.Errorf("score = %d, want 124", res.Score)
	}
	for stat, want := range map[string]int64{"makes": 29, "shots": 34, "swishes": 23, "best_streak": 5} {
		if res.Stats[stat] != want {
			t.Errorf("%s = %d, want %d", stat, res.Stats[stat], want)
		}
	}
	if res.MinDurationMs != 35840 {
		t.Errorf("min duration = %d, want 35840", res.MinDurationMs)
	}

	g, _ := NewBasketballGame("cafebabe", DefaultBasketballConfig())
	for _, e := range basketballGoldenLog {
		v, _ := parseTickEntry(e, 3)
		if _, err := g.Shoot(v[0], v[1], v[2]); err != nil {
			t.Fatalf("shoot %s: %v", e, err)
		}
	}
	if g.Distance() != 2 || g.Level() != 7 {
		t.Errorf("final spot %d level %d, want 2 / 7", g.Distance(), g.Level())
	}
}

var stackGoldenLog = strings.Split("38,75,111,149,183,215,247,281,311,340,368,398,424,449,496", ",")

func TestStackCrossLanguageGolden(t *testing.T) {
	res, err := ReplayStack("cafebabe", DefaultStackConfig(), stackGoldenLog)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if res.Score != 245 || !res.GameOver {
		t.Errorf("score %d over %v, want 245 / true", res.Score, res.GameOver)
	}
	for stat, want := range map[string]int64{"floors": 14, "perfects": 11, "best_streak": 3} {
		if res.Stats[stat] != want {
			t.Errorf("%s = %d, want %d", stat, res.Stats[stat], want)
		}
	}
	if res.MinDurationMs != 7936 {
		t.Errorf("min duration = %d, want 7936", res.MinDurationMs)
	}

	g, _ := NewStackGame("cafebabe", DefaultStackConfig())
	for _, e := range stackGoldenLog {
		var tick int
		for _, c := range e {
			tick = tick*10 + int(c-'0')
		}
		if _, err := g.Drop(tick); err != nil {
			t.Fatalf("drop %s: %v", e, err)
		}
	}
	if want := (StackBlock{-50, 50, -50, 6}); g.Top() != want {
		t.Errorf("top = %+v, want %+v", g.Top(), want)
	}
}

var archeryGoldenLog = strings.Split("40:-9:8,87:25:-11,134:-18:-2,181:-8:36,228:32:11,275:-24:-3,322:31:-1,369:13:-3,416:-6:-9,463:83:45", ",")

func TestArcheryCrossLanguageGolden(t *testing.T) {
	res, err := ReplayArchery("cafebabe", DefaultArcheryConfig(), archeryGoldenLog)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if res.Score != 83 || !res.GameOver {
		t.Errorf("score %d over %v, want 83 / true", res.Score, res.GameOver)
	}
	for stat, want := range map[string]int64{"arrows": 10, "tens": 5, "xs": 3} {
		if res.Stats[stat] != want {
			t.Errorf("%s = %d, want %d", stat, res.Stats[stat], want)
		}
	}
	if res.MinDurationMs != 11112 {
		t.Errorf("min duration = %d, want 11112", res.MinDurationMs)
	}

	// The wind each arrow flew in is part of what the player saw.
	wantWinds := []int{16, -13, 16, 19, -12, -2, -14, 20, 20, -11}
	g, _ := NewArcheryGame("cafebabe", DefaultArcheryConfig())
	for i, e := range archeryGoldenLog {
		if g.Wind() != wantWinds[i] {
			t.Errorf("arrow %d wind = %d, want %d", i, g.Wind(), wantWinds[i])
		}
		v, _ := parseTickEntry(e, 3)
		if _, err := g.Shoot(v[0], v[1], v[2]); err != nil {
			t.Fatalf("shoot %s: %v", e, err)
		}
	}
}
