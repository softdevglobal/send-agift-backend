# Promotions and free gift program: implementation plan

Status: **plan only, nothing here is built yet.**
Backend: `SendAGift_GO` (Go, chi, pgx, Postgres). Frontend: `send-agift-frontend`
(React, Vite, react-router).

This plan covers every kind of promotion in one design:

- discounts (percent or amount off), coupon codes, free delivery, bonus points
- seasonal campaigns such as Christmas, which switch a group of offers on and off
- the free gift program: a customer with a **verified email and phone** can send
  **one free gift**, once

---

## 1. Decisions in one page

| Question | Decision |
|---|---|
| How many routes create an order? | **Two.** `POST /customers/me/orders` (paid, existing) and `POST /customers/me/free-gifts` (new). |
| Do discounts and coupons get their own order route? | **No.** A discounted order is a normal paid order with a lower price. It uses the existing order route. |
| Why is the free gift separate? | Nothing is paid, it has a one-time limit, and its request is a different shape. A "make this free" field on the paid route would be an attack surface. |
| How many tables for promotions? | **One `promo.offers` table for every promotion type**, plus small tables for the facts that need their own integrity. 7 new tables in total. |
| Where are prices worked out? | **One function, `PriceCart`.** The product list, the cart, the delivery quote and the order all call it, so they cannot disagree. |
| Where are one-time limits enforced? | In the database, by a unique index. Never by a read-then-write check. |
| How many new routes? | **17 new, 2 changed** (section 6). |
| How does it stay off until ready? | Config flags `PROMOTIONS_ENABLED` and `FREE_GIFT_ENABLED`, both off by default. |

### The layers

```
              ┌────────────────────────┐   ┌───────────────────────────┐
 HTTP         │ POST /customers/me/    │   │ POST /customers/me/       │
 (two routes) │      orders   (paid)   │   │      free-gifts   (free)  │
              └───────────┬────────────┘   └─────────────┬─────────────┘
                          │                              │
 Rules        ┌───────────▼────────────┐   ┌─────────────▼─────────────┐
 (separate)   │ OrderService.Create    │   │ FreeGiftService.Send      │
              │ offers, coupon, total  │   │ verified, one-time, budget│
              └───────────┬────────────┘   └─────────────┬─────────────┘
                          │                              │
 Shared       ┌───────────▼──────────────────────────────▼─────────────┐
 (one copy)   │ recipient check · PriceCart · delivery zones ·         │
              │ checkOrderDate · order insert                          │
              └───────────────────────────┬────────────────────────────┘
                                          │
 Tables       ┌───────────────────────────▼────────────────────────────┐
 (one set)    │ marketplace.orders · order_items · order_shop_deliveries│
              │ promo.offers · redemptions · entitlements               │
              └─────────────────────────────────────────────────────────┘
```

Two routes is only safe while the shared layer has **one copy**. If the free-gift
service copies delivery pricing or date validation instead of calling it, the two
will drift apart.

---

## 2. What exists today (checked in the code)

| Piece | State |
|---|---|
| Product price | `seller.products.price_amount` only. No sale price, no compare-at price. |
| Order totals | `subtotal_amount`, `delivery_amount`, `total_amount`. No discount column. |
| Order creation | `OrderService.Create` in `order_service.go`. Price comes from the product, never from the client. Always starts at `pending_payment`. |
| Order statuses | `draft, pending_payment, paid, accepted, preparing, dispatched, delivered, cancelled, refunded` (CHECK in migration `000012`). |
| Order payment | No customer payment route. No code moves an order to `paid`. |
| Delivery pricing | From shop delivery zones, `priceShopDeliveries`. |
| Delivery-date validation | `checkOrderDate` in `delivery_date.go`. |
| Email and phone verification | `email_verified_at`, `phone_verified_at`, `phone_e164` on `customer.customers`; OTP by SMS and email. |
| Email-change hold | `ensureCanRedeem` in `customer_email_change.go`. |
| Reward points | Per-product `reward_points`, paid from the seller's points. A separate ledger. |
| Idempotency | `core.idempotency_keys`, used for shipment labels only. |
| Stock reservation | **None**, on any order. |
| Discounts, coupons, campaigns, free gifts | **Nothing.** |

Not read yet, and must be before phase 3: the seller earnings and settlement code,
the refund path, every check of `status == "paid"`, and whether the mobile app
works out prices on the device.

---

## 3. Rules

### 3.1 Rules for every promotion

1. **The server decides the price.** The client sends product ids, quantities and
   an optional coupon code. It never sends a price or a discount.
2. **One price everywhere.** List, cart, quote and order all come from `PriceCart`.
3. **An order keeps what it was given.** The applied offers are copied onto the
   order. Editing or ending an offer later never changes an old order.
4. **Money is integer minor units.** Percentages are basis points (2000 = 20%).
   No floats.
5. **Limits are atomic.** Uses, units and budget are taken with a guarded
   `UPDATE … WHERE used < cap`. No row updated means the offer is closed.
6. **Funding is explicit.** Every offer says who pays: `seller`, `platform`, or
   `shared` with a platform percentage.
7. **Sellers opt in.** A platform campaign is an invitation. A seller's own offer
   must respect the platform floor, and a seller can create only some offer types
   (section 3.4).
8. **Stacking is limited.** One product discount per line (the best one), plus at
   most one coupon, plus free delivery. Anything more needs `stackable = true`.
9. **A cancelled or refunded order gives the offer use back** exactly once.
10. **A discount never makes a line negative.**

### 3.2 Extra rules for the free gift

1. Customer is signed in, **email and phone both verified**, account active, and
   the email-change hold is not running.
2. **One per person, for the life of the program.** A new campaign, seller or
   product does not reset it.
3. **One unit** of one opted-in product. No gift points, no reward points, no
   coupon.
4. **No self-gift.** The recipient's email and phone must differ from the sender's.
5. **One free gift per recipient contact** (section 12, decision 6).
6. **Customer total is 0.** The seller's cost (item and delivery) goes in the ledger.
7. Paid gifts are never affected.

### 3.3 What "one per person" is keyed on

