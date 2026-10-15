package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"myapp/internal/models"
)

// ErrFreeGiftSellerNotFound is a seller id that is not a live seller.
var ErrFreeGiftSellerNotFound = errors.New("seller not found")

// SellerAcceptedFreeGiftTerms reports whether the seller agreed, at
// registration or since, to the marketing budget SendAGift sets for them.
func (r *FreeGiftRepository) SellerAcceptedFreeGiftTerms(ctx context.Context, sellerID uuid.UUID) (bool, error) {
	var accepted bool
	err := r.db.QueryRow(ctx, `
		select free_gift_terms_accepted_at is not null
		from seller.sellers where id = $1 and status <> 'deleted'`, sellerID).Scan(&accepted)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrFreeGiftSellerNotFound
	}
	return accepted, err
}

// AcceptFreeGiftTerms records the seller's agreement, once. A seller who
// registered before the agreement existed uses this to opt in.
func (r *FreeGiftRepository) AcceptFreeGiftTerms(ctx context.Context, sellerID uuid.UUID) error {
	_, err := r.db.Exec(ctx, `
		update seller.sellers
		set free_gift_terms_accepted_at = now(), updated_at = now()
		where id = $1 and free_gift_terms_accepted_at is null`, sellerID)
	return err
}

// SetSellerProgramStatus is the seller's own switch between active and
// paused. It never touches a program an admin stopped, and never the budget.
func (r *FreeGiftRepository) SetSellerProgramStatus(ctx context.Context, sellerID uuid.UUID, status string) (*models.FreeGiftProgram, error) {
	out, err := scanFreeGiftProgram(r.db.QueryRow(ctx, `
		update promo.free_gift_programs
		set status = $2, updated_at = now()
		where seller_id = $1 and status <> 'stopped'
		returning `+freeGiftProgramColumns, sellerID, status))
	if errors.Is(err, ErrFreeGiftProgramNotFound) {
		// Either there is no program, or an admin stopped it.
		current, gerr := r.GetProgram(ctx, sellerID)
		if gerr != nil {
			return nil, gerr
		}
		if current.Status == models.FreeGiftProgramStopped {
			return nil, ErrFreeGiftProgramStopped
		}
	}
	return out, err
}

