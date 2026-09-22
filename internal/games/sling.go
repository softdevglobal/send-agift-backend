package games

import (
	"encoding/json"
	"fmt"
)

// SlingSlug identifies Sling Shot in the catalog and the engine registry.
const SlingSlug = "sling-shot"

// SlingConfig is the Sling Shot rule set, versioned like every game.
type SlingConfig struct {
	TickMs            int `json:"tick_ms"`
	Levels            int `json:"levels"`
	ShotsPerLevel     int `json:"shots_per_level"`
	Gravity           int `json:"gravity"`
	LaunchScale       int `json:"launch_scale"`
	MaxPull           int `json:"max_pull"`
	MaxFlightTicks    int `json:"max_flight_ticks"`
	TargetPoints      int `json:"target_points"`
	WoodPoints        int `json:"wood_points"`
	ShotBonus         int `json:"shot_bonus"`
	MinMsPerShot      int `json:"min_ms_per_shot"`
	SessionTTLSeconds int `json:"session_ttl_seconds"`
}

// DefaultSlingConfig mirrors the 1.0.0 version seeded by migration 000031.
func DefaultSlingConfig() SlingConfig {
	return SlingConfig{
		TickMs:            20,
		Levels:            8,
		ShotsPerLevel:     3,
		Gravity:           6,
		LaunchScale:       3,
		MaxPull:           100,
		MaxFlightTicks:    360,
		TargetPoints:      500,
		WoodPoints:        50,
		ShotBonus:         300,
		MinMsPerShot:      700,
		SessionTTLSeconds: defaultSessionTTLSeconds,
	}
}

func (c SlingConfig) withDefaults() SlingConfig {
	d := DefaultSlingConfig()
	fill := func(v *int, def int) {
		if *v <= 0 {
			*v = def
		}
	}
	fill(&c.TickMs, d.TickMs)
	fill(&c.Levels, d.Levels)
	fill(&c.ShotsPerLevel, d.ShotsPerLevel)
	fill(&c.Gravity, d.Gravity)
	fill(&c.LaunchScale, d.LaunchScale)
	fill(&c.MaxPull, d.MaxPull)
	fill(&c.MaxFlightTicks, d.MaxFlightTicks)
	fill(&c.TargetPoints, d.TargetPoints)
	fill(&c.WoodPoints, d.WoodPoints)
	fill(&c.ShotBonus, d.ShotBonus)
	fill(&c.MinMsPerShot, d.MinMsPerShot)
	return c
}

// World geometry, in whole units (positions and velocities during flight are
// in sixteenths of a unit).
const (
	SlingCell   = 50
	SlingRadius = 14
	SlingStartX = 100
	SlingStartY = 150
	SlingWorldW = 1000
)

// slingTemplates are the structures, rows top to bottom: T target, W wood,
// S stone. Their order is part of the rules — the seed picks one by index.
var slingTemplates = [][]string{
	{".T.", ".W.", "WWW"},
	{"T.T", "W.W", "WSW"},
	{".T.", "WTW", "WWW", "W.W"},
	{"..T..", ".WWW.", ".W.W.", "SW.WS"},
	{"T...T", "S...S", "S.T.S", "SSSSS"},
	{"...T...", "..WWW..", ".WTWTW.", "SSSSSSS"},
}

// SlingBlock is one block of the structure.
type SlingBlock struct {
	ID    int
	X, Y  int
	W, H  int
	Kind  byte
	Alive bool
}

// SlingGame is Sling Shot: pull back the sling and knock the target blocks
// off their structure. Wood breaks and slows the shot, stone stops it, and
// anything left unsupported falls — far enough, and it breaks. Clear every
// target to move up a level; unused shots are a bonus.
//
// The structures are drawn from the seed and fully visible before the first
// shot, and the flight is integer ballistics, so the server replays exactly
// the shot the player took.
//
// The Dart implementation in the mobile app mirrors this file exactly.
type SlingGame struct {
	cfg       SlingConfig
	rng       *DeterministicRNG
	level     int
	cleared   int
	shotsLeft int
	shots     int
	targets   int
	blocks    []SlingBlock
	score     int64
	over      bool
	won       bool
}

// NewSlingGame builds the first level from the seed.
func NewSlingGame(seed string, cfg SlingConfig) (*SlingGame, error) {
	state, err := ParseSeed(seed)
	if err != nil {
		return nil, err
	}
	g := &SlingGame{cfg: cfg.withDefaults(), rng: NewRNG(state)}
	g.buildLevel()
	return g, nil
}

func (g *SlingGame) buildLevel() {
	choices := g.level + 2
	if choices > len(slingTemplates) {
		choices = len(slingTemplates)
	}
	tpl := slingTemplates[g.rng.NextInt(choices)]
	width := len(tpl[0]) * SlingCell
	baseX := 980 - width - g.rng.NextInt(4)*30
	g.blocks = g.blocks[:0]
	id := 0
	for r, row := range tpl {
		y := (len(tpl) - 1 - r) * SlingCell
		for c := 0; c < len(row); c++ {
			if row[c] == '.' {
				continue
			}
			g.blocks = append(g.blocks, SlingBlock{
				ID: id, X: baseX + c*SlingCell, Y: y, W: SlingCell, H: SlingCell,
				Kind: row[c], Alive: true,
			})
			id++
		}
	}
	g.shotsLeft = g.cfg.ShotsPerLevel
}

func (g *SlingGame) Score() int64 { return g.score }
func (g *SlingGame) Over() bool   { return g.over }

