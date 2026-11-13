package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"myapp/internal/models"
)

// Game entries: what a customer earns by buying a product a seller promotes,
// and spends to play a skill game. A balance never goes below zero, every
// change is a ledger row, and each row is keyed by what caused it so nothing
// is applied twice.

var (
	// ErrEntriesInsufficient is a play that costs more entries than are held.
	ErrEntriesInsufficient = errors.New("not enough game entries")
	// ErrEntriesDuplicate is a change whose idempotency key was already used.
	ErrEntriesDuplicate = errors.New("this game entries change was already applied")
	// ErrEntriesCustomerNotFound is a change for a customer that does not exist.
	ErrEntriesCustomerNotFound = errors.New("customer not found")
)

// GameEntriesRepository reads and writes the entries schema.
type GameEntriesRepository struct {
	db *pgxpool.Pool
}

func NewGameEntriesRepository(db *pgxpool.Pool) *GameEntriesRepository {
	return &GameEntriesRepository{db: db}
}

func (r *GameEntriesRepository) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// errSkip ends a transaction without error when the row moved on under us.
var errSkip = errors.New("skip")

// EntriesChange is one movement of a customer's entries.
type EntriesChange struct {
	CustomerID     uuid.UUID
	EntryType      string
	Delta          int64
	OrderID        *uuid.UUID
	OrderItemID    *uuid.UUID
	AdPurchaseID   *uuid.UUID
	PromotionID    *uuid.UUID
	CompetitionID  *uuid.UUID
	AttemptID      *uuid.UUID
	GameSessionID  *uuid.UUID
	IdempotencyKey string
	Reason         *string
	ActorType      string
	ActorID        *uuid.UUID
}

// lockEntriesAccount locks a customer's balance row, creating it at zero the
// first time, and returns the balance.
func lockEntriesAccount(ctx context.Context, q querier, customerID uuid.UUID) (int64, error) {
	if _, err := q.Exec(ctx, `
		insert into entries.accounts (customer_id)
		select id from customer.customers where id = $1
		on conflict (customer_id) do nothing`, customerID); err != nil {
		return 0, err
	}
	var balance int64
	err := q.QueryRow(ctx, `
		select balance from entries.accounts where customer_id = $1 for update`, customerID).Scan(&balance)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrEntriesCustomerNotFound
	}
	return balance, err
}

// applyEntries moves a customer's balance and writes the ledger row, inside
// the caller's transaction. A debit past zero fails with
// ErrEntriesInsufficient; a key already used fails with ErrEntriesDuplicate.
// Neither leaves the transaction broken.
func applyEntries(ctx context.Context, q querier, ch EntriesChange) (uuid.UUID, int64, error) {
	if ch.Delta == 0 || ch.IdempotencyKey == "" {
		return uuid.Nil, 0, fmt.Errorf("entries change needs an amount and an idempotency key")
	}
	balance, err := lockEntriesAccount(ctx, q, ch.CustomerID)
	if err != nil {
		return uuid.Nil, 0, err
	}
	// The account lock serialises this customer's changes, so a look-up is
	// enough to spot a repeat without tripping the unique index.
	var seen bool
	if err := q.QueryRow(ctx, `
		select exists(select 1 from entries.ledger where idempotency_key = $1)`, ch.IdempotencyKey).
		Scan(&seen); err != nil {
		return uuid.Nil, 0, err
	}
	if seen {
		return uuid.Nil, balance, ErrEntriesDuplicate
	}
	after := balance + ch.Delta
	if after < 0 {
		return uuid.Nil, balance, ErrEntriesInsufficient
	}
	if _, err := q.Exec(ctx, `
		update entries.accounts set balance = $2, updated_at = now() where customer_id = $1`,
		ch.CustomerID, after); err != nil {
		return uuid.Nil, 0, err
	}
	actor := ch.ActorType
	if actor == "" {
		actor = "system"
	}
	var id uuid.UUID
	err = q.QueryRow(ctx, `
		insert into entries.ledger (
			customer_id, entry_type, delta, balance_after, order_id, order_item_id,
			ad_purchase_id, promotion_id, competition_id, attempt_id, game_session_id,
			idempotency_key, reason, actor_type, actor_id
		) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		returning id`,
		ch.CustomerID, ch.EntryType, ch.Delta, after, ch.OrderID, ch.OrderItemID,
		ch.AdPurchaseID, ch.PromotionID, ch.CompetitionID, ch.AttemptID, ch.GameSessionID,
		ch.IdempotencyKey, ch.Reason, actor, ch.ActorID).Scan(&id)
	return id, after, err
}

