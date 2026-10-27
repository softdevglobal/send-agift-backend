package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"myapp/internal/models"
)

var (
	ErrOfferNotFound        = errors.New("membership offer not found")
	ErrAlreadyMember        = errors.New("customer already has a membership")
	ErrSubscriptionNotFound = errors.New("membership not found")
	ErrKeyReused            = errors.New("idempotency key was used for a different request")
)

// ─── Plan offers ─────────────────────────────────────────────────────────

const offerColumns = `o.id, o.tier_code, o.version, o.country_id, co.name, o.name, o.description,
	o.currency, o.price_minor, o.interval_unit, o.interval_count, o.member_bonus_points,
	o.member_discount_bps, o.gift_benefits, o.terms_version, o.status, o.effective_from, o.created_by_admin_id,
	o.submitted_by_admin_id, o.approved_by_admin_id, o.published_at, o.created_at, o.updated_at`

func scanOffer(row scanner) (*models.PlanOffer, error) {
	var o models.PlanOffer
	var benefits []byte
	err := row.Scan(&o.ID, &o.TierCode, &o.Version, &o.CountryID, &o.CountryName, &o.Name,
		&o.Description, &o.Currency, &o.PriceMinor, &o.IntervalUnit, &o.IntervalCount,
		&o.MemberBonusPoints, &o.MemberDiscountBPS, &benefits, &o.TermsVersion, &o.Status, &o.EffectiveFrom,
		&o.CreatedByAdminID, &o.SubmittedByAdminID, &o.ApprovedByAdminID, &o.PublishedAt,
		&o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		return nil, err
	}
	o.GiftBenefits = json.RawMessage(benefits)
	o.BillingLabel = BillingLabel(o.IntervalUnit, o.IntervalCount)
	return &o, nil
}

// BillingLabel says the true interval: "every 28 days", "every month",
// "every 3 months". Never "monthly" for a plan that is not.
func BillingLabel(unit string, count int) string {
	if count == 1 {
		return "every " + unit
	}
	return fmt.Sprintf("every %d %ss", count, unit)
}