func (g *SlingGame) targetsAlive() int {
	n := 0
	for _, b := range g.blocks {
		if b.Alive && b.Kind == 'T' {
			n++
		}
	}
	return n
}

func (g *SlingGame) destroy(b *SlingBlock) {
	b.Alive = false
	switch b.Kind {
	case 'T':
		g.score += int64(g.cfg.TargetPoints)
		g.targets++
	case 'W':
		g.score += int64(g.cfg.WoodPoints)
	}
}

func (g *SlingGame) collides(b SlingBlock, px, py int) bool {
	return px >= (b.X-SlingRadius)*16 && px <= (b.X+b.W+SlingRadius)*16 &&
		py >= (b.Y-SlingRadius)*16 && py <= (b.Y+b.H+SlingRadius)*16
}

// ValidPull reports whether a launch vector is one the sling can produce.
func (g *SlingGame) ValidPull(dx, dy int) bool {
	return dx >= 1 && absInt(dy) <= g.cfg.MaxPull && dx*dx+dy*dy <= g.cfg.MaxPull*g.cfg.MaxPull
}

// Shoot launches with velocity (dx, dy) — up is positive dy.
func (g *SlingGame) Shoot(dx, dy int) error {
	if g.over {
		return fmt.Errorf("%w: shot after the game ended", ErrInvalidMove)
	}
	if !g.ValidPull(dx, dy) {
		return fmt.Errorf("%w: pull (%d,%d) is out of range", ErrInvalidMove, dx, dy)
	}

	px, py := SlingStartX*16, SlingStartY*16
	vx, vy := dx*g.cfg.LaunchScale, dy*g.cfg.LaunchScale
	for t := 0; t < g.cfg.MaxFlightTicks; t++ {
		vy -= g.cfg.Gravity
		px += vx
		py += vy

		stop := false
		for i := range g.blocks {
			b := &g.blocks[i]
			if !b.Alive || !g.collides(*b, px, py) {
				continue
			}
			switch b.Kind {
			case 'T':
				g.destroy(b)
				vx, vy = vx*3/4, vy*3/4
			case 'W':
				g.destroy(b)
				vx, vy = vx/2, vy/2
			default: // stone stops the shot dead
				stop = true
			}
			break
		}
		if stop || py <= SlingRadius*16 || px > 1100*16 || px < -100*16 ||
			absInt(vx)+absInt(vy) < 8 {
			break
		}
	}

	g.collapse()
	g.shots++
	g.shotsLeft--
	switch {
	case g.targetsAlive() == 0:
		g.score += int64(g.cfg.ShotBonus * g.shotsLeft)
		g.cleared++
		g.level++
		if g.level >= g.cfg.Levels {
			g.won, g.over = true, true
		} else {
			g.buildLevel()
		}
	case g.shotsLeft == 0:
		g.over = true
	}
	return nil
}

// collapse lets every unsupported block fall onto whatever is below it,
// lowest first, until nothing moves. A target falling half a cell or wood
// falling a whole cell breaks.
func (g *SlingGame) collapse() {
	for changed := true; changed; {
		changed = false
		for _, i := range g.byHeight() {
			b := &g.blocks[i]
			if !b.Alive {
				continue
			}
			support := 0
			for j := range g.blocks {
				o := g.blocks[j]
				if j == i || !o.Alive || o.X >= b.X+b.W || o.X+o.W <= b.X {
					continue
				}
				if top := o.Y + o.H; top <= b.Y && top > support {
					support = top
				}
			}
			if support < b.Y {
				fall := b.Y - support
				b.Y = support
				changed = true
				if (b.Kind == 'T' && fall >= SlingCell/2) || (b.Kind == 'W' && fall >= SlingCell) {
					g.destroy(b)
				}
			}
		}
	}
}

// byHeight lists alive block indices lowest first, then left to right.
func (g *SlingGame) byHeight() []int {
	var order []int
	for i, b := range g.blocks {
		if b.Alive {
			order = append(order, i)
		}
	}
	for i := 1; i < len(order); i++ {
		for j := i; j > 0; j-- {
			a, b := g.blocks[order[j-1]], g.blocks[order[j]]
			if a.Y < b.Y || (a.Y == b.Y && a.X <= b.X) {
				break
			}
			order[j-1], order[j] = order[j], order[j-1]
		}
	}
	return order
}

// ReplaySling replays a Sling Shot log: one "<dx>:<dy>" per shot.
func ReplaySling(seed string, cfg SlingConfig, moves []string) (*Result, error) {
	cfg = cfg.withDefaults()
	g, err := NewSlingGame(seed, cfg)
	if err != nil {
		return nil, err
	}
	for i, m := range moves {
		v, err := parseTickEntry(m, 2)
		if err != nil {
			return nil, err
		}
		if err := g.Shoot(v[0], v[1]); err != nil {
			return nil, fmt.Errorf("shot %d: %w", i, err)
		}
	}
	return &Result{
		Score:         g.score,
		MovesUsed:     g.shots,
		GameOver:      g.over,
		Won:           g.won,
		MinDurationMs: int64(g.shots) * int64(cfg.MinMsPerShot),
		Stats: map[string]int64{
			"levels":  int64(g.cleared),
			"targets": int64(g.targets),
			"shots":   int64(g.shots),
		},
	}, nil
}

type slingEngine struct{}

func (slingEngine) Replay(seed string, raw json.RawMessage, moves []string) (*Result, error) {
	var cfg SlingConfig
	if err := decodeConfig(raw, &cfg); err != nil {
		return nil, err
	}
	return ReplaySling(seed, cfg, moves)
}
