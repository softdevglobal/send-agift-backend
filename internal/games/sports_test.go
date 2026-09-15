package games

import (
	"fmt"
	"testing"
)

func TestTriangleWave(t *testing.T) {
	// Period 8, amplitude 100: -100, -50, 0, 50, 100, 50, 0, -50, then repeats.
	want := []int{-100, -50, 0, 50, 100, 50, 0, -50, -100}
	for u, w := range want {
		if got := triangle(u, 8, 100); got != w {
			t.Errorf("triangle(%d) = %d, want %d", u, got, w)
		}
	}
}

// ─── Basketball ───────────────────────────────────────────────────────────

func perfectShot(g *BasketballGame, tick int) string {
	aim := g.HoopX(tick + g.cfg.FlightTicks)
	return fmt.Sprintf("%d:%d:%d", tick, aim, BasketballRequiredPower(g.distance))
}

func TestBasketballPerfectShotIsASwish(t *testing.T) {
	g, _ := NewBasketballGame("cafebabe", DefaultBasketballConfig())
	shot, err := g.Shoot(0, g.HoopX(14), BasketballRequiredPower(g.Distance()))
	if err != nil {
		t.Fatalf("shoot: %v", err)
	}
	if !shot.Made || !shot.Swish {
		t.Fatalf("a dead-centre shot should swish: %+v", shot)
	}
}

func TestBasketballPowerAndAimMustBothBeRight(t *testing.T) {
	for name, adjust := range map[string][2]int{
		"too weak":   {0, -8},
		"too strong": {0, 8},
		"off left":   {-13, 0},
		"off right":  {13, 0},
	} {
		t.Run(name, func(t *testing.T) {
			g, _ := NewBasketballGame("cafebabe", DefaultBasketballConfig())
			shot, err := g.Shoot(0, g.HoopX(14)+adjust[0], BasketballRequiredPower(g.Distance())+adjust[1])
			if err != nil {
				t.Fatalf("shoot: %v", err)
			}
			if shot.Made {
				t.Fatalf("expected a miss: %+v", shot)
			}
		})
	}
}

func TestBasketballHoopStartsStillThenMoves(t *testing.T) {
	g, _ := NewBasketballGame("cafebabe", DefaultBasketballConfig())
	for tick := 0; tick < 200; tick++ {
		if g.HoopX(tick) != 0 {
			t.Fatal("the hoop should not move before the first level")
		}
	}
	g.makes = g.cfg.MakesPerLevel
	moved := false
	for tick := 0; tick < 200; tick++ {
		if g.HoopX(tick) != 0 {
			moved = true
		}
		if x := g.HoopX(tick); absInt(x) > 20 {
			t.Fatalf("level 1 hoop at %d, beyond its 20 swing", x)
		}
	}
	if !moved {
		t.Fatal("the hoop should move at level 1")
	}
}

func TestBasketballOnFireDoublesPoints(t *testing.T) {
	g, _ := NewBasketballGame("cafebabe", DefaultBasketballConfig())
	var points []int
	for i := 0; i < 4; i++ {
		tick := i * 20
		shot, err := g.Shoot(tick, g.HoopX(tick+14), BasketballRequiredPower(g.Distance()))
		if err != nil {
			t.Fatalf("shot %d: %v", i, err)
		}
		points = append(points, shot.Points)
	}
	// Each swish is 3 (4 from the far spot); the fourth in a row is doubled.
	if points[3] < 6 || points[3]%2 != 0 {
		t.Fatalf("fourth straight basket should count double, got points %v", points)
	}
}

func TestReplayBasketballValidatesTheLog(t *testing.T) {
	cases := map[string][]string{
		"not three numbers":    {"0:10"},
		"not a number":         {"0:x:50"},
		"negative tick":        {"-1:0:50"},
		"aim out of range":     {"0:101:50"},
		"power out of range":   {"0:0:101"},
		"shot during cooldown": {"0:0:50", "10:0:50"},
		"out of order":         {"40:0:50", "20:0:50"},
		"after the buzzer":     {"900:0:50"},
	}
	for name, moves := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ReplayBasketball("cafebabe", DefaultBasketballConfig(), moves); err == nil {
				t.Fatalf("expected %v to be rejected", moves)
			}
		})
	}
}