func scanOffers(rows pgx.Rows) ([]models.PlanOffer, error) {
	defer rows.Close()
	out := make([]models.PlanOffer, 0)
	for rows.Next() {
		o, err := scanOffer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

func (r *CompetitionPointsRepository) GetOffer(ctx context.Context, id uuid.UUID) (*models.PlanOffer, error) {
	return getOffer(ctx, r.db, id, false)
}

func getOffer(ctx context.Context, q querier, id uuid.UUID, lock bool) (*models.PlanOffer, error) {
	sql := `select ` + offerColumns + ` from membership.plan_offers o
		inner join core.countries co on co.id = o.country_id where o.id = $1`
	if lock {
		sql += ` for update of o`
	}
	o, err := scanOffer(q.QueryRow(ctx, sql, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrOfferNotFound
	}
	return o, err
}

// ListOffers is every offer version for the admin console.
func (r *CompetitionPointsRepository) ListOffers(ctx context.Context) ([]models.PlanOffer, error) {
	rows, err := r.db.Query(ctx, `select `+offerColumns+` from membership.plan_offers o
		inner join core.countries co on co.id = o.country_id
		order by co.name, o.tier_code, o.version desc`)
	if err != nil {
		return nil, err
	}
	return scanOffers(rows)
}

// PublishedOffers are the versions on sale in a country now.
func (r *CompetitionPointsRepository) PublishedOffers(ctx context.Context, countryID uuid.UUID) ([]models.PlanOffer, error) {
	rows, err := r.db.Query(ctx, `select `+offerColumns+` from membership.plan_offers o
		inner join core.countries co on co.id = o.country_id
		where o.country_id = $1 and o.status = 'published' and o.effective_from <= now()
		order by o.price_minor`, countryID)
	if err != nil {
		return nil, err
	}
	return scanOffers(rows)
}

// SaveOfferDraft creates a draft (id nil) or edits one. A new draft takes the
// next version number of its tier in its market.
func (r *CompetitionPointsRepository) SaveOfferDraft(ctx context.Context, id *uuid.UUID, o *models.PlanOffer, audit models.AuditEntry) (*models.PlanOffer, error) {
	benefits := o.GiftBenefits
	if len(benefits) == 0 {
		benefits = json.RawMessage("[]")
	}
	var out *models.PlanOffer
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		var offerID uuid.UUID
		if id == nil {
			err := tx.QueryRow(ctx, `
				insert into membership.plan_offers
					(tier_code, version, country_id, name, description, currency, price_minor,
					 interval_unit, interval_count, member_bonus_points, member_discount_bps,
					 gift_benefits, terms_version, created_by_admin_id)
				values ($1, (select coalesce(max(version), 0) + 1 from membership.plan_offers
				             where tier_code = $1 and country_id = $2),
				        $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
				returning id`,
				o.TierCode, o.CountryID, o.Name, o.Description, o.Currency, o.PriceMinor, o.IntervalUnit,
				o.IntervalCount, o.MemberBonusPoints, o.MemberDiscountBPS, []byte(benefits), o.TermsVersion,
				o.CreatedByAdminID).
				Scan(&offerID)
			if err := mapVersionErr(err); err != nil {
				return err
			}
			audit.Action = "membership_offer.create"
		} else {
			before, err := getOffer(ctx, tx, *id, true)
			if err != nil {
				return err
			}
			if before.Status != models.VersionDraft {
				return ErrVersionNotDraft
			}
			if _, err := tx.Exec(ctx, `
				update membership.plan_offers
				set name = $2, description = $3, currency = $4, price_minor = $5, interval_unit = $6,
				    interval_count = $7, member_bonus_points = $8, gift_benefits = $9,
				    terms_version = $10, member_discount_bps = $11, updated_at = now()
				where id = $1`, *id, o.Name, o.Description, o.Currency, o.PriceMinor, o.IntervalUnit,
				o.IntervalCount, o.MemberBonusPoints, []byte(benefits), o.TermsVersion, o.MemberDiscountBPS); err != nil {
				return mapVersionErr(err)
			}
			offerID = *id
			audit.Before = before
			audit.Action = "membership_offer.update"
		}
		saved, err := getOffer(ctx, tx, offerID, false)
		if err != nil {
			return err
		}
		audit.EntityID = &offerID
		audit.After = saved
		if err := insertAudit(ctx, tx, audit); err != nil {
			return err
		}
		out = saved
		return nil
	})
	return out, err
}

// TransitionOffer moves a version along draft → awaiting_approval →
// published → suspended → retired. Approving publishes it, retiring the
// version it replaces, and needs a second admin.
func (r *CompetitionPointsRepository) TransitionOffer(ctx context.Context, id uuid.UUID, to string, adminID uuid.UUID, audit models.AuditEntry) (*models.PlanOffer, error) {
	var out *models.PlanOffer
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		o, err := getOffer(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if err := checkTransition(o.Status, to, o.SubmittedByAdminID, adminID); err != nil {
			return err
		}
		switch to {
		case models.VersionAwaitingApproval:
			_, err = tx.Exec(ctx, `update membership.plan_offers
				set status = $2, submitted_by_admin_id = $3, updated_at = now() where id = $1`, id, to, adminID)
		case models.VersionPublished:
			if o.Status == models.VersionAwaitingApproval {
				if _, err = tx.Exec(ctx, `update membership.plan_offers
					set status = 'retired', updated_at = now()
					where tier_code = $1 and country_id = $2 and status in ('published', 'suspended') and id <> $3`,
					o.TierCode, o.CountryID, id); err != nil {
					return err
				}
				_, err = tx.Exec(ctx, `update membership.plan_offers
					set status = 'published', approved_by_admin_id = $2, published_at = now(),
					    effective_from = coalesce(effective_from, now()), updated_at = now()
					where id = $1`, id, adminID)
			} else { // resumed from suspended
				_, err = tx.Exec(ctx, `update membership.plan_offers set status = 'published', updated_at = now()
					where id = $1`, id)
			}
		default:
			_, err = tx.Exec(ctx, `update membership.plan_offers set status = $2, updated_at = now()
				where id = $1`, id, to)
		}
		if err != nil {
			return mapVersionErr(err)
		}
		saved, err := getOffer(ctx, tx, id, false)
		if err != nil {
			return err
		}
		audit.EntityID = &id
		audit.Before = map[string]any{"status": o.Status}
		audit.After = map[string]any{"status": saved.Status}
		if err := insertAudit(ctx, tx, audit); err != nil {
			return err
		}
		out = saved
		return nil
	})
	return out, err
}

