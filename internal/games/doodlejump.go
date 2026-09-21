package games

import (
	"encoding/json"
	"fmt"
)

// DoodleSlug identifies Doodle Jump in the catalog and the engine registry.
const DoodleSlug = "doodle-jump"

// DoodleConfig is the Doodle Jump rule set, versioned like every game.
type DoodleConfig struct {
	Lanes             int `json:"lanes"`
	Platforms         int `json:"platforms"`
	SpringEvery       int `json:"spring_every"`
	PointsPerHop      int `json:"points_per_hop"`
	SpringBonus       int `json:"spring_bonus"`
	HeightBonusEvery  int `json:"height_bonus_every"`
	HeightBonus       int `json:"height_bonus"`
	MinMsPerHop       int `json:"min_ms_per_hop"`
	SessionTTLSeconds int `json:"session_ttl_seconds"`
}

// DefaultDoodleConfig mirrors the 1.0.0 version seeded by migration 000032.
func DefaultDoodleConfig() DoodleConfig {
	return DoodleConfig{
		Lanes:             5,
		Platforms:         120,
		SpringEvery:       9,
		PointsPerHop:      8,
		SpringBonus:       14,
		HeightBonusEvery:  10,
		HeightBonus:       25,
		MinMsPerHop:       200,
		SessionTTLSeconds: defaultSessionTTLSeconds,
	}
}

func (c DoodleConfig) withDefaults() DoodleConfig {
	d := DefaultDoodleConfig()
	fill := func(v *int, def int) {
		if *v <= 0 {
			*v = def
		}
	}
	fill(&c.Lanes, d.Lanes)
	fill(&c.Platforms, d.Platforms)
	fill(&c.SpringEvery, d.SpringEvery)
	fill(&c.PointsPerHop, d.PointsPerHop)
	fill(&c.SpringBonus, d.SpringBonus)
	fill(&c.HeightBonusEvery, d.HeightBonusEvery)
	fill(&c.HeightBonus, d.HeightBonus)
	fill(&c.MinMsPerHop, d.MinMsPerHop)
	fill(&c.SessionTTLSeconds, d.SessionTTLSeconds)
	return c
}

// DoodlePlatform is one rung of the tower: the lane always in reach, an
// optional second lane off to the side, and whether landing on it springs.
type DoodlePlatform struct {
	Lane   int
	Alt    int // a second ledge on this rung, or -1 when there is only one
	Spring bool
}

// Has reports whether this rung has a ledge in a lane.
func (p DoodlePlatform) Has(lane int) bool {
	return p.Lane == lane || (p.Alt >= 0 && p.Alt == lane)
}

// DoodleGame is Doodle Jump: the player hops up a tower of ledges, choosing a
// lane each time. Reachable lanes are limited to the one under them and its
// neighbours, so a ledge two lanes across is a miss and the round is over.
// Springs throw the player two ledges up at once and pay a bonus, and every
// tenth ledge pays a height bonus — so the climb rewards reading ahead.
//
// The tower is drawn from the seed, so the server replays the same ledges the
// player was looking at. The Dart implementation in the mobile app mirrors
// this file.
type DoodleGame struct {
	cfg       DoodleConfig
	platforms []DoodlePlatform

	lane    int
	height  int // index of the platform currently stood on
	hops    int
	springs int
	score   int64
	fell    bool
}

// NewDoodleGame builds the tower and stands the player on its first ledge.
func NewDoodleGame(seed string, cfg DoodleConfig) (*DoodleGame, error) {
	state, err := ParseSeed(seed)
	if err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()

	rng := NewRNG(state)
	platforms := make([]DoodlePlatform, 0, cfg.Platforms)
	lane := rng.NextInt(cfg.Lanes)
	for i := 0; i < cfg.Platforms; i++ {
		if i > 0 {
			// Each rung's main ledge stays within one lane of the last, so the
			// tower is always climbable. Drawing lanes freely would let two
			// rungs sit four lanes apart, ending the run through no fault of
			// the player — unwinnable, and unfair to score.
			step := rng.NextInt(3) - 1
			lane += step
			if lane < 0 {
				lane = 0
			}
			if lane >= cfg.Lanes {
				lane = cfg.Lanes - 1
			}
		}
		// Roughly every third rung carries a second ledge, which is where the
		// choice lives: the far one may line up better with what is above it.
		alt := -1
		if rng.NextInt(3) == 0 {
			candidate := rng.NextInt(cfg.Lanes)
			if candidate != lane {
				alt = candidate
			}
		}
		// Springs sit on a fixed cadence so the climb stays replayable, but
		// where they sit across the tower still comes from the seed.
		platforms = append(platforms, DoodlePlatform{
			Lane:   lane,
			Alt:    alt,
			Spring: i > 0 && i%cfg.SpringEvery == 0,
		})
	}
	return &DoodleGame{
		cfg:       cfg,
		platforms: platforms,
		lane:      platforms[0].Lane,
	}, nil
}