An account id alone is not enough: a customer can change their email, or open a
second account with the same phone. One free gift writes **three** entitlement
rows, and a clash on any of them refuses the gift.

| Key | Stops |
|---|---|
| `customer:<id>` | The same account sending twice |
| `phone:<hmac of verified E.164>` | A second account using the same phone |
| `email:<hmac of verified email>` | A second account using the same email, including after an email change |

Phone and email are stored as a keyed HMAC (secret in config), not the raw value
and not a plain hash.

### 3.4 Platform floor and admin-only types

**Platform floor.** A seller's own offer cannot go past the platform's limits:

| Limit | Config | Default (to be confirmed) | Applies to |
|---|---|---|---|
| Maximum percent off | `PROMO_MAX_SELLER_PERCENT_BPS` | `7000` (70%) | `percent_off`, and `amount_off` turned into a percent of the product price |
| Minimum remaining price | `PROMO_MIN_REMAINING_PRICE` | `100` minor units | The unit price left after the discount |

The floor is checked in two places:

1. **When the seller creates or edits the offer** (`SellerCreateOffer`,
   `SellerUpdateOffer`, `SellerJoinCampaign`), against every product the offer
   targets. Beyond the floor it is refused with `OFFER_BEYOND_FLOOR`.
2. **When a price is worked out** (`PriceCart`). A product's price can drop after
   the offer was created, so a seller-owned discount is cut down to the floor
   there too. The offer is not refused, only capped.

The floor does not apply to platform offers, or to `free_gift`, which is limited
by its uses cap and budget cap instead.

**Admin-only types.** Who can create what:

| Offer | Seller (`POST /sellers/me/offers`) | Admin (`POST /admin/offers`) |
|---|---|---|
| `percent_off`, `amount_off` on own products | Yes, inside the floor | Yes |
| `free_gift` on own products | Yes | Yes, on a seller's behalf |
| Joining a platform campaign | Yes (`POST /sellers/me/campaigns/{id}/join`) | n/a |
| Platform offers (`owner_type = 'platform'`) | **No** | Yes |
| `coupon_code` and rows in `promo.offer_codes` | **No** | Yes |
| `free_delivery`, `bonus_points`, `bundle` | **No** | Yes |
| Campaigns | **No** | Yes |

A seller who sends an admin-only type is refused with `OFFER_TYPE_NOT_ALLOWED`.
The seller route never reads `owner_type` or `seller_id` from the request: it
sets `owner_type = 'seller'` and the seller id from the signed-in seller. The
database backs this up with the `offer_seller_types` CHECK (section 4.3).

---

## 4. Tables

### 4.1 Why this structure

| Option | Verdict |
|---|---|
| One table with a column for every type's fields | Mostly NULLs; every new type needs a migration. **No.** |
| One table per type (`discounts`, `coupons`, `free_gifts`…) | Schedule, limits and funding copied into each; pricing must merge N tables. **No.** |
| One table, everything in one JSON blob | The database cannot enforce caps, dates or uniqueness. **No.** |
| **One `offers` table with shared columns, a small `params` JSON, and side tables for relational facts** | **Chosen.** |

What goes where:

| Goes in | What |
|---|---|
| `promo.offers` columns | Anything filtered, joined, sorted or constrained: type, status, dates, seller, funding, caps, counters, percent and amount |
| `promo.offers.params` (JSON) | The long tail that differs by type and is never filtered on: a bundle's X/Y rule, a points multiplier |
| Own table | Anything needing a foreign key, a unique key or an append-only history: targets, codes, redemptions, entitlements, order snapshot |

The free gift is **not** a separate set of tables. It is an offer row with
`type = 'free_gift'`, inside a campaign with `kind = 'program'`. It reuses the same
caps, dates, funding and ledger. Only the one-time rule needs its own table
(`promo.entitlements`), because that rule has to be a unique index.

### 4.2 The 7 new tables

| Table | One row is | Key constraint |
|---|---|---|
| `promo.campaigns` | A named event: Christmas 2026, or the free gift program | `slug` unique |
| `promo.offers` | One promotion rule | type and field CHECKs |
| `promo.offer_targets` | A product, shop or tag an offer covers | PK `(offer_id, target_type, target_id)` |
| `promo.offer_codes` | A coupon code | `code` unique |
| `promo.redemptions` | A ledger entry: held, committed, released or reversed | unique `(order_id, offer_id, kind)` |
| `promo.entitlements` | A used one-time right | unique `(scope_key, role, identity_key)` where used |
| `marketplace.order_discounts` | What an order was given, frozen | FK to the order |

Changed tables: `marketplace.orders` (4 columns, 1 status), `marketplace.order_items`
(2 columns).

### 4.3 Migration `000067_promotions`

