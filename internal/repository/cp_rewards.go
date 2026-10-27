package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"myapp/internal/models"
)

// ─── Reward policies ─────────────────────────────────────────────────────

const policyColumns = `p.id, p.country_id, co.name, co.default_currency, p.version, p.qualifying_unit,
	p.completion_status, p.min_eligible_minor, p.exclude_free_gifts, p.base_mode, p.base_fixed_points,
	p.base_points_per_unit, p.base_cap_points, p.expiry_days, p.status, p.effective_from,
	p.created_by_admin_id, p.submitted_by_admin_id, p.approved_by_admin_id, p.published_at,
	p.created_at, p.updated_at`

func scanPolicy(row scanner) (*models.RewardPolicy, error) {
	var p models.RewardPolicy
	err := row.Scan(&p.ID, &p.CountryID, &p.CountryName, &p.Currency, &p.Version, &p.QualifyingUnit,
		&p.CompletionStatus, &p.MinEligibleMinor, &p.ExcludeFreeGifts, &p.BaseMode, &p.BaseFixedPoints,
		&p.BasePointsPerUnit, &p.BaseCapPoints, &p.ExpiryDays, &p.Status, &p.EffectiveFrom,
		&p.CreatedByAdminID, &p.SubmittedByAdminID, &p.ApprovedByAdminID, &p.PublishedAt,
		&p.CreatedAt, &p.UpdatedAt)
	return &p, err
}

