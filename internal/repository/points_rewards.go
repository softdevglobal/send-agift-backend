package repository

import (
	"context"
	"errors"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"myapp/internal/models"
)

// Points that ride on orders: a product's reward points, promised by the
// seller when the order is placed and paid to the customer when the line is
// delivered, and points a customer attaches to a gift, taken from them at
// checkout and handed to the recipient on delivery.
//
// There is no payment step yet (orders stay pending_payment), so delivery
// is the only point at which an order is known to be complete; nothing is
// paid out before it. Every payout is keyed by what earned it, so the job
// below can run repeatedly, or alongside itself, without paying twice.

// reserveOrderRewards promises each line's reward points from its seller,
// inside the order's transaction. A line whose seller does not hold enough
// available points is sold without a reward rather than refused: the reward
// is the seller's promotion, not part of the price. Sellers are locked in id
// order so two checkouts can never wait on each other.
func reserveOrderRewards(ctx context.Context, q querier, items []models.OrderItem) error {
	idx := make([]int, 0, len(items))
	for i := range items {
		items[i].RewardStatus = "none"
		items[i].RewardPoints = 0
		if items[i].RewardPointsPerUnit > 0 {
			idx = append(idx, i)
		}
	}
	sort.SliceStable(idx, func(a, b int) bool {
		return items[idx[a]].SellerID.String() < items[idx[b]].SellerID.String()
	})
	for _, i := range idx {
		total := int64(items[i].RewardPointsPerUnit) * int64(items[i].Quantity)
		ok, err := reserveSellerPoints(ctx, q, items[i].SellerID, total)
		if err != nil {
			return err
		}
		if !ok {
			items[i].RewardPointsPerUnit = 0
			continue
		}
		items[i].RewardPoints = total
		items[i].RewardStatus = "reserved"
	}
	return nil
}

// holdGiftPoints takes the points a customer attached to a gift out of their
// balance, inside the order's transaction. The order row is already written
// as "held".
func holdGiftPoints(ctx context.Context, q querier, order *models.Order) error {
	if order.GiftPoints <= 0 {
		return nil
	}
	_, _, err := applyPoints(ctx, q, PointsChange{
		CustomerID: order.CustomerID, EntryType: models.PointsEntryGiftSent, Delta: -order.GiftPoints,
		OrderID: &order.ID, ReferenceType: ref("order"), ReferenceID: &order.ID,
		IdempotencyKey: "gift-send:" + order.ID.String(), ActorType: "customer", ActorID: &order.CustomerID,
	})
	return err
}