```sql
CREATE SCHEMA IF NOT EXISTS promo;

-- A named event. It only holds the schedule, countries and banner, so ending
-- "Christmas" switches every offer inside it off at once.
CREATE TABLE IF NOT EXISTS promo.campaigns (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug       text NOT NULL UNIQUE,            -- christmas-2026, welcome-free-gift
    name       text NOT NULL,
    kind       text NOT NULL DEFAULT 'seasonal'
               CHECK (kind IN ('seasonal', 'flash', 'always_on', 'program')),
    starts_at  timestamptz NOT NULL,
    ends_at    timestamptz,
    timezone   text NOT NULL DEFAULT 'UTC',
    countries  uuid[] NOT NULL DEFAULT '{}',    -- empty = every country
    banner_url text,
    headline   text,
    status     text NOT NULL DEFAULT 'draft'
               CHECK (status IN ('draft', 'scheduled', 'active', 'paused', 'ended')),
    created_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (ends_at IS NULL OR ends_at > starts_at)
);

-- Every promotion, whatever its type.
CREATE TABLE IF NOT EXISTS promo.offers (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    campaign_id  uuid REFERENCES promo.campaigns (id),
    owner_type   text NOT NULL CHECK (owner_type IN ('platform', 'seller')),
    seller_id    uuid REFERENCES seller.sellers (id),
    type         text NOT NULL CHECK (type IN (
                     'percent_off', 'amount_off', 'coupon_code', 'free_delivery',
                     'bonus_points', 'free_gift', 'bundle')),
    name         text NOT NULL,
    status       text NOT NULL DEFAULT 'draft'
                 CHECK (status IN ('draft', 'pending_approval', 'active',
                                   'paused', 'ended', 'rejected')),
    starts_at    timestamptz,
    ends_at      timestamptz,

    -- who it covers
    scope_type   text NOT NULL CHECK (scope_type IN ('all', 'shop', 'product', 'occasion_tag')),
    customer_types text[] NOT NULL DEFAULT '{personal,corporate}',
    new_customers_only      boolean NOT NULL DEFAULT false,
    requires_verified_email boolean NOT NULL DEFAULT false,
    requires_verified_phone boolean NOT NULL DEFAULT false,
    min_spend    integer NOT NULL DEFAULT 0 CHECK (min_spend >= 0),

    -- who pays
    funding      text NOT NULL CHECK (funding IN ('seller', 'platform', 'shared')),
    platform_share_pct integer NOT NULL DEFAULT 0 CHECK (platform_share_pct BETWEEN 0 AND 100),

    -- how it combines
    priority     integer NOT NULL DEFAULT 100,
    stackable    boolean NOT NULL DEFAULT false,

    -- the numbers the pricing code reads
    percent_bps  integer CHECK (percent_bps BETWEEN 1 AND 10000),   -- 2000 = 20%
    amount_off   integer CHECK (amount_off > 0),
    max_discount integer CHECK (max_discount > 0),

    -- limits and their running totals (always changed with a guarded UPDATE)
    uses_cap          integer CHECK (uses_cap > 0),
    uses_count        integer NOT NULL DEFAULT 0 CHECK (uses_count >= 0),
    uses_per_customer integer CHECK (uses_per_customer > 0),
    budget_cap        bigint CHECK (budget_cap > 0),
    budget_held       bigint NOT NULL DEFAULT 0 CHECK (budget_held >= 0),
    budget_spent      bigint NOT NULL DEFAULT 0 CHECK (budget_spent >= 0),
    currency          text,

    -- the long tail, checked by the application
    params         jsonb NOT NULL DEFAULT '{}',
    params_version integer NOT NULL DEFAULT 1,

    terms_version text,
    accepted_by   uuid,
    accepted_at   timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT offer_type_fields CHECK (
        (type = 'percent_off' AND percent_bps IS NOT NULL AND amount_off IS NULL) OR
        (type = 'amount_off'  AND amount_off  IS NOT NULL AND percent_bps IS NULL) OR
        (type = 'free_gift'   AND uses_cap IS NOT NULL AND budget_cap IS NOT NULL
                              AND seller_id IS NOT NULL) OR
        (type IN ('coupon_code', 'free_delivery', 'bonus_points', 'bundle'))),
    CONSTRAINT offer_dates CHECK (ends_at IS NULL OR starts_at IS NULL OR ends_at > starts_at),
    CONSTRAINT offer_seller CHECK ((owner_type = 'seller') = (seller_id IS NOT NULL)),
    -- Coupon codes, free delivery, bonus points and bundles are admin-only.
    CONSTRAINT offer_seller_types CHECK (
        owner_type = 'platform' OR type IN ('percent_off', 'amount_off', 'free_gift')),
    CONSTRAINT offer_uses   CHECK (uses_cap IS NULL OR uses_count <= uses_cap),
    CONSTRAINT offer_budget CHECK (budget_cap IS NULL OR budget_held + budget_spent <= budget_cap)
);

CREATE INDEX IF NOT EXISTS offers_active_idx
    ON promo.offers (starts_at, ends_at) WHERE status = 'active';
CREATE INDEX IF NOT EXISTS offers_seller_idx ON promo.offers (seller_id);

-- The products, shops or tags an offer covers. Empty when scope_type = 'all'.
CREATE TABLE IF NOT EXISTS promo.offer_targets (
    offer_id    uuid NOT NULL REFERENCES promo.offers (id) ON DELETE CASCADE,
    target_type text NOT NULL CHECK (target_type IN ('product', 'shop', 'occasion_tag')),
    target_id   text NOT NULL,           -- a uuid as text, or the tag itself
    PRIMARY KEY (offer_id, target_type, target_id)
);

CREATE INDEX IF NOT EXISTS offer_targets_lookup_idx
    ON promo.offer_targets (target_type, target_id);

-- Coupon codes. citext, so XMAS25 and xmas25 are the same code.
CREATE TABLE IF NOT EXISTS promo.offer_codes (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    offer_id    uuid NOT NULL REFERENCES promo.offers (id) ON DELETE CASCADE,
    code        citext NOT NULL UNIQUE,
    customer_id uuid REFERENCES customer.customers (id),   -- set = private code
    uses_cap    integer CHECK (uses_cap > 0),
    uses_count  integer NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    CHECK (uses_cap IS NULL OR uses_count <= uses_cap)
);

-- Append-only money trail. One row per order, offer and kind, so a retry
-- cannot count twice.
CREATE TABLE IF NOT EXISTS promo.redemptions (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    offer_id       uuid NOT NULL REFERENCES promo.offers (id),
    order_id       uuid NOT NULL REFERENCES marketplace.orders (id),
    customer_id    uuid NOT NULL REFERENCES customer.customers (id),
    kind           text NOT NULL CHECK (kind IN ('hold', 'commit', 'release', 'reverse')),
    amount         bigint NOT NULL,      -- minor units
    currency       text NOT NULL,
    seller_share   bigint NOT NULL DEFAULT 0,
    platform_share bigint NOT NULL DEFAULT 0,
    created_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (order_id, offer_id, kind),
    CHECK (seller_share + platform_share = amount)
);

CREATE INDEX IF NOT EXISTS redemptions_customer_idx
    ON promo.redemptions (customer_id, offer_id);

-- One-time rights. The unique index is the rule.
CREATE TABLE IF NOT EXISTS promo.entitlements (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    scope_key    text NOT NULL,          -- campaign slug, e.g. welcome-free-gift
    role         text NOT NULL CHECK (role IN ('send', 'receive', 'use')),
    identity_key text NOT NULL,          -- customer:<id> | phone:<hmac> | email:<hmac>
    key_version  integer NOT NULL DEFAULT 1,
    customer_id  uuid REFERENCES customer.customers (id),
    order_id     uuid REFERENCES marketplace.orders (id),
    state        text NOT NULL DEFAULT 'used' CHECK (state IN ('used', 'released')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    released_at  timestamptz
);

-- Released rows stop counting, so an allowed cancellation gives the right back.
CREATE UNIQUE INDEX IF NOT EXISTS entitlements_one_uq
    ON promo.entitlements (scope_key, role, identity_key) WHERE state = 'used';

-- Orders: the kind, the discount, and safe retries.
ALTER TABLE marketplace.orders
    ADD COLUMN IF NOT EXISTS order_kind text NOT NULL DEFAULT 'paid'
        CHECK (order_kind IN ('paid', 'free_gift')),
    ADD COLUMN IF NOT EXISTS discount_amount integer NOT NULL DEFAULT 0
        CHECK (discount_amount >= 0),
    ADD COLUMN IF NOT EXISTS coupon_code text,
    ADD COLUMN IF NOT EXISTS idempotency_key text;

CREATE UNIQUE INDEX IF NOT EXISTS orders_customer_idempotency_uq
    ON marketplace.orders (customer_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- A free gift has nothing to pay, so it never sits in pending_payment.
ALTER TABLE marketplace.orders DROP CONSTRAINT IF EXISTS orders_status_check;
ALTER TABLE marketplace.orders ADD CONSTRAINT orders_status_check
    CHECK (status IN ('draft', 'pending_payment', 'paid', 'confirmed', 'accepted',
                      'preparing', 'dispatched', 'delivered', 'cancelled', 'refunded'));

ALTER TABLE marketplace.order_items
    ADD COLUMN IF NOT EXISTS list_unit_amount integer,     -- price before offers
    ADD COLUMN IF NOT EXISTS discount_amount integer NOT NULL DEFAULT 0
        CHECK (discount_amount >= 0);

-- What the order was given, frozen at order time.
CREATE TABLE IF NOT EXISTS marketplace.order_discounts (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id       uuid NOT NULL REFERENCES marketplace.orders (id) ON DELETE CASCADE,
    order_item_id  uuid REFERENCES marketplace.order_items (id) ON DELETE CASCADE,
    offer_id       uuid REFERENCES promo.offers (id),
    kind           text NOT NULL,        -- the offer type at the time
    label          text NOT NULL,        -- "Christmas 20% off", shown to the customer
    amount         integer NOT NULL CHECK (amount >= 0),
    funding        text NOT NULL,
    seller_share   integer NOT NULL DEFAULT 0,
    platform_share integer NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS order_discounts_order_idx
    ON marketplace.order_discounts (order_id);

-- The free gift program is a campaign whose slug never changes.
INSERT INTO promo.campaigns (slug, name, kind, starts_at, status)
VALUES ('welcome-free-gift', 'Welcome free gift', 'program', now(), 'draft')
ON CONFLICT DO NOTHING;
```