func getPolicy(ctx context.Context, q querier, id uuid.UUID, lock bool) (*models.RewardPolicy, error) {
	sql := `select ` + policyColumns + ` from points.reward_policies p
		inner join core.countries co on co.id = p.country_id where p.id = $1`
	if lock {
		sql += ` for update of p`
	}
	p, err := scanPolicy(q.QueryRow(ctx, sql, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrVersionNotFound
	}
	return p, err
}

// ListPolicies is every reward policy version for the admin console.
func (r *CompetitionPointsRepository) ListPolicies(ctx context.Context) ([]models.RewardPolicy, error) {
	rows, err := r.db.Query(ctx, `select `+policyColumns+` from points.reward_policies p
		inner join core.countries co on co.id = p.country_id
		order by co.name, p.version desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]models.RewardPolicy, 0)
	for rows.Next() {
		p, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// PublishedPolicy is the policy a gift paid for now would earn under.
func (r *CompetitionPointsRepository) PublishedPolicy(ctx context.Context, countryID uuid.UUID) (*models.RewardPolicy, error) {
	p, err := scanPolicy(r.db.QueryRow(ctx, `select `+policyColumns+` from points.reward_policies p
		inner join core.countries co on co.id = p.country_id
		where p.country_id = $1 and p.status = 'published' and p.effective_from <= now()`, countryID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

// SavePolicyDraft creates (id nil) or edits a draft reward policy.
func (r *CompetitionPointsRepository) SavePolicyDraft(ctx context.Context, id *uuid.UUID, p *models.RewardPolicy, audit models.AuditEntry) (*models.RewardPolicy, error) {
	var out *models.RewardPolicy
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		var policyID uuid.UUID
		if id == nil {
			err := tx.QueryRow(ctx, `
				insert into points.reward_policies
					(country_id, version, min_eligible_minor, exclude_free_gifts, base_mode,
					 base_fixed_points, base_points_per_unit, base_cap_points, expiry_days, created_by_admin_id)
				values ($1, (select coalesce(max(version), 0) + 1 from points.reward_policies where country_id = $1),
				        $2, $3, $4, $5, $6, $7, $8, $9)
				returning id`, p.CountryID, p.MinEligibleMinor, p.ExcludeFreeGifts, p.BaseMode,
				p.BaseFixedPoints, p.BasePointsPerUnit, p.BaseCapPoints, p.ExpiryDays, p.CreatedByAdminID).
				Scan(&policyID)
			if err := mapVersionErr(err); err != nil {
				return err
			}
			audit.Action = "reward_policy.create"
		} else {
			before, err := getPolicy(ctx, tx, *id, true)
			if err != nil {
				return err
			}
			if before.Status != models.VersionDraft {
				return ErrVersionNotDraft
			}
			if _, err := tx.Exec(ctx, `
				update points.reward_policies
				set min_eligible_minor = $2, exclude_free_gifts = $3, base_mode = $4,
				    base_fixed_points = $5, base_points_per_unit = $6, base_cap_points = $7,
				    expiry_days = $8, updated_at = now()
				where id = $1`, *id, p.MinEligibleMinor, p.ExcludeFreeGifts, p.BaseMode, p.BaseFixedPoints,
				p.BasePointsPerUnit, p.BaseCapPoints, p.ExpiryDays); err != nil {
				return mapVersionErr(err)
			}
			policyID = *id
			audit.Before = before
			audit.Action = "reward_policy.update"
		}
		saved, err := getPolicy(ctx, tx, policyID, false)
		if err != nil {
			return err
		}
		audit.EntityID = &policyID
		audit.After = saved
		if err := insertAudit(ctx, tx, audit); err != nil {
			return err
		}
		out = saved
		return nil
	})
	return out, err
}

// TransitionPolicy moves a reward policy version between statuses, like
// TransitionOffer.
func (r *CompetitionPointsRepository) TransitionPolicy(ctx context.Context, id uuid.UUID, to string, adminID uuid.UUID, audit models.AuditEntry) (*models.RewardPolicy, error) {
	var out *models.RewardPolicy
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		p, err := getPolicy(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if err := checkTransition(p.Status, to, p.SubmittedByAdminID, adminID); err != nil {
			return err
		}
		switch to {
		case models.VersionAwaitingApproval:
			_, err = tx.Exec(ctx, `update points.reward_policies
				set status = $2, submitted_by_admin_id = $3, updated_at = now() where id = $1`, id, to, adminID)
		case models.VersionPublished:
			if p.Status == models.VersionAwaitingApproval {
				if _, err = tx.Exec(ctx, `update points.reward_policies set status = 'retired', updated_at = now()
					where country_id = $1 and status in ('published', 'suspended') and id <> $2`,
					p.CountryID, id); err != nil {
					return err
				}
				_, err = tx.Exec(ctx, `update points.reward_policies
					set status = 'published', approved_by_admin_id = $2, published_at = now(),
					    effective_from = coalesce(effective_from, now()), updated_at = now()
					where id = $1`, id, adminID)
			} else {
				_, err = tx.Exec(ctx, `update points.reward_policies set status = 'published', updated_at = now()
					where id = $1`, id)
			}
		default:
			_, err = tx.Exec(ctx, `update points.reward_policies set status = $2, updated_at = now() where id = $1`,
				id, to)
		}
		if err != nil {
			return mapVersionErr(err)
		}
		saved, err := getPolicy(ctx, tx, id, false)
		if err != nil {
			return err
		}
		audit.EntityID = &id
		audit.Before = map[string]any{"status": p.Status}
		audit.After = map[string]any{"status": saved.Status}
		if err := insertAudit(ctx, tx, audit); err != nil {
			return err
		}
		out = saved
		return nil
	})
	return out, err
}

// DeletePolicyDraft removes a reward policy draft never submitted.
func (r *CompetitionPointsRepository) DeletePolicyDraft(ctx context.Context, id uuid.UUID, audit models.AuditEntry) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `delete from points.reward_policies where id = $1 and status = 'draft'`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			if _, err := getPolicy(ctx, tx, id, false); err != nil {
				return err
			}
			return ErrVersionNotDraft
		}
		audit.EntityID = &id
		return insertAudit(ctx, tx, audit)
	})
}

// BasePoints is what a gift earns under a policy. Integer arithmetic only:
// per_currency_unit rounds down to whole points.
func BasePoints(p *models.RewardPolicy, eligibleMinor int64, currency string) int64 {
	var pts int64
	switch p.BaseMode {
	case "fixed":
		pts = p.BaseFixedPoints
	case "per_currency_unit":
		pts = PointsForAmount(eligibleMinor, currency, p.BasePointsPerUnit)
	}
	if p.BaseCapPoints != nil && pts > *p.BaseCapPoints {
		pts = *p.BaseCapPoints
	}
	return max(pts, 0)
}

// ─── Review cases ────────────────────────────────────────────────────────

// openCase opens a case once per subject; a second call changes nothing.
func openCase(ctx context.Context, q querier, kind string, customerID uuid.UUID, subjectType string, subjectID uuid.UUID, detail map[string]any) error {
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `
		insert into points.cp_review_cases (kind, customer_id, subject_type, subject_id, detail)
		values ($1, $2, $3, $4, $5)
		on conflict (kind, subject_type, subject_id) do nothing`, kind, customerID, subjectType, subjectID, raw)
	return err
}

// ListCases lists review cases, open ones first.
func (r *CompetitionPointsRepository) ListCases(ctx context.Context, status string) ([]models.CPReviewCase, error) {
	rows, err := r.db.Query(ctx, `
		select id, kind, customer_id, subject_type, subject_id, detail, status, resolution, resolved_at, created_at
		from points.cp_review_cases
		where ($1 = '' or status = $1)
		order by (status = 'open') desc, created_at desc limit 300`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]models.CPReviewCase, 0)
	for rows.Next() {
		var c models.CPReviewCase
		var detail []byte
		if err := rows.Scan(&c.ID, &c.Kind, &c.CustomerID, &c.SubjectType, &c.SubjectID, &detail, &c.Status,
			&c.Resolution, &c.ResolvedAt, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.Detail = detail
		out = append(out, c)
	}
	return out, rows.Err()
}

// ResolveCase closes a case with what was decided. Any change to points is
// a separately approved grant or restoration, never an edit here.
func (r *CompetitionPointsRepository) ResolveCase(ctx context.Context, id, adminID uuid.UUID, resolution string, audit models.AuditEntry) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			update points.cp_review_cases
			set status = 'resolved', resolution = $2, resolved_by_admin_id = $3, resolved_at = now()
			where id = $1 and status = 'open'`, id, resolution, adminID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrVersionNotFound
		}
		audit.EntityID = &id
		audit.After = map[string]any{"resolution": resolution}
		return insertAudit(ctx, tx, audit)
	})
}