// ListOrders is the free gifts that were sent, newest first: which seller
// funded each, who sent it and to whom, what it was, and what it cost. A nil
// sellerID lists every seller.
func (r *FreeGiftRepository) ListOrders(ctx context.Context, sellerID *uuid.UUID, limit int) ([]models.AdminFreeGiftOrder, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := r.db.Query(ctx, `
		select o.id, o.order_number, o.status, o.free_gift_budget_status,
		       o.created_at, o.delivery_date, o.currency,
		       o.free_gift_cost, o.subtotal_amount, o.delivery_amount,
		       (o.delivery_amount = 0 or exists (
		           select 1 from finance.payments pay
		           where pay.order_id = o.id and pay.status = 'succeeded')),
		       oi.seller_id,
		       coalesce(nullif(btrim(se.trading_name), ''), se.legal_name),
		       shp.name,
		       c.id, coalesce(nullif(btrim(c.display_name), ''), c.email::text), c.email::text,
		       r.name, ra.city,
		       p.id, p.name, p.image_url
		from marketplace.orders o
		inner join marketplace.order_items oi on oi.order_id = o.id
		inner join seller.sellers se on se.id = oi.seller_id
		inner join seller.shops shp on shp.id = oi.shop_id
		inner join seller.products p on p.id = oi.product_id
		inner join customer.customers c on c.id = o.customer_id
		left join customer.recipients r on r.id = o.recipient_id
		left join customer.recipient_addresses ra on ra.id = coalesce(
			o.delivery_address_id,
			r.default_address_id,
			(select id from customer.recipient_addresses where recipient_id = r.id
			 order by is_default desc, created_at asc limit 1)
		)
		where o.order_kind = 'free_gift'
		  and ($1::uuid is null or oi.seller_id = $1)
		order by o.created_at desc
		limit $2`, sellerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.AdminFreeGiftOrder{}
	for rows.Next() {
		var (
			g         models.AdminFreeGiftOrder
			delivery  time.Time
			recipient *string
		)
		if err := rows.Scan(&g.OrderID, &g.OrderNumber, &g.Status, &g.BudgetStatus,
			&g.CreatedAt, &delivery, &g.Currency,
			&g.Cost, &g.ProductAmount, &g.DeliveryAmount, &g.DeliveryPaid,
			&g.SellerID, &g.SellerName, &g.ShopName,
			&g.CustomerID, &g.CustomerName, &g.CustomerEmail,
			&recipient, &g.RecipientCity,
			&g.ProductID, &g.ProductName, &g.ProductImageURL); err != nil {
			return nil, err
		}
		g.DeliveryDate = delivery.Format("2006-01-02")
		g.RecipientName = recipient
		out = append(out, g)
	}
	return out, rows.Err()
}

// ─── Country budgets ──────────────────────────────────────────────────────

// ErrFreeGiftCountryNotFound is a country id that does not exist.
var ErrFreeGiftCountryNotFound = errors.New("country not found")

// UpsertCountryBudget saves the budget for one country.
func (r *FreeGiftRepository) UpsertCountryBudget(ctx context.Context, countryID uuid.UUID, currency string, cap int64, status string) error {
	_, err := r.db.Exec(ctx, `
		insert into promo.free_gift_country_budgets (country_id, currency, budget_cap, status)
		select c.id, $2, $3, $4 from core.countries c where c.id = $1
		on conflict (country_id) do update
		set currency = excluded.currency, budget_cap = excluded.budget_cap,
		    status = excluded.status, updated_at = now()`, countryID, currency, cap, status)
	if err != nil {
		return err
	}
	var exists bool
	if err := r.db.QueryRow(ctx, `select exists(select 1 from promo.free_gift_country_budgets where country_id = $1)`, countryID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrFreeGiftCountryNotFound
	}
	return nil
}

// CountryBudget is the budget set for a country, or nil when there is none.
func (r *FreeGiftRepository) CountryBudget(ctx context.Context, countryID uuid.UUID) (*models.FreeGiftCountryBudget, error) {
	b := &models.FreeGiftCountryBudget{}
	err := r.db.QueryRow(ctx, `
		select b.country_id, c.name, b.currency, b.budget_cap, b.status, b.updated_at
		from promo.free_gift_country_budgets b
		inner join core.countries c on c.id = b.country_id
		where b.country_id = $1`, countryID).
		Scan(&b.CountryID, &b.CountryName, &b.Currency, &b.BudgetCap, &b.Status, &b.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return b, err
}

// ListCountryBudgets is every country with a budget, with how many sellers
// are there and how many agreed.
func (r *FreeGiftRepository) ListCountryBudgets(ctx context.Context) ([]models.FreeGiftCountryBudget, error) {
	rows, err := r.db.Query(ctx, `
		select b.country_id, c.name, b.currency, b.budget_cap, b.status, b.updated_at,
		       count(s.id), count(s.id) filter (where s.free_gift_terms_accepted_at is not null)
		from promo.free_gift_country_budgets b
		inner join core.countries c on c.id = b.country_id
		left join seller.sellers s on s.country_id = b.country_id and s.status <> 'deleted'
		group by b.country_id, c.name, b.currency, b.budget_cap, b.status, b.updated_at
		order by c.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.FreeGiftCountryBudget{}
	for rows.Next() {
		var b models.FreeGiftCountryBudget
		if err := rows.Scan(&b.CountryID, &b.CountryName, &b.Currency, &b.BudgetCap, &b.Status, &b.UpdatedAt,
			&b.Sellers, &b.Agreed); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// CountrySellers lists the live sellers registered in a country and whether
// each has agreed to the marketing budget.
func (r *FreeGiftRepository) CountrySellers(ctx context.Context, countryID uuid.UUID) (map[uuid.UUID]bool, error) {
	rows, err := r.db.Query(ctx, `
		select id, free_gift_terms_accepted_at is not null
		from seller.sellers where country_id = $1 and status <> 'deleted'`, countryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]bool{}
	for rows.Next() {
		var id uuid.UUID
		var agreed bool
		if err := rows.Scan(&id, &agreed); err != nil {
			return nil, err
		}
		out[id] = agreed
	}
	return out, rows.Err()
}

// SellerCountry is the country a seller is registered in.
func (r *FreeGiftRepository) SellerCountry(ctx context.Context, sellerID uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := r.db.QueryRow(ctx, `select country_id from seller.sellers where id = $1`, sellerID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrFreeGiftSellerNotFound
	}
	return id, err
}
