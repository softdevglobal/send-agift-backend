package games

import (
	"encoding/json"
	"fmt"
)

// BasketballSlug identifies Basketball in the catalog and the engine registry.
const BasketballSlug = "basketball"

// BasketballConfig is the Basketball rule set, versioned like every game.
type BasketballConfig struct {
	TickMs              int `json:"tick_ms"`
	RoundTicks          int `json:"round_ticks"`
	FlightTicks         int `json:"flight_ticks"`
	ShotCooldownTicks   int `json:"shot_cooldown_ticks"`
	AimTolerance        int `json:"aim_tolerance"`
	PowerTolerance      int `json:"power_tolerance"`
	SwishAimTolerance   int `json:"swish_aim_tolerance"`
	SwishPowerTolerance int `json:"swish_power_tolerance"`
	MakesPerLevel       int `json:"makes_per_level"`
	SessionTTLSeconds   int `json:"session_ttl_seconds"`
}

// DefaultBasketballConfig mirrors the 1.0.0 version seeded by migration 000030.
func DefaultBasketballConfig() BasketballConfig {
	return BasketballConfig{
		TickMs:              50,
		RoundTicks:          900, // 45 seconds
		FlightTicks:         14,
		ShotCooldownTicks:   20,
		AimTolerance:        12,
		PowerTolerance:      7,
		SwishAimTolerance:   4,
		SwishPowerTolerance: 3,
		MakesPerLevel:       4,
		SessionTTLSeconds:   defaultSessionTTLSeconds,
	}
}

func (c BasketballConfig) withDefaults() BasketballConfig {
	d := DefaultBasketballConfig()
	if c.TickMs <= 0 {
		c.TickMs = d.TickMs
	}
	if c.RoundTicks <= 0 {
		c.RoundTicks = d.RoundTicks
	}
	if c.FlightTicks <= 0 {
		c.FlightTicks = d.FlightTicks
	}
	if c.ShotCooldownTicks <= 0 {
		c.ShotCooldownTicks = d.ShotCooldownTicks
	}
	// A new ball only appears once the last one has landed.
	if c.ShotCooldownTicks < c.FlightTicks {
		c.ShotCooldownTicks = c.FlightTicks
	}
	if c.AimTolerance <= 0 {
		c.AimTolerance = d.AimTolerance
	}
	if c.PowerTolerance <= 0 {
		c.PowerTolerance = d.PowerTolerance
	}
	if c.SwishAimTolerance <= 0 {
		c.SwishAimTolerance = d.SwishAimTolerance
	}
	if c.SwishPowerTolerance <= 0 {
		c.SwishPowerTolerance = d.SwishPowerTolerance
	}
	if c.MakesPerLevel <= 0 {
		c.MakesPerLevel = d.MakesPerLevel
	}
	return c
}

const (
	// Aim is the sideways spot on the court the shot is thrown at, and power
	// how hard; both are whole numbers so they replay exactly.
	BasketballAimLimit = 100
	BasketballMaxPower = 100
	// Shooting spots, nearest to farthest. The farthest is a three-pointer.
	BasketballSpots = 4
)

// BasketballRequiredPower is the power that lands a shot from a spot.
// The spot is on screen before every shot, so this is learnable skill.
func BasketballRequiredPower(distance int) int { return 40 + 15*distance }

// BasketballShot is the outcome of one shot.
type BasketballShot struct {
	Tick, Aim, Power, Distance, HoopX int
	Made, Swish                       bool
	Points                            int
}

// BasketballGame is a timed shoot-out: as many baskets as you can in the
// round. Each shot is aimed sideways and thrown with a power; the ball flies
// for a fixed number of ticks, so once the hoop starts moving the player has
// to lead it. Every position is a pure function of the tick, so the server
// replays exactly what the player saw.
//
// The Dart implementation in the mobile app mirrors this file exactly.
type BasketballGame struct {
	cfg        BasketballConfig
	rng        *DeterministicRNG
	phase      int
	distance   int
	makes      int
	shots      int
	swishes    int
	streak     int
	bestStreak int
	score      int64
	lastTick   int
}

// NewBasketballGame sets the hoop's rhythm and the first spot from the seed.
func NewBasketballGame(seed string, cfg BasketballConfig) (*BasketballGame, error) {
	state, err := ParseSeed(seed)
	if err != nil {
		return nil, err
	}
	g := &BasketballGame{cfg: cfg.withDefaults(), rng: NewRNG(state), lastTick: -1}
	g.phase = g.rng.NextInt(97)
	g.distance = g.rng.NextInt(BasketballSpots)
	return g, nil
}