// ─── The reward engine ───────────────────────────────────────────────────

// RewardGate says whether gift earning may run in a country now: an active
// approval and no stop. The service supplies it so the repository does not
// decide policy.
type RewardGate func(ctx context.Context, countryID uuid.UUID) (bool, error)

// RunCompetitionPoints is one pass of the competition-points job. Every
// step is keyed by what it records, so passes can repeat or overlap
// without paying twice.
func (r *CompetitionPointsRepository) RunCompetitionPoints(ctx context.Context, batch int, gate RewardGate, restoreWindow time.Duration) (*models.CPRunResult, error) {
	res := &models.CPRunResult{}
	steps := []func(context.Context, int, *models.CPRunResult) error{
		func(ctx context.Context, n int, res *models.CPRunResult) error {
			return r.qualifyPaidGifts(ctx, n, gate, res)
		},
		r.cancelUnfinishedGifts,
		r.awardCompletedGifts,
		r.awardDeferredBonuses,
		r.reverseRefundedGifts,
		r.expireLots,
		func(ctx context.Context, n int, res *models.CPRunResult) error {
			return r.releaseStaleEntries(ctx, n, restoreWindow, res)
		},
	}
	for _, step := range steps {
		if err := step(ctx, batch, res); err != nil {
			return res, err
		}
	}
	return res, nil
}