// Balance is a customer's entries, zero when they have never held any.
func (r *GameEntriesRepository) Balance(ctx context.Context, customerID uuid.UUID) (int64, error) {
	var balance int64
	err := r.db.QueryRow(ctx, `
		select coalesce((select balance from entries.accounts where customer_id = $1), 0)`, customerID).
		Scan(&balance)
	return balance, err
}

func describeEntry(e *models.GameEntryRow) string {
	switch e.EntryType {
	case models.EntryPurchaseGrant:
		if e.ProductName != nil {
			return "Earned for buying " + *e.ProductName
		}
		return "Earned with an order"
	case models.EntryPurchaseReversal:
		return "Taken back: the order was refunded or cancelled"
	case models.EntryPlayDebit:
		if e.CompetitionTitle != nil {
			return "Played " + *e.CompetitionTitle
		}
		return "Played a game"
	case models.EntryPlayRefund:
		return "Returned: a play was cancelled"
	case models.EntryFree:
		return "Free entry"
	case models.EntryAdminGrant:
		return "Added by SendAGift"
	case models.EntryAdminDeduction:
		return "Removed by SendAGift"
	}
	return "Game entries"
}

// Wallet is a customer's balance, totals and latest history.
func (r *GameEntriesRepository) Wallet(ctx context.Context, customerID uuid.UUID, limit int) (*models.GameEntriesWallet, error) {
	w := &models.GameEntriesWallet{History: []models.GameEntryRow{}}
	err := r.db.QueryRow(ctx, `
		select coalesce((select balance from entries.accounts where customer_id = $1), 0),
		       coalesce(sum(delta) filter (where entry_type in ('purchase_grant', 'free_entry', 'admin_grant')), 0),
		       coalesce(-sum(delta) filter (where entry_type = 'play_debit'), 0)
		         - coalesce(sum(delta) filter (where entry_type = 'play_refund'), 0),
		       coalesce(-sum(delta) filter (where entry_type in ('purchase_reversal', 'admin_deduction')), 0),
		       (select coalesce(sum(oi.game_entries), 0)
		          from marketplace.order_items oi
		          inner join marketplace.orders o on o.id = oi.order_id
		         where o.customer_id = $1 and oi.entries_status = 'reserved')
		from entries.ledger where customer_id = $1`, customerID).
		Scan(&w.Balance, &w.Totals.Earned, &w.Totals.Spent, &w.Totals.Reversed, &w.Totals.Pending)
	if err != nil {
		return nil, err
	}
	rows, err := r.db.Query(ctx, `
		select l.id, l.entry_type, l.delta, l.balance_after, l.order_id, o.order_number,
		       p.name, l.competition_id, c.title, l.reason, l.created_at
		from entries.ledger l
		left join marketplace.orders o on o.id = l.order_id
		left join marketplace.order_items oi on oi.id = l.order_item_id
		left join seller.products p on p.id = oi.product_id
		left join competition.competitions c on c.id = l.competition_id
		where l.customer_id = $1
		order by l.created_at desc, l.id desc
		limit $2`, customerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var e models.GameEntryRow
		if err := rows.Scan(&e.ID, &e.EntryType, &e.Delta, &e.BalanceAfter, &e.OrderID, &e.OrderNumber,
			&e.ProductName, &e.CompetitionID, &e.CompetitionTitle, &e.Reason, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Description = describeEntry(&e)
		w.History = append(w.History, e)
	}
	return w, rows.Err()
}

// Adjust is an admin adding or removing a customer's entries, audited.
func (r *GameEntriesRepository) Adjust(ctx context.Context, ch EntriesChange, audit models.AuditEntry) (int64, error) {
	var balance int64
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		if _, balance, err = applyEntries(ctx, tx, ch); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
	return balance, err
}

// ─── The gate ─────────────────────────────────────────────────────────────

// entryGate decides whether a customer may earn entries (op "earn") or
// spend them on a game (op "play") right now. Everything defaults to no:
// the programme, the customer's country and state, and the game must each
// have been switched on, and nothing may be stopped.
func entryGate(ctx context.Context, q querier, customerID uuid.UUID, op string, gameID *uuid.UUID) (*models.EntryGate, error) {
	g := &models.EntryGate{}
	var (
		programOn, programStopped         bool
		status                            string
		deleted                           bool
		countryID                         uuid.UUID
		stateID                           *uuid.UUID
		dob                               *time.Time
		countryHasStates                  bool
		jID                               *uuid.UUID
		earnOn, playOn, chanceOn, stopped *bool
		minAge, freePerDay                *int
		gameOn                            *bool
		gameType                          *string
		timezone                          string
	)
	err := q.QueryRow(ctx, `
		select p.enabled, p.stopped_at is not null,
		       c.status, c.deleted_at is not null, c.country_id, c.state_id, c.date_of_birth,
		       exists(select 1 from core.states s where s.country_id = c.country_id and s.is_active),
		       j.id, j.earning_enabled, j.play_enabled, j.chance_games_enabled, j.stopped_at is not null,
		       j.min_age, j.official_rules, j.customer_daily_cap, j.product_daily_cap, j.free_entries_per_day,
		       (select jg.enabled from entries.jurisdiction_games jg
		         where jg.jurisdiction_id = j.id and jg.game_id = $2),
		       (select g.game_type from competition.games g where g.id = $2),
		       co.default_timezone
		from customer.customers c
		cross join entries.program p
		inner join core.countries co on co.id = c.country_id
		left join entries.jurisdictions j
		       on j.country_id = c.country_id and j.state_id is not distinct from c.state_id
		where c.id = $1`, customerID, gameID).
		Scan(&programOn, &programStopped, &status, &deleted, &countryID, &stateID, &dob,
			&countryHasStates, &jID, &earnOn, &playOn, &chanceOn, &stopped,
			&minAge, &g.OfficialRules, &g.CustomerDailyCap, &g.ProductDailyCap, &freePerDay,
			&gameOn, &gameType, &timezone)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrEntriesCustomerNotFound
	}
	if err != nil {
		return nil, err
	}
	g.CountryID, g.StateID, g.JurisdictionID, g.Timezone = countryID, stateID, jID, timezone
	if minAge != nil {
		g.MinAge = *minAge
	}
	if freePerDay != nil {
		g.FreeEntriesPerDay = *freePerDay
	}
	deny := func(code, msg string) (*models.EntryGate, error) {
		g.Code, g.Message = code, msg
		return g, nil
	}
	on := func(b *bool) bool { return b != nil && *b }
	switch {
	case status != "active" || deleted:
		return deny(models.GateAccount, "This account cannot use games or game entries.")
	case !programOn:
		return deny(models.GateProgramOff, "Games and game entries are not available yet.")
	case programStopped:
		return deny(models.GateStopped, "Games and game entries are paused for now.")
	case countryHasStates && stateID == nil:
		return deny(models.GateStateRequired, "Tell us your state or region to use games and game entries.")
	case jID == nil:
		return deny(models.GateRegionOff, "Games and game entries are not available in your region.")
	case on(stopped):
		return deny(models.GateStopped, "Games and game entries are paused in your region for now.")
	case op == "earn" && !on(earnOn):
		return deny(models.GateRegionOff, "Game entries cannot be earned in your region.")
	case op == "play" && !on(playOn):
		return deny(models.GateRegionOff, "Games cannot be played in your region.")
	case dob == nil:
		return deny(models.GateAgeRequired, "Add your date of birth to use games and game entries.")
	case yearsOld(*dob, time.Now()) < g.MinAge:
		return deny(models.GateUnderAge, fmt.Sprintf("You must be %d or older to use games and game entries here.", g.MinAge))
	}
	if op == "play" && gameID != nil {
		switch {
		case gameType == nil || !on(gameOn):
			return deny(models.GateGameOff, "This game is not available in your region.")
		case *gameType == "chance" && !on(chanceOn):
			return deny(models.GateGameOff, "Games of chance are not available in your region.")
		}
	}
	g.Allowed = true
	return g, nil
}

