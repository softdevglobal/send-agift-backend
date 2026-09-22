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

	t.Run("higher levels tighten the turns without growing the board", func(t *testing.T) {
		boardPairs := games.DefaultMemoryConfig().Pairs
		var prevRatio int
		for level := 1; level <= maxSessionLevel; level++ {
			out, err := scaleConfigForLevel(games.MemorySlug, base, level)
			if err != nil {
				t.Fatalf("level %d: %v", level, err)
			}
			var cfg games.MemoryConfig
			if err := json.Unmarshal(out, &cfg); err != nil {
				t.Fatalf("level %d: unmarshal: %v", level, err)
			}
			// The grid must not grow. The client draws one look per pair and
			// has a fixed palette, so extra pairs would put two cards that
			// look identical, but are not a pair, on the same board.
			if cfg.Pairs != boardPairs {
				t.Fatalf("level %d: board changed to %d pairs, want %d",
					level, cfg.Pairs, boardPairs)
			}
			// The deal has to fill its grid, give or take the single odd slot
			// the client covers with an emblem. Any bigger shortfall would
			// leave real holes in the board.
			cards := cfg.Pairs * 2
			rows := (cards + cfg.Columns - 1) / cfg.Columns
			if spare := rows*cfg.Columns - cards; spare > 1 {
				t.Fatalf("level %d: %d cards in %d columns leaves %d empty slots",
					level, cards, cfg.Columns, spare)
			}
			ratio := cfg.MaxTurns / cfg.Pairs
			if level > 1 && ratio > prevRatio {
				t.Fatalf("level %d: turn ratio %d is looser than level %d's %d",
					level, ratio, level-1, prevRatio)
			}
			prevRatio = ratio
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