// PublishOffer puts a draft straight on sale, with no second approver. It is
// for a Super Admin (the route already requires one and a fresh password), so
// the two-person rule of the submit-and-approve path does not apply. The
// version it replaces is retired, and from here the version is immutable.
func (r *CompetitionPointsRepository) PublishOffer(ctx context.Context, id, adminID uuid.UUID, audit models.AuditEntry) (*models.PlanOffer, error) {
	var out *models.PlanOffer
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		o, err := getOffer(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if o.Status != models.VersionDraft {
			return ErrVersionNotDraft
		}
		if _, err := tx.Exec(ctx, `update membership.plan_offers
			set status = 'retired', updated_at = now()
			where tier_code = $1 and country_id = $2 and status in ('published', 'suspended') and id <> $3`,
			o.TierCode, o.CountryID, id); err != nil {
			return mapVersionErr(err)
		}
		if _, err := tx.Exec(ctx, `update membership.plan_offers
			set status = 'published', approved_by_admin_id = $2, published_at = now(),
			    effective_from = coalesce(effective_from, now()), updated_at = now()
			where id = $1`, id, adminID); err != nil {
			return mapVersionErr(err)
		}
		saved, err := getOffer(ctx, tx, id, false)
		if err != nil {
			return err
		}
		audit.EntityID = &id
		audit.Before = map[string]any{"status": o.Status}
		audit.After = map[string]any{"status": saved.Status, "published_directly": true}
		if err := insertAudit(ctx, tx, audit); err != nil {
			return err
		}
		out = saved
		return nil
	})
	return out, err
}

// DeleteOfferDraft removes a draft that was never submitted.
func (r *CompetitionPointsRepository) DeleteOfferDraft(ctx context.Context, id uuid.UUID, audit models.AuditEntry) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `delete from membership.plan_offers where id = $1 and status = 'draft'`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			if _, err := getOffer(ctx, tx, id, false); err != nil {
				return err
			}
			return ErrVersionNotDraft
		}
		audit.EntityID = &id
		return insertAudit(ctx, tx, audit)
	})
}

// checkTransition allows the status moves of a versioned rule.
func checkTransition(from, to string, submittedBy *uuid.UUID, adminID uuid.UUID) error {
	allowed := map[string][]string{
		models.VersionDraft:            {models.VersionAwaitingApproval},
		models.VersionAwaitingApproval: {models.VersionPublished, models.VersionDraft},
		models.VersionPublished:        {models.VersionSuspended, models.VersionRetired},
		models.VersionSuspended:        {models.VersionPublished, models.VersionRetired},
	}
	ok := false
	for _, s := range allowed[from] {
		if s == to {
			ok = true
		}
	}
	if !ok {
		if from == models.VersionDraft {
			return ErrVersionNotDraft
		}
		if to == models.VersionPublished && from != models.VersionSuspended {
			return ErrVersionNotAwaiting
		}
		return ErrVersionBadTransition
	}
	if from == models.VersionAwaitingApproval && to == models.VersionPublished &&
		submittedBy != nil && *submittedBy == adminID {
		return ErrVersionSamePerson
	}
	return nil
}

func mapVersionErr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23503":
			return ErrCountryNotFound
		case "23514":
			return fmt.Errorf("%w: %s", ErrInvalidVersion, pgErr.ConstraintName)
		case "P0001":
			return ErrVersionNotDraft
		}
	}
	return err
}

// ErrInvalidVersion is a version the database refused as inconsistent.
var ErrInvalidVersion = errors.New("invalid version")

// ─── Subscriptions ───────────────────────────────────────────────────────

