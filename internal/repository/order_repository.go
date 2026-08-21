package repository

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"myapp/internal/models"
)

var (
	ErrOrderNotFound      = errors.New("order not found")
	ErrOrderDuplicate     = errors.New("order number already exists")
	ErrOrderProductNotFound = errors.New("product not found")
	ErrOrderNotCancellable  = errors.New("order cannot be cancelled")
)

type OrderRepository struct {
	db *pgxpool.Pool
}

func NewOrderRepository(db *pgxpool.Pool) *OrderRepository {
	return &OrderRepository{db: db}
}

// CheckoutProduct is the catalog row used to stamp price/shop/seller onto an order item.
type CheckoutProduct struct {
	ID                     string
	ShopID                 string
	SellerID               string
	PriceAmount            int
	Currency               string
	Status                 string
	CustomerTypeVisibility string
	ShopStatus             string
}

func (r *OrderRepository) GetCheckoutProduct(ctx context.Context, productID string) (*CheckoutProduct, error) {
	p := &CheckoutProduct{}
	err := r.db.QueryRow(ctx, `
		select p.id::text, p.shop_id::text, s.seller_id::text, p.price_amount, p.currency,
		       p.status, p.customer_type_visibility, s.status
		from seller.products p
		inner join seller.shops s on s.id = p.shop_id
		where p.id = $1`, productID,
	).Scan(
		&p.ID, &p.ShopID, &p.SellerID, &p.PriceAmount, &p.Currency,
		&p.Status, &p.CustomerTypeVisibility, &p.ShopStatus,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrOrderProductNotFound
	}
	return p, err
}

func (r *OrderRepository) Create(ctx context.Context, order *models.Order, items []models.OrderItem) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	err = tx.QueryRow(ctx, `
		insert into marketplace.orders (
			order_number, customer_id, recipient_id, country_id, customer_type,
			delivery_date, status, subtotal_amount, delivery_amount, total_amount,
			currency, gift_message, media_greeting_id
		) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		returning id, created_at, updated_at`,
		order.OrderNumber, order.CustomerID, order.RecipientID, order.CountryID, order.CustomerType,
		order.DeliveryDate, order.Status, order.SubtotalAmount, order.DeliveryAmount, order.TotalAmount,
		order.Currency, order.GiftMessage, order.MediaGreetingID,
	).Scan(&order.ID, &order.CreatedAt, &order.UpdatedAt)
	if err != nil {
		return mapOrderWriteError(err)
	}

	for i := range items {
		items[i].OrderID = order.ID
		if items[i].FulfilmentStatus == "" {
			items[i].FulfilmentStatus = "pending"
		}
		err = tx.QueryRow(ctx, `
			insert into marketplace.order_items (
				order_id, seller_id, shop_id, product_id, quantity,
				unit_amount, total_amount, fulfilment_status
			) values ($1,$2,$3,$4,$5,$6,$7,$8)
			returning id, created_at, updated_at`,
			items[i].OrderID, items[i].SellerID, items[i].ShopID, items[i].ProductID, items[i].Quantity,
			items[i].UnitAmount, items[i].TotalAmount, items[i].FulfilmentStatus,
		).Scan(&items[i].ID, &items[i].CreatedAt, &items[i].UpdatedAt)
		if err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

func (r *OrderRepository) ListByCustomer(ctx context.Context, customerID string) ([]models.Order, error) {
	rows, err := r.db.Query(ctx, `
		select id, order_number, customer_id, recipient_id, country_id, customer_type,
		       delivery_date, status, subtotal_amount, delivery_amount, total_amount,
		       currency, gift_message, media_greeting_id, created_at, updated_at
		from marketplace.orders
		where customer_id = $1
		order by created_at desc`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []models.Order{}
	for rows.Next() {
		var o models.Order
		if err := rows.Scan(
			&o.ID, &o.OrderNumber, &o.CustomerID, &o.RecipientID, &o.CountryID, &o.CustomerType,
			&o.DeliveryDate, &o.Status, &o.SubtotalAmount, &o.DeliveryAmount, &o.TotalAmount,
			&o.Currency, &o.GiftMessage, &o.MediaGreetingID, &o.CreatedAt, &o.UpdatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, o)
	}
	return items, rows.Err()
}

func (r *OrderRepository) GetByIDForCustomer(ctx context.Context, customerID, orderID string) (*models.Order, error) {
	o := &models.Order{}
	err := r.db.QueryRow(ctx, `
		select id, order_number, customer_id, recipient_id, country_id, customer_type,
		       delivery_date, status, subtotal_amount, delivery_amount, total_amount,
		       currency, gift_message, media_greeting_id, created_at, updated_at
		from marketplace.orders
		where id = $1 and customer_id = $2`, orderID, customerID,
	).Scan(
		&o.ID, &o.OrderNumber, &o.CustomerID, &o.RecipientID, &o.CountryID, &o.CustomerType,
		&o.DeliveryDate, &o.Status, &o.SubtotalAmount, &o.DeliveryAmount, &o.TotalAmount,
		&o.Currency, &o.GiftMessage, &o.MediaGreetingID, &o.CreatedAt, &o.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrOrderNotFound
	}
	return o, err
}

func (r *OrderRepository) ListItems(ctx context.Context, orderID string) ([]models.OrderItem, error) {
	rows, err := r.db.Query(ctx, `
		select id, order_id, seller_id, shop_id, product_id, quantity,
		       unit_amount, total_amount, fulfilment_status, created_at, updated_at
		from marketplace.order_items
		where order_id = $1
		order by created_at asc`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []models.OrderItem{}
	for rows.Next() {
		var it models.OrderItem
		if err := rows.Scan(
			&it.ID, &it.OrderID, &it.SellerID, &it.ShopID, &it.ProductID, &it.Quantity,
			&it.UnitAmount, &it.TotalAmount, &it.FulfilmentStatus, &it.CreatedAt, &it.UpdatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

func (r *OrderRepository) CancelForCustomer(ctx context.Context, customerID, orderID string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var status string
	err = tx.QueryRow(ctx, `
		select status from marketplace.orders
		where id = $1 and customer_id = $2
		for update`, orderID, customerID,
	).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrOrderNotFound
	}
	if err != nil {
		return err
	}

	switch status {
	case "draft", "pending_payment", "paid", "accepted", "preparing":
	default:
		return ErrOrderNotCancellable
	}

	tag, err := tx.Exec(ctx, `
		update marketplace.orders
		set status = 'cancelled', updated_at = now()
		where id = $1 and customer_id = $2`, orderID, customerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrOrderNotFound
	}

	if _, err := tx.Exec(ctx, `
		update marketplace.order_items
		set fulfilment_status = 'cancelled', updated_at = now()
		where order_id = $1
		  and fulfilment_status not in ('delivered', 'cancelled')`, orderID); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func mapOrderWriteError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrOrderDuplicate
	}
	return err
}