func TestReplayBasketballMatchesLivePlay(t *testing.T) {
	live, _ := NewBasketballGame("deadbeef", DefaultBasketballConfig())
	var log []string
	for tick := 3; tick < 900; tick += 29 {
		entry := perfectShot(live, tick)
		v, _ := parseTickEntry(entry, 3)
		if _, err := live.Shoot(v[0], v[1], v[2]); err != nil {
			t.Fatalf("live shot: %v", err)
		}
		log = append(log, entry)
	}
	res, err := ReplayBasketball("deadbeef", DefaultBasketballConfig(), log)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if res.Score != live.Score() || res.Stats["makes"] != int64(live.makes) {
		t.Fatalf("replay %d/%d != live %d/%d", res.Score, res.Stats["makes"], live.Score(), live.makes)
	}
	if res.Stats["makes"] != int64(len(log)) {
		t.Fatalf("a perfect bot should make every shot: %d of %d", res.Stats["makes"], len(log))
	}
}

// ─── Stack Tower ──────────────────────────────────────────────────────────

// bestDropTick finds the tick in the next full swing where the floor sits
// most squarely on the tower.
func bestDropTick(g *StackGame) int {
	best, bestOff := g.layerStart+1, 1<<30
	for tick := g.layerStart + 1; tick <= g.layerStart+g.Period(); tick++ {
		if off := absInt(g.Offset(tick)); off < bestOff {
			best, bestOff = tick, off
		}
	}
	return best
}

func TestStackFloorStartsOffTheTower(t *testing.T) {
	g, _ := NewStackGame("cafebabe", DefaultStackConfig())
	if absInt(g.Offset(0)) != g.cfg.Travel {
		t.Fatalf("offset at start = %d, want ±%d", g.Offset(0), g.cfg.Travel)
	}
}

func TestStackPerfectDropKeepsTheWidth(t *testing.T) {
	g, _ := NewStackGame("cafebabe", DefaultStackConfig())
	drop, err := g.Drop(bestDropTick(g))
	if err != nil {
		t.Fatalf("drop: %v", err)
	}
	if !drop.Perfect || g.Top() != g.floors[0] {
		t.Fatalf("perfect drop should keep the base footprint: %+v top %+v", drop, g.Top())
	}
}

func TestStackOverhangIsSliced(t *testing.T) {
	g, _ := NewStackGame("cafebabe", DefaultStackConfig())
	tick := bestDropTick(g) + 3 // a few ticks late
	offset := g.Offset(tick)
	drop, err := g.Drop(tick)
	if err != nil {
		t.Fatalf("drop: %v", err)
	}
	if drop.Perfect {
		t.Skip("late drop still within the perfect window for this seed")
	}
	top := g.Top()
	if width := top.X1 - top.X0; width != 100-absInt(offset) {
		t.Fatalf("width = %d, want %d", width, 100-absInt(offset))
	}
	if top.Z1-top.Z0 != 100 {
		t.Fatal("the other axis must not change")
	}
}

func TestStackMissingTheTowerEndsTheGame(t *testing.T) {
	g, _ := NewStackGame("cafebabe", DefaultStackConfig())
	// Half a period in, the floor is at the far end of its travel.
	drop, err := g.Drop(g.Period() / 2)
	if err != nil {
		t.Fatalf("drop: %v", err)
	}
	if !drop.Fell {
		t.Fatalf("a drop %d units off a 100-wide tower should fall", g.Offset(g.Period()/2))
	}
	if _, err := g.Drop(g.Period()); err == nil {
		t.Fatal("no drops after the tower fell")
	}
}