Before running: confirm the real name of the orders status constraint
(`\d marketplace.orders`). `orders_status_check` is a guess.

### 4.4 How each order is stored

| Column | Paid order with a discount | Free gift |
|---|---|---|
| `order_kind` | `paid` | `free_gift` |
| `status` at creation | `pending_payment` | `confirmed` |
| `subtotal_amount` | sum of list prices | list price of the one unit |
| `delivery_amount` | zone delivery price | zone delivery price |
| `discount_amount` | sum of applied discounts | `subtotal_amount + delivery_amount` |
| `total_amount` | `subtotal + delivery − discount` | `0` |
| item `list_unit_amount` | product price | product price |
| item `unit_amount` | price after line discounts | product price (seller sees the value) |
| `gift_points`, reward points | as today | `0` |

Rule for every report from now on: **revenue is `total_amount`**, never
`subtotal_amount`.

### 4.5 Example rows

| Promotion | `type` | Key fields |
|---|---|---|
| Christmas 20% off hampers | `percent_off` | `percent_bps 2000`, `scope_type occasion_tag`, target `hampers`, campaign `christmas-2026` |
| $5 off orders over $40 | `amount_off` | `amount_off 500`, `min_spend 4000`, `scope_type all` |
| `XMAS25` code | `coupon_code` | `percent_bps 2500`, one row in `offer_codes` |
| Free delivery over $50 | `free_delivery` | `min_spend 5000` |
| Double points week | `bonus_points` | `params {"multiplier": 2}` |
| Free gift, one headset shop | `free_gift` | `seller_id`, `uses_cap 50`, `budget_cap 250000`, `requires_verified_email/phone true`, one `product` target, campaign `welcome-free-gift` |

---

## 5. Models (Go)

New file `internal/models/promo.go`:

