package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"myapp/internal/models"
)

// The first free gift program: a seller's budget, the products they offer,
// and the one-time rights of senders and recipients.
//
// Nothing here is decided by reading a row and then writing: the one-time
// limits are a unique index, and the budget, the per-product cap and the
// stock are guarded updates. Two requests racing for the last of anything
// leave exactly one winner.

var (
	ErrFreeGiftProgramNotFound = errors.New("free gift program not set up")
	ErrFreeGiftProductNotFound = errors.New("product not found")
	// ErrFreeGiftAlreadyUsed is a sender who has used their free gift.
	ErrFreeGiftAlreadyUsed = errors.New("free gift already used")
	// ErrFreeGiftRecipientUsed is a recipient who has had theirs.
	ErrFreeGiftRecipientUsed = errors.New("recipient already received a free gift")
	// ErrFreeGiftUnavailable is a product out of the program, a paused or
	// stopped program, or a budget or gift cap that is used up.
	ErrFreeGiftUnavailable = errors.New("free gift not available")
	ErrFreeGiftOutOfStock  = errors.New("free gift out of stock")
	// ErrFreeGiftProgramStopped is a seller changing a program an admin stopped.
	ErrFreeGiftProgramStopped = errors.New("free gift program stopped by an admin")
	// ErrFreeGiftTermsRequired is a first budget set without accepting the terms.
	ErrFreeGiftTermsRequired = errors.New("free gift terms not accepted")
	// ErrFreeGiftBudgetBelowCommitted is a budget lowered under what is
	// already held and spent.
	ErrFreeGiftBudgetBelowCommitted = errors.New("budget is below what is already held and spent")
	// ErrFreeGiftCurrencyLocked is a currency change once gifts or products
	// depend on the old one.
	ErrFreeGiftCurrencyLocked = errors.New("currency cannot change while the program has products or gifts")
	// ErrFreeGiftProductNotEligible is a product that cannot join the program.
	ErrFreeGiftProductNotEligible = errors.New("product cannot be offered as a free gift")
	// ErrFreeGiftCapBelowCount is a gift cap under the gifts already sent.
	ErrFreeGiftCapBelowCount = errors.New("gift cap is below the gifts already sent")
)

const (
	freeGiftRoleSend    = "send"
	freeGiftRoleReceive = "receive"
)

type FreeGiftRepository struct {
	db *pgxpool.Pool
}

func NewFreeGiftRepository(db *pgxpool.Pool) *FreeGiftRepository {
	return &FreeGiftRepository{db: db}
}

// ─── Seller program ───────────────────────────────────────────────────────

const freeGiftProgramColumns = `
	seller_id, status, currency, budget_cap, budget_held, budget_spent,
	terms_version, accepted_at, created_at, updated_at`

func scanFreeGiftProgram(row scanner) (*models.FreeGiftProgram, error) {
	p := &models.FreeGiftProgram{}
	err := row.Scan(&p.SellerID, &p.Status, &p.Currency, &p.BudgetCap, &p.BudgetHeld, &p.BudgetSpent,
		&p.TermsVersion, &p.AcceptedAt, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrFreeGiftProgramNotFound
	}
	if err != nil {
		return nil, err
	}
	p.BudgetRemaining = p.BudgetCap - p.BudgetHeld - p.BudgetSpent
	return p, nil
}

// GetProgram returns the seller's program, or ErrFreeGiftProgramNotFound.
func (r *FreeGiftRepository) GetProgram(ctx context.Context, sellerID uuid.UUID) (*models.FreeGiftProgram, error) {
	return scanFreeGiftProgram(r.db.QueryRow(ctx, `
		select `+freeGiftProgramColumns+`
		from promo.free_gift_programs where seller_id = $1`, sellerID))
}

// FreeGiftProgramWrite is a seller setting their budget.
type FreeGiftProgramWrite struct {
	SellerID     uuid.UUID
	Currency     string
	BudgetCap    int64
	Status       string // active | paused
	AcceptTerms  bool
	TermsVersion string
}

