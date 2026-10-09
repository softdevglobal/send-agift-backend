package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"myapp/internal/models"
)

// OrderEmailItem is one line of an order as an email shows it.
type OrderEmailItem struct {
	ProductName string
	ImageURL    *string
	ShopName    string
	Quantity    int
	TotalAmount int
}

// OrderEmailSummary is everything the order emails need about one order:
// the sender, the recipient and what was bought.
type OrderEmailSummary struct {
	OrderID        uuid.UUID
	OrderNumber    string
	Status         string
	CountryID      uuid.UUID
	DeliveryDate   time.Time
	SubtotalAmount int
	DeliveryAmount int
	TotalAmount    int
	Currency       string
	GiftMessage    *string
	GiftPoints     int64
	CreatedAt      time.Time

	CustomerID    uuid.UUID
	CustomerEmail string
	CustomerName  *string

	RecipientName  *string
	RecipientEmail *string
	RecipientPhone *string
	RecipientCity  *string
	// The recipient's SendAGift account, once one is linked to the order.
	RecipientCustomerID *uuid.UUID
	// Whether that account still has the default password it was made with.
	RecipientPasswordChangeRequired bool

	Items []OrderEmailItem
}

// OrderEmailSummary loads one order for its emails.
func (r *OrderRepository) OrderEmailSummary(ctx context.Context, orderID uuid.UUID) (*OrderEmailSummary, error) {
	s := &OrderEmailSummary{}
	err := r.db.QueryRow(ctx, `
		select o.id, o.order_number, o.status, o.country_id, o.delivery_date,
		       o.subtotal_amount, o.delivery_amount, o.total_amount, o.currency,
		       o.gift_message, o.gift_points, o.created_at,
		       c.id, c.email, c.display_name,
		       r.name, nullif(trim(r.email), ''), nullif(trim(r.phone), ''), ra.city,
		       o.recipient_customer_id, coalesce(rc.password_change_required, false)
		from marketplace.orders o
		inner join customer.customers c on c.id = o.customer_id
		left join customer.recipients r on r.id = o.recipient_id
		left join customer.recipient_addresses ra on ra.id = coalesce(
			r.default_address_id,
			(select id from customer.recipient_addresses where recipient_id = r.id
			 order by is_default desc, created_at asc limit 1)
		)
		left join customer.customers rc on rc.id = o.recipient_customer_id
		where o.id = $1`, orderID,
	).Scan(
		&s.OrderID, &s.OrderNumber, &s.Status, &s.CountryID, &s.DeliveryDate,
		&s.SubtotalAmount, &s.DeliveryAmount, &s.TotalAmount, &s.Currency,
		&s.GiftMessage, &s.GiftPoints, &s.CreatedAt,
		&s.CustomerID, &s.CustomerEmail, &s.CustomerName,
		&s.RecipientName, &s.RecipientEmail, &s.RecipientPhone, &s.RecipientCity,
		&s.RecipientCustomerID, &s.RecipientPasswordChangeRequired,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrOrderNotFound
	}
	if err != nil {
		return nil, err
	}

	rows, err := r.db.Query(ctx, `
		select p.name, p.image_url, shp.name, oi.quantity, oi.total_amount
		from marketplace.order_items oi
		inner join seller.products p on p.id = oi.product_id
		inner join seller.shops shp on shp.id = oi.shop_id
		where oi.order_id = $1 and oi.fulfilment_status <> 'cancelled'
		order by oi.created_at`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var it OrderEmailItem
		if err := rows.Scan(&it.ProductName, &it.ImageURL, &it.ShopName, &it.Quantity, &it.TotalAmount); err != nil {
			return nil, err
		}
		s.Items = append(s.Items, it)
	}
	return s, rows.Err()
}

// SetRecipientCustomer links the recipient's account to the order, so they
// can see the gift and review it.
func (r *OrderRepository) SetRecipientCustomer(ctx context.Context, orderID, customerID uuid.UUID) error {
	_, err := r.db.Exec(ctx, `
		update marketplace.orders set recipient_customer_id = $2
		where id = $1 and recipient_customer_id is distinct from $2`, orderID, customerID)
	return err
}

// ClaimGiftsByPhone links delivered gifts addressed to a phone number (and no
// account) to the customer who has just verified that number, so a recipient
// reached only by text can review what they were sent.
func (r *CustomerRepository) ClaimGiftsByPhone(ctx context.Context, customerID, e164 string) error {
	_, err := r.db.Exec(ctx, `
		update marketplace.orders o set recipient_customer_id = $1
		from customer.recipients rc
		where rc.id = o.recipient_id
		  and o.recipient_customer_id is null and o.customer_id <> $1
		  and rc.phone is not null and rc.phone <> ''
		  and right(regexp_replace(rc.phone, '\D', '', 'g'), 9) = right(regexp_replace($2, '\D', '', 'g'), 9)`,
		customerID, e164)
	return err
}

