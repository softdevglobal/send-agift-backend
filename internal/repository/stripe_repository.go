package repository

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrStripeOrderNotFound  = errors.New("order not found")
	ErrStripeSellerNotFound = errors.New("seller not found")
)

const stripeProvider = "stripe"

// StripeRepository stores card payments (finance.payments), refunds
// (finance.refunds), seller settlements (finance.payouts and
// finance.payment_splits) and seller Connect accounts
// (seller.seller_payout_accounts).
type StripeRepository struct {
	db *pgxpool.Pool
}

func NewStripeRepository(db *pgxpool.Pool) *StripeRepository {
	return &StripeRepository{db: db}
}

// PayableOrder is what a payment needs to know about an order.
type PayableOrder struct {
	ID          uuid.UUID
	OrderNumber string
	Status      string
	Total       int
	Currency    string
	Paid        bool
	// CustomerEmail pre-fills the payment page.
	CustomerEmail string
}

// GetPayableOrder reads the customer's own order. Paid is true once a
// payment for it has succeeded.
func (r *StripeRepository) GetPayableOrder(ctx context.Context, customerID, orderID string) (*PayableOrder, error) {
	o := &PayableOrder{}
	err := r.db.QueryRow(ctx, `
		select o.id, o.order_number, o.status, o.total_amount, o.currency,
		       exists (select 1 from finance.payments p
		               where p.order_id = o.id and p.status in ('succeeded', 'refunded')),
		       coalesce(c.email::text, '')
		from marketplace.orders o
		join customer.customers c on c.id = o.customer_id
		where o.id = $1 and o.customer_id = $2`, orderID, customerID,
	).Scan(&o.ID, &o.OrderNumber, &o.Status, &o.Total, &o.Currency, &o.Paid, &o.CustomerEmail)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrStripeOrderNotFound
	}
	return o, err
}

// SavePayment records a Checkout Session for an order, under the session's
// id until the payment completes (then MarkCheckoutPaid swaps in the
// PaymentIntent's). Asking again for the same session changes nothing.
func (r *StripeRepository) SavePayment(ctx context.Context, orderID, customerID uuid.UUID, sessionID string, amount int, currency string) error {
	_, err := r.db.Exec(ctx, `
		insert into finance.payments
		       (order_id, customer_id, provider, provider_payment_id,
		        amount, currency, idempotency_key)
		values ($1, $2, $3, $4, $5, lower($6), $7)
		on conflict (provider, provider_payment_id) do nothing`,
		orderID, customerID, stripeProvider, sessionID, amount, currency,
		"stripe-cs-"+sessionID)
	return err
}

// SaveIntentPayment records a PaymentIntent the app collects in-app. It is
// stored under its own id from the start; MarkCheckoutPaid settles it the
// same way as a Checkout Session.
func (r *StripeRepository) SaveIntentPayment(ctx context.Context, orderID, customerID uuid.UUID, intentID string, amount int, currency string) error {
	_, err := r.db.Exec(ctx, `
		insert into finance.payments
		       (order_id, customer_id, provider, provider_payment_id,
		        amount, currency, idempotency_key)
		values ($1, $2, $3, $4, $5, lower($6), $7)
		on conflict (provider, provider_payment_id) do nothing`,
		orderID, customerID, stripeProvider, intentID, amount, currency,
		"stripe-pi-"+intentID)
	return err
}

// CheckoutPaid is the outcome of a completed Checkout Session.
type CheckoutPaid struct {
	OrderID uuid.UUID
	// OrderStatus is the order's status after the payment was applied. A
	// payment that lands on an order cancelled in the meantime is left
	// "cancelled" and needs refunding.
	OrderStatus string
	// First is true the first time this payment was seen as paid, so what
	// follows a payment (emails, rewards) runs once.
	First bool
	// PaymentIntentID is what a refund is issued against.
	PaymentIntentID string
}