// UpsertProgram creates or changes the seller's program. The first write
// must accept the terms. A program an admin stopped cannot be changed, the
// budget cannot drop under what is held and spent, and the currency is fixed
// once products or gifts depend on it.
func (r *FreeGiftRepository) UpsertProgram(ctx context.Context, in FreeGiftProgramWrite) (*models.FreeGiftProgram, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	current, err := scanFreeGiftProgram(tx.QueryRow(ctx, `
		select `+freeGiftProgramColumns+`
		from promo.free_gift_programs where seller_id = $1 for update`, in.SellerID))
	if errors.Is(err, ErrFreeGiftProgramNotFound) {
		if !in.AcceptTerms {
			return nil, ErrFreeGiftTermsRequired
		}
		// Two first writes at once: the second waits on the primary key and
		// then updates what the first one made.
		out, err := scanFreeGiftProgram(tx.QueryRow(ctx, `
			insert into promo.free_gift_programs
				(seller_id, status, currency, budget_cap, terms_version, accepted_at)
			values ($1, $2, $3, $4, $5, now())
			on conflict (seller_id) do update set
				status = excluded.status, budget_cap = excluded.budget_cap, updated_at = now()
			where promo.free_gift_programs.status <> 'stopped'
			  and promo.free_gift_programs.budget_held + promo.free_gift_programs.budget_spent <= excluded.budget_cap
			returning `+freeGiftProgramColumns,
			in.SellerID, in.Status, in.Currency, in.BudgetCap, in.TermsVersion))
		if errors.Is(err, ErrFreeGiftProgramNotFound) {
			return nil, ErrFreeGiftBudgetBelowCommitted
		}
		if err != nil {
			return nil, err
		}
		return out, tx.Commit(ctx)
	}
	if err != nil {
		return nil, err
	}

	if current.Status == models.FreeGiftProgramStopped {
		return nil, ErrFreeGiftProgramStopped
	}
	if in.BudgetCap < current.BudgetHeld+current.BudgetSpent {
		return nil, ErrFreeGiftBudgetBelowCommitted
	}
	if !strings.EqualFold(in.Currency, current.Currency) {
		var products int
		if err := tx.QueryRow(ctx, `
			select count(*) from promo.free_gift_products where seller_id = $1`, in.SellerID).Scan(&products); err != nil {
			return nil, err
		}
		if products > 0 || current.BudgetHeld+current.BudgetSpent > 0 {
			return nil, ErrFreeGiftCurrencyLocked
		}
	}
	out, err := scanFreeGiftProgram(tx.QueryRow(ctx, `
		update promo.free_gift_programs
		set status = $2, currency = $3, budget_cap = $4,
		    terms_version = case when $5 then $6 else terms_version end,
		    accepted_at = case when $5 and terms_version is distinct from $6 then now() else accepted_at end,
		    updated_at = now()
		where seller_id = $1
		returning `+freeGiftProgramColumns,
		in.SellerID, in.Status, in.Currency, in.BudgetCap, in.AcceptTerms, in.TermsVersion))
	if err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}

// SetProgramStatus is the admin switch: it sets the status whatever it was.
func (r *FreeGiftRepository) SetProgramStatus(ctx context.Context, sellerID uuid.UUID, status string) (*models.FreeGiftProgram, error) {
	return scanFreeGiftProgram(r.db.QueryRow(ctx, `
		update promo.free_gift_programs
		set status = $2, updated_at = now()
		where seller_id = $1
		returning `+freeGiftProgramColumns, sellerID, status))
}