func yearsOld(dob, now time.Time) int {
	years := now.Year() - dob.Year()
	if now.Month() < dob.Month() || (now.Month() == dob.Month() && now.Day() < dob.Day()) {
		years--
	}
	return years
}

// EntryGateError is a play or an earning the gate refused.
type EntryGateError struct{ Gate *models.EntryGate }

func (e *EntryGateError) Error() string { return e.Gate.Message }

// requirePlay refuses a play the customer is not allowed to start: the
// game must be switched on where they live, and a competition limited to
// some states must include theirs. It runs inside the play's transaction,
// so a stop that lands before the play commits is honoured.
func requirePlay(ctx context.Context, q querier, customerID uuid.UUID, gameVersionID, competitionID *uuid.UUID) error {
	var gameID uuid.UUID
	var err error
	if competitionID != nil {
		err = q.QueryRow(ctx, `
			select v.game_id from competition.competitions c
			inner join competition.game_versions v on v.id = c.game_version_id
			where c.id = $1`, *competitionID).Scan(&gameID)
	} else {
		err = q.QueryRow(ctx, `
			select game_id from competition.game_versions where id = $1`, gameVersionID).Scan(&gameID)
	}
	if err != nil {
		return err
	}
	gate, err := entryGate(ctx, q, customerID, "play", &gameID)
	if err != nil {
		return err
	}
	if !gate.Allowed {
		return &EntryGateError{Gate: gate}
	}
	if competitionID != nil {
		var open bool
		if err := q.QueryRow(ctx, `
			select not exists(select 1 from competition.competition_states where competition_id = $1)
			    or exists(select 1 from competition.competition_states
			               where competition_id = $1 and state_id = $2)`, *competitionID, gate.StateID).
			Scan(&open); err != nil {
			return err
		}
		if !open {
			gate.Allowed, gate.Code = false, models.GateRegionOff
			gate.Message = "This competition is not open in your state."
			return &EntryGateError{Gate: gate}
		}
	}
	return nil
}