// DueRecipientNotices lists delivered orders whose recipient has not been
// told yet.
func (r *OrderRepository) DueRecipientNotices(ctx context.Context, limit int) ([]uuid.UUID, error) {
	rows, err := r.db.Query(ctx, `
		select id from marketplace.orders
		where status = 'delivered' and recipient_notified_at is null
		order by updated_at
		limit $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (r *OrderRepository) MarkRecipientNotified(ctx context.Context, orderID uuid.UUID) error {
	_, err := r.db.Exec(ctx, `
		update marketplace.orders set recipient_notified_at = now()
		where id = $1 and recipient_notified_at is null`, orderID)
	return err
}

// ListReceivedGifts returns the delivered gifts sent to a customer, newest
// first. Gifts on their way stay a surprise.
func (r *OrderRepository) ListReceivedGifts(ctx context.Context, customerID string) ([]models.ReceivedGift, error) {
	rows, err := r.db.Query(ctx, `
		select o.id, o.order_number,
		       coalesce(nullif(trim(c.display_name), ''), split_part(c.email::text, '@', 1)),
		       o.gift_message, o.gift_points, o.updated_at,
		       oi.id, oi.product_id, p.name, p.slug, p.image_url, shp.name, oi.quantity,
		       oi.fulfilment_status, pr.id
		from marketplace.orders o
		inner join customer.customers c on c.id = o.customer_id
		inner join marketplace.order_items oi on oi.order_id = o.id
		inner join seller.products p on p.id = oi.product_id
		inner join seller.shops shp on shp.id = oi.shop_id
		left join marketplace.product_reviews pr on pr.order_item_id = oi.id
		where o.recipient_customer_id = $1 and o.status = 'delivered'
		  and oi.fulfilment_status <> 'cancelled'
		order by o.updated_at desc, oi.created_at asc`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.ReceivedGift{}
	index := map[uuid.UUID]int{}
	for rows.Next() {
		var g models.ReceivedGift
		var it models.ReceivedGiftItem
		if err := rows.Scan(
			&g.OrderID, &g.OrderNumber, &g.SenderName, &g.GiftMessage, &g.GiftPoints, &g.DeliveredAt,
			&it.ID, &it.ProductID, &it.ProductName, &it.ProductSlug, &it.ProductImageURL, &it.ShopName,
			&it.Quantity, &it.FulfilmentStatus, &it.ReviewID,
		); err != nil {
			return nil, err
		}
		i, ok := index[g.OrderID]
		if !ok {
			g.Items = []models.ReceivedGiftItem{}
			out = append(out, g)
			i = len(out) - 1
			index[g.OrderID] = i
		}
		out[i].Items = append(out[i].Items, it)
	}
	return out, rows.Err()
}

// GetItemForReviewer returns an order line the customer may review: one they
// bought, or one sent to them as a gift.
func (r *OrderRepository) GetItemForReviewer(ctx context.Context, customerID, itemID string) (*models.OrderItem, error) {
	item := &models.OrderItem{}
	err := r.db.QueryRow(ctx, `
		select oi.id, oi.order_id, oi.seller_id, oi.shop_id, oi.product_id, oi.quantity,
		       oi.unit_amount, oi.total_amount, oi.fulfilment_status, oi.created_at, oi.updated_at
		from marketplace.order_items oi
		inner join marketplace.orders o on o.id = oi.order_id
		where oi.id = $1 and (o.customer_id = $2 or o.recipient_customer_id = $2)`, itemID, customerID,
	).Scan(
		&item.ID, &item.OrderID, &item.SellerID, &item.ShopID, &item.ProductID, &item.Quantity,
		&item.UnitAmount, &item.TotalAmount, &item.FulfilmentStatus, &item.CreatedAt, &item.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrOrderItemNotFound
	}
	return item, err
}

// EmailUsedByStaffOrSeller reports whether an email signs in to a seller or
// admin account, which a recipient account must not share.
func (r *CustomerRepository) EmailUsedByStaffOrSeller(ctx context.Context, email string) (bool, error) {
	var used bool
	err := r.db.QueryRow(ctx, `
		select exists(select 1 from seller.sellers where email = $1 and status <> 'deleted')
		    or exists(select 1 from admin.admin_users where email = $1)`, email).Scan(&used)
	return used, err
}
