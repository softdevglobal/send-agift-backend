package repository

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"myapp/internal/models"
)

// ErrSellerCustomerNotFound is a customer who has no orders with the seller.
var ErrSellerCustomerNotFound = errors.New("customer not found")

// ListSellerCustomers is everyone who has ordered from the seller, most
// recent first. With customerID set, just that customer.
func (r *OrderRepository) ListSellerCustomers(ctx context.Context, sellerID string, customerID *uuid.UUID) ([]models.SellerCustomer, error) {
	rows, err := r.db.Query(ctx, `
		with lines as (
			select o.customer_id, o.id as order_id, o.currency, o.order_kind, o.created_at,
			       oi.quantity, oi.total_amount, oi.fulfilment_status
			from marketplace.order_items oi
			inner join marketplace.orders o on o.id = oi.order_id
			where oi.seller_id = $1
			  and ($2::uuid is null or o.customer_id = $2)`+r.paidOnly()+`
		)
		select c.id, coalesce(nullif(trim(c.display_name), ''), 'Customer'), co.name, s.name,
		       count(distinct l.order_id),
		       coalesce(sum(l.quantity) filter (where l.fulfilment_status <> 'cancelled'), 0),
		       coalesce((
		           select jsonb_agg(jsonb_build_object('currency', x.currency, 'amount', x.amount) order by x.currency)
		           from (
		               select l2.currency, sum(l2.total_amount)::bigint as amount
		               from lines l2
		               where l2.customer_id = c.id and l2.order_kind = 'paid' and l2.fulfilment_status <> 'cancelled'
		               group by l2.currency
		           ) x
		       ), '[]'::jsonb),
		       min(l.created_at), max(l.created_at)
		from lines l
		inner join customer.customers c on c.id = l.customer_id
		inner join core.countries co on co.id = c.country_id
		left join core.states s on s.id = c.state_id
		group by c.id, c.display_name, co.name, s.name
		order by max(l.created_at) desc`, sellerID, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.SellerCustomer{}
	for rows.Next() {
		var c models.SellerCustomer
		if err := rows.Scan(&c.CustomerID, &c.Name, &c.CountryName, &c.StateName,
			&c.OrdersCount, &c.ItemsCount, &c.Spent, &c.FirstOrderAt, &c.LastOrderAt); err != nil {
			return nil, err
		}
		if c.Spent == nil {
			c.Spent = []models.SellerCustomerSpend{}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetSellerCustomer is one customer and every order they placed with the
// seller, holding only the seller's own lines.
func (r *OrderRepository) GetSellerCustomer(ctx context.Context, sellerID string, customerID uuid.UUID) (*models.SellerCustomerDetail, error) {
	list, err := r.ListSellerCustomers(ctx, sellerID, &customerID)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrSellerCustomerNotFound
	}
	d := &models.SellerCustomerDetail{SellerCustomer: list[0], Orders: []models.SellerCustomerOrder{}}

	rows, err := r.db.Query(ctx, `
		select o.id, o.order_number, o.status, o.order_kind, o.currency, o.delivery_date, o.created_at,
		       oi.id, p.name, p.image_url, shp.name, oi.quantity, oi.total_amount, oi.fulfilment_status, r.name
		`+sellerOrderItemFrom+`
		where oi.seller_id = $1 and o.customer_id = $2`+r.paidOnly()+`
		order by o.created_at desc, o.id, oi.created_at`, sellerID, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			o models.SellerCustomerOrder
			l models.SellerCustomerOrderLine
		)
		if err := rows.Scan(&o.OrderID, &o.OrderNumber, &o.Status, &o.OrderKind, &o.Currency,
			&o.DeliveryDate, &o.CreatedAt,
			&l.ID, &l.ProductName, &l.ProductImageURL, &l.ShopName, &l.Quantity, &l.TotalAmount,
			&l.FulfilmentStatus, &l.RecipientName); err != nil {
			return nil, err
		}
		if n := len(d.Orders); n == 0 || d.Orders[n-1].OrderID != o.OrderID {
			o.Items = []models.SellerCustomerOrderLine{}
			d.Orders = append(d.Orders, o)
		}
		cur := &d.Orders[len(d.Orders)-1]
		cur.Items = append(cur.Items, l)
		if l.FulfilmentStatus != "cancelled" {
			cur.Amount += l.TotalAmount
		}
	}
	return d, rows.Err()
}