```go
// Campaign maps to promo.campaigns.
type Campaign struct {
    ID        uuid.UUID   `json:"id"`
    Slug      string      `json:"slug"`
    Name      string      `json:"name"`
    Kind      string      `json:"kind"`   // seasonal | flash | always_on | program
    StartsAt  time.Time   `json:"starts_at"`
    EndsAt    *time.Time  `json:"ends_at,omitempty"`
    Timezone  string      `json:"timezone"`
    Countries []uuid.UUID `json:"countries"`
    BannerURL *string     `json:"banner_url,omitempty"`
    Headline  *string     `json:"headline,omitempty"`
    Status    string      `json:"status"`
}

// Offer maps to promo.offers.
type Offer struct {
    ID                    uuid.UUID       `json:"id"`
    CampaignID            *uuid.UUID      `json:"campaign_id,omitempty"`
    OwnerType             string          `json:"owner_type"` // platform | seller
    SellerID              *uuid.UUID      `json:"seller_id,omitempty"`
    Type                  string          `json:"type"`
    Name                  string          `json:"name"`
    Status                string          `json:"status"`
    StartsAt, EndsAt      *time.Time
    ScopeType             string          `json:"scope_type"`
    CustomerTypes         []string        `json:"customer_types"`
    NewCustomersOnly      bool            `json:"new_customers_only"`
    RequiresVerifiedEmail bool            `json:"requires_verified_email"`
    RequiresVerifiedPhone bool            `json:"requires_verified_phone"`
    MinSpend              int             `json:"min_spend"`
    Funding               string          `json:"funding"`
    PlatformSharePct      int             `json:"platform_share_pct"`
    Priority              int             `json:"priority"`
    Stackable             bool            `json:"stackable"`
    PercentBps            *int            `json:"percent_bps,omitempty"`
    AmountOff             *int            `json:"amount_off,omitempty"`
    MaxDiscount           *int            `json:"max_discount,omitempty"`
    UsesCap               *int            `json:"uses_cap,omitempty"`
    UsesCount             int             `json:"uses_count"`
    UsesPerCustomer       *int            `json:"uses_per_customer,omitempty"`
    BudgetCap             *int64          `json:"budget_cap,omitempty"`
    BudgetHeld            int64           `json:"budget_held"`
    BudgetSpent           int64           `json:"budget_spent"`
    Currency              *string         `json:"currency,omitempty"`
    Params                json.RawMessage `json:"params"`
    Targets               []OfferTarget   `json:"targets"`
}

type OfferTarget struct {
    TargetType string `json:"target_type"` // product | shop | occasion_tag
    TargetID   string `json:"target_id"`
}

type OfferCode struct {
    ID         uuid.UUID  `json:"id"`
    OfferID    uuid.UUID  `json:"offer_id"`
    Code       string     `json:"code"`
    CustomerID *uuid.UUID `json:"customer_id,omitempty"`
    UsesCap    *int       `json:"uses_cap,omitempty"`
    UsesCount  int        `json:"uses_count"`
}

// OrderDiscount maps to marketplace.order_discounts.
type OrderDiscount struct {
    ID          uuid.UUID  `json:"id"`
    OrderItemID *uuid.UUID `json:"order_item_id,omitempty"`
    OfferID     *uuid.UUID `json:"offer_id,omitempty"`
    Kind        string     `json:"kind"`
    Label       string     `json:"label"`
    Amount      int        `json:"amount"`
    Funding     string     `json:"funding"`
}
```

Added to existing models in `internal/models/order.go`:

```go
// Order
OrderKind      string  `json:"order_kind"`       // paid | free_gift
DiscountAmount int     `json:"discount_amount"`
CouponCode     *string `json:"coupon_code,omitempty"`

// OrderItem
ListUnitAmount *int `json:"list_unit_amount,omitempty"`
DiscountAmount int  `json:"discount_amount"`

// OrderDetails
Discounts []OrderDiscount `json:"discounts"`
```

The pricing types, in `internal/services/pricing_engine.go`:

```go
type CartLine struct {
    ProductID string
    Quantity  int
}

type CartInput struct {
    CustomerID   uuid.UUID
    CustomerType string     // personal | corporate
    CountryID    uuid.UUID
    Lines        []CartLine
    CouponCode   string
    Now          time.Time
}

type AppliedOffer struct {
    OfferID       uuid.UUID `json:"offer_id"`
    Kind          string    `json:"kind"`
    Label         string    `json:"label"`
    Amount        int       `json:"amount"`
    Funding       string    `json:"funding"`
    SellerShare   int       `json:"-"`
    PlatformShare int       `json:"-"`
}

type PricedLine struct {
    ProductID      string         `json:"product_id"`
    ShopID         string         `json:"shop_id"`
    Quantity       int            `json:"quantity"`
    ListUnitAmount int            `json:"list_unit_amount"`
    UnitAmount     int            `json:"unit_amount"`
    DiscountAmount int            `json:"discount_amount"`
    Applied        []AppliedOffer `json:"applied"`
}

type PricedCart struct {
    Lines           []PricedLine   `json:"lines"`
    Currency        string         `json:"currency"`
    SubtotalAmount  int            `json:"subtotal_amount"`
    DiscountAmount  int            `json:"discount_amount"`
    DeliveryWaived  bool           `json:"delivery_waived"`
    Applied         []AppliedOffer `json:"applied"`
    BonusPoints     int64          `json:"bonus_points"`
    CouponMessage   string         `json:"coupon_message,omitempty"` // why a code did not apply
}
```

---

## 6. Routes

All under `/api/v1`. **17 new, 2 changed.**

### 6.1 Public (2 new)

| Method and path | Purpose |
|---|---|
| `GET /offers/active?country=&tag=` | Running campaigns, banners and offers |
| `GET /campaigns/{slug}` | One campaign with its products and prices |

### 6.2 Customer (3 new, 2 changed)

| Method and path | New or changed | Purpose |
|---|---|---|
| `POST /customers/me/cart/price` | New | Price a cart with an optional coupon code. No order is written |
| `GET /customers/me/free-gifts/eligibility?recipient_id=` | New | Can I send one, and which offers reach this recipient |
| `POST /customers/me/free-gifts` | New | **Order-creating route 2.** `Idempotency-Key` header required |
| `POST /customers/me/orders` | Changed | **Order-creating route 1.** Accepts `coupon_code`; returns `discount_amount` and `discounts[]` |
| `POST /customers/me/shipping/quote` | Changed | Accepts `coupon_code`; returns discounts and a delivery waiver |

Also changed, as responses only: the product list, product detail and gift
availability search gain `list_price_amount`, `effective_price_amount` and `offers[]`.

### 6.3 Seller (5 new)

| Method and path | Purpose |
|---|---|
| `GET /sellers/me/offers` | Own offers with uses and budget used |
| `POST /sellers/me/offers` | Create a discount, or opt a product into the free gift program. `percent_off`, `amount_off` and `free_gift` only, inside the platform floor (section 3.4) |
| `PATCH /sellers/me/offers/{id}` | Change caps or dates, pause, end. The floor is checked again |
| `GET /sellers/me/campaigns` | Platform campaigns this seller can join |
| `POST /sellers/me/campaigns/{id}/join` | Opt products in with own caps, accepting the terms |

### 6.4 Admin (7 new)

