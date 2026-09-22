package games

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestSnakeStartsCentredFacingRight(t *testing.T) {
	g, err := NewSnakeGame("00000001", DefaultSnakeConfig())
	if err != nil {
		t.Fatalf("new game: %v", err)
	}
	// 15x15 board, centre (7,7), body stretching left.
	want := []int{7*15 + 7, 7*15 + 6, 7*15 + 5}
	if !reflect.DeepEqual(g.Body(), want) {
		t.Fatalf("body = %v, want %v", g.Body(), want)
	}
	for _, cell := range g.Body() {
		if cell == g.Food() {
			t.Fatal("food spawned on the snake")
		}
	}
}

func TestSnakeFoodIsSeeded(t *testing.T) {
	a, _ := NewSnakeGame("12345678", DefaultSnakeConfig())
	b, _ := NewSnakeGame("12345678", DefaultSnakeConfig())
	if a.Food() != b.Food() {
		t.Fatalf("same seed placed food at %d and %d", a.Food(), b.Food())
	}
}

// Heading straight right from x=7 on a 15-wide board, the head reaches x=14
// after 7 ticks and leaves the board on the 8th. Eating on the way changes
// the length, not the path.
func TestSnakeDiesAtTheWall(t *testing.T) {
	g, _ := NewSnakeGame("00000001", DefaultSnakeConfig())
	for i := 0; i < 100 && !g.Over(); i++ {
		g.Step()
	}
	if !g.dead {
		t.Fatal("expected the snake to hit the wall")
	}
	if g.Ticks() != 8 {
		t.Errorf("died after %d ticks, want 8", g.Ticks())
	}
}

func TestSnakeIgnoresReversal(t *testing.T) {
	g, _ := NewSnakeGame("00000001", DefaultSnakeConfig())
	g.Turn(MoveLeft)
	if g.heading != MoveRight {
		t.Fatalf("reversing into itself changed heading to %s", g.heading)
	}
}

// Chasing your own tail is legal: the tail vacates its cell on the same tick
// the head enters it. One segment longer and the same loop is fatal.
func TestSnakeTailRule(t *testing.T) {
	for _, tc := range []struct {
		length   int
		wantDead bool
	}{{4, false}, {5, true}} {
		t.Run(fmt.Sprintf("length %d", tc.length), func(t *testing.T) {
			cfg := DefaultSnakeConfig()
			cfg.StartLength = tc.length
			g, _ := NewSnakeGame("00000001", cfg)

			// The loop passes (7,8), (6,8) and back to (6,7).
			for _, cell := range []int{8*15 + 7, 8*15 + 6, 7*15 + 6} {
				if g.Food() == cell {
					t.Skip("food sits on the loop for this seed")
				}
			}
			for _, d := range []string{MoveDown, MoveLeft, MoveUp} {
				g.Turn(d)
				g.Step()
			}
			if g.dead != tc.wantDead {
				t.Errorf("dead = %v, want %v", g.dead, tc.wantDead)
			}
		})
	}
}

func TestSnakeSpeedsUpAsItEats(t *testing.T) {
	g, _ := NewSnakeGame("00000001", DefaultSnakeConfig())
	start := g.TickIntervalMs()
	g.foods = 5
	if got, want := g.TickIntervalMs(), start-5*g.cfg.SpeedupMsPerFood; got != want {
		t.Errorf("interval after 5 foods = %d, want %d", got, want)
	}
	g.foods = 1000
	if got := g.TickIntervalMs(); got != g.cfg.MinTickMs {
		t.Errorf("interval never floors: got %d, want %d", got, g.cfg.MinTickMs)
	}
}

func TestReplaySnakeValidatesTheLog(t *testing.T) {
	cfg := DefaultSnakeConfig()
	cases := map[string][]string{
		"empty log":           nil,
		"no end marker":       {"0:up"},
		"end is not last":     {"5:end", "6:up"},
		"bad direction":       {"0:diagonal", "5:end"},
		"ticks out of order":  {"3:up", "2:left", "5:end"},
		"duplicate tick":      {"3:up", "3:left", "5:end"},
		"turn after end":      {"9:up", "5:end"},
		"not tick:direction":  {"up", "5:end"},
		"over the tick limit": {fmt.Sprintf("%d:end", cfg.MaxTicks+1)},
	}
	for name, moves := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ReplaySnake("00000001", cfg, moves); err == nil {
				t.Fatalf("expected %v to be rejected", moves)
			}
		})
	}
}