// MarkCheckoutPaid settles the payment recorded for a Checkout Session and
// moves its order from pending_payment to paid. It returns nil when the
// session is not one of ours or was already settled.
func (r *StripeRepository) MarkCheckoutPaid(ctx context.Context, sessionID, intentID string) (*CheckoutPaid, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	out := &CheckoutPaid{First: true, PaymentIntentID: intentID}
	err = tx.QueryRow(ctx, `
		update finance.payments
		set status = 'succeeded', paid_at = coalesce(paid_at, now()),
		    provider_payment_id = coalesce(nullif($2, ''), provider_payment_id),
		    updated_at = now()
		where provider = $3 and provider_payment_id = $1
		  and status in ('requires_payment', 'processing', 'failed', 'cancelled')
		returning order_id`, sessionID, intentID, stripeProvider).Scan(&out.OrderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := tx.QueryRow(ctx, `
		update marketplace.orders
		set status = case when status = 'pending_payment' then 'paid' else status end,
		    updated_at = now()
		where id = $1
		returning status`, out.OrderID).Scan(&out.OrderStatus); err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}

// MarkCheckoutExpired notes that a Checkout Session ran out unpaid.
func (r *StripeRepository) MarkCheckoutExpired(ctx context.Context, sessionID string) error {
	_, err := r.db.Exec(ctx, `
		update finance.payments
		set status = 'cancelled', updated_at = now()
		where provider = $1 and provider_payment_id = $2 and status = 'requires_payment'`,
		stripeProvider, sessionID)
	return err
}

// PaidIntentForOrder is the PaymentIntent that paid an order, if one did.
func (r *StripeRepository) PaidIntentForOrder(ctx context.Context, orderID uuid.UUID) (string, error) {
	var id string
	err := r.db.QueryRow(ctx, `
		select provider_payment_id from finance.payments
		where order_id = $1 and provider = $2 and status = 'succeeded'
		  and provider_payment_id like 'pi\_%'
		order by paid_at desc nulls last limit 1`, orderID, stripeProvider).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// UnpaidOrder is an order that has been waiting for payment too long.
type UnpaidOrder struct {
	OrderID    uuid.UUID
	CustomerID uuid.UUID
}

// ExpiredUnpaidOrders lists orders left pending_payment for longer than
// olderThan hours with no successful payment. Orders from before card
// payments went live are never listed: they were not paid through Stripe.
func (r *StripeRepository) ExpiredUnpaidOrders(ctx context.Context, olderThanHours, limit int) ([]UnpaidOrder, error) {
	rows, err := r.db.Query(ctx, `
		select o.id, o.customer_id
		from marketplace.orders o
		where o.status = 'pending_payment'
		  and o.created_at >= `+paymentCutoffSQL+`
		  and o.created_at < now() - make_interval(hours => $1)
		  and not exists (select 1 from finance.payments p
		                  where p.order_id = o.id and p.status = 'succeeded')
		order by o.created_at
		limit $2`, olderThanHours, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UnpaidOrder
	for rows.Next() {
		var u UnpaidOrder
		if err := rows.Scan(&u.OrderID, &u.CustomerID); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ApplyRefund records a refund from the payment's cumulative refunded
// amount: whatever is not yet in finance.refunds becomes a new row. A full
// refund marks the payment and its order refunded.
func (r *StripeRepository) ApplyRefund(ctx context.Context, intentID string, amountRefunded int) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var paymentID, orderID uuid.UUID
	var amount, already int
	err = tx.QueryRow(ctx, `
		select p.id, p.order_id, p.amount,
		       coalesce((select sum(f.amount) from finance.refunds f
		                 where f.payment_id = p.id), 0)::int
		from finance.payments p
		where p.provider = $1 and p.provider_payment_id = $2
		for update of p`, stripeProvider, intentID).Scan(&paymentID, &orderID, &amount, &already)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if delta := amountRefunded - already; delta > 0 {
		if _, err := tx.Exec(ctx, `
			insert into finance.refunds
			       (order_id, payment_id, amount, reason, status)
			values ($1, $2, $3, 'refunded in Stripe', 'succeeded')`,
			orderID, paymentID, delta); err != nil {
			return err
		}
	}
	if amountRefunded >= amount {
		if _, err := tx.Exec(ctx, `
			update finance.payments set status = 'refunded', updated_at = now()
			where id = $1`, paymentID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			update marketplace.orders set status = 'refunded', updated_at = now()
			where id = $1 and status not in ('refunded', 'cancelled')`, orderID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// RecordEvent notes that a webhook event arrived, in core.idempotency_keys
// under scope 'webhook'. It reports false when the event was already
// handled, so a Stripe retry is a no-op.
func (r *StripeRepository) RecordEvent(ctx context.Context, id string) (bool, error) {
	tag, err := r.db.Exec(ctx, `
		insert into core.idempotency_keys (key, scope, status)
		values ($1, 'webhook', 'completed')
		on conflict (key) do nothing`, "stripe:"+id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ForgetEvent lets Stripe's retry through again after a handler failed.
func (r *StripeRepository) ForgetEvent(ctx context.Context, id string) {
	_, _ = r.db.Exec(ctx, `delete from core.idempotency_keys where key = $1`, "stripe:"+id)
}

// SellerStripe is a seller's Connect state.
type SellerStripe struct {
	SellerID         uuid.UUID
	Email            string
	CountryISO       string
	AccountID        string
	ChargesEnabled   bool
	PayoutsEnabled   bool
	DetailsSubmitted bool
	PayoutMode       string // automatic | manual
}

func (r *StripeRepository) GetSellerStripe(ctx context.Context, sellerID string) (*SellerStripe, error) {
	s := &SellerStripe{}
	err := r.db.QueryRow(ctx, `
		select s.id, s.email, c.iso_code,
		       coalesce(a.provider_account_id, ''),
		       coalesce(a.charges_enabled, false),
		       coalesce(a.payouts_enabled, false),
		       coalesce(a.details_submitted, false),
		       coalesce(a.payout_mode, 'automatic')
		from seller.sellers s
		join core.countries c on c.id = s.country_id
		left join seller.seller_payout_accounts a
		       on a.seller_id = s.id and a.provider = $2
		where s.id = $1`, sellerID, stripeProvider,
	).Scan(&s.SellerID, &s.Email, &s.CountryISO, &s.AccountID,
		&s.ChargesEnabled, &s.PayoutsEnabled, &s.DetailsSubmitted, &s.PayoutMode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrStripeSellerNotFound
	}
	return s, err
}

// SetSellerAccount stores the seller's Connect account id, in the seller's
// own country.
func (r *StripeRepository) SetSellerAccount(ctx context.Context, sellerID, accountID string) error {
	_, err := r.db.Exec(ctx, `
		insert into seller.seller_payout_accounts
		       (seller_id, provider, provider_account_id, country_id)
		select s.id, $2, $3, s.country_id from seller.sellers s where s.id = $1
		on conflict (seller_id, provider) do update
		   set provider_account_id = excluded.provider_account_id, updated_at = now()`,
		sellerID, stripeProvider, accountID)
	return err
}

// AccountStatus is what Stripe says a connected account can do.
type AccountStatus struct {
	ChargesEnabled   bool
	PayoutsEnabled   bool
	DetailsSubmitted bool
	Disabled         bool
	Requirements     json.RawMessage
}

// UpdateAccountStatus stores a connected account's state. Accounts that are
// not a SendAGift seller match no row and change nothing.
func (r *StripeRepository) UpdateAccountStatus(ctx context.Context, accountID string, st AccountStatus) error {
	status := "pending"
	switch {
	case st.Disabled:
		status = "disabled"
	case st.PayoutsEnabled:
		status = "enabled"
	case st.DetailsSubmitted:
		status = "restricted"
	}
	req := st.Requirements
	if len(req) == 0 {
		req = json.RawMessage(`{}`)
	}
	_, err := r.db.Exec(ctx, `
		update seller.seller_payout_accounts
		set capabilities_status = $3, charges_enabled = $4, payouts_enabled = $5,
		    details_submitted = $6, requirements_due = $7, updated_at = now()
		where provider = $1 and provider_account_id = $2`,
		stripeProvider, accountID, status, st.ChargesEnabled && !st.Disabled,
		st.PayoutsEnabled && !st.Disabled, st.DetailsSubmitted, req)
	return err
}

// DueTransfer is a delivered order line that has not been paid out yet.
type DueTransfer struct {
	OrderItemID uuid.UUID
	OrderID     uuid.UUID
	OrderNumber string
	SellerID    uuid.UUID
	AccountID   string
	Amount      int
	Currency    string
	// The customer's Stripe payment (pi_...), so the transfer can draw on
	// its charge. Empty for payments recorded some other way.
	PaymentIntentID string
}

// DueTransfers lists delivered lines of paid orders that the automatic
// payout loop should send: the seller can be paid, has not been paid for
// them yet, and has not chosen to withdraw by hand.
func (r *StripeRepository) DueTransfers(ctx context.Context, limit int) ([]DueTransfer, error) {
	return r.dueTransfers(ctx, "a.payout_mode = 'automatic'", limit, "")
}

// DueTransfersForSeller lists one seller's lines that are ready to pay out,
// whatever their payout mode. It backs the seller's own "Withdraw" button.
func (r *StripeRepository) DueTransfersForSeller(ctx context.Context, sellerID string, limit int) ([]DueTransfer, error) {
	return r.dueTransfers(ctx, "oi.seller_id = $3::uuid", limit, sellerID)
}

func (r *StripeRepository) dueTransfers(ctx context.Context, cond string, limit int, sellerID string) ([]DueTransfer, error) {
	args := []any{stripeProvider, limit}
	if sellerID != "" {
		args = append(args, sellerID)
	}
	rows, err := r.db.Query(ctx, `
		select oi.id, oi.order_id, o.order_number, oi.seller_id,
		       a.provider_account_id,
		       case when o.order_kind = 'free_gift' then o.delivery_amount else oi.total_amount end,
		       o.currency,
		       coalesce((select p.provider_payment_id from finance.payments p
		                 where p.order_id = o.id and p.status = 'succeeded'
		                   and p.provider_payment_id like 'pi\_%'
		                 order by p.paid_at desc nulls last limit 1), '')
		from marketplace.order_items oi
		join marketplace.orders o on o.id = oi.order_id
		join seller.seller_payout_accounts a
		     on a.seller_id = oi.seller_id and a.provider = $1 and a.payouts_enabled
		where oi.fulfilment_status = 'delivered'
		  and o.status <> 'refunded'
		  and `+cond+`
		  and exists (select 1 from finance.payments p
		              where p.order_id = o.id and p.status = 'succeeded')
		  and not exists (select 1 from finance.payouts t
		                  where t.order_item_id = oi.id)
		order by oi.updated_at
		limit $2`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DueTransfer
	for rows.Next() {
		var d DueTransfer
		if err := rows.Scan(&d.OrderItemID, &d.OrderID, &d.OrderNumber, &d.SellerID,
			&d.AccountID, &d.Amount, &d.Currency, &d.PaymentIntentID); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// SetPayoutMode stores how a seller wants to be paid. It reports false when
// the seller has no payout account yet.
func (r *StripeRepository) SetPayoutMode(ctx context.Context, sellerID, mode string) (bool, error) {
	tag, err := r.db.Exec(ctx, `
		update seller.seller_payout_accounts
		set payout_mode = $3
		where seller_id = $1 and provider = $2`, sellerID, stripeProvider, mode)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// RecordTransfer stores a settlement Stripe accepted: the payout row, and
// the seller / platform split on the payment it came from.
func (r *StripeRepository) RecordTransfer(ctx context.Context, d DueTransfer, transferID string, fee int) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		insert into finance.payouts
		       (seller_id, provider, provider_payout_id, amount, currency, status,
		        order_item_id, order_id, gross_amount, fee_amount)
		values ($1, $2, $3, $4, lower($5), 'paid', $6, $7, $8, $9)
		on conflict (order_item_id) do nothing`,
		d.SellerID, stripeProvider, transferID, d.Amount-fee, d.Currency,
		d.OrderItemID, d.OrderID, d.Amount, fee)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 { // already recorded
		return nil
	}

	var paymentID uuid.UUID
	err = tx.QueryRow(ctx, `
		select id from finance.payments
		where order_id = $1 and status = 'succeeded'
		order by paid_at desc nulls last limit 1`, d.OrderID).Scan(&paymentID)
	if err == nil {
		if _, err := tx.Exec(ctx, `
			insert into finance.payment_splits
			       (payment_id, seller_id, split_type, amount, currency)
			values ($1, $2, 'seller_gross', $3, lower($4))`,
			paymentID, d.SellerID, d.Amount, d.Currency); err != nil {
			return err
		}
		if fee > 0 {
			if _, err := tx.Exec(ctx, `
				insert into finance.payment_splits
				       (payment_id, seller_id, split_type, amount, currency)
				values ($1, null, 'platform_commission', $2, lower($3))`,
				paymentID, fee, d.Currency); err != nil {
				return err
			}
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	return tx.Commit(ctx)
}

// CheckoutLine is one row of an order's receipt: a gift, or a shop's delivery.
type CheckoutLine struct {
	Name       string
	UnitAmount int
	Quantity   int
}

// CheckoutLines lists what an order is made of, for Stripe's payment page:
// each gift at its price, then each shop's delivery.
func (r *StripeRepository) CheckoutLines(ctx context.Context, orderID uuid.UUID) ([]CheckoutLine, error) {
	var out []CheckoutLine
	rows, err := r.db.Query(ctx, `
		select coalesce(nullif(p.name, ''), 'Gift'), oi.unit_amount, oi.quantity
		from marketplace.order_items oi
		join seller.products p on p.id = oi.product_id
		where oi.order_id = $1 and oi.fulfilment_status <> 'cancelled'
		order by oi.created_at, oi.id`, orderID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var l CheckoutLine
		if err := rows.Scan(&l.Name, &l.UnitAmount, &l.Quantity); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = r.db.Query(ctx, `
		select 'Delivery · ' || coalesce(nullif(s.name, ''), 'shop'), d.amount
		from marketplace.order_shop_deliveries d
		left join seller.shops s on s.id = d.shop_id
		where d.order_id = $1 and d.amount > 0
		order by s.name, d.shop_id`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		l := CheckoutLine{Quantity: 1}
		if err := rows.Scan(&l.Name, &l.UnitAmount); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// PendingPayments lists the Stripe payments still waiting to be confirmed.
// orderID may be nil for all of them; recent ones only, since a payment page
// lapses after a day.
func (r *StripeRepository) PendingPayments(ctx context.Context, orderID *uuid.UUID, limit int) ([]string, error) {
	rows, err := r.db.Query(ctx, `
		select provider_payment_id from finance.payments
		where provider = $1 and status = 'requires_payment'
		  and created_at > now() - interval '3 days'
		  and ($2::uuid is null or order_id = $2)
		order by created_at
		limit $3`, stripeProvider, orderID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// PayoutRow is one settlement to a seller, as the seller's page lists it.
type PayoutRow struct {
	ID          uuid.UUID `json:"id"`
	OrderNumber string    `json:"order_number"`
	ProductName string    `json:"product_name"`
	Gross       int       `json:"gross_amount"`
	Fee         int       `json:"fee_amount"`
	Net         int       `json:"amount"`
	Currency    string    `json:"currency"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

// PayoutTotals is what a seller has been paid, and what is still to come, in
// one currency.
type PayoutTotals struct {
	Currency string `json:"currency"`
	// PaidOut is the net amount already sent to the seller's Stripe account.
	PaidOut int `json:"paid_out"`
	// Fees is the platform's cut of what was paid out.
	Fees int `json:"fees"`
	// OnTheWay is the value of paid gifts not delivered yet.
	OnTheWay int `json:"on_the_way"`
	// Awaiting is the value of delivered gifts not paid out yet (the next
	// payout round, or the seller still has to finish payout setup).
	Awaiting int `json:"awaiting"`
}

// SellerPayouts lists a seller's latest settlements and totals per currency.
func (r *StripeRepository) SellerPayouts(ctx context.Context, sellerID string, limit int) ([]PayoutRow, []PayoutTotals, error) {
	rows, err := r.db.Query(ctx, `
		select t.id, coalesce(o.order_number, ''), coalesce(p.name, ''),
		       coalesce(t.gross_amount, t.amount), t.fee_amount, t.amount,
		       t.currency, t.status, t.created_at
		from finance.payouts t
		left join marketplace.orders o on o.id = t.order_id
		left join marketplace.order_items oi on oi.id = t.order_item_id
		left join seller.products p on p.id = oi.product_id
		where t.seller_id = $1 and t.provider = $2
		order by t.created_at desc
		limit $3`, sellerID, stripeProvider, limit)
	if err != nil {
		return nil, nil, err
	}
	items := []PayoutRow{}
	for rows.Next() {
		var it PayoutRow
		if err := rows.Scan(&it.ID, &it.OrderNumber, &it.ProductName, &it.Gross,
			&it.Fee, &it.Net, &it.Currency, &it.Status, &it.CreatedAt); err != nil {
			rows.Close()
			return nil, nil, err
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	byCurrency := map[string]*PayoutTotals{}
	totals := func(cur string) *PayoutTotals {
		cur = strings.ToLower(cur)
		if byCurrency[cur] == nil {
			byCurrency[cur] = &PayoutTotals{Currency: cur}
		}
		return byCurrency[cur]
	}

	paid, err := r.db.Query(ctx, `
		select currency, coalesce(sum(amount), 0)::int, coalesce(sum(fee_amount), 0)::int
		from finance.payouts
		where seller_id = $1 and provider = $2
		group by currency`, sellerID, stripeProvider)
	if err != nil {
		return nil, nil, err
	}
	for paid.Next() {
		var cur string
		var net, fee int
		if err := paid.Scan(&cur, &net, &fee); err != nil {
			paid.Close()
			return nil, nil, err
		}
		t := totals(cur)
		t.PaidOut, t.Fees = net, fee
	}
	paid.Close()
	if err := paid.Err(); err != nil {
		return nil, nil, err
	}

	open, err := r.db.Query(ctx, `
		select o.currency,
		       coalesce(sum(case when o.order_kind = 'free_gift' then o.delivery_amount else oi.total_amount end) filter (
		           where oi.fulfilment_status not in ('delivered', 'cancelled')), 0)::int,
		       coalesce(sum(case when o.order_kind = 'free_gift' then o.delivery_amount else oi.total_amount end) filter (
		           where oi.fulfilment_status = 'delivered'
		             and not exists (select 1 from finance.payouts t
		                             where t.order_item_id = oi.id)), 0)::int
		from marketplace.order_items oi
		join marketplace.orders o on o.id = oi.order_id
		where oi.seller_id = $1
		  and o.status <> 'refunded'
		  and exists (select 1 from finance.payments pay
		              where pay.order_id = o.id and pay.status = 'succeeded')
		group by o.currency`, sellerID)
	if err != nil {
		return nil, nil, err
	}
	for open.Next() {
		var cur string
		var onTheWay, awaiting int
		if err := open.Scan(&cur, &onTheWay, &awaiting); err != nil {
			open.Close()
			return nil, nil, err
		}
		t := totals(cur)
		t.OnTheWay, t.Awaiting = onTheWay, awaiting
	}
	open.Close()
	if err := open.Err(); err != nil {
		return nil, nil, err
	}

	out := make([]PayoutTotals, 0, len(byCurrency))
	for _, t := range byCurrency {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Currency < out[j].Currency })
	return items, out, nil
}