func TestStackPerfectRunGrowsTheFloorBack(t *testing.T) {
	g, _ := NewStackGame("cafebabe", DefaultStackConfig())
	// One sloppy drop to lose width, then a run of perfect ones.
	sloppy := bestDropTick(g) + 4
	if _, err := g.Drop(sloppy); err != nil {
		t.Fatalf("drop: %v", err)
	}
	width := func() int {
		top := g.Top()
		return (top.X1 - top.X0) * (top.Z1 - top.Z0)
	}
	before := width()
	for i := 0; i < g.cfg.GrowEveryPerfects; i++ {
		d, err := g.Drop(bestDropTick(g))
		if err != nil || !d.Perfect {
			t.Fatalf("perfect drop %d: %+v %v", i, d, err)
		}
	}
	if width() <= before {
		t.Fatalf("area after %d perfects = %d, want more than %d", g.cfg.GrowEveryPerfects, width(), before)
	}
}

func TestStackSpeedsUpToAFloor(t *testing.T) {
	g, _ := NewStackGame("cafebabe", DefaultStackConfig())
	if g.Period() != 150 {
		t.Fatalf("first period %d, want 150", g.Period())
	}
	for i := 0; i < 40; i++ {
		g.floors = append(g.floors, g.Top())
	}
	if g.Period() != 66 {
		t.Fatalf("period floors at %d, want 66", g.Period())
	}
}

func TestReplayStackValidatesTheLog(t *testing.T) {
	cases := map[string][]string{
		"not a tick":          {"soon"},
		"drop at tick zero":   {"0"},
		"same tick twice":     {"70", "70"},
		"out of order":        {"70", "60"},
		"over the tick limit": {"90001"},
		"drop after it fell":  {"75", "76"},
	}
	for name, moves := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ReplayStack("cafebabe", DefaultStackConfig(), moves); err == nil {
				t.Fatalf("expected %v to be rejected", moves)
			}
		})
	}
}

// ─── Archery ──────────────────────────────────────────────────────────────

func TestArcheryRings(t *testing.T) {
	cases := []struct{ x, y, want int }{
		{0, 0, 10}, {10, 0, 10}, {11, 0, 9}, {0, -20, 9}, {60, 80, 1}, {61, 80, 0}, {200, 0, 0},
	}
	for _, c := range cases {
		if got := ArcheryPoints(c.x, c.y, 10); got != c.want {
			t.Errorf("(%d,%d) = %d, want %d", c.x, c.y, got, c.want)
		}
	}
}

func TestArcheryCompensatingForWindAndSwayHitsTheX(t *testing.T) {
	g, _ := NewArcheryGame("cafebabe", DefaultArcheryConfig())
	sx, sy := g.Sway(40)
	arrow, err := g.Shoot(40, -sx-g.Wind(), -sy)
	if err != nil {
		t.Fatalf("shoot: %v", err)
	}
	if arrow.Points != 10 || !arrow.X {
		t.Fatalf("a fully compensated arrow should hit the X: %+v", arrow)
	}
}

func TestArcheryWindIsSeededAndBounded(t *testing.T) {
	a, _ := NewArcheryGame("12345678", DefaultArcheryConfig())
	b, _ := NewArcheryGame("12345678", DefaultArcheryConfig())
	if a.Wind() != b.Wind() {
		t.Fatal("the same seed must give the same wind")
	}
	for i := 0; i < 200; i++ {
		if w := a.drawWind(); absInt(w) > a.cfg.MaxWind {
			t.Fatalf("wind %d beyond ±%d", w, a.cfg.MaxWind)
		}
	}
}

func TestReplayArcheryValidatesTheLog(t *testing.T) {
	eleven := make([]string, 11)
	for i := range eleven {
		eleven[i] = fmt.Sprintf("%d:0:0", i*30)
	}
	cases := map[string][]string{
		"not three numbers": {"0:0"},
		"aim out of range":  {"0:151:0"},
		"before reloading":  {"0:0:0", "29:0:0"},
		"out of order":      {"60:0:0", "30:0:0"},
		"eleven arrows":     eleven,
	}
	for name, moves := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ReplayArchery("cafebabe", DefaultArcheryConfig(), moves); err == nil {
				t.Fatalf("expected %v to be rejected", moves)
			}
		})
	}
}