// PlayGate is whether a customer could start a play of a competition now,
// and the reason when they could not. It changes nothing.
func (r *GameEntriesRepository) PlayGate(ctx context.Context, customerID, competitionID uuid.UUID) (bool, *models.EntryGate, error) {
	err := requirePlay(ctx, r.db, customerID, nil, &competitionID)
	var refused *EntryGateError
	if errors.As(err, &refused) {
		return false, refused.Gate, nil
	}
	return err == nil, nil, err
}

// StateOf is the state a customer has given, if any.
func (r *GameEntriesRepository) StateOf(ctx context.Context, customerID uuid.UUID) (*uuid.UUID, error) {
	var id *uuid.UUID
	err := r.db.QueryRow(ctx, `select state_id from customer.customers where id = $1`, customerID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrEntriesCustomerNotFound
	}
	return id, err
}

// CompetitionOpenTo is whether a competition's state list includes a state
// (always true for a competition that is not limited to states).
func (r *GameEntriesRepository) CompetitionOpenTo(ctx context.Context, competitionID uuid.UUID, stateID *uuid.UUID) (bool, error) {
	var open bool
	err := r.db.QueryRow(ctx, `
		select not exists(select 1 from competition.competition_states where competition_id = $1)
		    or exists(select 1 from competition.competition_states
		               where competition_id = $1 and state_id = $2)`, competitionID, stateID).Scan(&open)
	return open, err
}

// Gate is entryGate for callers outside a transaction.
func (r *GameEntriesRepository) Gate(ctx context.Context, customerID uuid.UUID, op string, gameID *uuid.UUID) (*models.EntryGate, error) {
	return entryGate(ctx, r.db, customerID, op, gameID)
}

// ─── Entries riding on orders ─────────────────────────────────────────────

// livePromotion is a product's place in a seller's active package.
type livePromotion struct {
	id, purchaseID uuid.UUID
	entries        int
}

