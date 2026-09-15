package games

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// The logs below were produced by bots on seed "cafebabe". The Flutter
// engines replay the same logs in send-agift-mobile/test/*_test.dart and must
// land on the same numbers — if either side drifts, both fail.

var blockBlastGoldenLog = strings.Split("0:0:0,1:0:4,2:0:5,1:0:6,0:0:0,2:0:1,0:0:6,1:0:4,2:1:0,0:0:3,1:5:3,2:0:3,0:2:2,1:1:4,2:1:0,2:3:0,0:0:0,1:4:0,0:4:4,1:2:2,2:0:4,0:1:5,1:2:0,2:2:4,0:4:4,1:2:6,2:3:0,0:4:0,2:5:0,1:0:0,0:6:2,1:2:5,2:4:2,0:1:0,2:6:0,1:3:0,0:4:4,1:2:2,2:4:5,2:5:6,0:4:0,1:6:0,0:4:3,1:0:0,2:4:1,1:7:1,0:0:2,2:2:0,0:0:6,1:1:6", ",")

func TestBlockBlastCrossLanguageGolden(t *testing.T) {
	res, err := ReplayBlockBlast("cafebabe", DefaultBlockBlastConfig(), blockBlastGoldenLog)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if res.Score != 382 || !res.GameOver || res.MinDurationMs != 12500 {
		t.Errorf("score %d over %v min %d, want 382 / true / 12500", res.Score, res.GameOver, res.MinDurationMs)
	}
	for stat, want := range map[string]int64{"lines": 18, "best_combo": 2, "placed": 50} {
		if res.Stats[stat] != want {
			t.Errorf("%s = %d, want %d", stat, res.Stats[stat], want)
		}
	}

	g, _ := NewBlockBlastGame("cafebabe", DefaultBlockBlastConfig())
	for _, e := range blockBlastGoldenLog {
		v, _ := parseTickEntry(e, 3)
		if _, err := g.Place(v[0], v[1], v[2]); err != nil {
			t.Fatalf("place %s: %v", e, err)
		}
	}
	wantBoard := []int{19, 19, 5, 0, 2, 2, 14, 14, 0, 0, 5, 0, 0, 13, 9, 14, 23, 23, 5, 0, 23, 23, 9, 17, 24, 23, 23, 0, 8, 15, 9, 0, 23, 18, 0, 18, 0, 20, 9, 20, 0, 18, 0, 0, 0, 0, 9, 0, 0, 0, 0, 0, 16, 24, 0, 17, 22, 4, 0, 4, 0, 0, 0, 17}
	if !reflect.DeepEqual(g.board, wantBoard) {
		t.Errorf("board = %v", g.board)
	}
	if fmt.Sprint(g.Hand()) != "[-1 -1 7]" {
		t.Errorf("hand = %v, want [-1 -1 7]", g.Hand())
	}
}

var cricketGoldenLog = strings.Split("92:-80,251:-80,409:79,584:-80,740:-80,902:-80,1058:13,1378:-80,1523:-80,1686:13,1845:-80", ",")

func TestCricketCrossLanguageGolden(t *testing.T) {
	g, _ := NewCricketGame("cafebabe", DefaultCricketConfig())
	if got := fmt.Sprint(g.balls); got != "[{47 -1} {44 1} {48 1} {59 -1} {50 1} {56 1} {54 -1} {50 -1} {50 1} {38 -1} {47 1} {38 0}]" {
		t.Errorf("balls = %s", got)
	}
	if got := fmt.Sprint(g.fielders); got != "[[79 56 -32 65 69] [13 69 39 -66 -67]]" {
		t.Errorf("fielders = %s", got)
	}

	res, err := ReplayCricket("cafebabe", DefaultCricketConfig(), cricketGoldenLog)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if res.Score != 43 || res.MinDurationMs != 29520 {
		t.Errorf("runs %d min %d, want 43 / 29520", res.Score, res.MinDurationMs)
	}
	for stat, want := range map[string]int64{"fours": 3, "sixes": 5, "wickets": 2} {
		if res.Stats[stat] != want {
			t.Errorf("%s = %d, want %d", stat, res.Stats[stat], want)
		}
	}
}

var slingGoldenLog = strings.Split("60:72,84:44,60:72,56:68,80:48,84:40,80:48,48:72,32:92", ",")

func TestSlingCrossLanguageGolden(t *testing.T) {
	res, err := ReplaySling("cafebabe", DefaultSlingConfig(), slingGoldenLog)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if res.Score != 13250 || !res.Won || !res.GameOver || res.MinDurationMs != 6300 {
		t.Errorf("score %d won %v over %v min %d, want 13250 / true / true / 6300",
			res.Score, res.Won, res.GameOver, res.MinDurationMs)
	}
	for stat, want := range map[string]int64{"levels": 8, "targets": 14, "shots": 9} {
		if res.Stats[stat] != want {
			t.Errorf("%s = %d, want %d", stat, res.Stats[stat], want)
		}
	}
}

var hillGoldenLog = strings.Split("0:g,70:n,90:g,160:n,180:g,250:n,270:g,285:b,300:g,340:n,360:g,430:n,450:g,520:n,540:g,585:b,600:g,610:n,630:g,700:n,720:g,790:n,810:g,880:n,900:g,970:n,990:g,1060:n,1080:g,1150:n,1170:g,1183:end", ",")

func TestHillCrossLanguageGolden(t *testing.T) {
	res, err := ReplayHill("cafebabe", DefaultHillConfig(), hillGoldenLog)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if res.Score != 927 || !res.GameOver || res.MinDurationMs != 18928 {
		t.Errorf("score %d over %v min %d, want 927 / true / 18928", res.Score, res.GameOver, res.MinDurationMs)
	}
	for stat, want := range map[string]int64{"distance": 892, "air_ticks": 177, "fuel_cans": 3} {
		if res.Stats[stat] != want {
			t.Errorf("%s = %d, want %d", stat, res.Stats[stat], want)
		}
	}
}
