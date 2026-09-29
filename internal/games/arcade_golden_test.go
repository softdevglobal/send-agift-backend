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

// Played on the harder road of migration 000041 by a bot that holds a
// target speed, braking into the crests, until a landing goes wrong.
var hillGoldenLog = strings.Split("0:g,20:b,30:g,50:b,60:g,70:b,80:g,110:b,120:g,140:b,150:g,170:b,180:g,210:b,220:g,230:n,240:g,250:b,260:g,270:b,280:g,290:n,300:g,310:b,320:g,330:n,340:g,450:n,460:b,470:g,510:b,570:n,580:g,590:b,600:g,610:b,620:n,630:g,640:b,650:g,660:n,670:g,680:b,690:g,700:n,710:b,720:n,730:g,810:n,820:g,830:b,840:g,880:n,890:g,920:n,930:g,940:n,950:g,980:b,990:n,1010:g,1020:n,1030:g,1040:b,1050:g,1070:b,1117:end", ",")

func TestHillCrossLanguageGolden(t *testing.T) {
	res, err := ReplayHill("cafebabe", DefaultHillConfig(), hillGoldenLog)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if res.Score != 682 || !res.GameOver || res.MinDurationMs != 17872 {
		t.Errorf("score %d over %v min %d, want 682 / true / 17872", res.Score, res.GameOver, res.MinDurationMs)
	}
	for stat, want := range map[string]int64{"distance": 651, "air_ticks": 157, "fuel_cans": 2} {
		if res.Stats[stat] != want {
			t.Errorf("%s = %d, want %d", stat, res.Stats[stat], want)
		}
	}
}

// Bubble Shooter's log is a mix of aims — sideways units per 1000 of rise,
// so the flight banks off the walls — and "s" swaps of the two queued
// colours. It was played by a bot that picks the best shot available, which
// is what makes it worth pinning: a bot firing at random on this same seed
// pops nothing at all, so these numbers only hold if the flight physics and
// the queue both replay identically on the client.
var bubbleGoldenLog = strings.Split("-4000,-4000,-4000,s,-3400,s,-3000,s,-3000,-3200,s,-1000,s,-1000,-4000,s,-4000,-4000,-4000,s,-2800,-4000,-4000,s,-2600,-4000,s,-2600,s,-3600,s,-1800,-600,s,-800,-4000,-4000,s,-3400,-4000,-4000,-4000,-4000,-4000,s,-3600,-4000,-4000,s,-3800,-4000,-4000,s,-2600,-4000,-4000,s,-2800,-4000,-4000,s,-3200,-4000,-4000,-4000,-4000,-4000,-4000,-4000,-4000,-4000,-4000,-4000,-4000,-4000,-4000,-4000,-4000", ",")

func TestBubbleCrossLanguageGolden(t *testing.T) {
	res, err := ReplayBubble("cafebabe", DefaultBubbleConfig(), bubbleGoldenLog)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if res.Score != 810 || res.GameOver || res.MinDurationMs != 13200 {
		t.Errorf("score %d over %v min %d, want 810 / false / 13200",
			res.Score, res.GameOver, res.MinDurationMs)
	}
	for stat, want := range map[string]int64{"pops": 71, "shots": 60, "best_combo": 1} {
		if res.Stats[stat] != want {
			t.Errorf("%s = %d, want %d", stat, res.Stats[stat], want)
		}
	}
}
