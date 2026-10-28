package repository

import (
	"context"
	"time"

	"github.com/google/uuid"

	"myapp/internal/models"
)

// CPWallet is a customer's competition points: lots with their buckets,
// gifts still waiting to complete, entries still held and recent activity.
// It is a read model; spending authority stays in the locked transactions.
func (r *CompetitionPointsRepository) CPWallet(ctx context.Context, customerID uuid.UUID, activityLimit int) (*models.CPWallet, error) {
	w := &models.CPWallet{BySource: map[string]int64{}, Lots: []models.CPLot{},
		PendingGifts: []models.GiftQualification{}, OpenEntries: []models.CPEntry{}, Activity: []models.CPActivity{}}

	rows, err := r.db.Query(ctx, `
		select l.id, l.source, l.source_id, l.original, l.available, l.reserved, l.consumed, l.reversed,
		       l.expired, l.expires_at, l.restrictions, l.created_at,
		       coalesce(og.order_number, ob.order_number),
		       (l.expires_at is null or l.expires_at > now() or l.grace_until > now())
		from points.cp_lots l
		left join points.gift_qualifications g on l.source = 'gift' and g.id = l.source_id
		left join marketplace.orders og on og.id = g.order_id
		left join membership.bonus_slots b on l.source = 'member_bonus' and b.id = l.source_id
		left join points.gift_qualifications gb on gb.id = b.qualifying_gift_id
		left join marketplace.orders ob on ob.id = gb.order_id
		where l.customer_id = $1
		order by (l.available + l.reserved > 0) desc, l.created_at desc
		limit 200`, customerID)
	if err != nil {
		return nil, err
	}
	soon := time.Now().Add(30 * 24 * time.Hour)
	for rows.Next() {
		var (
			l      models.CPLot
			raw    []byte
			usable bool
		)
		if err := rows.Scan(&l.ID, &l.Source, &l.SourceID, &l.Original, &l.Available, &l.Reserved, &l.Consumed,
			&l.Reversed, &l.Expired, &l.ExpiresAt, &raw, &l.CreatedAt, &l.OrderNumber, &usable); err != nil {
			rows.Close()
			return nil, err
		}
		l.Restrictions = raw
		w.Reserved += l.Reserved
		if usable {
			w.Available += l.Available
			w.BySource[l.Source] += l.Available
			if l.ExpiresAt != nil && l.ExpiresAt.Before(soon) {
				w.ExpiringSoon += l.Available
			}
		}
		w.Lots = append(w.Lots, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = r.db.Query(ctx, `
		select g.id, g.order_id, o.order_number, g.policy_id, g.anchor_at, g.eligible_minor, g.currency,
		       g.base_points, g.status, g.base_lot_id, g.bonus_slot_id, g.completed_at, g.status_reason, g.created_at
		from points.gift_qualifications g
		inner join marketplace.orders o on o.id = g.order_id
		where g.customer_id = $1 and g.status in ('pending', 'review')
		order by g.anchor_at desc limit 50`, customerID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var g models.GiftQualification
		if err := rows.Scan(&g.ID, &g.OrderID, &g.OrderNumber, &g.PolicyID, &g.AnchorAt, &g.EligibleMinor,
			&g.Currency, &g.BasePoints, &g.Status, &g.BaseLotID, &g.BonusSlotID, &g.CompletedAt,
			&g.StatusReason, &g.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		if g.Status == models.GiftPending {
			w.Pending += g.BasePoints
		}
		w.PendingGifts = append(w.PendingGifts, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = r.db.Query(ctx, `select `+entryColumns+`
		from competition.cp_entries e inner join competition.competitions c on c.id = e.competition_id
		where e.customer_id = $1 and e.state in ('reserved', 'uncertain')
		order by e.reserved_at desc`, customerID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		w.OpenEntries = append(w.OpenEntries, *e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = r.db.Query(ctx, `
		select t.id, t.kind, t.reason, t.created_at,
		       coalesce(sum(p.delta) filter (where p.bucket = 'available'), 0),
		       coalesce(sum(abs(p.delta)) filter (where p.bucket <> 'issuance'), 0) / 2
		         + coalesce(sum(-p.delta) filter (where p.bucket = 'issuance'), 0) / 2,
		       coalesce(min(l.source), '')
		from points.cp_transactions t
		inner join points.cp_postings p on p.transaction_id = t.id
		inner join points.cp_lots l on l.id = p.lot_id
		where t.customer_id = $1
		group by t.id
		order by t.created_at desc
		limit $2`, customerID, activityLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a models.CPActivity
		if err := rows.Scan(&a.ID, &a.Kind, &a.Reason, &a.CreatedAt, &a.Delta, &a.Amount, &a.Source); err != nil {
			return nil, err
		}
		a.Description = describeCPActivity(a)
		w.Activity = append(w.Activity, a)
	}
	return w, rows.Err()
}

func describeCPActivity(a models.CPActivity) string {
	switch a.Kind {
	case "grant":
		if a.Source == models.LotSourceMemberBonus {
			return "Member bonus unlocked"
		}
		if a.Source == models.LotSourceGift {
			return "Earned from a completed gift"
		}
		return "Points granted"
	case "reserve":
		return "Held for a competition entry"
	case "capture":
		return "Competition attempt started"
	case "release":
		return "Returned: the attempt did not start"
	case "reverse":
		return "Taken back: the gift was refunded"
	case "expire":
		return "Expired"
	case "restore":
		return "Restored: the attempt was voided"
	}
	return a.Kind
}

// ReconcileCompetitionPoints compares every lot's buckets with its postings,
// counts holds that should not be open and reports the oldest pending gift.
// It reports; it never repairs a balance.
func (r *CompetitionPointsRepository) ReconcileCompetitionPoints(ctx context.Context) (*models.CPReconciliation, error) {
	out := &models.CPReconciliation{Mismatches: []uuid.UUID{}}
	rows, err := r.db.Query(ctx, `
		select l.id,
		       l.original <> -coalesce(p.issuance, 0)
		    or l.available <> coalesce(p.available, 0) or l.reserved <> coalesce(p.reserved, 0)
		    or l.consumed <> coalesce(p.consumed, 0) or l.reversed <> coalesce(p.reversed, 0)
		    or l.expired <> coalesce(p.expired, 0)
		from points.cp_lots l
		left join lateral (
			select sum(delta) filter (where bucket = 'issuance') issuance,
			       sum(delta) filter (where bucket = 'available') available,
			       sum(delta) filter (where bucket = 'reserved') reserved,
			       sum(delta) filter (where bucket = 'consumed') consumed,
			       sum(delta) filter (where bucket = 'reversed') reversed,
			       sum(delta) filter (where bucket = 'expired') expired
			from points.cp_postings where lot_id = l.id) p on true`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var (
			id  uuid.UUID
			bad bool
		)
		if err := rows.Scan(&id, &bad); err != nil {
			rows.Close()
			return nil, err
		}
		out.LotsChecked++
		if bad {
			out.Mismatches = append(out.Mismatches, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Reserved units on a lot must equal what open entries hold of it.
	if err := r.db.QueryRow(ctx, `
		select count(*) from points.cp_lots l
		where l.reserved <> coalesce((select sum(el.quantity) from competition.cp_entry_lots el
		                              inner join competition.cp_entries e on e.id = el.entry_id
		                              where el.lot_id = l.id and e.state in ('reserved', 'uncertain')), 0)`).
		Scan(&out.OrphanedHolds); err != nil {
		return nil, err
	}
	if err := r.db.QueryRow(ctx, `select count(*) from competition.cp_entries where state = 'uncertain'`).
		Scan(&out.UncertainStarts); err != nil {
		return nil, err
	}
	if err := r.db.QueryRow(ctx, `select min(anchor_at) from points.gift_qualifications where status = 'pending'`).
		Scan(&out.OldestPendingAt); err != nil {
		return nil, err
	}
	return out, nil
}