const subscriptionColumns = `s.id, s.customer_id, s.offer_id, s.scheduled_offer_id, s.provider,
	s.provider_subscription_id, s.checkout_url, s.status, s.cancel_at_period_end, s.current_period_end,
	s.cancel_requested_at, s.ended_at, s.renewal_consent_at, s.created_at, s.updated_at`

func scanSubscription(row scanner) (*models.Subscription, error) {
	var s models.Subscription
	err := row.Scan(&s.ID, &s.CustomerID, &s.OfferID, &s.ScheduledOfferID, &s.Provider,
		&s.ProviderSubscriptionID, &s.CheckoutURL, &s.Status, &s.CancelAtPeriodEnd, &s.CurrentPeriodEnd,
		&s.CancelRequestedAt, &s.EndedAt, &s.RenewalConsentAt, &s.CreatedAt, &s.UpdatedAt)
	return &s, err
}

// StartSubscription records a customer's request to join, before payment.
// The same idempotency key returns the same subscription; a second live
// membership is refused.
func (r *CompetitionPointsRepository) StartSubscription(ctx context.Context, customerID, offerID uuid.UUID, provider, key string, consentAt time.Time) (*models.Subscription, bool, error) {
	var out *models.Subscription
	created := false
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		// Serialise this customer's membership changes.
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('membership:' || $1::text, 0))`,
			customerID); err != nil {
			return err
		}
		prior, err := scanSubscription(tx.QueryRow(ctx, `select `+subscriptionColumns+`
			from membership.subscriptions s where s.customer_id = $1 and s.idempotency_key = $2`,
			customerID, key))
		if err == nil {
			if prior.OfferID != offerID {
				return ErrKeyReused
			}
			out = prior
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		// An unpaid checkout left open is replaced, not a second membership.
		if _, err := tx.Exec(ctx, `update membership.subscriptions
			set status = 'abandoned', updated_at = now()
			where customer_id = $1 and status = 'incomplete' and provider_subscription_id is null`,
			customerID); err != nil {
			return err
		}
		var id uuid.UUID
		err = tx.QueryRow(ctx, `
			insert into membership.subscriptions
				(customer_id, offer_id, provider, renewal_consent_at, idempotency_key)
			values ($1, $2, $3, $4, $5) returning id`, customerID, offerID, provider, consentAt, key).Scan(&id)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrAlreadyMember
		}
		if err != nil {
			return err
		}
		created = true
		out, err = scanSubscription(tx.QueryRow(ctx, `select `+subscriptionColumns+`
			from membership.subscriptions s where s.id = $1`, id))
		return err
	})
	return out, created, err
}

// SetSubscriptionCheckout stores the provider's hosted checkout.
func (r *CompetitionPointsRepository) SetSubscriptionCheckout(ctx context.Context, id uuid.UUID, sessionID, url string) error {
	_, err := r.db.Exec(ctx, `update membership.subscriptions
		set checkout_session_id = $2, checkout_url = $3, updated_at = now() where id = $1`, id, sessionID, url)
	return err
}

// AbandonSubscription marks an unpaid checkout that could not be started.
func (r *CompetitionPointsRepository) AbandonSubscription(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.Exec(ctx, `update membership.subscriptions set status = 'abandoned', updated_at = now()
		where id = $1 and status = 'incomplete' and provider_subscription_id is null`, id)
	return err
}

// GetSubscription reads one of a customer's memberships.
func (r *CompetitionPointsRepository) GetSubscription(ctx context.Context, id uuid.UUID) (*models.Subscription, error) {
	s, err := scanSubscription(r.db.QueryRow(ctx, `select `+subscriptionColumns+`
		from membership.subscriptions s where s.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSubscriptionNotFound
	}
	return s, err
}

// SubscriptionRefs are the provider ids needed to manage a membership.
type SubscriptionRefs struct {
	ID                     uuid.UUID
	CustomerID             uuid.UUID
	Provider               string
	ProviderSubscriptionID *string
	Status                 string
}