// settleUndeliveredPoints gives back what an order will now never pay out:
// rewards reserved on lines that were cancelled (or whose order was), and
// gift points held on an order that was cancelled or refunded before
// delivery. The caller holds the transaction.
func settleUndeliveredPoints(ctx context.Context, q querier, orderID uuid.UUID) (released int, returned bool, err error) {
	var (
		status, giftStatus string
		giftPoints         int64
		customerID         uuid.UUID
	)
	if err = q.QueryRow(ctx, `
		select status, gift_points, gift_points_status, customer_id
		from marketplace.orders where id = $1 for update`, orderID).
		Scan(&status, &giftPoints, &giftStatus, &customerID); err != nil {
		return 0, false, err
	}
	orderDead := status == "cancelled" || status == "refunded"

	rows, err := q.Query(ctx, `
		select id, seller_id, reward_points
		from marketplace.order_items
		where order_id = $1 and reward_status = 'reserved'
		  and (fulfilment_status = 'cancelled' or $2)
		order by seller_id
		for update`, orderID, orderDead)
	if err != nil {
		return 0, false, err
	}
	type line struct {
		id, seller uuid.UUID
		points     int64
	}
	var lines []line
	for rows.Next() {
		var l line
		if err := rows.Scan(&l.id, &l.seller, &l.points); err != nil {
			rows.Close()
			return 0, false, err
		}
		lines = append(lines, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, false, err
	}
	for _, l := range lines {
		if err := releaseSellerPoints(ctx, q, l.seller, l.points); err != nil {
			return released, false, err
		}
		if _, err := q.Exec(ctx, `
			update marketplace.order_items set reward_status = 'released', updated_at = now()
			where id = $1`, l.id); err != nil {
			return released, false, err
		}
		released++
	}

	if giftStatus == "held" && orderDead {
		reason := "Returned: the gift was cancelled"
		if _, _, err := applyPoints(ctx, q, PointsChange{
			CustomerID: customerID, EntryType: models.PointsEntryGiftReturned, Delta: giftPoints,
			OrderID: &orderID, ReferenceType: ref("order"), ReferenceID: &orderID,
			IdempotencyKey: "gift-return:" + orderID.String(), Reason: &reason, ActorType: "system",
		}); err != nil {
			return released, false, err
		}
		if _, err := q.Exec(ctx, `
			update marketplace.orders set gift_points_status = 'returned' where id = $1`, orderID); err != nil {
			return released, false, err
		}
		returned = true
	}
	return released, returned, nil
}

// payLineReward pays a line's reserved reward: out of the seller's points,
// into the customer's, and the line marked awarded. The caller holds the
// transaction and has checked the line is still reserved.
func payLineReward(ctx context.Context, q querier, itemID, sellerID, customerID, orderID uuid.UUID, points int64) error {
	if _, _, err := applyPoints(ctx, q, PointsChange{
		SellerID: &sellerID, EntryType: models.PointsEntryRewardFunding, Delta: -points,
		ConsumeReserved: points, OrderID: &orderID,
		ReferenceType: ref("order_item"), ReferenceID: &itemID,
		IdempotencyKey: "reward-fund:" + itemID.String(), ActorType: "system",
	}); err != nil {
		return err
	}
	if _, _, err := applyPoints(ctx, q, PointsChange{
		CustomerID: customerID, EntryType: models.PointsEntryProductReward, Delta: points,
		OrderID: &orderID, ReferenceType: ref("order_item"), ReferenceID: &itemID,
		IdempotencyKey: "reward:" + itemID.String(), ActorType: "system",
	}); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `
		update marketplace.order_items set reward_status = 'awarded', updated_at = now()
		where id = $1`, itemID)
	return err
}

// ErrRewardSpent is cancelling an order whose reward points the customer has
// already spent: giving them back is the price of cancelling.
var ErrRewardSpent = errors.New("reward points from this order have already been spent")

// takeBackCancelledRewards reverses rewards already paid on lines that have
// just been cancelled: in full, from the customer back to the seller. When
// the customer no longer holds them the cancellation is refused, so points
// can never be kept from an order that was cancelled. The caller holds the
// transaction.
func takeBackCancelledRewards(ctx context.Context, q querier, orderID uuid.UUID) error {
	rows, err := q.Query(ctx, `
		select oi.id, oi.seller_id, o.customer_id, oi.reward_points
		from marketplace.order_items oi
		inner join marketplace.orders o on o.id = oi.order_id
		where oi.order_id = $1 and oi.reward_status = 'awarded' and oi.fulfilment_status = 'cancelled'
		order by oi.seller_id
		for update of oi`, orderID)
	if err != nil {
		return err
	}
	type line struct {
		id, seller, customer uuid.UUID
		points               int64
	}
	var lines []line
	var total int64
	for rows.Next() {
		var l line
		if err := rows.Scan(&l.id, &l.seller, &l.customer, &l.points); err != nil {
			rows.Close()
			return err
		}
		lines = append(lines, l)
		total += l.points
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(lines) == 0 {
		return err
	}
	for _, l := range lines {
		if _, _, err := lockSellerPointsAccount(ctx, q, l.seller); err != nil {
			return err
		}
	}
	balance, err := lockPointsAccount(ctx, q, lines[0].customer)
	if err != nil {
		return err
	}
	if balance < total {
		return ErrRewardSpent
	}
	reason := "Order cancelled"
	for _, l := range lines {
		if _, _, err := applyPoints(ctx, q, PointsChange{
			CustomerID: l.customer, EntryType: models.PointsEntryProductRewardReversal, Delta: -l.points,
			OrderID: &orderID, ReferenceType: ref("order_item"), ReferenceID: &l.id,
			IdempotencyKey: "reward-reversal:" + l.id.String(), Reason: &reason, ActorType: "system",
		}); err != nil {
			return err
		}
		if _, _, err := applyPoints(ctx, q, PointsChange{
			SellerID: &l.seller, EntryType: models.PointsEntryRewardFundingReturn, Delta: l.points,
			OrderID: &orderID, ReferenceType: ref("order_item"), ReferenceID: &l.id,
			IdempotencyKey: "reward-return:" + l.id.String(), Reason: &reason, ActorType: "system",
		}); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `
			update marketplace.order_items set reward_status = 'reversed', updated_at = now()
			where id = $1`, l.id); err != nil {
			return err
		}
	}
	return nil
}