| Method and path | Purpose |
|---|---|
| `GET /admin/campaigns` | List |
| `POST /admin/campaigns` | Create |
| `PATCH /admin/campaigns/{id}` | Edit, activate, pause (the kill switch), end |
| `GET /admin/offers` | List, with a filter for those awaiting approval |
| `POST /admin/offers` | Create a platform offer and its codes. The only route that can create coupon codes, free delivery, bonus points and bundles |
| `PATCH /admin/offers/{id}` | Approve, reject, pause, end |
| `GET /admin/offers/{id}/report` | Uses, spend, budget, and ledger against orders |

### 6.5 Requests

Paid order with a coupon (existing body, one new field):

```json
{
  "country_id": "…", "recipient_id": "…", "delivery_date": "2026-12-20",
  "items": [{ "product_id": "…", "quantity": 2 }],
  "coupon_code": "XMAS25"
}
```

Free gift:

```json
{
  "offer_id": "…", "recipient_id": "…",
  "delivery_date": "2026-12-20", "gift_message": "Happy birthday"
}
```

### 6.6 Error codes

Every refusal returns `{"error": "<message>", "code": "<CODE>"}`.

| Code | HTTP | Meaning |
|---|---|---|
| `COUPON_INVALID` | 422 | Unknown, expired, not for this customer, or below the minimum spend |
| `COUPON_USED_UP` | 409 | The code or offer reached its cap |
| `OFFER_CLOSED` | 409 | Paused, ended, or out of uses or budget |
| `OFFER_BEYOND_FLOOR` | 422 | A seller's offer passes the maximum percent or leaves less than the minimum price. Returns the limits |
| `OFFER_TYPE_NOT_ALLOWED` | 403 | A seller tried to create an admin-only offer type |
| `PRICE_CHANGED` | 409 | The price moved between the quote and the order. Returns the new total |
| `FREE_GIFT_DISABLED` | 404 | Program or flag is off |
| `VERIFY_EMAIL_REQUIRED` | 403 | Email is not verified |
| `VERIFY_PHONE_REQUIRED` | 403 | Phone is not verified |
| `ALREADY_USED` | 409 | This person has sent their free gift |
| `RECIPIENT_ALREADY_USED` | 409 | This recipient already got one (no details given) |
| `SELF_GIFT` | 422 | Recipient is the sender |
| `OUT_OF_STOCK` | 409 | No stock left |
| `IDEMPOTENCY_MISMATCH` | 422 | Same key, different request |
| `DELIVERY_DATE_UNAVAILABLE` | 422 | Existing, from `checkOrderDate` |
| `REDEMPTION_LOCKED` | 423 | Existing, email changed in the last few days |

---

## 7. Backend files and functions

### 7.1 New files

| File | Functions |
|---|---|
| `internal/database/migrations/000067_promotions.up.sql` / `.down.sql` | Section 4.3 |
| `internal/models/promo.go` | Section 5 |
| `internal/repository/offer_repository.go` | `ActiveForProducts`, `CodeByText`, `GetOffer`, `ListForSeller`, `ListForAdmin`, `CreateOffer`, `UpdateOffer`, `TakeUse`, `GiveBackUse`, `HoldBudget`, `CommitBudget`, `ReleaseBudget`, `InsertRedemption`, `CustomerUses` |
| `internal/repository/campaign_repository.go` | `Active`, `BySlug`, `Create`, `Update`, `SetStatus`, `DueToStart`, `DueToEnd` |
| `internal/repository/entitlement_repository.go` | `Use` (insert; unique clash = already used), `Release`, `IsUsed` |
| `internal/services/pricing_engine.go` | `PriceCart`, and the types in section 5 |
| `internal/services/offer_rules.go` | `eligible`, `applyPercent`, `applyAmount`, `applyCoupon`, `applyFreeDelivery`, `bonusPoints`, `resolveStacking`, `splitFunding`, `roundHalfUp`, `checkSellerFloor`, `capToFloor`, `sellerMayCreate` |
| `internal/services/offer_service.go` | `ActiveOffers`, `CampaignPage`, `AdminCreateCampaign`, `AdminUpdateCampaign`, `AdminCreateOffer`, `AdminDecideOffer`, `SellerCreateOffer`, `SellerUpdateOffer`, `SellerJoinCampaign`, `Report` |
| `internal/services/offer_scheduler.go` | `RunSchedule`: starts and ends campaigns and offers on time |
| `internal/services/free_gift_identity.go` | `identityKeys(customer)`, `hmacKey(kind, value)` |
| `internal/services/free_gift_service.go` | `Eligibility`, `Send`, `OnCancelled`, `OnDelivered` |
| `internal/handlers/offer_handler.go` | Public, seller and admin handlers |
| `internal/handlers/free_gift_handler.go` | The two free-gift handlers and error mapping |
| `internal/routes/offer_routes.go`, `free_gift_routes.go` | Section 6 |
| Tests | `pricing_engine_test.go`, `offer_rules_test.go`, `free_gift_service_test.go`, `promotions_integration_test.go` |

### 7.2 Key signatures

```go
// The only place a price is worked out.
func (e *PricingEngine) PriceCart(ctx context.Context, in CartInput) (*PricedCart, error)

// Free gift.
func (s *FreeGiftService) Eligibility(ctx context.Context, customerID, recipientID string) (*FreeGiftEligibility, error)
func (s *FreeGiftService) Send(ctx context.Context, customerID string, in FreeGiftInput) (*models.OrderDetails, error)

// Limits: true means taken, false means the cap was reached.
func (r *OfferRepository) TakeUse(ctx context.Context, tx pgx.Tx, offerID uuid.UUID) (bool, error)
func (r *OfferRepository) HoldBudget(ctx context.Context, tx pgx.Tx, offerID uuid.UUID, amount int64) (bool, error)

// One-time rule: ErrEntitlementUsed on a unique clash.
func (r *EntitlementRepository) Use(ctx context.Context, tx pgx.Tx, scope, role string, keys []string, customerID, orderID uuid.UUID) error
```

### 7.3 Changes to existing files