func (g *DoodleGame) Score() int64                { return g.score }
func (g *DoodleGame) Height() int                 { return g.height }
func (g *DoodleGame) Hops() int                   { return g.hops }
func (g *DoodleGame) Lane() int                   { return g.lane }
func (g *DoodleGame) Fell() bool                  { return g.fell }
func (g *DoodleGame) Platforms() []DoodlePlatform { return g.platforms }

// Reached reports whether the climb has run out of tower.
func (g *DoodleGame) Topped() bool { return g.height >= len(g.platforms)-1 }

// PlatformAt is the ledge at a height, for the client to draw.
func (g *DoodleGame) PlatformAt(index int) DoodlePlatform {
	if index < 0 || index >= len(g.platforms) {
		return DoodlePlatform{Lane: -1, Alt: -1}
	}
	return g.platforms[index]
}

// Hop jumps to the next ledge, landing in the chosen lane.
func (g *DoodleGame) Hop(lane int) (bool, error) {
	switch {
	case g.fell:
		return false, fmt.Errorf("%w: hop after falling", ErrInvalidMove)
	case lane < 0 || lane >= g.cfg.Lanes:
		return false, fmt.Errorf("%w: lane %d does not exist", ErrInvalidMove, lane)
	case g.Topped():
		return false, fmt.Errorf("%w: the tower is climbed", ErrInvalidMove)
	case absInt(lane-g.lane) > 1:
		return false, fmt.Errorf("%w: lane %d is out of reach from %d", ErrInvalidMove, lane, g.lane)
	}

	next := g.platforms[g.height+1]
	g.hops++
	g.lane = lane
	if !next.Has(lane) {
		// Nothing under the landing: the climb ends here.
		g.fell = true
		return false, nil
	}

	g.height++
	g.score += int64(g.cfg.PointsPerHop)
	if next.Spring {
		g.springs++
		g.score += int64(g.cfg.SpringBonus)
		// A spring carries the player over the following ledge.
		if !g.Topped() {
			g.height++
			g.lane = g.platforms[g.height].Lane
		}
	}
	if g.height%g.cfg.HeightBonusEvery == 0 {
		g.score += int64(g.cfg.HeightBonus)
	}
	return true, nil
}

// ReplayDoodle replays a Doodle Jump log: the lane of every hop.
func ReplayDoodle(seed string, cfg DoodleConfig, moves []string) (*Result, error) {
	cfg = cfg.withDefaults()
	g, err := NewDoodleGame(seed, cfg)
	if err != nil {
		return nil, err
	}
	for i, m := range moves {
		fields, err := parseTickEntry(m, 1)
		if err != nil {
			return nil, fmt.Errorf("hop %d: %w", i, err)
		}
		if _, err := g.Hop(fields[0]); err != nil {
			return nil, fmt.Errorf("hop %d: %w", i, err)
		}
	}
	return &Result{
		Score:         g.score,
		MovesUsed:     len(moves),
		Won:           g.Topped(),
		GameOver:      g.fell || g.Topped(),
		MinDurationMs: int64(g.hops) * int64(cfg.MinMsPerHop),
		Stats: map[string]int64{
			"height":  int64(g.height),
			"hops":    int64(g.hops),
			"springs": int64(g.springs),
		},
	}, nil
}

type doodleEngine struct{}

func (doodleEngine) Replay(seed string, raw json.RawMessage, moves []string) (*Result, error) {
	var cfg DoodleConfig
	if err := decodeConfig(raw, &cfg); err != nil {
		return nil, err
	}
	return ReplayDoodle(seed, cfg, moves)
}