// qualifyPaidGifts snapshots each newly paid order against the reward policy
// that was in force when the payment was captured. The capture time is the
// gift's immutable anchor.
func (r *CompetitionPointsRepository) qualifyPaidGifts(ctx context.Context, batch int, gate RewardGate, res *models.CPRunResult) error {
	type paid struct {
		orderID, customerID, countryID, paymentID uuid.UUID
		subtotal                                  int64
		currency                                  string
		paidAt                                    time.Time
		free                                      bool
	}
	rows, err := r.db.Query(ctx, `
		select o.id, o.customer_id, o.country_id, pay.id, o.subtotal_amount - o.member_discount_amount, o.currency, pay.paid_at,
		       exists (select 1 from promo.free_gift_claims fc where fc.order_id = o.id)
		from marketplace.orders o
		inner join lateral (
			select id, paid_at from finance.payments
			where order_id = o.id and status = 'succeeded' and paid_at is not null
			order by paid_at limit 1) pay on true
		where o.status not in ('draft', 'pending_payment', 'cancelled', 'refunded')
		  and exists (select 1 from points.reward_policies rp
		              where rp.country_id = o.country_id and rp.effective_from <= pay.paid_at
		                and rp.status in ('published', 'retired', 'suspended'))
		  and not exists (select 1 from points.gift_qualifications g where g.order_id = o.id)
		order by pay.paid_at
		limit $1`, batch)
	if err != nil {
		return err
	}
	var list []paid
	for rows.Next() {
		var p paid
		if err := rows.Scan(&p.orderID, &p.customerID, &p.countryID, &p.paymentID, &p.subtotal, &p.currency,
			&p.paidAt, &p.free); err != nil {
			rows.Close()
			return err
		}
		list = append(list, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	allowed := map[uuid.UUID]bool{}
	for _, p := range list {
		ok, seen := allowed[p.countryID]
		if !seen {
			if ok, err = gate(ctx, p.countryID); err != nil {
				return err
			}
			allowed[p.countryID] = ok
		}
		if !ok {
			continue // not approved here now; looked at again on a later pass
		}
		// The version in force at the anchor: the latest that had taken
		// effect by then. A suspended version earns nothing new.
		policy, err := scanPolicy(r.db.QueryRow(ctx, `select `+policyColumns+`
			from points.reward_policies p inner join core.countries co on co.id = p.country_id
			where p.country_id = $1 and p.effective_from <= $2
			  and p.status in ('published', 'retired', 'suspended') and p.published_at <= $2
			order by p.effective_from desc, p.version desc limit 1`, p.countryID, p.paidAt))
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		status, reason := models.GiftPending, (*string)(nil)
		switch {
		case policy.Status == models.VersionSuspended:
			continue
		case policy.ExcludeFreeGifts && p.free:
			status, reason = models.GiftCancelled, strPtr("a free gift does not earn competition points")
		case p.subtotal < policy.MinEligibleMinor:
			status, reason = models.GiftCancelled, strPtr("below the minimum eligible spend")
		}
		base := BasePoints(policy, p.subtotal, p.currency)
		tag, err := r.db.Exec(ctx, `
			insert into points.gift_qualifications
				(order_id, customer_id, policy_id, payment_id, anchor_at, eligible_minor, currency,
				 base_points, status, status_reason)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			on conflict (order_id) do nothing`,
			p.orderID, p.customerID, policy.ID, p.paymentID, p.paidAt, p.subtotal, p.currency, base, status, reason)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 && status == models.GiftPending {
			res.Qualified++
		}
	}
	return nil
}

// giftStillValid is whether the order behind a qualification is still a
// completed, paid, unrefunded gift.
const giftStillValidSQL = `
	o.status = 'delivered'
	and not exists (select 1 from finance.payments px
	                where px.order_id = o.id and px.status = 'refunded')
	and exists (select 1 from finance.payments py
	            where py.order_id = o.id and py.status = 'succeeded')`

// cancelUnfinishedGifts cancels pending rewards whose gift was cancelled or
// refunded before it completed. Nothing was granted, so nothing is reversed.
func (r *CompetitionPointsRepository) cancelUnfinishedGifts(ctx context.Context, batch int, res *models.CPRunResult) error {
	tag, err := r.db.Exec(ctx, `
		update points.gift_qualifications g
		set status = 'cancelled', status_reason = 'the gift was cancelled or refunded before completion',
		    updated_at = now()
		from marketplace.orders o
		where o.id = g.order_id and g.status = 'pending'
		  and (o.status in ('cancelled', 'refunded')
		       or exists (select 1 from finance.payments px where px.order_id = o.id and px.status = 'refunded'))
		  and g.id in (select id from points.gift_qualifications where status = 'pending' limit $1)`, batch)
	if err != nil {
		return err
	}
	res.GiftsCancelled += int(tag.RowsAffected())
	return nil
}

// awardCompletedGifts grants the base points of each completed gift, once,
// and claims the bonus slot of the paid period its anchor falls in, if that
// slot is still unawarded. Base and bonus are separate lots committed in one
// transaction.
func (r *CompetitionPointsRepository) awardCompletedGifts(ctx context.Context, batch int, res *models.CPRunResult) error {
	ids, err := r.idList(ctx, `
		select g.id from points.gift_qualifications g
		inner join marketplace.orders o on o.id = g.order_id
		where g.status = 'pending' and `+giftStillValidSQL+`
		order by g.anchor_at limit $1`, batch)
	if err != nil {
		return err
	}
	for _, id := range ids {
		var base, bonus int64
		err := r.inTx(ctx, func(tx pgx.Tx) error {
			var b, bn int64
			var err error
			b, bn, err = awardGift(ctx, tx, id)
			base, bonus = b, bn
			return err
		})
		if errors.Is(err, errSkip) {
			continue
		}
		if err != nil {
			return err
		}
		res.GiftsAwarded++
		res.BasePoints += base
		if bonus > 0 {
			res.BonusesAwarded++
			res.BonusPoints += bonus
		}
	}
	return nil
}

func (r *CompetitionPointsRepository) idList(ctx context.Context, sql string, args ...any) ([]uuid.UUID, error) {
	rows, err := r.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

type lockedGift struct {
	ID, CustomerID, PolicyID uuid.UUID
	AnchorAt                 time.Time
	BasePoints               int64
	Status                   string
	BaseLotID, BonusSlotID   *uuid.UUID
	ExpiryDays               *int
	OrderNumber              string
}

func lockGift(ctx context.Context, q querier, id uuid.UUID) (*lockedGift, bool, error) {
	var g lockedGift
	var valid bool
	err := q.QueryRow(ctx, `
		select g.id, g.customer_id, g.policy_id, g.anchor_at, g.base_points, g.status, g.base_lot_id,
		       g.bonus_slot_id, p.expiry_days, o.order_number, (`+giftStillValidSQL+`)
		from points.gift_qualifications g
		inner join points.reward_policies p on p.id = g.policy_id
		inner join marketplace.orders o on o.id = g.order_id
		where g.id = $1
		for update of g`, id).
		Scan(&g.ID, &g.CustomerID, &g.PolicyID, &g.AnchorAt, &g.BasePoints, &g.Status, &g.BaseLotID,
			&g.BonusSlotID, &g.ExpiryDays, &g.OrderNumber, &valid)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, errSkip
	}
	return &g, valid, err
}

func lotExpiry(days *int) *time.Time {
	if days == nil {
		return nil
	}
	t := time.Now().UTC().AddDate(0, 0, *days)
	return &t
}

// awardGift is one gift's award, inside the caller's transaction.
func awardGift(ctx context.Context, tx pgx.Tx, id uuid.UUID) (base, bonus int64, err error) {
	g, valid, err := lockGift(ctx, tx, id)
	if err != nil {
		return 0, 0, err
	}
	if g.Status != models.GiftPending || !valid {
		return 0, 0, errSkip // moved on, or no longer a completed paid gift
	}
	reason := "gift " + g.OrderNumber + " completed"
	var baseLot *uuid.UUID
	if g.BasePoints > 0 {
		lot, _, err := grantCPLot(ctx, tx, CPGrant{
			CustomerID: g.CustomerID, Source: models.LotSourceGift, SourceID: g.ID, PolicyID: &g.PolicyID,
			Quantity: g.BasePoints, ExpiresAt: lotExpiry(g.ExpiryDays),
			Key: "cp:gift:" + g.ID.String(), Actor: systemActor, Reason: &reason,
		})
		if err != nil {
			return 0, 0, err
		}
		baseLot = &lot
		base = g.BasePoints
	}
	slotID, bonusPts, err := claimBonus(ctx, tx, g)
	if err != nil {
		return 0, 0, err
	}
	if _, err := tx.Exec(ctx, `
		update points.gift_qualifications
		set status = 'awarded', base_lot_id = $2, bonus_slot_id = coalesce($3, bonus_slot_id),
		    completed_at = now(), updated_at = now()
		where id = $1`, g.ID, baseLot, slotID); err != nil {
		return 0, 0, err
	}
	return base, bonusPts, nil
}

// claimBonus awards the bonus of the paid period containing the gift's
// anchor, if its slot is still unawarded. The slot row is the lock: of two
// gifts completing at once, the second finds it awarded and gets ordinary
// points only.
func claimBonus(ctx context.Context, tx pgx.Tx, g *lockedGift) (*uuid.UUID, int64, error) {
	var (
		slotID   uuid.UUID
		points   int64
		policyID = g.PolicyID
	)
	err := tx.QueryRow(ctx, `
		select b.id, b.bonus_points
		from membership.paid_periods p
		inner join membership.bonus_slots b on b.paid_period_id = p.id
		where p.customer_id = $1 and p.status = 'paid'
		  and p.period_start <= $2 and $2 < p.period_end
		  and b.status = 'unawarded'
		order by p.period_start
		limit 1
		for update of b skip locked`, g.CustomerID, g.AnchorAt).Scan(&slotID, &points)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	// Re-read under the lock: another gift may have claimed it first.
	var status string
	if err := tx.QueryRow(ctx, `select status from membership.bonus_slots where id = $1`, slotID).
		Scan(&status); err != nil {
		return nil, 0, err
	}
	if status != models.BonusUnawarded {
		return nil, 0, nil
	}
	var lotID *uuid.UUID
	if points > 0 {
		reason := "member bonus unlocked by gift " + g.OrderNumber
		lot, _, err := grantCPLot(ctx, tx, CPGrant{
			CustomerID: g.CustomerID, Source: models.LotSourceMemberBonus, SourceID: slotID, PolicyID: &policyID,
			Quantity: points, ExpiresAt: lotExpiry(g.ExpiryDays),
			Key: "cp:bonus:" + slotID.String(), Actor: systemActor, Reason: &reason,
		})
		if err != nil {
			return nil, 0, err
		}
		lotID = &lot
	}
	if _, err := tx.Exec(ctx, `
		update membership.bonus_slots
		set status = 'awarded', qualifying_gift_id = $2, awarded_lot_id = $3, awarded_at = now(), updated_at = now()
		where id = $1 and status = 'unawarded'`, slotID, g.ID, lotID); err != nil {
		return nil, 0, err
	}
	return &slotID, points, nil
}

// awardDeferredBonuses gives a bonus to a gift that completed before the
// membership payment that covers its anchor was confirmed. The gift and its
// payment are checked again first.
func (r *CompetitionPointsRepository) awardDeferredBonuses(ctx context.Context, batch int, res *models.CPRunResult) error {
	ids, err := r.idList(ctx, `
		select g.id from points.gift_qualifications g
		inner join marketplace.orders o on o.id = g.order_id
		where g.status = 'awarded' and g.bonus_slot_id is null and `+giftStillValidSQL+`
		  and exists (select 1 from membership.paid_periods p
		              inner join membership.bonus_slots b on b.paid_period_id = p.id
		              where p.customer_id = g.customer_id and p.status = 'paid'
		                and p.period_start <= g.anchor_at and g.anchor_at < p.period_end
		                and b.status = 'unawarded')
		order by g.anchor_at limit $1`, batch)
	if err != nil {
		return err
	}
	for _, id := range ids {
		var bonus int64
		err := r.inTx(ctx, func(tx pgx.Tx) error {
			g, valid, err := lockGift(ctx, tx, id)
			if err != nil {
				return err
			}
			if g.Status != models.GiftAwarded || g.BonusSlotID != nil || !valid {
				return errSkip
			}
			slotID, pts, err := claimBonus(ctx, tx, g)
			if err != nil || slotID == nil {
				return err
			}
			bonus = pts
			_, err = tx.Exec(ctx, `update points.gift_qualifications
				set bonus_slot_id = $2, updated_at = now() where id = $1`, g.ID, *slotID)
			return err
		})
		if errors.Is(err, errSkip) {
			continue
		}
		if err != nil {
			return err
		}
		if bonus > 0 {
			res.BonusesAwarded++
			res.BonusPoints += bonus
		}
	}
	return nil
}

// reverseRefundedGifts takes back the unused points of a gift refunded after
// its award: from the gift's own lot and, if it unlocked one, its bonus lot.
// Units held by an entry wait until the entry settles; units already spent
// open a case instead of a negative balance. The bonus slot stays reversed,
// so the period cannot pay a second bonus by itself.
func (r *CompetitionPointsRepository) reverseRefundedGifts(ctx context.Context, batch int, res *models.CPRunResult) error {
	ids, err := r.idList(ctx, `
		select g.id from points.gift_qualifications g
		inner join marketplace.orders o on o.id = g.order_id
		where g.status = 'awarded'
		  and (o.status in ('cancelled', 'refunded')
		       or exists (select 1 from finance.payments px where px.order_id = o.id and px.status = 'refunded'))
		order by g.updated_at limit $1`, batch)
	if err != nil {
		return err
	}
	for _, id := range ids {
		var reversed, opened bool
		err := r.inTx(ctx, func(tx pgx.Tx) error {
			g, _, err := lockGift(ctx, tx, id)
			if err != nil {
				return err
			}
			if g.Status != models.GiftAwarded {
				return errSkip
			}
			var lots []uuid.UUID
			if g.BaseLotID != nil {
				lots = append(lots, *g.BaseLotID)
			}
			if g.BonusSlotID != nil {
				var bonusLot *uuid.UUID
				if err := tx.QueryRow(ctx, `select awarded_lot_id from membership.bonus_slots
					where id = $1 and qualifying_gift_id = $2`, *g.BonusSlotID, g.ID).Scan(&bonusLot); err != nil &&
					!errors.Is(err, pgx.ErrNoRows) {
					return err
				}
				if bonusLot != nil {
					lots = append(lots, *bonusLot)
				}
			}
			if err := lockLots(ctx, tx, lots); err != nil {
				return err
			}
			held, spent := false, false
			reason := "gift " + g.OrderNumber + " refunded"
			for _, lotID := range lots {
				var available, reserved, consumed, already int64
				if err := tx.QueryRow(ctx, `select available, reserved, consumed, reversed from points.cp_lots
					where id = $1`, lotID).Scan(&available, &reserved, &consumed, &already); err != nil {
					return err
				}
				if reserved > 0 {
					held = true
				}
				if consumed > 0 {
					spent = true
				}
				if available > 0 {
					txID, first, err := beginCPTx(ctx, tx, fmt.Sprintf("cp:reverse:%s:%d", lotID, already), "reverse",
						g.CustomerID, systemActor, &g.ID, &reason)
					if err != nil {
						return err
					}
					if first {
						if err := moveCP(ctx, tx, txID, lotID, "available", "reversed", available); err != nil {
							return err
						}
					}
				}
			}
			if held {
				return nil // the entry settles first; looked at again next pass
			}
			status := models.GiftReversed
			if spent {
				status = models.GiftReview
				if err := openCase(ctx, tx, "refund_after_spend", g.CustomerID, "gift_qualification", g.ID,
					map[string]any{"order": g.OrderNumber, "lots": lots}); err != nil {
					return err
				}
				opened = true
			}
			if g.BonusSlotID != nil {
				if _, err := tx.Exec(ctx, `update membership.bonus_slots
					set status = 'reversed', status_reason = $2, updated_at = now()
					where id = $1 and qualifying_gift_id = $3 and status = 'awarded'`,
					*g.BonusSlotID, "the gift that unlocked it was refunded", g.ID); err != nil {
					return err
				}
			}
			reversed = true
			_, err = tx.Exec(ctx, `update points.gift_qualifications
				set status = $2, status_reason = $3, updated_at = now() where id = $1`, g.ID, status, reason)
			return err
		})
		if errors.Is(err, errSkip) {
			continue
		}
		if err != nil {
			return err
		}
		if reversed {
			res.GiftsReversed++
		}
		if opened {
			res.CasesOpened++
		}
	}
	return nil
}

// expireLots expires the available units of lots past their disclosed
// expiry (and any restoration window). Held units are never expired here.
func (r *CompetitionPointsRepository) expireLots(ctx context.Context, batch int, res *models.CPRunResult) error {
	ids, err := r.idList(ctx, `
		select id from points.cp_lots
		where available > 0 and expires_at is not null and expires_at <= now()
		  and (grace_until is null or grace_until <= now())
		order by expires_at limit $1`, batch)
	if err != nil {
		return err
	}
	for _, id := range ids {
		err := r.inTx(ctx, func(tx pgx.Tx) error {
			var (
				customerID         uuid.UUID
				available, expired int64
				due                bool
			)
			err := tx.QueryRow(ctx, `
				select customer_id, available, expired,
				       expires_at <= now() and (grace_until is null or grace_until <= now())
				from points.cp_lots where id = $1 for update`, id).Scan(&customerID, &available, &expired, &due)
			if err != nil {
				return err
			}
			if !due || available == 0 {
				return errSkip
			}
			reason := "expired under the terms they were granted with"
			txID, first, err := beginCPTx(ctx, tx, fmt.Sprintf("cp:expire:%s:%d", id, expired), "expire",
				customerID, systemActor, &id, &reason)
			if err != nil || !first {
				return err
			}
			return moveCP(ctx, tx, txID, id, "available", "expired", available)
		})
		if errors.Is(err, errSkip) {
			continue
		}
		if err != nil {
			return err
		}
		res.LotsExpired++
	}
	return nil
}