// inTx runs fn in a transaction of its own.
func (r *PointsRepository) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
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

func (r *PointsRepository) ids(ctx context.Context, sql string, batch int) ([]uuid.UUID, error) {
	rows, err := r.db.Query(ctx, sql, batch)
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

// runOrderPoints is the order part of the earning job: pay rewards on
// delivered lines, give back what cancelled orders will never pay, deliver
// gift points, and take points back from refunded orders.
func (r *PointsRepository) runOrderPoints(ctx context.Context, batch int, res *models.EarningRunResult) error {
	// Delivered lines: the seller's reserved points go to the customer.
	lines, err := r.ids(ctx, `
		select oi.id
		from marketplace.order_items oi
		inner join marketplace.orders o on o.id = oi.order_id
		where oi.reward_status = 'reserved' and oi.fulfilment_status = 'delivered'
		  and o.status not in ('cancelled', 'refunded')
		order by oi.updated_at
		limit $1`, batch)
	if err != nil {
		return err
	}
	for _, id := range lines {
		var points int64
		err := r.inTx(ctx, func(tx pgx.Tx) error {
			var (
				sellerID, customerID, orderID uuid.UUID
				status                        string
			)
			err := tx.QueryRow(ctx, `
				select oi.seller_id, o.customer_id, oi.order_id, oi.reward_points, oi.reward_status
				from marketplace.order_items oi
				inner join marketplace.orders o on o.id = oi.order_id
				where oi.id = $1 and oi.fulfilment_status = 'delivered'
				  and o.status not in ('cancelled', 'refunded')
				for update of oi`, id).Scan(&sellerID, &customerID, &orderID, &points, &status)
			if errors.Is(err, pgx.ErrNoRows) || (err == nil && status != "reserved") {
				return errSkip
			}
			if err != nil {
				return err
			}
			return payLineReward(ctx, tx, id, sellerID, customerID, orderID, points)
		})
		if errors.Is(err, errSkip) || errors.Is(err, ErrPointsDuplicate) {
			continue
		}
		if err != nil {
			return err
		}
		res.ProductRewards++
		res.ProductRewardPoints += points
	}

	// Cancelled lines and orders: reserved rewards back to the seller, held
	// gift points back to the sender.
	orders, err := r.ids(ctx, `
		select distinct o.id
		from marketplace.orders o
		left join marketplace.order_items oi on oi.order_id = o.id and oi.reward_status = 'reserved'
		where (o.gift_points_status = 'held' and o.status in ('cancelled', 'refunded'))
		   or (oi.id is not null and (oi.fulfilment_status = 'cancelled' or o.status in ('cancelled', 'refunded')))
		limit $1`, batch)
	if err != nil {
		return err
	}
	for _, id := range orders {
		var (
			released int
			returned bool
		)
		err := r.inTx(ctx, func(tx pgx.Tx) error {
			var err error
			released, returned, err = settleUndeliveredPoints(ctx, tx, id)
			return err
		})
		if errors.Is(err, ErrPointsDuplicate) {
			continue
		}
		if err != nil {
			return err
		}
		res.RewardsReleased += released
		if returned {
			res.GiftPointsReturned++
		}
	}

	// Delivered gifts: the points go to the recipient's account, matched on
	// the recipient's email, or back to the sender when they have none.
	gifts, err := r.ids(ctx, `
		select id from marketplace.orders
		where gift_points_status = 'held' and status = 'delivered'
		order by updated_at
		limit $1`, batch)
	if err != nil {
		return err
	}
	for _, id := range gifts {
		var delivered bool
		err := r.inTx(ctx, func(tx pgx.Tx) error {
			var (
				senderID    uuid.UUID
				recipientID *uuid.UUID
				points      int64
				status      string
			)
			err := tx.QueryRow(ctx, `
				select o.customer_id, o.gift_points, o.gift_points_status,
				       (select c.id from customer.recipients rc
				        inner join customer.customers c
				                on c.email = rc.email and c.deleted_at is null and c.status = 'active'
				        where rc.id = o.recipient_id)
				from marketplace.orders o
				where o.id = $1 and o.status = 'delivered'
				for update of o`, id).Scan(&senderID, &points, &status, &recipientID)
			if errors.Is(err, pgx.ErrNoRows) || (err == nil && status != "held") {
				return errSkip
			}
			if err != nil {
				return err
			}
			if recipientID == nil {
				reason := "Returned: the recipient has no SendAGift account"
				if _, _, err := applyPoints(ctx, tx, PointsChange{
					CustomerID: senderID, EntryType: models.PointsEntryGiftReturned, Delta: points,
					OrderID: &id, ReferenceType: ref("order"), ReferenceID: &id,
					IdempotencyKey: "gift-return:" + id.String(), Reason: &reason, ActorType: "system",
				}); err != nil {
					return err
				}
				_, err = tx.Exec(ctx, `
					update marketplace.orders set gift_points_status = 'returned' where id = $1`, id)
				return err
			}
			if _, _, err := applyPoints(ctx, tx, PointsChange{
				CustomerID: *recipientID, EntryType: models.PointsEntryGiftReceived, Delta: points,
				OrderID: &id, ReferenceType: ref("order"), ReferenceID: &id,
				IdempotencyKey: "gift-recv:" + id.String(), ActorType: "system",
			}); err != nil {
				return err
			}
			delivered = true
			_, err = tx.Exec(ctx, `
				update marketplace.orders
				set gift_points_status = 'delivered', gift_points_recipient_id = $2
				where id = $1`, id, *recipientID)
			return err
		})
		if errors.Is(err, errSkip) || errors.Is(err, ErrPointsDuplicate) {
			continue
		}
		if err != nil {
			return err
		}
		if delivered {
			res.GiftPointsDelivered++
		} else {
			res.GiftPointsReturned++
		}
	}

	// Refunded after the points were paid: take back what the customer
	// still holds and return it to whoever paid it. Only what is still held
	// can be taken, so a refund never pushes anyone below zero.
	rewarded, err := r.ids(ctx, `
		select oi.id
		from marketplace.order_items oi
		inner join marketplace.orders o on o.id = oi.order_id
		where oi.reward_status = 'awarded' and o.status in ('refunded', 'cancelled')
		limit $1`, batch)
	if err != nil {
		return err
	}
	for _, id := range rewarded {
		err := r.inTx(ctx, func(tx pgx.Tx) error {
			var (
				sellerID, customerID, orderID uuid.UUID
				points                        int64
				status                        string
			)
			err := tx.QueryRow(ctx, `
				select oi.seller_id, o.customer_id, oi.order_id, oi.reward_points, oi.reward_status
				from marketplace.order_items oi
				inner join marketplace.orders o on o.id = oi.order_id
				where oi.id = $1
				for update of oi`, id).Scan(&sellerID, &customerID, &orderID, &points, &status)
			if err != nil {
				return err
			}
			if status != "awarded" {
				return errSkip
			}
			// Seller first, then customer: the same order the payout locks in.
			if _, _, err := lockSellerPointsAccount(ctx, tx, sellerID); err != nil {
				return err
			}
			balance, err := lockPointsAccount(ctx, tx, customerID)
			if err != nil {
				return err
			}
			take := min(points, balance)
			if take > 0 {
				reason := "Order refunded"
				if take < points {
					reason += " (only the points still held were taken back)"
				}
				if _, _, err := applyPoints(ctx, tx, PointsChange{
					CustomerID: customerID, EntryType: models.PointsEntryProductRewardReversal, Delta: -take,
					OrderID: &orderID, ReferenceType: ref("order_item"), ReferenceID: &id,
					IdempotencyKey: "reward-reversal:" + id.String(), Reason: &reason, ActorType: "system",
				}); err != nil {
					return err
				}
				if _, _, err := applyPoints(ctx, tx, PointsChange{
					SellerID: &sellerID, EntryType: models.PointsEntryRewardFundingReturn, Delta: take,
					OrderID: &orderID, ReferenceType: ref("order_item"), ReferenceID: &id,
					IdempotencyKey: "reward-return:" + id.String(), Reason: &reason, ActorType: "system",
				}); err != nil {
					return err
				}
			}
			_, err = tx.Exec(ctx, `
				update marketplace.order_items set reward_status = 'reversed', updated_at = now()
				where id = $1`, id)
			return err
		})
		if errors.Is(err, errSkip) || errors.Is(err, ErrPointsDuplicate) {
			continue
		}
		if err != nil {
			return err
		}
		res.RewardsReversed++
	}

	// Gifts refunded after the recipient got the points: the same, from the
	// recipient back to the sender.
	refundedGifts, err := r.ids(ctx, `
		select id from marketplace.orders
		where gift_points_status = 'delivered' and status = 'refunded'
		limit $1`, batch)
	if err != nil {
		return err
	}
	for _, id := range refundedGifts {
		err := r.inTx(ctx, func(tx pgx.Tx) error {
			var (
				senderID, recipientID uuid.UUID
				points                int64
				status                string
			)
			err := tx.QueryRow(ctx, `
				select customer_id, gift_points_recipient_id, gift_points, gift_points_status
				from marketplace.orders where id = $1 for update`, id).
				Scan(&senderID, &recipientID, &points, &status)
			if err != nil {
				return err
			}
			if status != "delivered" {
				return errSkip
			}
			// Both accounts, in id order, before either moves.
			first, second := senderID, recipientID
			if second.String() < first.String() {
				first, second = second, first
			}
			if _, err := lockPointsAccount(ctx, tx, first); err != nil {
				return err
			}
			if _, err := lockPointsAccount(ctx, tx, second); err != nil {
				return err
			}
			balance, err := lockPointsAccount(ctx, tx, recipientID)
			if err != nil {
				return err
			}
			take := min(points, balance)
			if take > 0 && recipientID != senderID {
				reason := "Returned: the gift was refunded"
				if _, _, err := applyPoints(ctx, tx, PointsChange{
					CustomerID: recipientID, EntryType: models.PointsEntryGiftReversal, Delta: -take,
					OrderID: &id, ReferenceType: ref("order"), ReferenceID: &id,
					IdempotencyKey: "gift-reversal:" + id.String(), ActorType: "system",
				}); err != nil {
					return err
				}
				if _, _, err := applyPoints(ctx, tx, PointsChange{
					CustomerID: senderID, EntryType: models.PointsEntryGiftReturned, Delta: take,
					OrderID: &id, ReferenceType: ref("order"), ReferenceID: &id,
					IdempotencyKey: "gift-reversal-return:" + id.String(), Reason: &reason, ActorType: "system",
				}); err != nil {
					return err
				}
			}
			_, err = tx.Exec(ctx, `
				update marketplace.orders set gift_points_status = 'reversed' where id = $1`, id)
			return err
		})
		if errors.Is(err, errSkip) || errors.Is(err, ErrPointsDuplicate) {
			continue
		}
		if err != nil {
			return err
		}
		res.GiftPointsReversed++
	}
	return nil
}
