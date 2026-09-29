package repository

import "time"

// Pure prize arithmetic, kept apart from storage so it can be tested on its
// own. All money is integer minor units.

// PlayIncrement is what one eligible play adds to a prize: the configured
// increment, cut down to whatever room is left under the cap (spec §4.1 step
// 6). capReached reports that the prize is already at its cap.
func PlayIncrement(growth bool, increment int64, max *int64, current int64) (added int64, capReached bool) {
	if !growth || increment <= 0 {
		return 0, max != nil && current >= *max
	}
	if max == nil {
		return increment, false
	}
	room := *max - current
	if room <= 0 {
		return 0, true
	}
	if increment > room {
		return room, false
	}
	return increment, false
}

// SplitPrize divides a prize between winners: equal shares, with the cents
// that do not divide evenly going to first place so the parts always add up
// to the whole.
func SplitPrize(total int64, winners int) []int64 {
	if winners < 1 {
		return nil
	}
	if total < 0 {
		total = 0
	}
	out := make([]int64, winners)
	share := total / int64(winners)
	for i := range out {
		out[i] = share
	}
	out[0] += total - share*int64(winners)
	return out
}

// DayWindow is the calendar day containing now in the round's time zone, as
// UTC instants: when today's plays started counting and when tomorrow's
// begin. An unknown zone falls back to UTC.
func DayWindow(now time.Time, timezone string) (start, next time.Time) {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		loc = time.UTC
	}
	local := now.In(loc)
	start = time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	next = start.AddDate(0, 0, 1)
	return start.UTC(), next.UTC()
}