// livePromotionFor finds the active promotion of a product, if it has one.
func livePromotionFor(ctx context.Context, q querier, productID uuid.UUID) (*livePromotion, error) {
	p := &livePromotion{}
	err := q.QueryRow(ctx, `
		select pr.id, pr.purchase_id, pr.entries_per_purchase
		from ads.promotions pr
		inner join ads.purchases pu on pu.id = pr.purchase_id
		where pr.product_id = $1 and pr.removed_at is null
		  and pu.status = 'active' and coalesce(pu.expires_at > now(), true)`, productID).
		Scan(&p.id, &p.purchaseID, &p.entries)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

// earnedToday is what a customer has been promised or granted since the
// start of the day in tz, overall and for one product.
func earnedToday(ctx context.Context, q querier, customerID, productID uuid.UUID, tz string) (all, product int, err error) {
	err = q.QueryRow(ctx, `
		select coalesce(sum(oi.game_entries), 0),
		       coalesce(sum(oi.game_entries) filter (where oi.product_id = $2), 0)
		from marketplace.order_items oi
		inner join marketplace.orders o on o.id = oi.order_id
		where o.customer_id = $1 and oi.entries_status in ('reserved', 'granted')
		  and oi.created_at >= date_trunc('day', now() at time zone $3) at time zone $3`,
		customerID, productID, tz).Scan(&all, &product)
	return all, product, err
}

// ownProduct is true when the customer buying is the seller of the product:
// the same email address on both accounts.
func ownProduct(ctx context.Context, q querier, customerID, sellerID uuid.UUID) (bool, error) {
	var own bool
	err := q.QueryRow(ctx, `
		select exists(
			select 1 from seller.sellers s
			inner join customer.customers c on lower(c.email::text) = lower(s.email::text)
			where s.id = $2 and c.id = $1)`, customerID, sellerID).Scan(&own)
	return own, err
}

// offerFor is the entries one line of a product would earn this customer
// now, and why not when it is none. With hold set, the caller's transaction
// reserves them from the seller's package.
func offerFor(ctx context.Context, q querier, gate *models.EntryGate, customerID, sellerID, productID uuid.UUID, extraAll, extraProduct int, hold bool) (entries int, promo *livePromotion, reason string, err error) {
	promo, err = livePromotionFor(ctx, q, productID)
	if err != nil || promo == nil {
		return 0, nil, "", err
	}
	if !gate.Allowed {
		return 0, nil, gate.Message, nil
	}
	own, err := ownProduct(ctx, q, customerID, sellerID)
	if err != nil {
		return 0, nil, "", err
	}
	if own {
		return 0, nil, "You cannot earn game entries from your own products.", nil
	}
	all, product, err := earnedToday(ctx, q, customerID, productID, gate.Timezone)
	if err != nil {
		return 0, nil, "", err
	}
	if gate.CustomerDailyCap != nil && all+extraAll+promo.entries > *gate.CustomerDailyCap {
		return 0, nil, "You have reached today's limit for earning game entries.", nil
	}
	if gate.ProductDailyCap != nil && product+extraProduct+promo.entries > *gate.ProductDailyCap {
		return 0, nil, "You have reached today's limit for earning game entries from this product.", nil
	}
	lock := ""
	if hold {
		lock = " for update"
	}
	var pool, used, held int
	var live bool
	if err = q.QueryRow(ctx, `
		select entries_pool, entries_used, entries_held, status = 'active' and coalesce(expires_at > now(), true)
		from ads.purchases where id = $1`+lock, promo.purchaseID).Scan(&pool, &used, &held, &live); err != nil {
		return 0, nil, "", err
	}
	// Never a part award: the full amount for the level, or nothing.
	if !live || pool-used-held < promo.entries {
		return 0, nil, "", nil
	}
	if hold {
		if _, err = q.Exec(ctx, `
			update ads.purchases set entries_held = entries_held + $2, updated_at = now() where id = $1`,
			promo.purchaseID, promo.entries); err != nil {
			return 0, nil, "", err
		}
	}
	return promo.entries, promo, "", nil
}

// reserveOrderEntries promises each promoted line its entries, inside the
// order's transaction: taken out of the seller's package as a hold, and
// granted only when the payment is confirmed. A line that cannot earn is
// simply sold without entries; the offer is the seller's promotion, not
// part of the price.
func reserveOrderEntries(ctx context.Context, q querier, customerID uuid.UUID, items []models.OrderItem) error {
	for i := range items {
		items[i].GameEntries, items[i].EntriesStatus, items[i].PromotionID = 0, "none", nil
	}
	gate, err := entryGate(ctx, q, customerID, "earn", nil)
	if err != nil {
		return err
	}
	if !gate.Allowed {
		return nil
	}
	// One customer's checkouts are serialised, so the daily caps hold.
	if _, err := lockEntriesAccount(ctx, q, customerID); err != nil {
		return err
	}
	all := 0
	perProduct := map[uuid.UUID]int{}
	for i := range items {
		n, promo, _, err := offerFor(ctx, q, gate, customerID, items[i].SellerID, items[i].ProductID,
			all, perProduct[items[i].ProductID], true)
		if err != nil {
			return err
		}
		if n == 0 || promo == nil {
			continue
		}
		items[i].GameEntries, items[i].EntriesStatus, items[i].PromotionID = n, "reserved", &promo.id
		all += n
		perProduct[items[i].ProductID] += n
	}
	return nil
}

// EntryOffer is what buying one product would earn a customer.
type EntryOffer struct {
	ProductID uuid.UUID `json:"product_id"`
	Entries   int       `json:"game_entries"`
	Reason    string    `json:"reason,omitempty"`
}

// Offers is what each product would earn the customer if bought now. It
// reserves nothing.
func (r *GameEntriesRepository) Offers(ctx context.Context, customerID uuid.UUID, productIDs []uuid.UUID) ([]EntryOffer, *models.EntryGate, error) {
	gate, err := entryGate(ctx, r.db, customerID, "earn", nil)
	if err != nil {
		return nil, nil, err
	}
	out := make([]EntryOffer, 0, len(productIDs))
	all := 0
	for _, pid := range productIDs {
		var sellerID uuid.UUID
		err := r.db.QueryRow(ctx, `
			select s.seller_id from seller.products p inner join seller.shops s on s.id = p.shop_id
			where p.id = $1`, pid).Scan(&sellerID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		n, _, reason, err := offerFor(ctx, r.db, gate, customerID, sellerID, pid, all, 0, false)
		if err != nil {
			return nil, nil, err
		}
		all += n
		out = append(out, EntryOffer{ProductID: pid, Entries: n, Reason: reason})
	}
	return out, gate, nil
}

type entryLine struct {
	id, order, customer, purchase uuid.UUID
	promotion                     *uuid.UUID
	entries                       int
}

func entryLines(ctx context.Context, q querier, sql string, args ...any) ([]entryLine, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []entryLine
	for rows.Next() {
		var l entryLine
		if err := rows.Scan(&l.id, &l.order, &l.customer, &l.purchase, &l.promotion, &l.entries); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

const entryLineCols = `oi.id, oi.order_id, o.customer_id, pr.purchase_id, oi.promotion_id, oi.game_entries
	from marketplace.order_items oi
	inner join marketplace.orders o on o.id = oi.order_id
	inner join ads.promotions pr on pr.id = oi.promotion_id`

// grantOrderEntries turns the promises on a paid order into entries the
// customer holds, and counts them against each seller's package at the same
// moment. The caller holds the transaction and has confirmed the payment.
// Safe to run twice: only lines still reserved are granted.
func grantOrderEntries(ctx context.Context, q querier, orderID uuid.UUID) error {
	var customerID uuid.UUID
	err := q.QueryRow(ctx, `
		select customer_id from marketplace.orders
		where id = $1 and status not in ('cancelled', 'refunded')
		for update`, orderID).Scan(&customerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	lines, err := entryLines(ctx, q, `select `+entryLineCols+`
		where oi.order_id = $1 and oi.entries_status = 'reserved' and oi.fulfilment_status <> 'cancelled'
		order by pr.purchase_id, oi.id
		for update of oi`, orderID)
	if err != nil || len(lines) == 0 {
		return err
	}
	// Eligibility is checked again now: a stop between checkout and payment
	// means the promise is released, not paid.
	gate, err := entryGate(ctx, q, customerID, "earn", nil)
	if err != nil {
		return err
	}
	for _, l := range lines {
		if !gate.Allowed {
			if err := releaseLine(ctx, q, l); err != nil {
				return err
			}
			continue
		}
		if _, err := q.Exec(ctx, `
			update ads.purchases
			set entries_held = entries_held - $2, entries_used = entries_used + $2, updated_at = now()
			where id = $1`, l.purchase, l.entries); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `
			update ads.promotions
			set entries_granted = entries_granted + $2, orders_count = orders_count + 1, updated_at = now()
			where id = $1`, l.promotion, l.entries); err != nil {
			return err
		}
		if _, _, err := applyEntries(ctx, q, EntriesChange{
			CustomerID: customerID, EntryType: models.EntryPurchaseGrant, Delta: int64(l.entries),
			OrderID: &l.order, OrderItemID: &l.id, AdPurchaseID: &l.purchase, PromotionID: l.promotion,
			IdempotencyKey: "grant:" + l.id.String(),
		}); err != nil && !errors.Is(err, ErrEntriesDuplicate) {
			return err
		}
		if _, err := q.Exec(ctx, `
			update marketplace.order_items set entries_status = 'granted', updated_at = now()
			where id = $1`, l.id); err != nil {
			return err
		}
		if err := afterPackageUse(ctx, q, l.purchase); err != nil {
			return err
		}
	}
	return nil
}

// releaseLine gives a line's promised entries back to the seller's package.
func releaseLine(ctx context.Context, q querier, l entryLine) error {
	if _, err := q.Exec(ctx, `
		update ads.purchases
		set entries_held = greatest(entries_held - $2, 0), updated_at = now()
		where id = $1`, l.purchase, l.entries); err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `
		update marketplace.order_items set entries_status = 'released', updated_at = now()
		where id = $1`, l.id); err != nil {
		return err
	}
	return afterPackageUse(ctx, q, l.purchase)
}

// settleCancelledEntries undoes the entries of an order, or of its cancelled
// lines, that will now never be kept: promises are released, and entries
// already granted are taken back. Only what the customer still holds can be
// taken; the rest is recorded as a shortfall for review, so a refund is
// never blocked and a balance never goes below zero. The caller holds the
// transaction.
func settleCancelledEntries(ctx context.Context, q querier, orderID uuid.UUID) (released, reversed int, err error) {
	var status string
	if err = q.QueryRow(ctx, `
		select status from marketplace.orders where id = $1 for update`, orderID).Scan(&status); err != nil {
		return 0, 0, err
	}
	dead := status == "cancelled" || status == "refunded"
	lines, err := entryLines(ctx, q, `select `+entryLineCols+`
		where oi.order_id = $1 and oi.entries_status = 'reserved'
		  and (oi.fulfilment_status = 'cancelled' or $2)
		order by pr.purchase_id, oi.id
		for update of oi`, orderID, dead)
	if err != nil {
		return 0, 0, err
	}
	for _, l := range lines {
		if err := releaseLine(ctx, q, l); err != nil {
			return released, 0, err
		}
		released++
	}

	lines, err = entryLines(ctx, q, `select `+entryLineCols+`
		where oi.order_id = $1 and oi.entries_status = 'granted'
		  and (oi.fulfilment_status = 'cancelled' or $2)
		order by pr.purchase_id, oi.id
		for update of oi`, orderID, dead)
	if err != nil {
		return released, 0, err
	}
	for _, l := range lines {
		balance, err := lockEntriesAccount(ctx, q, l.customer)
		if err != nil {
			return released, reversed, err
		}
		take := min(int64(l.entries), balance)
		if take > 0 {
			reason := "Order refunded or cancelled"
			if _, _, err := applyEntries(ctx, q, EntriesChange{
				CustomerID: l.customer, EntryType: models.EntryPurchaseReversal, Delta: -take,
				OrderID: &l.order, OrderItemID: &l.id, AdPurchaseID: &l.purchase, PromotionID: l.promotion,
				IdempotencyKey: "grant-reversal:" + l.id.String(), Reason: &reason,
			}); err != nil && !errors.Is(err, ErrEntriesDuplicate) {
				return released, reversed, err
			}
		}
		if short := int64(l.entries) - take; short > 0 {
			if _, err := q.Exec(ctx, `
				insert into entries.shortfalls
					(customer_id, order_id, order_item_id, entries_granted, entries_recovered, entries_short)
				values ($1, $2, $3, $4, $5, $6)
				on conflict (order_item_id) do nothing`,
				l.customer, l.order, l.id, l.entries, take, short); err != nil {
				return released, reversed, err
			}
		}
		// The seller's budget gets the award back: the sale did not stand.
		if _, err := q.Exec(ctx, `
			update ads.purchases
			set entries_used = greatest(entries_used - $2, 0),
			    status = case when status = 'exhausted' and coalesce(expires_at > now(), true) then 'active' else status end,
			    ended_at = case when status = 'exhausted' and coalesce(expires_at > now(), true) then null else ended_at end,
			    updated_at = now()
			where id = $1`, l.purchase, l.entries); err != nil {
			return released, reversed, err
		}
		if _, err := q.Exec(ctx, `
			update ads.promotions
			set entries_granted = greatest(entries_granted - $2, 0),
			    orders_count = greatest(orders_count - 1, 0), updated_at = now()
			where id = $1`, l.promotion, l.entries); err != nil {
			return released, reversed, err
		}
		if _, err := q.Exec(ctx, `
			update marketplace.order_items set entries_status = 'reversed', updated_at = now()
			where id = $1`, l.id); err != nil {
			return released, reversed, err
		}
		reversed++
	}
	return released, reversed, nil
}

// GrantForPaidOrder grants a just-paid order's entries in a transaction of
// its own.
func (r *GameEntriesRepository) GrantForPaidOrder(ctx context.Context, orderID uuid.UUID) error {
	return r.inTx(ctx, func(tx pgx.Tx) error { return grantOrderEntries(ctx, tx, orderID) })
}

// SettleRun is what one pass of the housekeeping job did.
type SettleRun struct {
	Granted         int `json:"granted"`
	Released        int `json:"released"`
	Reversed        int `json:"reversed"`
	PackagesExpired int `json:"packages_expired"`
}

// Settle is the housekeeping pass: it undoes entries on orders that were
// cancelled or refunded outside the checkout flow, and ends packages whose
// time has run out.
func (r *GameEntriesRepository) Settle(ctx context.Context, batch int) (*SettleRun, error) {
	res := &SettleRun{}
	rows, err := r.db.Query(ctx, `
		select distinct o.id
		from marketplace.orders o
		inner join marketplace.order_items oi on oi.order_id = o.id
		where oi.entries_status in ('reserved', 'granted')
		  and (oi.fulfilment_status = 'cancelled' or o.status in ('cancelled', 'refunded'))
		limit $1`, batch)
	if err != nil {
		return nil, err
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		err := r.inTx(ctx, func(tx pgx.Tx) error {
			released, reversed, err := settleCancelledEntries(ctx, tx, id)
			res.Released += released
			res.Reversed += reversed
			return err
		})
		if err != nil {
			return res, err
		}
	}
	// Paid orders whose entries were not granted when the payment landed
	// (the hook failed, or the server stopped): granted now.
	rows, err = r.db.Query(ctx, `
		select distinct o.id
		from marketplace.orders o
		inner join marketplace.order_items oi on oi.order_id = o.id
		where oi.entries_status = 'reserved' and oi.fulfilment_status <> 'cancelled'
		  and o.status in ('paid', 'accepted', 'preparing', 'dispatched', 'delivered')
		limit $1`, batch)
	if err != nil {
		return res, err
	}
	ids = ids[:0]
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return res, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	for _, id := range ids {
		if err := r.GrantForPaidOrder(ctx, id); err != nil {
			return res, err
		}
		res.Granted++
	}

	tag, err := r.db.Exec(ctx, `
		update ads.purchases
		set status = 'expired', ended_at = now(), updated_at = now()
		where status = 'active' and expires_at <= now()`)
	if err != nil {
		return res, err
	}
	res.PackagesExpired = int(tag.RowsAffected())
	return res, nil
}

// ─── Free entries and review ──────────────────────────────────────────────

// ClaimFree grants the day's free entries where the customer's region
// offers a no-purchase route. It returns 0 when already claimed today.
func (r *GameEntriesRepository) ClaimFree(ctx context.Context, customerID uuid.UUID) (granted int, gate *models.EntryGate, err error) {
	err = r.inTx(ctx, func(tx pgx.Tx) error {
		gate, err = entryGate(ctx, tx, customerID, "earn", nil)
		if err != nil || !gate.Allowed || gate.FreeEntriesPerDay <= 0 {
			return err
		}
		var day string
		if err := tx.QueryRow(ctx, `select to_char(now() at time zone $1, 'YYYY-MM-DD')`, gate.Timezone).
			Scan(&day); err != nil {
			return err
		}
		reason := "Free entry"
		_, _, err = applyEntries(ctx, tx, EntriesChange{
			CustomerID: customerID, EntryType: models.EntryFree, Delta: int64(gate.FreeEntriesPerDay),
			IdempotencyKey: "free:" + customerID.String() + ":" + day, Reason: &reason,
			ActorType: "customer", ActorID: &customerID,
		})
		if errors.Is(err, ErrEntriesDuplicate) {
			return nil
		}
		if err == nil {
			granted = gate.FreeEntriesPerDay
		}
		return err
	})
	return granted, gate, err
}

// ListShortfalls is the review queue of entries that could not be taken back.
func (r *GameEntriesRepository) ListShortfalls(ctx context.Context, status string, limit int) ([]models.EntryShortfall, error) {
	rows, err := r.db.Query(ctx, `
		select s.id, s.customer_id, c.email::text, c.display_name, s.order_id, o.order_number,
		       s.entries_granted, s.entries_recovered, s.entries_short, s.status, s.resolution,
		       s.resolved_at, s.created_at
		from entries.shortfalls s
		inner join customer.customers c on c.id = s.customer_id
		left join marketplace.orders o on o.id = s.order_id
		where ($1 = '' or s.status = $1)
		order by s.created_at desc
		limit $2`, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.EntryShortfall{}
	for rows.Next() {
		var s models.EntryShortfall
		if err := rows.Scan(&s.ID, &s.CustomerID, &s.CustomerEmail, &s.CustomerName, &s.OrderID, &s.OrderNumber,
			&s.EntriesGranted, &s.EntriesRecovered, &s.EntriesShort, &s.Status, &s.Resolution,
			&s.ResolvedAt, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ResolveShortfall closes a review case with what was decided.
func (r *GameEntriesRepository) ResolveShortfall(ctx context.Context, id, adminID uuid.UUID, resolution string, audit models.AuditEntry) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			update entries.shortfalls
			set status = 'resolved', resolution = $2, resolved_by_admin_id = $3, resolved_at = now()
			where id = $1 and status = 'open'`, id, resolution, adminID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
		return insertAudit(ctx, tx, audit)
	})
}

// SearchCustomers finds customers by email or name for the admin tools,
// newest first when there is no query, with the entries each holds.
func (r *GameEntriesRepository) SearchCustomers(ctx context.Context, query string, limit int) ([]models.CustomerEntriesSummary, error) {
	rows, err := r.db.Query(ctx, `
		select c.id, c.email::text, c.display_name, co.name, s.name, c.status,
		       coalesce(a.balance, 0), c.created_at
		from customer.customers c
		inner join core.countries co on co.id = c.country_id
		left join core.states s on s.id = c.state_id
		left join entries.accounts a on a.customer_id = c.id
		where c.deleted_at is null
		  and ($1 = '' or c.email::text ilike '%' || $1 || '%' or c.display_name ilike '%' || $1 || '%')
		order by c.created_at desc
		limit $2`, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]models.CustomerEntriesSummary, 0)
	for rows.Next() {
		var c models.CustomerEntriesSummary
		if err := rows.Scan(&c.CustomerID, &c.Email, &c.DisplayName, &c.CountryName, &c.StateName, &c.Status,
			&c.Balance, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