| File | Change |
|---|---|
| `internal/services/order_service.go` | `Create` calls `PriceCart` instead of `snap.PriceAmount * qty`. Moves the shared steps (recipient check, `priceShopDeliveries`, `checkOrderDate`) into helpers the free-gift service also calls |
| `internal/repository/order_repository.go` | `Create` also writes `order_discounts` and `redemptions` (hold), in the same transaction. New columns in the selects |
| `internal/services/shipping_service.go` | `QuoteDelivery` calls `PriceCart` and returns discounts |
| `internal/services/availability_service.go`, product list and get | Return effective and list price and `offers[]` |
| `internal/repository/points_rewards.go` | Reward points worked out on the paid price; add `bonus_points` offers |
| Order cancel path (`order_repository.go` ~line 352) | Treat `confirmed` like `paid`. Release redemptions, give uses back; for a free gift also stock and entitlements |
| Delivered path (`gift_recipient_service.go`) | Turn redemption `hold` into `commit` |
| `internal/models/order.go` | Fields in section 5 |
| `internal/config/config.go` | `PROMOTIONS_ENABLED`, `FREE_GIFT_ENABLED`, `FREE_GIFT_CAMPAIGN_SLUG`, `FREE_GIFT_IDENTITY_SECRET`, `PROMO_MAX_SELLER_PERCENT_BPS`, `PROMO_MIN_REMAINING_PRICE` |
| `cmd/api/main.go`, `internal/routes/router.go` | Wiring, and the scheduler loop |
| Seller earnings code | **Read first.** Must use the paid amount less the seller's share of discounts |
| Every `status == "paid"` check | **Search first.** Decide for each whether `confirmed` counts |

---

## 8. How a price is worked out

`PriceCart`, in this order:

1. Load each product: price, currency, shop, seller, occasion tags.
2. Load active offers that could apply: status `active`, inside their dates, in an
   active campaign, and targeting `all`, one of the shops, products or tags.
3. Drop offers the customer is not eligible for (customer type, country, new
   customer, verified email or phone, minimum spend, per-customer uses).
4. **Line discounts.** For each line, work out every `percent_off` and
   `amount_off` and keep the **best one**. Add others only when both are `stackable`.
5. **Coupon.** If a code was sent, check it and apply it to the amount left after
   line discounts. A code that does not apply is not an error for pricing: the
   reason goes in `coupon_message`.
6. **Free delivery.** If one applies, mark delivery as waived.
7. **Bonus points.** Work out on the amount actually paid.
8. Split each discount between seller and platform by `funding`.
9. Return the lines, totals and applied offers.

Rounding:

- Percent discount on a line: `roundHalfUp(list_unit × quantity × percent_bps / 10000)`.
- Cap by `max_discount`, then by the line total, so a line never goes below 0.
- A seller-owned discount is then capped to the platform floor (section 3.4).
- An order-level amount is spread over lines in proportion, with the remainder on
  the largest line, so the lines always add up to the order.
- Funding split: platform share rounded half up, seller share is the rest.

---

## 9. The two transactions

### 9.1 Paid order (existing route)

```
1. validate input, recipient, delivery date           (as today)
2. cart = PriceCart(...)                               ← new
3. price delivery from zones; apply the waiver        (as today, plus waiver)
4. checkOrderDate                                     (as today)
5. BEGIN
6.   for each applied offer:
        TakeUse      → false = OFFER_CLOSED / COUPON_USED_UP, rollback
        HoldBudget   → false = OFFER_CLOSED, rollback
7.   insert order (order_kind paid, discount_amount, total)
8.   insert order_items (list_unit_amount, unit_amount, discount_amount)
9.   insert order_discounts, redemptions (hold)
10.  reward points and gift points                    (as today)
11. COMMIT
```

### 9.2 Free gift (new route)

```
1. flag on?  same idempotency key already has an order → return it
2. customer verified (email + phone), active, ensureCanRedeem
3. offer active and in date; product published; shop active
4. recipient belongs to customer; not a self-gift
5. price delivery from zones; checkOrderDate           (shared helpers)
6. BEGIN
7.   entitlements.Use(send,    customer + phone + email keys) → clash = ALREADY_USED
8.   entitlements.Use(receive, recipient phone + email keys)  → clash = RECIPIENT_ALREADY_USED
9.   TakeUse(offer)                                           → false = OFFER_CLOSED
10.  HoldBudget(offer, item + delivery)                       → false = OFFER_CLOSED
11.  reserve one unit of stock                                → 0 rows = OUT_OF_STOCK
12.  insert order (order_kind free_gift, status confirmed, total 0)
13.  insert order_item, order_shop_delivery, order_discounts, redemption (hold)
14. COMMIT
15. queue the confirmation email
```

Stock, step 11 (no order reserves stock today, and a free gift must):

```sql
UPDATE seller.inventory
SET reserved_qty = reserved_qty + 1
WHERE product_id = $1 AND available_qty - reserved_qty >= 1;
```

Afterwards, for both kinds of order:

| Event | Effect |
|---|---|
| Delivered | redemption `commit`; `budget_held` moves to `budget_spent` |
| Cancelled before dispatch | redemption `release`; use and budget given back; free gift: stock and entitlements back |
| Refunded after delivery | redemption `reverse`; no automatic second free gift |

---

## 10. Frontend changes (`send-agift-frontend`)

| File | Change |
|---|---|
| `src/api/offers.ts` (new) | Active offers, campaign page, cart price, seller and admin calls |
| `src/api/free-gifts.ts` (new) | `getFreeGiftEligibility`, `sendFreeGift` |
| `src/features/customer-commerce/gift-card.tsx`, product page | Struck-through list price, offer badge |
| `src/pages/products-page.tsx` | "On offer" filter |
| `src/pages/home-page.tsx` | Campaign banner from `GET /offers/active` |
| `src/pages/campaign-page.tsx` (new), route `/offers/:slug` | Campaign landing page |
| `src/pages/cart-page.tsx` | Coupon code box; prices from `POST /cart/price` |
| `src/pages/checkout-page.tsx` | Discount lines, free delivery line, a link to `/free-gift` when eligible. No free-gift logic here |
| `src/features/free-gifts/free-gift-banner.tsx` (new) | "You have 1 free gift" |
| `src/features/free-gifts/verify-to-unlock.tsx` (new) | Says which of email or phone is unverified, links to `/account/profile` |
| `src/pages/free-gift-page.tsx` (new), route `/free-gift` | Pick an offer, recipient and date, then confirm |
| Order list and order detail | "Free gift" badge, discount lines, total |
| `src/pages/seller-offers-page.tsx` (new), route `/seller/offers` | Create a discount, join a campaign, opt into free gifts, caps, pause, usage |
| `src/features/seller-orders/` | "Free gift (you fund this)" marker, and the seller's share of a discount |
| `src/pages/admin-campaigns-page.tsx`, `admin-offers-page.tsx` (new) | Create, approve, pause, report |