func TestReplaySnakeMatchesLivePlay(t *testing.T) {
	cfg := DefaultSnakeConfig()
	live, _ := NewSnakeGame("deadbeef", cfg)

	// Zig-zag down the board so the snake survives a while and eats.
	var log []string
	turns := map[int]string{0: MoveDown, 2: MoveLeft, 6: MoveDown, 8: MoveRight, 14: MoveDown, 16: MoveLeft}
	tick := 0
	for ; tick < 20 && !live.Over(); tick++ {
		if d, ok := turns[tick]; ok {
			live.Turn(d)
			log = append(log, fmt.Sprintf("%d:%s", tick, d))
		}
		live.Step()
	}
	log = append(log, fmt.Sprintf("%d:end", live.Ticks()))

	res, err := ReplaySnake("deadbeef", cfg, log)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if res.Score != live.Score() || res.MovesUsed != live.Ticks() {
		t.Errorf("replay (score %d, ticks %d) != live (score %d, ticks %d)",
			res.Score, res.MovesUsed, live.Score(), live.Ticks())
	}
	if res.Stats["length"] != int64(len(live.Body())) {
		t.Errorf("replay length %d, live %d", res.Stats["length"], len(live.Body()))
	}
}

// The speed floor is what stops a sped-up client from playing hundreds of
// ticks in a second.
func TestReplaySnakeReportsAMinimumDuration(t *testing.T) {
	res, err := ReplaySnake("00000001", DefaultSnakeConfig(), []string{"0:down", "5:end"})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if res.MinDurationMs <= 0 {
		t.Fatalf("min duration = %d, want a positive floor", res.MinDurationMs)
	}
	if max := int64(5 * DefaultSnakeConfig().TickMs); res.MinDurationMs > max {
		t.Errorf("min duration %d is above the full tick time %d", res.MinDurationMs, max)
	}
}

// snakeGoldenLog was produced by a food-chasing bot. The Flutter engine
// replays the same log in send-agift-mobile/test/snake_game_test.dart and must
// land on the same numbers — if either side drifts, both fail.
var snakeGoldenLog = strings.Split("0:down,1:left,6:down,12:right,14:up,16:left,19:up,21:right,31:up,35:left,46:up,51:right,54:down,66:right,77:up,87:left,93:down,100:right,104:up,106:left,110:down,115:left,121:up,122:right,131:up,139:left,144:down,154:right,162:up,163:left,177:up,185:right,197:down,202:right,204:up,206:left,207:up,211:left,221:up,223:right,226:down,227:left,230:end", ",")

func TestSnakeCrossLanguageGolden(t *testing.T) {
	res, err := ReplaySnake("cafebabe", DefaultSnakeConfig(), snakeGoldenLog)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if res.Score != 190 || !res.GameOver || res.Won {
		t.Errorf("score %d over %v won %v, want 190 / true / false", res.Score, res.GameOver, res.Won)
	}
	for stat, want := range map[string]int64{"length": 22, "food": 19, "ticks": 230} {
		if res.Stats[stat] != want {
			t.Errorf("%s = %d, want %d", stat, res.Stats[stat], want)
		}
	}
	if res.MinDurationMs != 24299 {
		t.Errorf("min duration = %d, want 24299", res.MinDurationMs)
	}

	// The final position itself, not just the totals.
	g, _ := NewSnakeGame("cafebabe", DefaultSnakeConfig())
	turns := map[int]string{}
	for _, e := range snakeGoldenLog[:len(snakeGoldenLog)-1] {
		tick, dir, _ := parseSnakeEntry(e)
		turns[tick] = dir
	}
	for tick := 0; tick < 230 && !g.Over(); tick++ {
		if d, ok := turns[tick]; ok {
			g.Turn(d)
		}
		g.Step()
	}
	wantBody := []int{49, 50, 51, 36, 35, 34, 33, 48, 63, 64, 65, 66, 67, 68, 69, 70, 71, 72, 73, 88, 103, 118}
	if !reflect.DeepEqual(g.Body(), wantBody) {
		t.Errorf("body = %v\nwant   %v", g.Body(), wantBody)
	}
	if g.Food() != 215 {
		t.Errorf("food = %d, want 215", g.Food())
	}
}
