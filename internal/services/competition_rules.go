package services

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"myapp/internal/models"
	"myapp/internal/repository"
)

// Pure competition rules, kept apart from storage so they can be tested on
// their own.

// effectiveCompetitionStatus layers the clock over the stored status: a
// scheduled competition is live from its start and closed from its end, to
// the second (§13.6). The server clock is the only one that counts.
func effectiveCompetitionStatus(stored string, startsAt, endsAt, now time.Time) string {
	switch stored {
	case "scheduled":
		if !now.Before(endsAt) {
			return "closed"
		}
		if !now.Before(startsAt) {
			return "live"
		}
	case "live":
		if !now.Before(endsAt) {
			return "closed"
		}
	}
	return stored
}

// competitionEditable reports whether the rules may still change. Once a
// competition has started, runtime, points, attempts, prize, winners, game
// version and eligibility are all locked (§13.8).
func competitionEditable(stored string, startsAt, now time.Time) bool {
	return stored == "draft" || (stored == "scheduled" && now.Before(startsAt))
}

// publicDisplayName shortens a name to what may be published (§19.1): first
// name and surname initial, e.g. "Sarah Mitchell" -> "Sarah M.".
func publicDisplayName(display *string) string {
	if display == nil {
		return "Player"
	}
	fields := strings.Fields(*display)
	if len(fields) == 0 {
		return "Player"
	}
	first := fields[0]
	if len(fields) == 1 {
		return first
	}
	r, _ := utf8.DecodeRuneInString(fields[len(fields)-1])
	return first + " " + string(unicode.ToUpper(r)) + "."
}

// ageOn is a person's age in whole years on a given day.
func ageOn(dob, now time.Time) int {
	age := now.Year() - dob.Year()
	if now.Month() < dob.Month() || (now.Month() == dob.Month() && now.Day() < dob.Day()) {
		age--
	}
	return age
}

// leaderboardStatus maps a score's validation state to what the board shows.
func leaderboardStatus(validation string, final bool) string {
	if validation == "manual_review" {
		return models.EntryUnderReview
	}
	if final {
		return models.EntryVerified
	}
	return models.EntryProvisional
}

// winnerCandidate is one player in final-ranking order.
type winnerCandidate struct {
	CustomerID   uuid.UUID
	SubmissionID uuid.UUID
	Rank         int
	Eligible     bool
	Reason       string
}

// planWinners assigns prize positions down the final ranking.
//
// Ineligible players are recorded as disqualified with their reason and the
// next eligible player takes the position — no random selection (§19.3).
//
// A tie that decides who gets which prize cannot be settled by chance or by
// who submitted first (§14.4). It needs a skill playoff; the admin passes the
// playoff finishing order, and without one the tie is refused.
func planWinners(candidates []winnerCandidate, winners int, playoff map[uuid.UUID]int) ([]repository.WinnerPlan, error) {
	separated := func(a, b winnerCandidate) bool {
		if a.Rank != b.Rank {
			return true
		}
		pa, okA := playoff[a.CustomerID]
		pb, okB := playoff[b.CustomerID]
		return okA && okB && pa != pb
	}
	before := func(a, b winnerCandidate) bool {
		if a.Rank != b.Rank {
			return a.Rank < b.Rank
		}
		pa, okA := playoff[a.CustomerID]
		pb, okB := playoff[b.CustomerID]
		if okA && okB {
			return pa < pb
		}
		return okA && !okB
	}

	ordered := append([]winnerCandidate(nil), candidates...)
	// Insertion sort keeps the input order for anything the playoff does not
	// distinguish, which the tie check below then rejects.
	for i := 1; i < len(ordered); i++ {
		for j := i; j > 0 && before(ordered[j], ordered[j-1]); j-- {
			ordered[j], ordered[j-1] = ordered[j-1], ordered[j]
		}
	}

	var plans []repository.WinnerPlan
	var assigned []winnerCandidate
	lastIndex := -1
	position := 1
	for i, c := range ordered {
		if position > winners {
			break
		}
		plan := repository.WinnerPlan{
			CustomerID:    c.CustomerID,
			SubmissionID:  c.SubmissionID,
			PrizePosition: position,
			Rank:          c.Rank,
		}
		if !c.Eligible {
			reason := c.Reason
			plan.Status = "disqualified"
			plan.Reason = &reason
			plans = append(plans, plan)
			continue
		}
		plan.Status = "pending_validation"
		plans = append(plans, plan)
		assigned = append(assigned, c)
		lastIndex = i
		position++
	}

	for i := 1; i < len(assigned); i++ {
		if !separated(assigned[i-1], assigned[i]) {
			return nil, ErrTieAtCutoff
		}
	}
	// The last prize must also be clearly ahead of the first player who misses out.
	if len(assigned) > 0 {
		last := assigned[len(assigned)-1]
		for _, c := range ordered[lastIndex+1:] {
			if !c.Eligible {
				continue
			}
			if !separated(last, c) {
				return nil, ErrTieAtCutoff
			}
			break
		}
	}
	return plans, nil
}