// LiveSubscription is the customer's membership that is not over, if any.
func (r *CompetitionPointsRepository) LiveSubscription(ctx context.Context, customerID uuid.UUID) (*models.Subscription, error) {
	s, err := scanSubscription(r.db.QueryRow(ctx, `select `+subscriptionColumns+`
		from membership.subscriptions s
		where s.customer_id = $1 and s.status in ('incomplete', 'active', 'past_due', 'cancelling')`, customerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

// ProviderSubscriptionUpdate is what the provider says a membership is now.
type ProviderSubscriptionUpdate struct {
	Provider               string
	ProviderSubscriptionID string
	// LocalID is our subscription id, carried in the provider's metadata.
	LocalID            *uuid.UUID
	ProviderCustomerID *string
	CheckoutSessionID  *string
	// ProviderStatus is the provider's own word: active, past_due,
	// canceled, unpaid, incomplete, incomplete_expired, trialing.
	ProviderStatus    string
	CancelAtPeriodEnd bool
	CurrentPeriodEnd  *time.Time
	EndedAt           *time.Time
}

// mapProviderStatus turns a provider status into ours.
func mapProviderStatus(u ProviderSubscriptionUpdate) string {
	switch u.ProviderStatus {
	case "active", "trialing":
		if u.CancelAtPeriodEnd {
			return models.SubscriptionCancelling
		}
		return models.SubscriptionActive
	case "past_due", "unpaid":
		return models.SubscriptionPastDue
	case "canceled":
		return models.SubscriptionCancelled
	case "incomplete_expired":
		return models.SubscriptionExpired
	default:
		return models.SubscriptionIncomplete
	}
}

// ApplyProviderSubscription records the provider's view of a membership.
// It never creates a paid period: only a paid invoice does.
func (r *CompetitionPointsRepository) ApplyProviderSubscription(ctx context.Context, u ProviderSubscriptionUpdate) (*models.Subscription, error) {
	var out *models.Subscription
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		s, err := lockSubscriptionByProvider(ctx, tx, u.Provider, u.ProviderSubscriptionID, u.LocalID, u.CheckoutSessionID)
		if err != nil {
			return err
		}
		status := mapProviderStatus(u)
		// An ended membership stays ended; a late event cannot revive it.
		if s.Status == models.SubscriptionCancelled || s.Status == models.SubscriptionExpired {
			status = s.Status
		}
		if _, err := tx.Exec(ctx, `
			update membership.subscriptions
			set provider_subscription_id = coalesce(provider_subscription_id, $2),
			    provider_customer_id = coalesce($3, provider_customer_id),
			    status = $4, cancel_at_period_end = $5,
			    current_period_end = coalesce($6, current_period_end),
			    ended_at = coalesce(ended_at, $7), updated_at = now()
			where id = $1`, s.ID, u.ProviderSubscriptionID, u.ProviderCustomerID, status,
			u.CancelAtPeriodEnd, u.CurrentPeriodEnd, u.EndedAt); err != nil {
			return err
		}
		out, err = scanSubscription(tx.QueryRow(ctx, `select `+subscriptionColumns+`
			from membership.subscriptions s where s.id = $1`, s.ID))
		return err
	})
	return out, err
}

func lockSubscriptionByProvider(ctx context.Context, tx pgx.Tx, provider, providerSubID string, localID *uuid.UUID, sessionID *string) (*models.Subscription, error) {
	s, err := scanSubscription(tx.QueryRow(ctx, `select `+subscriptionColumns+`
		from membership.subscriptions s
		where s.provider = $1 and s.provider_subscription_id = $2 for update`, provider, providerSubID))
	if err == nil {
		return s, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if localID != nil {
		s, err = scanSubscription(tx.QueryRow(ctx, `select `+subscriptionColumns+`
			from membership.subscriptions s
			where s.id = $1 and s.provider = $2
			  and (s.provider_subscription_id is null or s.provider_subscription_id = $3)
			for update`, *localID, provider, providerSubID))
		if err == nil {
			return s, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	if sessionID != nil {
		s, err = scanSubscription(tx.QueryRow(ctx, `select `+subscriptionColumns+`
			from membership.subscriptions s
			where s.checkout_session_id = $1 and s.provider = $2 for update`, *sessionID, provider))
		if err == nil {
			return s, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	return nil, ErrSubscriptionNotFound
}

// RequestCancel records that the customer asked to stop renewing. The
// provider's confirmation then moves it to cancelling.
func (r *CompetitionPointsRepository) RequestCancel(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.Exec(ctx, `update membership.subscriptions
		set cancel_requested_at = coalesce(cancel_requested_at, now()), updated_at = now()
		where id = $1`, id)
	return err
}

// ─── Paid periods ────────────────────────────────────────────────────────

// PaidInvoice is a provider-confirmed payment for one membership period.
type PaidInvoice struct {
	Provider               string
	ProviderSubscriptionID string
	LocalID                *uuid.UUID
	InvoiceID              string
	PaymentID              *string
	PeriodStart            time.Time
	PeriodEnd              time.Time
	AmountMinor            int64
	Currency               string
	PaidAt                 time.Time
}

// PaidPeriodResult says what recording an invoice did.
type PaidPeriodResult struct {
	PeriodID   uuid.UUID
	CustomerID uuid.UUID
	Created    bool
	// SlotCreated is false for a zero-bonus offer or an overlapping period.
	SlotCreated bool
	Overlap     bool
}

// RecordPaidPeriod turns a paid invoice into a paid period and, once per
// canonical period, its bonus slot. It grants no points: those wait for a
// qualifying gift. Repeating the same invoice changes nothing.
func (r *CompetitionPointsRepository) RecordPaidPeriod(ctx context.Context, inv PaidInvoice) (*PaidPeriodResult, error) {
	if !inv.PeriodEnd.After(inv.PeriodStart) {
		return nil, fmt.Errorf("membership: invoice %s has an empty period", inv.InvoiceID)
	}
	res := &PaidPeriodResult{}
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		s, err := lockSubscriptionByProvider(ctx, tx, inv.Provider, inv.ProviderSubscriptionID, inv.LocalID, nil)
		if err != nil {
			return err
		}
		res.CustomerID = s.CustomerID
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('membership:' || $1::text, 0))`,
			s.CustomerID); err != nil {
			return err
		}
		// A renewal applies the offer version scheduled for it.
		offerID := s.OfferID
		if s.ScheduledOfferID != nil {
			offerID = *s.ScheduledOfferID
		}
		err = tx.QueryRow(ctx, `
			insert into membership.paid_periods
				(customer_id, subscription_id, offer_id, period_start, period_end, provider_invoice_id,
				 provider_payment_id, amount_minor, currency, paid_at)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			on conflict (subscription_id, provider_invoice_id) do nothing
			returning id`, s.CustomerID, s.ID, offerID, inv.PeriodStart, inv.PeriodEnd, inv.InvoiceID,
			inv.PaymentID, inv.AmountMinor, inv.Currency, inv.PaidAt).Scan(&res.PeriodID)
		if errors.Is(err, pgx.ErrNoRows) {
			return tx.QueryRow(ctx, `select id from membership.paid_periods
				where subscription_id = $1 and provider_invoice_id = $2`, s.ID, inv.InvoiceID).Scan(&res.PeriodID)
		}
		if err != nil {
			return err
		}
		res.Created = true
		if _, err := tx.Exec(ctx, `
			update membership.subscriptions
			set offer_id = $2, scheduled_offer_id = null,
			    status = case when status in ('cancelled', 'expired') then status
			                  when cancel_at_period_end then 'cancelling' else 'active' end,
			    current_period_end = greatest(coalesce(current_period_end, $3), $3), updated_at = now()
			where id = $1`, s.ID, offerID, inv.PeriodEnd); err != nil {
			return err
		}

		// Canonical period: an invoice that overlaps a period already paid
		// for (an upgrade, a retry, a rejoin on another device) is recorded
		// but does not open a second bonus.
		if err := tx.QueryRow(ctx, `
			select exists (select 1 from membership.paid_periods
			               where customer_id = $1 and id <> $2 and status = 'paid'
			                 and period_start < $4 and period_end > $3)`,
			s.CustomerID, res.PeriodID, inv.PeriodStart, inv.PeriodEnd).Scan(&res.Overlap); err != nil {
			return err
		}
		if res.Overlap {
			return openCase(ctx, tx, "reconciliation", s.CustomerID, "paid_period", res.PeriodID, map[string]any{
				"problem": "paid period overlaps another paid period; no second bonus slot was created",
				"invoice": inv.InvoiceID,
			})
		}
		var bonus int64
		if err := tx.QueryRow(ctx, `select member_bonus_points from membership.plan_offers where id = $1`, offerID).
			Scan(&bonus); err != nil {
			return err
		}
		if bonus <= 0 {
			return nil
		}
		tag, err := tx.Exec(ctx, `
			insert into membership.bonus_slots (customer_id, paid_period_id, bonus_points)
			values ($1, $2, $3) on conflict (customer_id, paid_period_id) do nothing`,
			s.CustomerID, res.PeriodID, bonus)
		if err != nil {
			return err
		}
		res.SlotCreated = tag.RowsAffected() == 1
		return nil
	})
	return res, err
}

// SetPeriodPaymentStatus records a refund or dispute of a membership
// payment. A slot not yet awarded can no longer unlock; an awarded one opens
// a case, because its points may already be spent.
func (r *CompetitionPointsRepository) SetPeriodPaymentStatus(ctx context.Context, provider, paymentOrInvoiceID, status string) (bool, error) {
	changed := false
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		var (
			periodID, customerID uuid.UUID
			prior                string
		)
		err := tx.QueryRow(ctx, `
			select p.id, p.customer_id, p.status
			from membership.paid_periods p
			inner join membership.subscriptions s on s.id = p.subscription_id
			where s.provider = $1 and (p.provider_payment_id = $2 or p.provider_invoice_id = $2)
			for update of p`, provider, paymentOrInvoiceID).Scan(&periodID, &customerID, &prior)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if prior == status || prior == "voided" {
			return nil
		}
		if _, err := tx.Exec(ctx, `update membership.paid_periods
			set status = $2, status_changed_at = now() where id = $1`, periodID, status); err != nil {
			return err
		}
		changed = true
		var slotStatus string
		var slotID uuid.UUID
		err = tx.QueryRow(ctx, `select id, status from membership.bonus_slots where paid_period_id = $1 for update`,
			periodID).Scan(&slotID, &slotStatus)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if slotStatus == models.BonusAwarded {
			return openCase(ctx, tx, "membership_dispute", customerID, "bonus_slot", slotID, map[string]any{
				"period_id": periodID, "payment_status": status,
			})
		}
		return nil
	})
	return changed, err
}

// ─── Status ──────────────────────────────────────────────────────────────

const periodColumns = `p.id, p.subscription_id, p.offer_id, p.period_start, p.period_end, p.amount_minor,
	p.currency, p.status, p.paid_at, p.status_changed_at, p.provider_invoice_id,
	b.id, b.bonus_points, b.status, b.qualifying_gift_id, b.awarded_lot_id, b.awarded_at, b.status_reason`

func scanPeriod(row scanner) (*models.PaidPeriod, error) {
	var (
		p          models.PaidPeriod
		slotID     *uuid.UUID
		bonus      *int64
		slotStatus *string
		gift, lot  *uuid.UUID
		awardedAt  *time.Time
		reason     *string
	)
	err := row.Scan(&p.ID, &p.SubscriptionID, &p.OfferID, &p.PeriodStart, &p.PeriodEnd, &p.AmountMinor,
		&p.Currency, &p.Status, &p.PaidAt, &p.StatusChangedAt, &p.ProviderInvoiceID,
		&slotID, &bonus, &slotStatus, &gift, &lot, &awardedAt, &reason)
	if err != nil {
		return nil, err
	}
	if slotID != nil {
		p.BonusSlot = &models.BonusSlot{ID: *slotID, PaidPeriodID: p.ID, BonusPoints: *bonus,
			Status: *slotStatus, QualifyingGiftID: gift, AwardedLotID: lot, AwardedAt: awardedAt, StatusReason: reason}
	}
	return &p, nil
}

// Periods lists a customer's paid periods, newest first.
func (r *CompetitionPointsRepository) Periods(ctx context.Context, customerID uuid.UUID, limit int) ([]models.PaidPeriod, error) {
	rows, err := r.db.Query(ctx, `select `+periodColumns+`
		from membership.paid_periods p
		left join membership.bonus_slots b on b.paid_period_id = p.id
		where p.customer_id = $1
		order by p.period_start desc limit $2`, customerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]models.PaidPeriod, 0)
	for rows.Next() {
		p, err := scanPeriod(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// LatestSubscription is the customer's most recent membership of any state.
func (r *CompetitionPointsRepository) LatestSubscription(ctx context.Context, customerID uuid.UUID) (*models.Subscription, error) {
	s, err := scanSubscription(r.db.QueryRow(ctx, `select `+subscriptionColumns+`
		from membership.subscriptions s
		where s.customer_id = $1 and s.status <> 'abandoned'
		order by (s.status in ('incomplete', 'active', 'past_due', 'cancelling')) desc, s.created_at desc
		limit 1`, customerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

// HasPendingGiftInPeriod is whether a gift paid for in the period is still
// waiting to be completed: the bonus is then "pending completion".
func (r *CompetitionPointsRepository) HasPendingGiftInPeriod(ctx context.Context, customerID uuid.UUID, start, end time.Time) (bool, error) {
	var yes bool
	err := r.db.QueryRow(ctx, `
		select exists (select 1 from points.gift_qualifications
		               where customer_id = $1 and status = 'pending'
		                 and anchor_at >= $2 and anchor_at < $3)`, customerID, start, end).Scan(&yes)
	return yes, err
}

// RenewingSubscription is a membership that will renew at its period end.
type RenewingSubscription struct {
	ID                     uuid.UUID
	CustomerID             uuid.UUID
	CountryID              uuid.UUID
	Provider               string
	ProviderSubscriptionID string
}

// RenewingSubscriptions lists memberships set to renew, for the check that
// renewal is still approved where the customer is.
func (r *CompetitionPointsRepository) RenewingSubscriptions(ctx context.Context, limit int) ([]RenewingSubscription, error) {
	rows, err := r.db.Query(ctx, `
		select s.id, s.customer_id, c.country_id, s.provider, s.provider_subscription_id
		from membership.subscriptions s
		inner join customer.customers c on c.id = s.customer_id
		where s.status in ('active', 'past_due') and not s.cancel_at_period_end
		  and s.provider_subscription_id is not null
		order by s.current_period_end nulls last
		limit $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RenewingSubscription
	for rows.Next() {
		var s RenewingSubscription
		if err := rows.Scan(&s.ID, &s.CustomerID, &s.CountryID, &s.Provider, &s.ProviderSubscriptionID); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// MemberDiscount is the product discount a customer gets right now: the
// percentage (in basis points) of the offer their paid period in force was
// sold under, with that period. Zero when they have no paid period now.
// A cancelled membership keeps it to the end of the period already paid for.
func (r *CompetitionPointsRepository) MemberDiscount(ctx context.Context, customerID uuid.UUID) (bps int, periodID uuid.UUID, err error) {
	return memberDiscount(ctx, r.db, customerID)
}

func memberDiscount(ctx context.Context, q querier, customerID uuid.UUID) (bps int, periodID uuid.UUID, err error) {
	err = q.QueryRow(ctx, `
		select o.member_discount_bps, p.id
		from membership.paid_periods p
		inner join membership.plan_offers o on o.id = p.offer_id
		where p.customer_id = $1 and p.status = 'paid'
		  and p.period_start <= now() and now() < p.period_end
		  and o.member_discount_bps > 0
		order by o.member_discount_bps desc, p.period_start desc
		limit 1`, customerID).Scan(&bps, &periodID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, uuid.Nil, nil
	}
	return bps, periodID, err
}

// PeriodEnd is when a paid period ends.
func (r *CompetitionPointsRepository) PeriodEnd(ctx context.Context, periodID uuid.UUID) (*time.Time, error) {
	var end time.Time
	if err := r.db.QueryRow(ctx, `select period_end from membership.paid_periods where id = $1`, periodID).
		Scan(&end); err != nil {
		return nil, err
	}
	return &end, nil
}
