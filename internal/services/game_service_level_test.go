package services

import (
	"encoding/json"
	"testing"

	"myapp/internal/games"
)

func TestScaleConfigForLevelMemory(t *testing.T) {
	base, err := json.Marshal(games.DefaultMemoryConfig())
	if err != nil {
		t.Fatalf("marshal base config: %v", err)
	}

	t.Run("level 1 is untouched", func(t *testing.T) {
		out, err := scaleConfigForLevel(games.MemorySlug, base, 1)
		if err != nil {
			t.Fatalf("scaleConfigForLevel: %v", err)
		}
		if string(out) != string(base) {
			t.Fatalf("level 1 changed the config: %s", out)
		}
	})

	t.Run("higher levels deal more pairs in fewer turns per pair", func(t *testing.T) {
		var prevPairs, prevRatio int
		for level := 1; level <= maxSessionLevel; level++ {
			out, err := scaleConfigForLevel(games.MemorySlug, base, level)
			if err != nil {
				t.Fatalf("level %d: %v", level, err)
			}
			var cfg games.MemoryConfig
			if err := json.Unmarshal(out, &cfg); err != nil {
				t.Fatalf("level %d: unmarshal: %v", level, err)
			}
			if (cfg.Pairs*2)%cfg.Columns != 0 {
				t.Fatalf("level %d: %d pairs (%d cards) does not fill %d columns evenly",
					level, cfg.Pairs, cfg.Pairs*2, cfg.Columns)
			}
			if level > 1 {
				if cfg.Pairs <= prevPairs {
					t.Fatalf("level %d: pairs %d did not grow from level %d's %d",
						level, cfg.Pairs, level-1, prevPairs)
				}
				ratio := cfg.MaxTurns / cfg.Pairs
				if ratio > prevRatio {
					t.Fatalf("level %d: turn ratio %d is looser than level %d's %d",
						level, ratio, level-1, prevRatio)
				}
			}
			prevPairs = cfg.Pairs
			prevRatio = cfg.MaxTurns / cfg.Pairs
		}
	})

	t.Run("a level over the cap is clamped", func(t *testing.T) {
		out, err := scaleConfigForLevel(games.MemorySlug, base, maxSessionLevel+50)
		if err != nil {
			t.Fatalf("scaleConfigForLevel: %v", err)
		}
		capped, err := scaleConfigForLevel(games.MemorySlug, base, maxSessionLevel)
		if err != nil {
			t.Fatalf("scaleConfigForLevel at cap: %v", err)
		}
		if string(out) != string(capped) {
			t.Fatalf("level beyond the cap was not clamped to it: %s vs %s", out, capped)
		}
	})

	t.Run("games without level progression are unaffected", func(t *testing.T) {
		out, err := scaleConfigForLevel("snake", base, 5)
		if err != nil {
			t.Fatalf("scaleConfigForLevel: %v", err)
		}
		if string(out) != string(base) {
			t.Fatalf("non-level game's config changed: %s", out)
		}
	})
}
