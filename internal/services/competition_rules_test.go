package services

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestEffectiveCompetitionStatus(t *testing.T) {
	start := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	cases := []struct {
		stored string
		now    time.Time
		want   string
	}{
		{"scheduled", start.Add(-time.Second), "scheduled"},
		{"scheduled", start, "live"},
		{"scheduled", end.Add(-time.Second), "live"},
		{"scheduled", end, "closed"},
		{"live", end, "closed"},
		{"draft", end.Add(time.Hour), "draft"},
		{"frozen", end.Add(time.Hour), "frozen"},
		{"cancelled", start.Add(time.Hour), "cancelled"},
	}
	for _, tc := range cases {
		if got := effectiveCompetitionStatus(tc.stored, start, end, tc.now); got != tc.want {
			t.Errorf("%s at %v = %s, want %s", tc.stored, tc.now.Sub(start), got, tc.want)
		}
	}
}

func TestCompetitionEditableLocksAtStart(t *testing.T) {
	start := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	if !competitionEditable("draft", start, start.Add(time.Hour)) {
		t.Error("a draft is always editable")
	}
	if !competitionEditable("scheduled", start, start.Add(-time.Minute)) {
		t.Error("a scheduled competition is editable before it starts")
	}
	if competitionEditable("scheduled", start, start) {
		t.Error("rules must lock the moment the competition starts")
	}
	if competitionEditable("live", start, start.Add(-time.Minute)) {
		t.Error("a live competition is never editable")
	}
}

func TestPublicDisplayName(t *testing.T) {
	str := func(s string) *string { return &s }
	cases := map[string]struct {
		in   *string
		want string
	}{
		"full name":     {str("Sarah Mitchell"), "Sarah M."},
		"middle name":   {str("Sarah Jane mitchell"), "Sarah M."},
		"single name":   {str("Sarah"), "Sarah"},
		"no name":       {nil, "Player"},
		"blank":         {str("   "), "Player"},
		"accented":      {str("Zoë Ångström"), "Zoë Å."},
		"extra spacing": {str("  Ravi   Kumar "), "Ravi K."},
	}
	for name, tc := range cases {
		if got := publicDisplayName(tc.in); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}

func TestAgeOn(t *testing.T) {
	dob := time.Date(2008, 9, 15, 0, 0, 0, 0, time.UTC)
	if got := ageOn(dob, time.Date(2026, 9, 14, 23, 0, 0, 0, time.UTC)); got != 17 {
		t.Errorf("day before 18th birthday = %d, want 17", got)
	}
	if got := ageOn(dob, time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)); got != 18 {
		t.Errorf("on 18th birthday = %d, want 18", got)
	}
}

func candidate(rank int, eligible bool) winnerCandidate {
	return winnerCandidate{
		CustomerID:   uuid.New(),
		SubmissionID: uuid.New(),
		Rank:         rank,
		Eligible:     eligible,
		Reason:       "age not verified",
	}
}

func TestPlanWinnersSkipsIneligiblePlayers(t *testing.T) {
	a, b, c := candidate(1, false), candidate(2, true), candidate(3, true)
	plans, err := planWinners([]winnerCandidate{a, b, c}, 1, nil)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plans) != 2 {
		t.Fatalf("got %d plan rows, want the disqualified leader plus the winner", len(plans))
	}
	if plans[0].CustomerID != a.CustomerID || plans[0].Status != "disqualified" {
		t.Errorf("first row should record the ineligible leader as disqualified: %+v", plans[0])
	}
	if plans[1].CustomerID != b.CustomerID || plans[1].Status != "pending_validation" || plans[1].PrizePosition != 1 {
		t.Errorf("next eligible player should take first prize: %+v", plans[1])
	}
}

func TestPlanWinnersRefusesATieAtThePrizeCutoff(t *testing.T) {
	a, b := candidate(1, true), candidate(1, true)
	if _, err := planWinners([]winnerCandidate{a, b}, 1, nil); !errors.Is(err, ErrTieAtCutoff) {
		t.Fatalf("tie for the only prize: err = %v, want ErrTieAtCutoff", err)
	}
	if _, err := planWinners([]winnerCandidate{a, b}, 2, nil); !errors.Is(err, ErrTieAtCutoff) {
		t.Fatalf("tie between first and second prize: err = %v, want ErrTieAtCutoff", err)
	}
}

func TestPlanWinnersAcceptsAPlayoffResult(t *testing.T) {
	a, b := candidate(1, true), candidate(1, true)
	// b won the playoff.
	plans, err := planWinners([]winnerCandidate{a, b}, 1, map[uuid.UUID]int{b.CustomerID: 1, a.CustomerID: 2})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plans) != 1 || plans[0].CustomerID != b.CustomerID {
		t.Fatalf("playoff winner should take the prize: %+v", plans)
	}
}

func TestPlanWinnersIgnoresTiesBelowTheCutoff(t *testing.T) {
	a, b, c := candidate(1, true), candidate(2, true), candidate(2, true)
	plans, err := planWinners([]winnerCandidate{a, b, c}, 1, nil)
	if err != nil {
		t.Fatalf("a tie for second does not affect a single prize: %v", err)
	}
	if len(plans) != 1 || plans[0].CustomerID != a.CustomerID {
		t.Fatalf("unexpected plan: %+v", plans)
	}
}

func TestPlanWinnersWithFewerPlayersThanPrizes(t *testing.T) {
	plans, err := planWinners([]winnerCandidate{candidate(1, true)}, 3, nil)
	if err != nil || len(plans) != 1 {
		t.Fatalf("one player, three prizes: plans %d err %v", len(plans), err)
	}
}