// ListPrograms is every seller's program, for admins.
func (r *FreeGiftRepository) ListPrograms(ctx context.Context) ([]models.AdminFreeGiftProgram, error) {
	rows, err := r.db.Query(ctx, `
		select pg.seller_id, pg.status, pg.currency, pg.budget_cap, pg.budget_held, pg.budget_spent,
		       pg.terms_version, pg.accepted_at, pg.created_at, pg.updated_at,
		       coalesce(nullif(btrim(se.trading_name), ''), se.legal_name), se.email::text,
		       (select count(*) from promo.free_gift_products fp where fp.seller_id = pg.seller_id),
		       (select count(distinct o.id)
		        from marketplace.orders o
		        inner join marketplace.order_items oi on oi.order_id = o.id
		        where oi.seller_id = pg.seller_id and o.order_kind = 'free_gift'
		          and o.free_gift_budget_status in ('held', 'spent'))
		from promo.free_gift_programs pg
		inner join seller.sellers se on se.id = pg.seller_id
		order by pg.updated_at desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.AdminFreeGiftProgram{}
	for rows.Next() {
		var p models.AdminFreeGiftProgram
		if err := rows.Scan(&p.SellerID, &p.Status, &p.Currency, &p.BudgetCap, &p.BudgetHeld, &p.BudgetSpent,
			&p.TermsVersion, &p.AcceptedAt, &p.CreatedAt, &p.UpdatedAt,
			&p.SellerName, &p.SellerEmail, &p.Products, &p.GiftsSent); err != nil {
			return nil, err
		}
		p.BudgetRemaining = p.BudgetCap - p.BudgetHeld - p.BudgetSpent
		out = append(out, p)
	}
	return out, rows.Err()
}

// ─── Seller products ──────────────────────────────────────────────────────

const freeGiftProductSelect = `
	select fp.product_id, fp.seller_id, fp.status, fp.gifts_cap, fp.gifts_count,
	       fp.created_at, fp.updated_at,
	       p.name, p.image_url, p.status, p.price_amount, p.currency, s.name,
	       i.available_qty - i.reserved_qty
	from promo.free_gift_products fp
	inner join seller.products p on p.id = fp.product_id
	inner join seller.shops s on s.id = p.shop_id
	left join seller.inventory i on i.product_id = p.id`

func scanFreeGiftProduct(row scanner) (*models.FreeGiftProduct, error) {
	p := &models.FreeGiftProduct{}
	err := row.Scan(&p.ProductID, &p.SellerID, &p.Status, &p.GiftsCap, &p.GiftsCount,
		&p.CreatedAt, &p.UpdatedAt,
		&p.ProductName, &p.ProductImageURL, &p.ProductStatus, &p.PriceAmount, &p.Currency, &p.ShopName,
		&p.StockLeft)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrFreeGiftProductNotFound
	}
	return p, err
}

// ListProducts is the seller's products in the program, newest first.
func (r *FreeGiftRepository) ListProducts(ctx context.Context, sellerID uuid.UUID) ([]models.FreeGiftProduct, error) {
	rows, err := r.db.Query(ctx, freeGiftProductSelect+`
		where fp.seller_id = $1
		order by fp.created_at desc`, sellerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.FreeGiftProduct{}
	for rows.Next() {
		p, err := scanFreeGiftProduct(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// UpsertProduct puts one of the seller's products in the program, or changes
// its cap or status. The product must be published, in an active shop and
// priced in the program's currency.
func (r *FreeGiftRepository) UpsertProduct(ctx context.Context, sellerID, productID uuid.UUID, giftsCap *int, status string) (*models.FreeGiftProduct, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var programCurrency string
	err = tx.QueryRow(ctx, `
		select currency from promo.free_gift_programs where seller_id = $1 for share`, sellerID).Scan(&programCurrency)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrFreeGiftProgramNotFound
	}
	if err != nil {
		return nil, err
	}

	var productStatus, shopStatus, currency string
	err = tx.QueryRow(ctx, `
		select p.status, s.status, p.currency
		from seller.products p
		inner join seller.shops s on s.id = p.shop_id
		where p.id = $1 and s.seller_id = $2`, productID, sellerID).Scan(&productStatus, &shopStatus, &currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrFreeGiftProductNotFound
	}
	if err != nil {
		return nil, err
	}
	switch {
	case productStatus != "published":
		return nil, errors.Join(ErrFreeGiftProductNotEligible, errors.New("publish the product first"))
	case shopStatus != "active":
		return nil, errors.Join(ErrFreeGiftProductNotEligible, errors.New("its shop is not active"))
	case !strings.EqualFold(currency, programCurrency):
		return nil, errors.Join(ErrFreeGiftProductNotEligible,
			errors.New("it is priced in "+strings.ToUpper(currency)+" but your budget is in "+strings.ToUpper(programCurrency)))
	}

	tag, err := tx.Exec(ctx, `
		insert into promo.free_gift_products (product_id, seller_id, status, gifts_cap)
		values ($1, $2, $3, $4)
		on conflict (product_id) do update set
			status = excluded.status, gifts_cap = excluded.gifts_cap, updated_at = now()
		where excluded.gifts_cap is null
		   or promo.free_gift_products.gifts_count <= excluded.gifts_cap`,
		productID, sellerID, status, giftsCap)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrFreeGiftCapBelowCount
	}
	out, err := scanFreeGiftProduct(tx.QueryRow(ctx, freeGiftProductSelect+`
		where fp.product_id = $1`, productID))
	if err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}

// RemoveProduct takes a product out of the program. Gifts already confirmed
// for it are still fulfilled.
func (r *FreeGiftRepository) RemoveProduct(ctx context.Context, sellerID, productID uuid.UUID) error {
	tag, err := r.db.Exec(ctx, `
		delete from promo.free_gift_products where product_id = $1 and seller_id = $2`, productID, sellerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrFreeGiftProductNotFound
	}
	return nil
}

// ─── Customer side ────────────────────────────────────────────────────────

// ListAvailable is the products that can be sent as a free gift right now:
// in an active program with budget and gifts left, published, in an active
// shop, and in stock. country is a country id or ISO code; empty lists all.
func (r *FreeGiftRepository) ListAvailable(ctx context.Context, country string) ([]models.FreeGiftOffer, error) {
	rows, err := r.db.Query(ctx, `
		select p.id, p.name, p.slug, p.description, p.image_url, p.price_amount, p.currency,
		       s.id, s.name, s.slug, s.country_id
		from promo.free_gift_products fp
		inner join promo.free_gift_programs pg on pg.seller_id = fp.seller_id
		inner join seller.products p on p.id = fp.product_id
		inner join seller.shops s on s.id = p.shop_id
		inner join seller.sellers se on se.id = s.seller_id
		inner join seller.inventory i on i.product_id = p.id
		left join core.countries c on c.id = s.country_id
		where fp.status = 'active' and pg.status = 'active'
		  and p.status = 'published' and s.status = 'active' and se.status = 'active'
		  and p.customer_type_visibility in ('both', 'personal')
		  and upper(p.currency) = upper(pg.currency)
		  and (fp.gifts_cap is null or fp.gifts_count < fp.gifts_cap)
		  -- The real cost needs the recipient's distance. The shop's cheapest
		  -- zone is the least it can be, so a budget that cannot cover even
		  -- that is not offered.
		  and pg.budget_held + pg.budget_spent + p.price_amount + coalesce(
		        (select min(z.price_amount) from seller.shop_delivery_zones z where z.shop_id = s.id), 0)
		      <= pg.budget_cap
		  and i.available_qty - i.reserved_qty >= 1
		  and ($1 = '' or s.country_id::text = lower($1) or upper(c.iso_code) = upper($1))
		order by p.price_amount desc, p.name
		limit 200`, strings.TrimSpace(country))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.FreeGiftOffer{}
	for rows.Next() {
		var o models.FreeGiftOffer
		if err := rows.Scan(&o.ProductID, &o.Name, &o.Slug, &o.Description, &o.ImageURL, &o.PriceAmount, &o.Currency,
			&o.ShopID, &o.ShopName, &o.ShopSlug, &o.CountryID); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// FreeGiftCustomer is what the one-time limits need to know about a sender.
type FreeGiftCustomer struct {
	ID              uuid.UUID
	Status          string
	Email           string
	EmailVerifiedAt *time.Time
	PhoneE164       *string
	PhoneVerifiedAt *time.Time
}

// CustomerIdentity loads the sender's verified email and phone.
func (r *FreeGiftRepository) CustomerIdentity(ctx context.Context, customerID uuid.UUID) (*FreeGiftCustomer, error) {
	c := &FreeGiftCustomer{}
	err := r.db.QueryRow(ctx, `
		select id, status, email::text, email_verified_at, phone_e164, phone_verified_at
		from customer.customers
		where id = $1 and deleted_at is null`, customerID).
		Scan(&c.ID, &c.Status, &c.Email, &c.EmailVerifiedAt, &c.PhoneE164, &c.PhoneVerifiedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCustomerNotFound
	}
	return c, err
}

// SendClaimUsed reports whether any of these sender keys has been used.
func (r *FreeGiftRepository) SendClaimUsed(ctx context.Context, keys []string) (bool, error) {
	return r.claimUsed(ctx, freeGiftRoleSend, keys)
}

// ReceiveClaimUsed reports whether any of these recipient keys has been used.
func (r *FreeGiftRepository) ReceiveClaimUsed(ctx context.Context, keys []string) (bool, error) {
	return r.claimUsed(ctx, freeGiftRoleReceive, keys)
}

func (r *FreeGiftRepository) claimUsed(ctx context.Context, role string, keys []string) (bool, error) {
	var used bool
	err := r.db.QueryRow(ctx, `
		select exists(
			select 1 from promo.free_gift_claims
			where role = $1 and identity_key = any($2) and state = 'used')`, role, keys).Scan(&used)
	return used, err
}

// FreeGiftReplay is the order an Idempotency-Key already made, with what it
// was for, so a retry can be told from a different request.
type FreeGiftReplay struct {
	OrderID      uuid.UUID
	OrderKind    string
	RecipientID  *uuid.UUID
	DeliveryDate time.Time
	ProductIDs   []uuid.UUID
}

// FindByIdempotencyKey returns the customer's order made with this key, or
// ErrOrderNotFound.
func (r *FreeGiftRepository) FindByIdempotencyKey(ctx context.Context, customerID uuid.UUID, key string) (*FreeGiftReplay, error) {
	out := &FreeGiftReplay{}
	err := r.db.QueryRow(ctx, `
		select o.id, o.order_kind, o.recipient_id, o.delivery_date,
		       array(select oi.product_id from marketplace.order_items oi where oi.order_id = o.id)
		from marketplace.orders o
		where o.customer_id = $1 and o.idempotency_key = $2`, customerID, key).
		Scan(&out.OrderID, &out.OrderKind, &out.RecipientID, &out.DeliveryDate, &out.ProductIDs)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrOrderNotFound
	}
	return out, err
}

// FreeGiftOrder is everything one free gift writes.
type FreeGiftOrder struct {
	Order       *models.Order
	Items       []models.OrderItem
	Deliveries  []models.OrderShopDelivery
	SendKeys    []string
	ReceiveKeys []string
}

// CreateOrder sends a free gift in one transaction: the one-time rights are
// taken, the product's gift count and the seller's budget move, a unit of
// stock is reserved, and the order is written. Any refusal undoes all of it.
//
// The rights go first, so the sender or recipient who has already used
// theirs is told that rather than something about budgets.
func (r *FreeGiftRepository) CreateOrder(ctx context.Context, in FreeGiftOrder) error {
	if len(in.Items) != 1 || in.Items[0].Quantity != 1 {
		return ErrFreeGiftUnavailable
	}
	item := &in.Items[0]
	order := in.Order
	cost := int64(order.FreeGiftCost)

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	claims, err := useClaims(ctx, tx, freeGiftRoleSend, in.SendKeys, order.CustomerID)
	if err != nil {
		return err
	}
	if claims == nil {
		return ErrFreeGiftAlreadyUsed
	}
	received, err := useClaims(ctx, tx, freeGiftRoleReceive, in.ReceiveKeys, order.CustomerID)
	if err != nil {
		return err
	}
	if received == nil {
		return ErrFreeGiftRecipientUsed
	}
	claims = append(claims, received...)

	if ok, err := takeGift(ctx, tx, item.ProductID, item.SellerID); err != nil {
		return err
	} else if !ok {
		return ErrFreeGiftUnavailable
	}
	if ok, err := holdBudget(ctx, tx, item.SellerID, cost, order.Currency); err != nil {
		return err
	} else if !ok {
		return ErrFreeGiftUnavailable
	}
	if ok, err := reserveStock(ctx, tx, item.ProductID); err != nil {
		return err
	} else if !ok {
		return ErrFreeGiftOutOfStock
	}

	order.OrderKind = models.OrderKindFreeGift
	order.FreeGiftBudgetStatus = "held"
	if err := insertOrderHeader(ctx, tx, order); err != nil {
		return err
	}
	if err := insertOrderLines(ctx, tx, order.ID, in.Items, in.Deliveries); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		update promo.free_gift_claims set order_id = $1 where id = any($2)`, order.ID, claims); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// useClaims takes one-time rights: one row per key. It returns the new rows'
// ids, or nil when any key is already used. A request racing for the same
// key waits on the unique index, then sees the other's row.
func useClaims(ctx context.Context, q querier, role string, keys []string, customerID uuid.UUID) ([]uuid.UUID, error) {
	ids := make([]uuid.UUID, 0, len(keys))
	for _, key := range keys {
		var id uuid.UUID
		err := q.QueryRow(ctx, `
			insert into promo.free_gift_claims (role, identity_key, customer_id)
			values ($1, $2, $3)
			on conflict (role, identity_key) where state = 'used' do nothing
			returning id`, role, key, customerID).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// takeGift counts one more gift on the product, while it is active and
// under its cap.
func takeGift(ctx context.Context, q querier, productID, sellerID uuid.UUID) (bool, error) {
	tag, err := q.Exec(ctx, `
		update promo.free_gift_products
		set gifts_count = gifts_count + 1, updated_at = now()
		where product_id = $1 and seller_id = $2 and status = 'active'
		  and (gifts_cap is null or gifts_count < gifts_cap)`, productID, sellerID)
	return err == nil && tag.RowsAffected() == 1, err
}

// holdBudget holds a gift's cost from the seller's budget, while the program
// is active, in this currency, and has room for it.
func holdBudget(ctx context.Context, q querier, sellerID uuid.UUID, cost int64, currency string) (bool, error) {
	tag, err := q.Exec(ctx, `
		update promo.free_gift_programs
		set budget_held = budget_held + $2, updated_at = now()
		where seller_id = $1 and status = 'active'
		  and upper(currency) = upper($3)
		  and budget_held + budget_spent + $2 <= budget_cap`, sellerID, cost, currency)
	return err == nil && tag.RowsAffected() == 1, err
}

// reserveStock reserves one unit, while one is free.
func reserveStock(ctx context.Context, q querier, productID uuid.UUID) (bool, error) {
	tag, err := q.Exec(ctx, `
		update seller.inventory
		set reserved_qty = reserved_qty + 1, updated_at = now()
		where product_id = $1 and available_qty - reserved_qty >= 1`, productID)
	return err == nil && tag.RowsAffected() == 1, err
}

// settleFreeGift moves a free gift order's budget out of 'held', once. It
// returns the cost and the line, or ok false when the order is not a free
// gift or was already settled. onlyDelivered keeps a delivery hook from
// spending a gift whose order is not complete.
func settleFreeGift(ctx context.Context, q querier, orderID uuid.UUID, to string, onlyDelivered bool) (cost int64, sellerID, productID uuid.UUID, qty int, ok bool, err error) {
	err = q.QueryRow(ctx, `
		update marketplace.orders
		set free_gift_budget_status = $2, updated_at = now()
		where id = $1 and order_kind = 'free_gift' and free_gift_budget_status = 'held'
		  and (not $3 or status = 'delivered')
		returning free_gift_cost`, orderID, to, onlyDelivered).Scan(&cost)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, uuid.Nil, uuid.Nil, 0, false, nil
	}
	if err != nil {
		return 0, uuid.Nil, uuid.Nil, 0, false, err
	}
	err = q.QueryRow(ctx, `
		select seller_id, product_id, quantity from marketplace.order_items
		where order_id = $1 order by created_at limit 1`, orderID).Scan(&sellerID, &productID, &qty)
	if err != nil {
		return 0, uuid.Nil, uuid.Nil, 0, false, err
	}
	return cost, sellerID, productID, qty, true, nil
}

// releaseFreeGift gives back everything a cancelled free gift took: the
// product's gift count, the seller's held budget, the reserved unit, and the
// sender's and recipient's one-time rights. It acts once, and does nothing
// for a paid order. The caller holds the transaction.
//
// Rows are touched in the order CreateOrder takes them (product, program,
// stock), so a cancellation and a new gift never wait on each other.
func releaseFreeGift(ctx context.Context, q querier, orderID uuid.UUID) error {
	cost, sellerID, productID, qty, ok, err := settleFreeGift(ctx, q, orderID, "released", false)
	if err != nil || !ok {
		return err
	}
	if _, err := q.Exec(ctx, `
		update promo.free_gift_products
		set gifts_count = greatest(gifts_count - 1, 0), updated_at = now()
		where product_id = $1`, productID); err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `
		update promo.free_gift_programs
		set budget_held = greatest(budget_held - $2, 0), updated_at = now()
		where seller_id = $1`, sellerID, cost); err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `
		update seller.inventory
		set reserved_qty = greatest(reserved_qty - $2, 0), updated_at = now()
		where product_id = $1`, productID, qty); err != nil {
		return err
	}
	_, err = q.Exec(ctx, `
		update promo.free_gift_claims
		set state = 'released', released_at = now()
		where order_id = $1 and state = 'used'`, orderID)
	return err
}

// spendFreeGift settles a delivered free gift: the seller's budget moves
// from held to spent and the reserved unit leaves stock. It acts once, only
// when the order is delivered, and does nothing for a paid order. The caller
// holds the transaction.
func spendFreeGift(ctx context.Context, q querier, orderID uuid.UUID) error {
	cost, sellerID, productID, qty, ok, err := settleFreeGift(ctx, q, orderID, "spent", true)
	if err != nil || !ok {
		return err
	}
	if _, err := q.Exec(ctx, `
		update promo.free_gift_programs
		set budget_held = greatest(budget_held - $2, 0),
		    budget_spent = budget_spent + least($2, budget_held),
		    updated_at = now()
		where seller_id = $1`, sellerID, cost); err != nil {
		return err
	}
	_, err = q.Exec(ctx, `
		update seller.inventory
		set available_qty = greatest(available_qty - $2, 0),
		    reserved_qty = greatest(reserved_qty - $2, 0),
		    updated_at = now()
		where product_id = $1`, productID, qty)
	return err
}
