package repository

import (
	"testing"
	"time"
)

func TestPlayIncrement(t *testing.T) {
	max := int64(500000)
	cases := []struct {
		name           string
		growth         bool
		inc, current   int64
		max            *int64
		want           int64
		wantCapReached bool
	}{
		{"fixed prize adds nothing", false, 100, 10000, nil, 0, false},
		{"uncapped adds the increment", true, 100, 10000, nil, 100, false},
		{"under the cap adds the increment", true, 100, 10000, &max, 100, false},
		{"$4,999.50 + $1 at a $5,000 cap adds $0.50", true, 100, 499950, &max, 50, false},
		{"at the cap adds nothing", true, 100, 500000, &max, 0, true},
		{"zero increment adds nothing", true, 0, 10000, &max, 0, false},
	}
	for _, c := range cases {
		got, capReached := PlayIncrement(c.growth, c.inc, c.max, c.current)
		if got != c.want || capReached != c.wantCapReached {
			t.Errorf("%s: got %d (cap %v), want %d (cap %v)", c.name, got, capReached, c.want, c.wantCapReached)
		}
	}
}

func TestSplitPrizeAlwaysAddsUp(t *testing.T) {
	for _, c := range []struct {
		total   int64
		winners int
		want    []int64
	}{
		{10301, 2, []int64{5151, 5150}},
		{10000, 3, []int64{3334, 3333, 3333}},
		{34800, 1, []int64{34800}},
		{0, 2, []int64{0, 0}},
	} {
		got := SplitPrize(c.total, c.winners)
		var sum int64
		for i, v := range got {
			sum += v
			if v != c.want[i] {
				t.Fatalf("SplitPrize(%d, %d) = %v, want %v", c.total, c.winners, got, c.want)
			}
		}
		if sum != c.total {
			t.Fatalf("parts of %d add up to %d", c.total, sum)
		}
	}
}

func TestDayWindowFollowsTheRoundsZone(t *testing.T) {
	// 23:30 UTC on the 1st is already 05:00 on the 2nd in Colombo (+05:30).
	now := time.Date(2026, 9, 1, 23, 30, 0, 0, time.UTC)
	start, next := DayWindow(now, "Asia/Colombo")
	if want := time.Date(2026, 9, 1, 18, 30, 0, 0, time.UTC); !start.Equal(want) {
		t.Fatalf("start %v, want %v", start, want)
	}
	if want := time.Date(2026, 9, 2, 18, 30, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("next %v, want %v", next, want)
	}
	if s, _ := DayWindow(now, "Not/AZone"); !s.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("unknown zone should fall back to UTC, got %v", s)
	}
}