The client never works out a price or decides eligibility. It shows what the API
returns. For the free gift it generates one `Idempotency-Key` (a UUID) per confirm
screen and reuses it on retry.

---

## 11. Edge cases, tests, phases

### 11.1 Edge cases

| Case | What happens |
|---|---|
| Price changes between the quote and the order | Order is refused with `PRICE_CHANGED` and the new total; the customer confirms again |
| Offer ends while the customer is at checkout | Same as above |
| Last coupon use, two customers at once | The guarded `UPDATE` gives it to one; the other gets `COUPON_USED_UP` |
| Offer edited after an order | The order keeps its `order_discounts` snapshot |
| Double click on the free gift | Same idempotency key returns the same order |
| Free gift from two tabs | The unique index lets one through; the other gets `ALREADY_USED` |
| Email changed, or a new account on the same phone | Blocked by the other identity keys |
| Seller pauses an offer | New orders refused; confirmed orders are still fulfilled |
| Account deleted | Entitlement rows stay, so re-registering does not reset the free gift |

### 11.2 Tests

| Test | Pass condition |
|---|---|
| List, cart price, quote and order for the same cart | The same total every time |
| Best single discount | Two offers on one line: only the larger applies |
| Stacking | A coupon applies after the line discount; non-stackable offers never combine |
| Rounding | Lines add up to the order; no line below 0 |
| Funding split | `seller_share + platform_share = amount` on every row |
| Cap race | 10 parallel orders on the last use: exactly one succeeds |
| Budget race | Parallel orders never push held plus spent past the cap |
| Cancel | Release written once; use and budget returned once |
| Campaign schedule | Offers start and stop at the campaign's timezone boundary |
| Seller floor | A seller cannot create an offer beyond the platform limits (`OFFER_BEYOND_FLOOR`) |
| Seller floor after a price drop | The discount is capped at pricing time; the unit price never goes below the minimum |
| Admin-only types | A seller sending `coupon_code`, `free_delivery`, `bonus_points`, `bundle` or `owner_type platform` gets `OFFER_TYPE_NOT_ALLOWED`; an admin can create each |
| Free gift: unverified email or phone | Refused with the right code |
| Free gift: first send | Order with total 0, three `send` rows, redemption `hold` |
| Free gift: 20 parallel requests | Exactly one order |
| Free gift: same phone, second account | `ALREADY_USED` |
| Free gift: last unit, 10 customers | Exactly one order |
| Free gift: idempotency | Same key returns the same order; a different body is refused |
| Paid order with no offers | Unchanged: same totals, status and rows as today |
| Flags off | No offers apply; free-gift routes return `FREE_GIFT_DISABLED` |

The parallel tests need the CI Postgres. They cannot be trusted from unit tests.

### 11.3 Phases

| Phase | Work | Done when |
|---|---|---|
| 0 | Decisions in section 12. Read the earnings, refund and `paid`-status code | Written answers |
| 1 | Migration, models, `PriceCart` with `percent_off` and `amount_off`, order snapshot | List, quote and order agree; paid orders without offers unchanged |
| 2 | Coupons, caps, budget, redemption ledger, cancel and delivered hooks | Cap and budget race tests pass |
| 3 | Seller offers, campaign opt-in, funding split in earnings | Settlement shows the seller's share |
| 4 | Customer screens: badges, filter, campaign page, coupon box, checkout lines | Works at phone width |
| 5 | Free delivery, bonus points, first-order rule, scheduler | Each has a pass and a fail test |
| 6 | Free gift: entitlements, stock reservation, `Send`, the `/free-gift` page | A verified customer can send one, and only one |
| 7 | Admin screens, report, kill switch | Ledger adds up against orders |
| 8 | Pilot: one seller, small caps, flags on in staging only | Sign-off recorded |

Free gift first instead? Phases 1 and 6 can swap, because the free gift needs only
the migration, entitlements and the shared helpers, not the pricing engine.

---

## 12. Decisions needed before building

1. **Who funds platform campaigns**, and is the split fixed or set per campaign?
2. **Stacking:** one line discount plus one coupon (recommended), or more?
3. **Reward points on a discounted order:** on the paid price (recommended) or the list price?
4. **Seller offers:** live at once, or approved by an admin first? Confirm the
   floor numbers in section 3.4 (defaults: 70% maximum, 100 minor units minimum
   remaining price), and whether `free_delivery` should stay admin-only.
5. **Tax:** are prices tax-inclusive, and how is a discount shown against GST?
6. **Free gift recipient limit.** Recommended: one per recipient email and phone,
   using what the sender typed. It does not prove the recipient owns that contact,
   but it stops many accounts sending free gifts to one address. The stronger
   version is in section 13.
7. **Free gift: sending only, or also a limit on receiving?**
8. **Does a cancellation give the free gift back?** Recommended: yes before
   dispatch, no after.
9. **Who pays delivery on a free gift?** Recommended: the seller, inside their cap.
10. **Countries, minimum age, and whether the free gift expires** if never used.
11. **Seller terms text and version.**

---

## 13. Later, not in this plan

- **Recipient claim page.** The recipient opens a link, proves their phone or email
  with a code, and accepts. Makes the recipient limit real. Uses
  `promo.entitlements` with `role = 'receive'`; no new tables.
- **Bundles and buy-X-get-Y.** The `bundle` type and `params` are reserved for it.
- **Stock reservation for paid orders.**
- **App integrity checks** (App Attest, Play Integrity) as a risk signal.