func (g *BasketballGame) Score() int64  { return g.score }
func (g *BasketballGame) Distance() int { return g.distance }

// Level rises every few baskets; from level 1 the hoop moves, faster and
// wider as the level climbs.
func (g *BasketballGame) Level() int { return g.makes / g.cfg.MakesPerLevel }

// HoopX is where the hoop is at a tick, at the current level.
func (g *BasketballGame) HoopX(tick int) int {
	level := g.Level()
	if level == 0 {
		return 0
	}
	amp := 20 * level
	if amp > 70 {
		amp = 70
	}
	period := 120 - 16*level
	if period < 48 {
		period = 48
	}
	return triangle(tick+g.phase+13*level, period, amp)
}

// CanShoot reports whether a ball is ready at a tick.
func (g *BasketballGame) CanShoot(tick int) bool {
	if tick < 0 || tick >= g.cfg.RoundTicks {
		return false
	}
	return g.lastTick < 0 || tick >= g.lastTick+g.cfg.ShotCooldownTicks
}

// Shoot throws the ball. The hoop is judged where it will be when the ball
// arrives, FlightTicks later.
func (g *BasketballGame) Shoot(tick, aim, power int) (BasketballShot, error) {
	if !g.CanShoot(tick) {
		return BasketballShot{}, fmt.Errorf("%w: no ball ready at tick %d", ErrInvalidMove, tick)
	}
	if absInt(aim) > BasketballAimLimit || power < 0 || power > BasketballMaxPower {
		return BasketballShot{}, fmt.Errorf("%w: aim %d / power %d out of range", ErrInvalidMove, aim, power)
	}

	hoop := g.HoopX(tick + g.cfg.FlightTicks)
	aimErr := absInt(aim - hoop)
	powerErr := absInt(power - BasketballRequiredPower(g.distance))

	shot := BasketballShot{Tick: tick, Aim: aim, Power: power, Distance: g.distance, HoopX: hoop}
	shot.Made = aimErr <= g.cfg.AimTolerance && powerErr <= g.cfg.PowerTolerance
	shot.Swish = shot.Made && aimErr <= g.cfg.SwishAimTolerance && powerErr <= g.cfg.SwishPowerTolerance

	if shot.Made {
		points := 2
		if g.distance == BasketballSpots-1 {
			points = 3
		}
		if shot.Swish {
			points++
		}
		// Three in a row and you are on fire: every basket counts double.
		if g.streak >= 3 {
			points *= 2
		}
		shot.Points = points
		g.score += int64(points)
		g.makes++
		g.streak++
		if shot.Swish {
			g.swishes++
		}
		if g.streak > g.bestStreak {
			g.bestStreak = g.streak
		}
	} else {
		g.streak = 0
	}

	g.shots++
	g.lastTick = tick
	g.distance = g.rng.NextInt(BasketballSpots)
	return shot, nil
}

// ReplayBasketball replays a Basketball log: one "<tick>:<aim>:<power>" per
// shot, in the order they were thrown.
func ReplayBasketball(seed string, cfg BasketballConfig, moves []string) (*Result, error) {
	cfg = cfg.withDefaults()
	g, err := NewBasketballGame(seed, cfg)
	if err != nil {
		return nil, err
	}
	for i, m := range moves {
		v, err := parseTickEntry(m, 3)
		if err != nil {
			return nil, err
		}
		if _, err := g.Shoot(v[0], v[1], v[2]); err != nil {
			return nil, fmt.Errorf("shot %d: %w", i, err)
		}
	}
	return &Result{
		Score:         g.score,
		MovesUsed:     g.shots,
		GameOver:      true,
		MinDurationMs: tickFloorMs(g.lastTick, cfg.TickMs),
		Stats: map[string]int64{
			"makes":       int64(g.makes),
			"shots":       int64(g.shots),
			"swishes":     int64(g.swishes),
			"best_streak": int64(g.bestStreak),
		},
	}, nil
}

type basketballEngine struct{}

func (basketballEngine) Replay(seed string, raw json.RawMessage, moves []string) (*Result, error) {
	var cfg BasketballConfig
	if err := decodeConfig(raw, &cfg); err != nil {
		return nil, err
	}
	return ReplayBasketball(seed, cfg, moves)
}
