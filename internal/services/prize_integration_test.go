package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"myapp/internal/database"
	"myapp/internal/games"
	"myapp/internal/models"
	"myapp/internal/repository"
)

// Integration tests for the Progressive Prize engine, run against a real
// Postgres because the guarantees under test — row locks, one transaction,
// unique keys, check constraints — live in the database. Each maps to an
// acceptance criterion in the spec (§11).
//
//	TEST_DATABASE_URL=postgres://user:pass@localhost:5432/scratch_db?sslmode=disable \
//	    go test ./internal/services -run Prize -v
//
// Point it at a throwaway database: it migrates it and writes test rows.

type prizeFixture struct {
	t        *testing.T
	ctx      context.Context
	pool     *pgxpool.Pool
	svc      *CompetitionService
	points   *PointsService
	admin    AdminActor
	country  uuid.UUID
	currency string
}

func newPrizeFixture(t *testing.T) *prizeFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database-backed prize engine tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	f := &prizeFixture{t: t, ctx: ctx, pool: pool, admin: AdminActor{ID: uuid.New()}, currency: "USD"}
	// Unique per run so tests never share a country, and so its rounds.
	iso := "T" + strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", "")[:10])
	if err := pool.QueryRow(ctx, `
		insert into core.countries (iso_code, name, default_currency, default_timezone)
		values ($1, $2, 'USD', 'UTC') returning id`, iso, "Testland "+iso).Scan(&f.country); err != nil {
		t.Fatalf("country: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		insert into core.country_capabilities
			(country_id, skill_competitions_enabled, progressive_prizes_enabled, points_usage_enabled,
			 points_earning_enabled, chance_games_enabled)
		values ($1, true, true, true, true, true)`, f.country); err != nil {
		t.Fatalf("capabilities: %v", err)
	}

	competitions := repository.NewCompetitionRepository(pool)
	pointsRepo := repository.NewPointsRepository(pool)
	f.svc = NewCompetitionService(competitions, repository.NewGameRepository(pool),
		repository.NewCustomerRepository(pool), repository.NewCountryCapabilityRepository(pool),
		repository.NewCountryRepository(pool), pointsRepo)
	f.points = NewPointsService(pointsRepo)
	return f
}

// customer makes an eligible player holding the given points.
func (f *prizeFixture) customer(points int64) uuid.UUID {
	f.t.Helper()
	var id uuid.UUID
	if err := f.pool.QueryRow(f.ctx, `
		insert into customer.customers
			(country_id, email, password_hash, display_name, date_of_birth, age_verified_at, identity_verified_at)
		values ($1, $2, 'x', 'Test Player', '1990-01-01', now(), now()) returning id`,
		f.country, uuid.NewString()+"@example.test").Scan(&id); err != nil {
		f.t.Fatalf("customer: %v", err)
	}
	if points > 0 {
		if _, err := f.points.AdjustPoints(f.ctx, f.admin, id, AdjustPointsInput{
			Amount: points, Reason: "test float", IdempotencyKey: uuid.NewString(),
		}); err != nil {
			f.t.Fatalf("grant points: %v", err)
		}
	}
	return id
}

type roundOpts struct {
	start, increment int64
	max              *int64
	cost             int
	maxPlays         int
	daily            *int
	continueAtCap    bool
	winners          int
	game             string
	winOdds          *int
	quiz             []games.QuizQuestion
}

// liveRound publishes a round and moves its start into the past, as if the
// clock had reached it.
func (f *prizeFixture) liveRound(o roundOpts) *models.Competition {
	f.t.Helper()
	if o.maxPlays == 0 {
		o.maxPlays = 100
	}
	if o.winners == 0 {
		o.winners = 1
	}
	if o.game == "" {
		o.game = "2048"
	}
	if o.increment > 0 && o.max == nil {
		// A growing prize must be capped to open; make the cap out of reach.
		o.max = int64p(o.start + 10_000_000)
	}
	rules := "Highest score wins."
	continueAtCap := o.continueAtCap
	in := CompetitionInput{
		CountryID: f.country.String(), GameSlug: o.game, Title: "Prize engine test",
		StartsAt: time.Now().Add(time.Hour), EndsAt: time.Now().Add(48 * time.Hour), Timezone: "UTC",
		PointsPerAttempt: o.cost, MaxAttemptsPerCustomer: o.maxPlays, NumberOfWinners: o.winners,
		PrizeDescription: "Cash prize", PrizeCurrency: &f.currency, OfficialRules: &rules,
		PrizeGrowthEnabled: o.increment > 0, StartPrizeCents: &o.start, IncrementPerPlayCents: o.increment,
		MaxPrizeCents: o.max, DailyPlayLimit: o.daily, ContinueAtCap: &continueAtCap,
		WinOdds: o.winOdds, QuizQuestions: o.quiz,
	}
	view, err := f.svc.CreateCompetition(f.ctx, f.admin, in)
	if err != nil {
		f.t.Fatalf("create: %v", err)
	}
	reserve := o.start
	if o.max != nil {
		reserve = *o.max
	}
	if _, err := f.svc.SetReserve(f.ctx, f.admin, view.ID, ReserveInput{
		ReserveAmount: reserve + 1, Currency: f.currency, FundingSource: "sendagift"}); err != nil {
		f.t.Fatalf("reserve: %v", err)
	}
	if _, err := f.svc.FundReserve(f.ctx, f.admin, view.ID, "escrow #1"); err != nil {
		f.t.Fatalf("fund: %v", err)
	}
	if _, err := f.svc.ScheduleCompetition(f.ctx, f.admin, view.ID); err != nil {
		f.t.Fatalf("schedule: %v", err)
	}
	if _, err := f.pool.Exec(f.ctx, `
		update competition.competitions set starts_at = now() - interval '1 minute' where id = $1`, view.ID); err != nil {
		f.t.Fatal(err)
	}
	c, err := f.svc.load(f.ctx, view.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	if f.svc.status(c) != "live" {
		f.t.Fatalf("round should be live, is %s", f.svc.status(c))
	}
	return c
}

func (f *prizeFixture) play(c *models.Competition, customer uuid.UUID, key string) (*models.AttemptStartView, error) {
	return f.svc.Play(f.ctx, c.ID, SocialActor{CustomerID: customer.String()}, PlayRequest{ClientRequestID: key})
}

func (f *prizeFixture) round(id uuid.UUID) *models.Competition {
	f.t.Helper()
	c, err := f.svc.repo.GetByID(f.ctx, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

func (f *prizeFixture) balance(customer uuid.UUID) int64 {
	f.t.Helper()
	b, err := f.svc.points.Balance(f.ctx, customer)
	if err != nil {
		f.t.Fatal(err)
	}
	return b
}

func (f *prizeFixture) count(sql string, args ...any) int64 {
	f.t.Helper()
	var n int64
	if err := f.pool.QueryRow(f.ctx, sql, args...).Scan(&n); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func (f *prizeFixture) reconciled(id uuid.UUID) {
	f.t.Helper()
	rec, err := f.svc.Reconcile(f.ctx, id)
	if err != nil {
		f.t.Fatal(err)
	}
	if rec.Status != "ok" {
		for _, c := range rec.Checks {
			if !c.OK {
				f.t.Errorf("reconciliation: %s: expected %d, got %d", c.Name, c.Expected, c.Actual)
			}
		}
		f.t.FailNow()
	}
}

func wantRefusal(t *testing.T, err error, code string) *PlayRefusal {
	t.Helper()
	var r *PlayRefusal
	if !errors.As(err, &r) || r.Code != code {
		t.Fatalf("want refusal %s, got %v", code, err)
	}
	return r
}

func int64p(v int64) *int64 { return &v }
func intp(v int) *int       { return &v }

// AC-02, AC-03: publishing seeds the prize once; one play charges once and
// grows the prize once.
func TestPrizeSeedAndSinglePlay(t *testing.T) {
	f := newPrizeFixture(t)
	c := f.liveRound(roundOpts{start: 10000, increment: 100, cost: 10})
	if n := f.count(`select count(*) from competition.prize_ledger where competition_id = $1 and entry_type = 'seed'`, c.ID); n != 1 {
		t.Fatalf("want exactly one SEED entry, got %d", n)
	}
	if c.CurrentPrizeCents != 10000 {
		t.Fatalf("seeded prize %d", c.CurrentPrizeCents)
	}

	player := f.customer(100)
	view, err := f.play(c, player, "k1")
	if err != nil {
		t.Fatal(err)
	}
	if view.PointsSpent != 10 || view.WalletPointsRemaining != 90 || view.Status != "COMPLETED" {
		t.Fatalf("receipt %+v", view)
	}
	if view.PrizeBeforeCents != 10000 || view.PrizeIncrementCents != 100 || view.PrizeAfterCents != 10100 {
		t.Fatalf("prize movement %+v", view)
	}
	if got := f.round(c.ID); got.CurrentPrizeCents != 10100 || got.EligiblePlayCount != 1 || got.UniquePlayerCount != 1 {
		t.Fatalf("round after play: prize %d plays %d players %d", got.CurrentPrizeCents, got.EligiblePlayCount, got.UniquePlayerCount)
	}
	if f.balance(player) != 90 {
		t.Fatal("points not debited exactly once")
	}
	f.reconciled(c.ID)
}

// AC-04: a retried request never charges, plays or grows the prize again.
func TestPrizeIdempotentRetry(t *testing.T) {
	f := newPrizeFixture(t)
	c := f.liveRound(roundOpts{start: 10000, increment: 100, cost: 10})
	player := f.customer(100)
	first, err := f.play(c, player, "same-key")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			again, err := f.play(c, player, "same-key")
			if err != nil || !again.Replayed || again.PlayID != first.PlayID {
				t.Errorf("retry: %+v %v", again, err)
			}
		}()
	}
	wg.Wait()
	if f.balance(player) != 90 {
		t.Fatalf("balance %d after retries", f.balance(player))
	}
	if got := f.round(c.ID); got.EligiblePlayCount != 1 || got.CurrentPrizeCents != 10100 {
		t.Fatalf("retries changed the round: %d plays, %d cents", got.EligiblePlayCount, got.CurrentPrizeCents)
	}

	// The same key cannot be reused on another round.
	other := f.liveRound(roundOpts{start: 500, increment: 1, max: int64p(1000), cost: 1})
	_, err = f.play(other, player, "same-key")
	wantRefusal(t, err, PlayIdempotencyConflict)
	f.reconciled(c.ID)
}

// AC-05: 100 plays at once — 100 plays, 100 charges, 100 increments, none
// lost.
func TestPrizeConcurrentPlays(t *testing.T) {
	f := newPrizeFixture(t)
	c := f.liveRound(roundOpts{start: 10000, increment: 100, cost: 10})
	players := make([]uuid.UUID, 25)
	for i := range players {
		players[i] = f.customer(40)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := f.play(c, players[i%25], uuid.NewString()); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("play failed: %v", err)
	}
	got := f.round(c.ID)
	if got.EligiblePlayCount != 100 || got.CurrentPrizeCents != 10000+100*100 || got.UniquePlayerCount != 25 {
		t.Fatalf("after 100 plays: plays %d prize %d players %d", got.EligiblePlayCount, got.CurrentPrizeCents, got.UniquePlayerCount)
	}
	if n := f.count(`select count(*) from finance.points_ledger where competition_id = $1 and entry_type = 'play_debit'`, c.ID); n != 100 {
		t.Fatalf("want 100 charges, got %d", n)
	}
	if n := f.count(`select count(*) from competition.prize_ledger where competition_id = $1 and entry_type = 'play_increment'`, c.ID); n != 100 {
		t.Fatalf("want 100 increments, got %d", n)
	}
	for _, p := range players {
		if f.balance(p) != 0 {
			t.Fatalf("player balance %d, want 0", f.balance(p))
		}
	}
	f.reconciled(c.ID)
}

// AC-08: the cap holds under concurrency, with the last increment cut to
// what is left ($4,999.50 + $1 → $5,000.00 is +$0.50).
func TestPrizeCapUnderConcurrency(t *testing.T) {
	f := newPrizeFixture(t)
	c := f.liveRound(roundOpts{start: 499950, increment: 100, max: int64p(500000), cost: 1, continueAtCap: true})
	player := f.customer(100)
	first, err := f.play(c, player, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if first.PrizeIncrementCents != 50 || first.PrizeAfterCents != 500000 {
		t.Fatalf("partial increment: %+v", first)
	}

	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			view, err := f.play(c, player, uuid.NewString())
			if err != nil {
				t.Errorf("play at cap: %v", err)
				return
			}
			if view.PrizeIncrementCents != 0 || !view.PrizeCapReached {
				t.Errorf("a play at the cap must add nothing and say so: %+v", view)
			}
		}()
	}
	wg.Wait()
	if got := f.round(c.ID); got.CurrentPrizeCents != 500000 || got.EligiblePlayCount != 31 {
		t.Fatalf("prize %d plays %d", got.CurrentPrizeCents, got.EligiblePlayCount)
	}
	f.reconciled(c.ID)

	// A round configured to stop at the cap refuses, and charges nothing.
	stop := f.liveRound(roundOpts{start: 900, increment: 100, max: int64p(1000), cost: 5})
	p2 := f.customer(100)
	if _, err := f.play(stop, p2, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	_, err = f.play(stop, p2, uuid.NewString())
	wantRefusal(t, err, PlayPrizeCapReached)
	if f.balance(p2) != 95 {
		t.Fatalf("refused play charged: balance %d", f.balance(p2))
	}
}

// AC-06, AC-07: refusals charge nothing and create nothing.
func TestPrizeRefusalsChargeNothing(t *testing.T) {
	f := newPrizeFixture(t)
	c := f.liveRound(roundOpts{start: 10000, increment: 100, cost: 10, maxPlays: 2, daily: intp(1)})

	broke := f.customer(5)
	r := wantRefusal(t, func() error { _, err := f.play(c, broke, "a"); return err }(), PlayInsufficientPoints)
	if r.Details["points_required"] != int64(10) || r.Details["points_balance"] != int64(5) {
		t.Fatalf("details %+v", r.Details)
	}

	player := f.customer(100)
	if _, err := f.play(c, player, "d1"); err != nil {
		t.Fatal(err)
	}
	r = wantRefusal(t, func() error { _, err := f.play(c, player, "d2"); return err }(), PlayLimitReached)
	if r.Details["kind"] != "daily" || r.Details["next_eligible_at"] == nil {
		t.Fatalf("daily limit details %+v", r.Details)
	}

	if _, err := f.svc.PauseCompetition(f.ctx, f.admin, c.ID); err != nil {
		t.Fatal(err)
	}
	wantRefusal(t, func() error { _, err := f.play(c, f.customer(100), "p"); return err }(), PlayGameNotActive)
	if _, err := f.svc.ResumeCompetition(f.ctx, f.admin, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CloseCompetition(f.ctx, f.admin, c.ID); err != nil {
		t.Fatal(err)
	}
	wantRefusal(t, func() error { _, err := f.play(c, f.customer(100), "c"); return err }(), PlayGameNotActive)

	got := f.round(c.ID)
	if got.EligiblePlayCount != 1 || got.CurrentPrizeCents != 10100 || f.balance(broke) != 5 || f.balance(player) != 90 {
		t.Fatalf("a refusal changed something: plays %d prize %d", got.EligiblePlayCount, got.CurrentPrizeCents)
	}
	if got.FinalPrizeCents == nil || *got.FinalPrizeCents != 10100 {
		t.Fatalf("closing should record the final prize, got %v", got.FinalPrizeCents)
	}
	if n := f.count(`select count(*) from competition.play_rejections where competition_id = $1`, c.ID); n < 4 {
		t.Fatalf("rejections recorded: %d", n)
	}
	f.reconciled(c.ID)
}

// AC-15: a write failing midway leaves wallet, play count and prize exactly
// as they were.
func TestPrizeFailedTransactionRollsBack(t *testing.T) {
	f := newPrizeFixture(t)
	c := f.liveRound(roundOpts{start: 10000, increment: 100, cost: 10})
	player := f.customer(100)
	fn := "fail_session_" + strings.ReplaceAll(player.String(), "-", "")
	if _, err := f.pool.Exec(f.ctx, fmt.Sprintf(`
		create function competition.%[1]s() returns trigger language plpgsql as $$
		begin
			if new.customer_id = '%[2]s' then raise exception 'simulated failure'; end if;
			return new;
		end $$;
		create trigger %[1]s before insert on competition.competition_attempts
			for each row execute function competition.%[1]s();`, fn, player)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), fmt.Sprintf(`
			drop trigger if exists %[1]s on competition.competition_attempts;
			drop function if exists competition.%[1]s();`, fn))
	})

	if _, err := f.play(c, player, "boom"); err == nil {
		t.Fatal("the play should have failed")
	}
	got := f.round(c.ID)
	if f.balance(player) != 100 || got.EligiblePlayCount != 0 || got.CurrentPrizeCents != 10000 {
		t.Fatalf("partial write survived: balance %d plays %d prize %d", f.balance(player), got.EligiblePlayCount, got.CurrentPrizeCents)
	}
	if n := f.count(`select count(*) from competition.game_sessions where competition_id = $1`, c.ID); n != 0 {
		t.Fatalf("a session survived the rollback")
	}
	f.reconciled(c.ID)
}

// AC-09, AC-10, AC-12: adjustments and voids are compensating entries that
// reconcile; tampering with the cache is caught.
func TestPrizeAdjustVoidAndReconcile(t *testing.T) {
	f := newPrizeFixture(t)
	c := f.liveRound(roundOpts{start: 20000, increment: 100, max: int64p(30000), cost: 10})
	player := f.customer(100)
	view, err := f.play(c, player, "v1")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.svc.AdjustPrize(f.ctx, f.admin, c.ID, AdjustPrizeInput{AmountDeltaCents: 5000}); !errors.Is(err, ErrInvalidAdjustment) {
		t.Fatalf("an adjustment without a reason must be refused: %v", err)
	}
	entry, err := f.svc.AdjustPrize(f.ctx, f.admin, c.ID, AdjustPrizeInput{AmountDeltaCents: 5000, Reason: "sponsor top-up"})
	if err != nil {
		t.Fatal(err)
	}
	if entry.BalanceAfterCents != 25100 {
		t.Fatalf("adjusted balance %d", entry.BalanceAfterCents)
	}
	if _, err := f.svc.AdjustPrize(f.ctx, f.admin, c.ID, AdjustPrizeInput{AmountDeltaCents: 10000, Reason: "too far"}); !errors.Is(err, ErrPrizeOutOfRange) {
		t.Fatalf("an adjustment past the cap must be refused: %v", err)
	}
	if n := f.count(`select count(*) from admin.audit_log where entity_id = $1 and action = 'competition.prize_adjusted' and reason = 'sponsor top-up'`, c.ID); n != 1 {
		t.Fatalf("adjustment audit rows: %d", n)
	}

	if err := f.svc.VoidPlay(f.ctx, f.admin, c.ID, view.PlayID, VoidPlayInput{Reason: "duplicate device"}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.VoidPlay(f.ctx, f.admin, c.ID, view.PlayID, VoidPlayInput{Reason: "again"}); !errors.Is(err, ErrPlayState) {
		t.Fatalf("voiding twice: %v", err)
	}
	got := f.round(c.ID)
	if got.CurrentPrizeCents != 25000 || got.EligiblePlayCount != 0 || f.balance(player) != 100 {
		t.Fatalf("after void: prize %d plays %d balance %d", got.CurrentPrizeCents, got.EligiblePlayCount, f.balance(player))
	}
	f.reconciled(c.ID)

	// Ledger rows cannot be edited, and a tampered cache is reported.
	if _, err := f.pool.Exec(f.ctx, `update competition.prize_ledger set amount_delta_cents = 1 where competition_id = $1`, c.ID); err == nil {
		t.Fatal("the prize ledger must be append-only")
	}
	if _, err := f.pool.Exec(f.ctx, `update competition.competitions set current_prize_cents = 1 where id = $1`, c.ID); err != nil {
		t.Fatal(err)
	}
	rec, err := f.svc.Reconcile(f.ctx, c.ID)
	if err != nil || rec.Status != "discrepancy" || rec.LedgerTotalCents != 25000 {
		t.Fatalf("tampering not caught: %+v %v", rec, err)
	}
}

// Cancelling returns every point and withdraws the prize.
func TestPrizeCancelRefundsEverything(t *testing.T) {
	f := newPrizeFixture(t)
	c := f.liveRound(roundOpts{start: 10000, increment: 100, cost: 10})
	a, b := f.customer(100), f.customer(100)
	for i, p := range []uuid.UUID{a, a, b} {
		if _, err := f.play(c, p, fmt.Sprint("c", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.svc.CancelCompetition(f.ctx, f.admin, c.ID, CancelInput{Reason: "technical_failure"}); err != nil {
		t.Fatal(err)
	}
	if f.balance(a) != 100 || f.balance(b) != 100 {
		t.Fatalf("balances after cancel: %d %d", f.balance(a), f.balance(b))
	}
	got := f.round(c.ID)
	if got.CurrentPrizeCents != 0 || got.FinalPrizeCents == nil || *got.FinalPrizeCents != 10300 {
		t.Fatalf("prize after cancel %d, final %v", got.CurrentPrizeCents, got.FinalPrizeCents)
	}
	if n := f.count(`select count(*) from finance.points_ledger where competition_id = $1 and entry_type = 'play_refund'`, c.ID); n != 3 {
		t.Fatalf("refunds %d", n)
	}
}

// A round played to the end: scores, close, freeze, finalise with the
// grown prize split between winners, validate and settle — and the ledger
// still explains every cent.
func TestPrizeSettlement(t *testing.T) {
	f := newPrizeFixture(t)
	c := f.liveRound(roundOpts{start: 10001, increment: 100, max: int64p(50000), cost: 1, winners: 2})
	var sessions []*models.GameSession
	players := []uuid.UUID{f.customer(10), f.customer(10), f.customer(10)}
	for _, p := range players {
		view, err := f.play(c, p, uuid.NewString())
		if err != nil {
			t.Fatal(err)
		}
		s, err := f.svc.games.GetSession(f.ctx, view.Session.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		sessions = append(sessions, s)
	}
	for i, s := range sessions {
		// Different move counts give different durations, so no ties.
		time.Sleep(time.Duration(i+1) * 5 * time.Millisecond)
		if _, err := f.svc.SubmitOfficial(f.ctx, s, SubmitScoreInput{Moves: []string{}}); err != nil {
			t.Fatalf("submit: %v", err)
		}
	}
	if _, err := f.svc.CloseCompetition(f.ctx, f.admin, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.FreezeCompetition(f.ctx, f.admin, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.FinaliseCompetition(f.ctx, f.admin, c.ID, FinaliseInput{}); err != nil {
		t.Fatalf("finalise: %v", err)
	}
	winners, err := f.svc.AdminWinners(f.ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	// $103.01 split two ways: $51.51 to first (it takes the odd cent), $51.50 to second.
	want := map[int]int64{1: 5151, 2: 5150}
	for _, w := range winners {
		if w.PrizeValueCents == nil || *w.PrizeValueCents != want[w.PrizePosition] {
			t.Fatalf("position %d pays %v", w.PrizePosition, w.PrizeValueCents)
		}
		if err := f.svc.ValidateWinner(f.ctx, f.admin, c.ID, w.ID); err != nil {
			t.Fatal(err)
		}
	}
	ref := "BANK-123"
	settled, err := f.svc.SettleWinners(f.ctx, f.admin, c.ID, SettleInput{Reference: &ref})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range settled {
		if w.SettlementStatus != "settled" {
			t.Fatalf("winner %d not settled", w.PrizePosition)
		}
		if err := f.svc.DisqualifyWinner(f.ctx, f.admin, c.ID, w.ID, "late"); !errors.Is(err, ErrWinnerSettled) {
			t.Fatalf("a paid winner cannot be disqualified: %v", err)
		}
	}
	if _, err := f.svc.SettleWinners(f.ctx, f.admin, c.ID, SettleInput{}); err != nil {
		t.Fatalf("settling again should be a no-op: %v", err)
	}
	if n := f.count(`select count(*) from competition.prize_ledger where competition_id = $1 and entry_type = 'winner_settlement'`, c.ID); n != 2 {
		t.Fatalf("settlement entries %d", n)
	}
	if got := f.round(c.ID); got.CurrentPrizeCents != 0 || *got.FinalPrizeCents != 10301 {
		t.Fatalf("after settlement: prize %d final %d", got.CurrentPrizeCents, *got.FinalPrizeCents)
	}
	f.reconciled(c.ID)

	a, err := f.svc.Analytics(f.ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.ValidPlays != 3 || a.SettledCents != 10301 || a.PrizeGrowthCents != 300 || a.PointsSpent != 3 {
		t.Fatalf("analytics %+v", a)
	}
}

// Points: grants need a reason and a key, and a retried grant is refused
// rather than paid twice.
func TestPrizePointsAdjustments(t *testing.T) {
	f := newPrizeFixture(t)
	p := f.customer(0)
	key := uuid.NewString()
	if _, err := f.points.AdjustPoints(f.ctx, f.admin, p, AdjustPointsInput{Amount: 50, Reason: "welcome", IdempotencyKey: key}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.points.AdjustPoints(f.ctx, f.admin, p, AdjustPointsInput{Amount: 50, Reason: "welcome", IdempotencyKey: key}); !errors.Is(err, ErrConfigConflict) {
		t.Fatalf("duplicate grant: %v", err)
	}
	if _, err := f.points.AdjustPoints(f.ctx, f.admin, p, AdjustPointsInput{Amount: -60, Reason: "too much", IdempotencyKey: uuid.NewString()}); !errors.Is(err, ErrInvalidPoints) {
		t.Fatalf("overdraw: %v", err)
	}
	w, err := f.points.CustomerWallet(f.ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if w.Balance != 50 || len(w.Entries) != 1 {
		t.Fatalf("wallet %+v", w)
	}
}

// forceDraws makes the next secure draws return these values (mod n), then
// always 1 — i.e. a losing draw.
func forceDraws(t *testing.T, values ...int64) {
	t.Helper()
	orig := games.SecureIntn
	var mu sync.Mutex
	i := 0
	games.SecureIntn = func(n int64) (int64, error) {
		mu.Lock()
		defer mu.Unlock()
		v := int64(1)
		if i < len(values) {
			v = values[i]
			i++
		}
		return v % n, nil
	}
	t.Cleanup(func() { games.SecureIntn = orig })
}

// An instant-win round: the winning play is recorded with the prize it left,
// the round closes at once, and the audit data comes back with every play.
func TestPrizeInstantWin(t *testing.T) {
	f := newPrizeFixture(t)
	c := f.liveRound(roundOpts{start: 10000, increment: 100, cost: 5, game: "spin-wheel", winOdds: intp(50)})
	a, b := f.customer(100), f.customer(100)

	// Draw values in order: spin 1 loses (then picks a segment), spin 2
	// loses, spin 3 wins.
	forceDraws(t, 7, 2, 30, 4, 0)
	var last *models.AttemptStartView
	for i, p := range []uuid.UUID{a, b, a} {
		v, err := f.play(c, p, fmt.Sprint("spin-", i))
		if err != nil {
			t.Fatalf("spin %d: %v", i, err)
		}
		var res games.ChanceResult
		if err := json.Unmarshal(v.Result, &res); err != nil || res.Odds != 50 || res.Algorithm == "" {
			t.Fatalf("spin %d result %s: %v", i, v.Result, err)
		}
		if res.Won != (i == 2) || (res.Won && *res.Segment != 0) || (!res.Won && *res.Segment == 0) {
			t.Fatalf("spin %d: %+v", i, res)
		}
		last = v
	}
	if last.PrizeAfterCents != 10300 {
		t.Fatalf("winning play left the prize at %d", last.PrizeAfterCents)
	}
	got := f.round(c.ID)
	if got.Status != "closed" || got.FinalPrizeCents == nil || *got.FinalPrizeCents != 10300 {
		t.Fatalf("an instant win closes the round: %s %v", got.Status, got.FinalPrizeCents)
	}
	_, err := f.play(c, b, "after-win")
	wantRefusal(t, err, PlayGameNotActive)

	winners, err := f.svc.AdminWinners(f.ctx, c.ID)
	if err != nil || len(winners) != 1 || winners[0].CustomerID != a || *winners[0].PrizeValueCents != 10300 {
		t.Fatalf("winner %+v %v", winners, err)
	}
	if _, err := f.svc.FinaliseCompetition(f.ctx, f.admin, c.ID, FinaliseInput{}); err != nil {
		t.Fatalf("finalise: %v", err)
	}
	if err := f.svc.ValidateWinner(f.ctx, f.admin, c.ID, winners[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SettleWinners(f.ctx, f.admin, c.ID, SettleInput{}); err != nil {
		t.Fatal(err)
	}
	f.reconciled(c.ID)
	if f.round(c.ID).CurrentPrizeCents != 0 {
		t.Fatal("the paid-out jackpot leaves nothing owing")
	}
}

// A prize draw: every play is an entry, winners are distinct customers, and
// the draw is stored in full and cannot be run twice.
func TestPrizeDraw(t *testing.T) {
	f := newPrizeFixture(t)
	c := f.liveRound(roundOpts{start: 30001, cost: 1, game: "prize-draw", winners: 2})
	players := []uuid.UUID{f.customer(10), f.customer(10), f.customer(10)}
	for i := 0; i < 7; i++ {
		v, err := f.play(c, players[i%3], fmt.Sprint("entry-", i))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(v.Result), `"entered":true`) {
			t.Fatalf("a draw play is an entry: %s", v.Result)
		}
	}
	if _, err := f.svc.RunDraw(f.ctx, f.admin, c.ID); !errors.Is(err, ErrCompetitionState) {
		t.Fatalf("a live round cannot be drawn: %v", err)
	}
	if _, err := f.svc.CloseCompetition(f.ctx, f.admin, c.ID); err != nil {
		t.Fatal(err)
	}
	view, err := f.svc.RunDraw(f.ctx, f.admin, c.ID)
	if err != nil {
		t.Fatalf("draw: %v", err)
	}
	if view.Status != "finalised" {
		t.Fatalf("the draw finalises the round, got %s", view.Status)
	}
	winners, _ := f.svc.AdminWinners(f.ctx, c.ID)
	if len(winners) != 2 || winners[0].CustomerID == winners[1].CustomerID {
		t.Fatalf("two distinct winners: %+v", winners)
	}
	if *winners[0].PrizeValueCents != 15001 || *winners[1].PrizeValueCents != 15000 {
		t.Fatalf("split %d / %d", *winners[0].PrizeValueCents, *winners[1].PrizeValueCents)
	}
	d, err := f.svc.Draw(f.ctx, c.ID)
	if err != nil || d == nil || d.EntryCount != 7 || len(d.EntriesSHA256) != 64 {
		t.Fatalf("draw record %+v %v", d, err)
	}
	if _, err := f.svc.RunDraw(f.ctx, f.admin, c.ID); !errors.Is(err, ErrCompetitionState) {
		t.Fatalf("a draw runs once: %v", err)
	}
	if _, err := f.pool.Exec(f.ctx, `delete from competition.prize_draws where competition_id = $1`, c.ID); err == nil {
		t.Fatal("a draw record cannot be deleted")
	}
	f.reconciled(c.ID)
}

// Chance rounds need their own country approval to open or be played.
func TestPrizeChanceGate(t *testing.T) {
	f := newPrizeFixture(t)
	c := f.liveRound(roundOpts{start: 100, cost: 1, game: "instant-win", winOdds: intp(10)})
	if _, err := f.pool.Exec(f.ctx, `update core.country_capabilities set chance_games_enabled = false where country_id = $1`, f.country); err != nil {
		t.Fatal(err)
	}
	_, err := f.play(c, f.customer(10), "gated")
	wantRefusal(t, err, PlayNotEligible)

	rules := "r"
	one := 1
	if _, err := f.svc.CreateCompetition(f.ctx, f.admin, CompetitionInput{
		CountryID: f.country.String(), GameSlug: "instant-win", Title: "No odds",
		StartsAt: time.Now().Add(time.Hour), EndsAt: time.Now().Add(2 * time.Hour), Timezone: "UTC",
		PrizeDescription: "x", OfficialRules: &rules, NumberOfWinners: 1, MaxAttemptsPerCustomer: one,
	}); !errors.Is(err, ErrInvalidCompetition) {
		t.Fatalf("an instant-win round needs odds: %v", err)
	}
}

// Points earning: once per delivered order, taken back on refund, sign-up
// bonus only for customers who join after it was switched on.
func TestPointsEarning(t *testing.T) {
	f := newPrizeFixture(t)
	early := f.customer(0)
	if _, err := f.points.SetEarningRule(f.ctx, f.admin, f.country, EarningRuleInput{
		Enabled: true, PointsPerUnit: 2, SignupBonus: 25,
	}); err != nil {
		t.Fatal(err)
	}
	late := f.customer(0)

	var orderID uuid.UUID
	if err := f.pool.QueryRow(f.ctx, `
		insert into marketplace.orders
			(order_number, customer_id, country_id, delivery_date, status, subtotal_amount, total_amount, currency)
		values ($1, $2, $3, current_date, 'delivered', 12345, 12345, 'USD') returning id`,
		"T-"+uuid.NewString()[:8], late, f.country).Scan(&orderID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := f.points.RunEarning(f.ctx); err != nil {
			t.Fatal(err)
		}
	}
	// $123.45 at 2 points per $1 is 246 points, plus the 25-point welcome.
	if b := f.balance(late); b != 246+25 {
		t.Fatalf("late customer balance %d, want 271", b)
	}
	if b := f.balance(early); b != 0 {
		t.Fatalf("customers from before the bonus get none, got %d", b)
	}

	if _, err := f.pool.Exec(f.ctx, `update marketplace.orders set status = 'refunded' where id = $1`, orderID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.points.RunEarning(f.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.points.RunEarning(f.ctx); err != nil {
		t.Fatal(err)
	}
	if b := f.balance(late); b != 25 {
		t.Fatalf("after refund balance %d, want 25", b)
	}
	w, _ := f.points.CustomerWallet(f.ctx, late)
	if len(w.Entries) != 3 {
		t.Fatalf("want reward, reversal and bonus entries, got %d", len(w.Entries))
	}
}

// A quiz round: the device gets the questions without their answers, and
// the server scores the answers from its own copy — at submission and again
// at finalisation.
func TestPrizeQuiz(t *testing.T) {
	f := newPrizeFixture(t)
	c := f.liveRound(roundOpts{start: 5000, cost: 1, game: "quiz", winners: 1, quiz: []games.QuizQuestion{
		{Prompt: "2 + 2?", Options: []string{"3", "4"}, CorrectIndex: 1, TimeLimitSeconds: 10},
		{Prompt: "Largest ocean?", Options: []string{"Atlantic", "Pacific", "Indian"}, CorrectIndex: 1, TimeLimitSeconds: 10},
	}})
	admin, err := f.svc.AdminGet(f.ctx, c.ID)
	if err != nil || len(admin.QuizQuestions) != 2 {
		t.Fatalf("admins see the questions: %+v %v", admin.QuizQuestions, err)
	}

	ace, slow := f.customer(10), f.customer(10)
	score := func(p uuid.UUID, moves []string) int64 {
		t.Helper()
		v, err := f.play(c, p, uuid.NewString())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(v.Session.Config), "correct") {
			t.Fatalf("the answers reached the device: %s", v.Session.Config)
		}
		if !strings.Contains(string(v.Session.Config), "Largest ocean?") {
			t.Fatalf("the questions did not: %s", v.Session.Config)
		}
		s, _ := f.svc.games.GetSession(f.ctx, v.Session.SessionID)
		// Make the play look as long as its answers say, so it is not held
		// for review as impossibly fast.
		if _, err := f.pool.Exec(f.ctx, `update competition.game_sessions set started_at = now() - interval '30 seconds' where id = $1`, s.ID); err != nil {
			t.Fatal(err)
		}
		s, _ = f.svc.games.GetSession(f.ctx, v.Session.SessionID)
		res, err := f.svc.SubmitOfficial(f.ctx, s, SubmitScoreInput{Moves: moves})
		if err != nil {
			t.Fatalf("submit: %v", err)
		}
		if !res.Accepted {
			t.Fatalf("quiz score held back: %+v", res)
		}
		return res.Score
	}
	if got := score(ace, []string{"0:1:0", "1:1:5000"}); got != 150+125 {
		t.Fatalf("ace scored %d, want 275", got)
	}
	if got := score(slow, []string{"0:0:1000", "1:-1:10000"}); got != 0 {
		t.Fatalf("wrong and timed out scored %d", got)
	}

	for _, step := range []func() error{
		func() error { _, err := f.svc.CloseCompetition(f.ctx, f.admin, c.ID); return err },
		func() error { _, err := f.svc.FreezeCompetition(f.ctx, f.admin, c.ID); return err },
		func() error { _, err := f.svc.FinaliseCompetition(f.ctx, f.admin, c.ID, FinaliseInput{}); return err },
	} {
		if err := step(); err != nil {
			t.Fatalf("closing the quiz: %v", err)
		}
	}
	winners, _ := f.svc.AdminWinners(f.ctx, c.ID)
	if len(winners) != 1 || winners[0].CustomerID != ace {
		t.Fatalf("the best quiz score wins: %+v", winners)
	}

	// A quiz needs valid questions.
	rules := "r"
	if _, err := f.svc.CreateCompetition(f.ctx, f.admin, CompetitionInput{
		CountryID: f.country.String(), GameSlug: "quiz", Title: "Empty quiz",
		StartsAt: time.Now().Add(time.Hour), EndsAt: time.Now().Add(2 * time.Hour), Timezone: "UTC",
		PrizeDescription: "x", OfficialRules: &rules, NumberOfWinners: 1, MaxAttemptsPerCustomer: 1,
	}); !errors.Is(err, ErrInvalidCompetition) {
		t.Fatalf("a quiz without questions must be refused: %v", err)
	}
}
