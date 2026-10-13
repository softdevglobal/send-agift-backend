# First free gift program: implementation plan

Status: **built (phases 1 to 6), behind `FREE_GIFT_ENABLED`, off by default. Not
piloted.** Where the code differs from this plan is listed in section 13.
Backend: `SendAGift_GO` (Go, chi, pgx, Postgres). Frontend: `send-agift-frontend`.

This plan covers the free gift program only. It does not need the wider
promotions design in `free-gift-program.md` (discounts, coupons, campaigns) and
can be built before it.

---

## 1. The program in one page

| Who | What they get or do |
|---|---|
| Customer | Every registered customer has **one free gift**. They can send it to any recipient, once. |
| Customer | Must have a **verified email and a verified phone** before sending. |
| Recipient | Can receive **one free gift only**, from anyone, ever. |
| Seller | Sets a **marketing budget** and **chooses which products** can be sent as a free gift. |
| Seller | Pays for the gift: the item and its delivery come out of the budget. The customer pays 0. |
| Admin | Can stop one seller's program, or the whole program, at any time. |

| Question | Decision |
|---|---|
| How is the free gift ordered? | Its own route, `POST /customers/me/free-gifts`. The paid route `POST /customers/me/orders` is not touched. |
| Where is the order stored? | In the existing `marketplace.orders` and `order_items`, marked `order_kind = 'free_gift'`. |
| How is "one per customer" enforced? | A unique index in the database. Never a read-then-write check. |
| How is "one per recipient" enforced? | The same unique index, keyed on the recipient's email and phone. |
| How is the budget protected? | A guarded `UPDATE … WHERE held + spent + cost <= cap`. No row updated means the budget is used up. |
| New tables | **3**, plus 4 new columns and 1 new status on `marketplace.orders`. |
| New routes | **10 new**, 3 existing routes change their behaviour or response. |
| How does it stay off until ready? | Config flag `FREE_GIFT_ENABLED`, off by default. |

---

## 2. What exists today (checked in the code)

| Piece | State |
|---|---|
| Email and phone verification | `email_verified_at`, `phone_verified_at`, `phone_e164` on `customer.customers`. Phone is verified by `POST /customers/me/phone/code` and `/phone/verify`. Email is verified by the login code flow. |
| Email-change hold | `ensureCanRedeem` in `customer_email_change.go`. |
| Recipients | `customer.recipients` with `email` (citext) and `phone` (free text). **Both can be empty.** |
| Order creation | `OrderService.Create`. Always starts at `pending_payment`. Nothing moves an order to `paid`. |
| Order statuses | `draft, pending_payment, paid, accepted, preparing, dispatched, delivered, cancelled, refunded`. |
| Delivery price | `priceShopDeliveries`, from the shop's delivery zones. |
| Delivery date | `checkOrderDate` in `delivery_date.go`. |
| Seller fulfilment | `PATCH /sellers/me/order-items/{id}/accept`, then `POST …/shipping/local` and `…/shipping/local/delivered`. |
| Cancel | `CancelForCustomer` allows `draft, pending_payment, paid, accepted, preparing`. |
| Stock | `seller.inventory` has `available_qty` and `reserved_qty`, but **no order reserves stock**. |
| Seller budget, free gift products, one-time limits | **Nothing.** |

---

## 3. Rules

### 3.1 Sending

1. The customer is signed in and active, with **email and phone both verified**,
   and the email-change hold is not running.
2. **One free gift per customer, for life.** A new seller or product does not
   reset it.
3. The gift is **one unit of one product** the seller has put in the program.
4. The customer pays **0**. No gift points, no reward points.
5. **No self-gift.** The recipient's email and phone must differ from the sender's.
6. The recipient must have **both an email and a phone**. Without them the
   one-per-recipient rule cannot be checked.
7. Paid orders are never affected.

### 3.2 Receiving

1. **One free gift per recipient**, across all senders and all sellers.
2. The limit is on the recipient's contact details as the sender typed them, at
   the moment of sending. Editing the recipient afterwards changes nothing.

### 3.3 What the one-time limits are keyed on

An account id alone is not enough: a customer can open a second account with the
same phone, or change their email. So one free gift writes five rows, and a clash
on any of them refuses the gift.

| Role | Key | Stops |
|---|---|---|
| `send` | `customer:<id>` | The same account sending twice |
| `send` | `phone:<hmac of the sender's verified E.164 phone>` | A second account on the same phone |
| `send` | `email:<hmac of the sender's verified email>` | A second account on the same email |
| `receive` | `phone:<hmac of the recipient's E.164 phone>` | A second gift to the same phone |
| `receive` | `email:<hmac of the recipient's email>` | A second gift to the same email |

Emails are lower-cased and trimmed. Recipient phones are free text today, so they
are normalised to E.164 with the same function used for `phone_e164`; a phone that
cannot be normalised is refused. Keys are a keyed HMAC (secret in config), not the
raw value.

### 3.4 Seller budget

1. The seller sets one **budget cap** in one currency. It is a limit, not a
   prepaid balance: no money moves, the seller gives away their own stock and
   delivers it.
2. Each free gift costs `product price + delivery price`. That cost is **held**
   when the order is created, **spent** when it is delivered, and **released** if
   it is cancelled before dispatch.
   So the budget goes down **the moment a customer orders a free gift**:
   `remaining = cap − held − spent`.
3. When `held + spent + cost` would pass the cap, the product stops being offered.
4. The seller can also cap the number of gifts per product.
5. A product in the program must be `published`, in an active shop, in the
   budget's currency, and in stock.
6. Pausing the program or removing a product stops new gifts. Gifts already
   confirmed are still fulfilled.

---

## 4. Table changes

### 4.1 Summary

| Table | New or changed | One row is |
|---|---|---|
| `promo.free_gift_programs` | New | One seller's budget and on/off state |
| `promo.free_gift_products` | New | One product the seller offers as a free gift |
| `promo.free_gift_claims` | New | One used one-time right (a sender or a recipient) |
| `marketplace.orders` | Changed | `order_kind`, `free_gift_cost`, `free_gift_budget_status`, `idempotency_key`; new status `confirmed` |

`customer.customers`, `customer.recipients`, `seller.products` and
`seller.inventory` are not changed. A customer needs no "grant" row: they have
their free gift for as long as no `send` claim exists for them.

### 4.2 Migration `000067_first_free_gift`

```sql
CREATE SCHEMA IF NOT EXISTS promo;

-- One seller's marketing budget for free gifts.
CREATE TABLE IF NOT EXISTS promo.free_gift_programs (
    seller_id     uuid PRIMARY KEY REFERENCES seller.sellers (id) ON DELETE CASCADE,
    status        text NOT NULL DEFAULT 'paused'
                  CHECK (status IN ('active', 'paused', 'stopped')),  -- stopped = by an admin
    currency      text NOT NULL,
    budget_cap    bigint NOT NULL CHECK (budget_cap > 0),             -- minor units
    budget_held   bigint NOT NULL DEFAULT 0 CHECK (budget_held >= 0),
    budget_spent  bigint NOT NULL DEFAULT 0 CHECK (budget_spent >= 0),
    terms_version text,
    accepted_at   timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT free_gift_budget_within_cap CHECK (budget_held + budget_spent <= budget_cap)
);

-- The products a seller offers as a free gift.
CREATE TABLE IF NOT EXISTS promo.free_gift_products (
    product_id  uuid PRIMARY KEY REFERENCES seller.products (id) ON DELETE CASCADE,
    seller_id   uuid NOT NULL REFERENCES promo.free_gift_programs (seller_id) ON DELETE CASCADE,
    status      text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused')),
    gifts_cap   integer CHECK (gifts_cap > 0),                        -- null = budget only
    gifts_count integer NOT NULL DEFAULT 0 CHECK (gifts_count >= 0),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT free_gift_products_within_cap CHECK (gifts_cap IS NULL OR gifts_count <= gifts_cap)
);

CREATE INDEX IF NOT EXISTS free_gift_products_seller_idx
    ON promo.free_gift_products (seller_id) WHERE status = 'active';

-- One-time rights. The unique index is the rule.
CREATE TABLE IF NOT EXISTS promo.free_gift_claims (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    role         text NOT NULL CHECK (role IN ('send', 'receive')),
    identity_key text NOT NULL,          -- customer:<id> | phone:<hmac> | email:<hmac>
    key_version  integer NOT NULL DEFAULT 1,
    customer_id  uuid REFERENCES customer.customers (id) ON DELETE SET NULL,
    order_id     uuid REFERENCES marketplace.orders (id),
    state        text NOT NULL DEFAULT 'used' CHECK (state IN ('used', 'released')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    released_at  timestamptz
);

-- Released rows stop counting, so a cancellation before dispatch gives the
-- right back.
CREATE UNIQUE INDEX IF NOT EXISTS free_gift_claims_one_uq
    ON promo.free_gift_claims (role, identity_key) WHERE state = 'used';

CREATE INDEX IF NOT EXISTS free_gift_claims_order_idx
    ON promo.free_gift_claims (order_id);

-- Orders: the kind, what the seller funded, and safe retries.
ALTER TABLE marketplace.orders
    ADD COLUMN IF NOT EXISTS order_kind text NOT NULL DEFAULT 'paid'
        CHECK (order_kind IN ('paid', 'free_gift')),
    ADD COLUMN IF NOT EXISTS free_gift_cost integer NOT NULL DEFAULT 0
        CHECK (free_gift_cost >= 0),
    ADD COLUMN IF NOT EXISTS free_gift_budget_status text NOT NULL DEFAULT 'none'
        CHECK (free_gift_budget_status IN ('none', 'held', 'spent', 'released')),
    ADD COLUMN IF NOT EXISTS idempotency_key text;

ALTER TABLE marketplace.orders
    DROP CONSTRAINT IF EXISTS orders_free_gift_kind,
    ADD CONSTRAINT orders_free_gift_kind
        CHECK ((order_kind = 'free_gift') = (free_gift_budget_status <> 'none'));

CREATE UNIQUE INDEX IF NOT EXISTS orders_customer_idempotency_uq
    ON marketplace.orders (customer_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- A free gift has nothing to pay, so it never sits in pending_payment.
ALTER TABLE marketplace.orders DROP CONSTRAINT IF EXISTS orders_status_check;
ALTER TABLE marketplace.orders ADD CONSTRAINT orders_status_check
    CHECK (status IN ('draft', 'pending_payment', 'paid', 'confirmed', 'accepted',
                      'preparing', 'dispatched', 'delivered', 'cancelled', 'refunded'));
```

`orders_status_check` is the name Postgres gives the unnamed CHECK from migration
`000012`. Confirm it with `\d marketplace.orders` before running.

### 4.3 How a free gift order is stored

| Column | Paid order (today) | Free gift |
|---|---|---|
| `order_kind` | `paid` | `free_gift` |
| `status` at creation | `pending_payment` | `confirmed` |
| `subtotal_amount` | sum of item prices | price of the one unit |
| `delivery_amount` | zone delivery price | zone delivery price |
| `total_amount` | subtotal + delivery | `0` |
| `free_gift_cost` | `0` | subtotal + delivery |
| `free_gift_budget_status` | `none` | `held`, then `spent` or `released` |
| `gift_points`, reward points | as today | `0` |
| item `unit_amount` | product price | product price (the seller sees the value) |

Rule for every report from now on: **revenue is `total_amount`**, never
`subtotal_amount`.

---

## 5. Routes

All under `/api/v1`. **10 new, 3 changed.**

### 5.1 Public (1 new)

| Method and path | Purpose |
|---|---|
| `GET /free-gifts/products?country=` | Products that can be sent as a free gift right now. For the marketing page, no sign-in |

### 5.2 Customer (2 new)

| Method and path | Purpose |
|---|---|
| `GET /customers/me/free-gift?recipient_id=` | Do I still have my free gift, what is missing (email or phone), and can this recipient receive one |
| `POST /customers/me/free-gifts` | **Send the free gift. Creates the order.** `Idempotency-Key` header required |

### 5.3 Seller (5 new)

| Method and path | Purpose |
|---|---|
| `GET /sellers/me/free-gift-program` | Budget cap, held, spent, remaining, status |
| `PUT /sellers/me/free-gift-program` | Set the budget and currency, accept the terms, turn on or pause |
| `GET /sellers/me/free-gift-products` | Products in the program, with gifts sent and the cap |
| `PUT /sellers/me/free-gift-products/{productID}` | Add a product, or change its cap or pause it |
| `DELETE /sellers/me/free-gift-products/{productID}` | Take a product out of the program |

### 5.4 Admin (2 new)

| Method and path | Purpose |
|---|---|
| `GET /admin/free-gifts` | Every seller's program: budget, held, spent, gifts sent |
| `PATCH /admin/free-gifts/sellers/{sellerID}` | Stop or re-allow one seller's program (the kill switch) |

### 5.5 Existing routes that change

| Method and path | Change |
|---|---|
| `GET /customers/me/orders`, `GET /customers/me/orders/{id}` | Return `order_kind` and `free_gift_cost` |
| `POST /customers/me/orders/{id}/cancel` | Accepts `confirmed`. For a free gift also gives back budget, stock and the one-time rights |
| `GET /sellers/me/order-items`, `GET /sellers/me/order-items/{id}` | Return `order_kind`, so the seller sees "free gift, you fund this" |

Existing routes used as they are: `POST /customers/me/phone/code`,
`POST /customers/me/phone/verify`, the login code routes (email verification),
the recipient routes, `PATCH /sellers/me/order-items/{id}/accept`, and the two
`…/shipping/local` routes.

### 5.6 Requests and responses

`GET /customers/me/free-gift`:

```json
{
  "available": true,
  "used": false,
  "missing": [],
  "recipient": { "id": "…", "can_receive": true, "reason": null }
}
```

`missing` holds `"email"`, `"phone"` or both when they are not verified. `reason`
is `SELF_GIFT`, `RECIPIENT_CONTACT_REQUIRED` or `RECIPIENT_ALREADY_USED`.

`PUT /sellers/me/free-gift-program`:

```json
{ "budget_cap": 250000, "currency": "AUD", "status": "active", "accept_terms": true }
```

`PUT /sellers/me/free-gift-products/{productID}`:

```json
{ "gifts_cap": 50, "status": "active" }
```

`POST /customers/me/free-gifts`:

```json
{
  "product_id": "…", "recipient_id": "…", "country_id": "…",
  "delivery_date": "2026-12-20", "gift_message": "Happy birthday"
}
```

It returns the same order details as `POST /customers/me/orders`. The body has no
price and no quantity: the server reads the price and the quantity is always 1.

### 5.7 Error codes

Every refusal returns `{"error": "<message>", "code": "<CODE>"}`.

| Code | HTTP | Meaning |
|---|---|---|
| `FREE_GIFT_DISABLED` | 404 | The flag is off |
| `VERIFY_EMAIL_REQUIRED` | 403 | Email is not verified |
| `VERIFY_PHONE_REQUIRED` | 403 | Phone is not verified |
| `REDEMPTION_LOCKED` | 423 | Existing. The email was changed in the last few days |
| `ALREADY_USED` | 409 | This customer has sent their free gift |
| `RECIPIENT_ALREADY_USED` | 409 | This recipient already got one. No other details are given |
| `RECIPIENT_CONTACT_REQUIRED` | 422 | The recipient has no email or no usable phone |
| `SELF_GIFT` | 422 | The recipient is the sender |
| `FREE_GIFT_UNAVAILABLE` | 409 | Product not in the program, program paused, or budget or gift cap used up |
| `OUT_OF_STOCK` | 409 | No stock left |
| `DELIVERY_DATE_UNAVAILABLE` | 422 | Existing, from `checkOrderDate` |
| `IDEMPOTENCY_MISMATCH` | 422 | Same key, different request |

---

## 6. The order things happen in

### 6.1 Seller sets up (once)

```
1. PUT  /sellers/me/free-gift-program                 set budget, accept terms, status active
2. PUT  /sellers/me/free-gift-products/{productID}    choose each product, optional cap
3. GET  /sellers/me/free-gift-program                 watch held, spent, remaining
```

Step 2 is refused until step 1 has been done.

### 6.2 Customer sends the free gift

```
1. POST /customers/register                           account exists; the free gift exists with it
2. POST /customers/login/code  → /verify              email verified
3. POST /customers/me/phone/code → /verify            phone verified
4. GET  /free-gifts/products                          pick a product
5. POST /customers/me/recipients                      add the recipient (email and phone)
6. GET  /customers/me/free-gift?recipient_id=         check: available, nothing missing, recipient ok
7. POST /customers/me/free-gifts                      order created, status confirmed, total 0
8. GET  /customers/me/orders/{id}                     follow the order
```

Steps 2 to 5 can happen in any order. Step 7 checks everything again on the
server, so skipping step 6 is safe.

### 6.3 Seller fulfils (existing routes, unchanged)

```
1. GET   /sellers/me/order-items                                       sees the free gift
2. PATCH /sellers/me/order-items/{id}/accept
3. POST  /sellers/me/orders/{orderID}/shops/{shopID}/shipping/local
4. POST  /sellers/me/orders/{orderID}/shops/{shopID}/shipping/local/delivered
```

At step 4 the budget moves from held to spent, and the reserved unit leaves stock.

### 6.4 Order status

```
paid order:   pending_payment → paid → accepted → preparing → dispatched → delivered
free gift:                 confirmed → accepted → preparing → dispatched → delivered
                               │           │           │
                               └───────────┴───────────┴──→ cancelled (everything given back)
```

### 6.5 Inside `POST /customers/me/free-gifts`

```
1.  flag on?  same Idempotency-Key already has an order → return that order
2.  customer active, email verified, phone verified, ensureCanRedeem
3.  recipient belongs to the customer, has email and phone, is not the sender
4.  product is in the program and published; shop active; program active
5.  delivery price from zones; checkOrderDate            (shared with the paid route)
6.  BEGIN
7.    claims: insert 3 send rows       → unique clash = ALREADY_USED
8.    claims: insert 2 receive rows    → unique clash = RECIPIENT_ALREADY_USED
9.    product: gifts_count + 1         → 0 rows = FREE_GIFT_UNAVAILABLE
10.   program: budget_held + cost      → 0 rows = FREE_GIFT_UNAVAILABLE
11.   inventory: reserved_qty + 1      → 0 rows = OUT_OF_STOCK
12.   insert order (free_gift, confirmed, total 0, budget status held)
13.   insert order_item and order_shop_delivery
14. COMMIT
15. queue the confirmation email
```

Steps 9 to 11 are guarded updates:

```sql
UPDATE promo.free_gift_products
SET gifts_count = gifts_count + 1, updated_at = now()
WHERE product_id = $1 AND status = 'active'
  AND (gifts_cap IS NULL OR gifts_count < gifts_cap);

UPDATE promo.free_gift_programs
SET budget_held = budget_held + $2, updated_at = now()
WHERE seller_id = $1 AND status = 'active'
  AND budget_held + budget_spent + $2 <= budget_cap;

UPDATE seller.inventory
SET reserved_qty = reserved_qty + 1, updated_at = now()
WHERE product_id = $1 AND available_qty - reserved_qty >= 1;
```

### 6.6 After the order

| Event | Budget | Stock | One-time rights |
|---|---|---|---|
| Delivered | held → spent | `available_qty − 1`, `reserved_qty − 1` | kept |
| Cancelled before dispatch | held released | `reserved_qty − 1` | released: sender and recipient can use them again |
| Dispatched or later | cannot be cancelled (as today) | | |

Each of these runs once: it only acts while `free_gift_budget_status = 'held'`.

---

## 7. Backend files

### 7.1 New

| File | Holds |
|---|---|
| `internal/database/migrations/000067_first_free_gift.up.sql` / `.down.sql` | Section 4.2 |
| `internal/models/free_gift.go` | `FreeGiftProgram`, `FreeGiftProduct`, `FreeGiftStatus` |
| `internal/repository/free_gift_repository.go` | `GetProgram`, `UpsertProgram`, `SetProgramStatus`, `ListProducts`, `UpsertProduct`, `RemoveProduct`, `ListAvailable`, `UseClaims`, `ReleaseClaims`, `ClaimUsed`, `TakeGift`, `HoldBudget`, `SpendBudget`, `ReleaseBudget`, `ReserveStock` |
| `internal/services/free_gift_identity.go` | `sendKeys(customer)`, `receiveKeys(recipient)`, `hmacKey(kind, value)` |
| `internal/services/free_gift_service.go` | `Status`, `Send`, `OnCancelled`, `OnDelivered`, and the seller and admin program functions |
| `internal/handlers/free_gift_handler.go` | Handlers and error mapping |
| `internal/routes/free_gift_routes.go` | Section 5 |
| `free_gift_service_test.go`, `free_gift_integration_test.go` | Section 9 |

### 7.2 Changed

| File | Change |
|---|---|
| `internal/services/order_service.go` | Move the recipient check, `priceShopDeliveries` and `checkOrderDate` steps of `Create` into helpers that `FreeGiftService.Send` also calls. One copy, not two |
| `internal/repository/order_repository.go` | `Create` writes the new columns. `CancelForCustomer` accepts `confirmed` and calls the free gift give-back. Selects return `order_kind` and `free_gift_cost` |
| Delivered path (`shipping_service.go`, `CompleteLocalDelivery`) | For a free gift, move the budget from held to spent and take the unit out of stock |
| `internal/models/order.go` | `OrderKind`, `FreeGiftCost` |
| `internal/config/config.go` | `FREE_GIFT_ENABLED`, `FREE_GIFT_IDENTITY_SECRET` |
| `cmd/api/main.go`, `internal/routes/router.go` | Wiring |
| Every check of an order's status | **Search first.** Decide for each whether `confirmed` counts the same as `paid` |

---

## 8. Frontend changes (`send-agift-frontend`)

| Screen | Change |
|---|---|
| Home page and account page | "You have 1 free gift" banner, from `GET /customers/me/free-gift` |
| Verify prompt (new) | Says which of email or phone is not verified and links to the profile page |
| `/free-gift` page (new) | Pick a product, a recipient and a date, then confirm |
| Order list and order detail | "Free gift" badge, total 0 |
| Seller: `/seller/free-gifts` page (new) | Set the budget, choose products and caps, pause, see held, spent and remaining |
| Seller order screens | "Free gift (you fund this)" marker |
| Admin: free gift page (new) | Every seller's program, and the stop switch |

The client never decides eligibility. It shows what the API returns. It creates
one `Idempotency-Key` (a UUID) per confirm screen and reuses it on retry.

---

## 9. Edge cases and tests

| Case | What must happen |
|---|---|
| Email or phone not verified | Refused with `VERIFY_EMAIL_REQUIRED` or `VERIFY_PHONE_REQUIRED` |
| First send | Order with total 0, status `confirmed`, 3 send rows, 2 receive rows, budget held |
| Double click | The same idempotency key returns the same order |
| Two tabs, 20 parallel requests | Exactly one order; the rest get `ALREADY_USED` |
| Second account on the same phone or email | `ALREADY_USED` |
| Customer changes email, then tries again | `ALREADY_USED` (the customer id and phone keys still clash) |
| Two customers send to the same recipient at once | Exactly one order; the other gets `RECIPIENT_ALREADY_USED` |
| Same recipient saved twice under different names | Still refused: the key is the email and phone, not the recipient row |
| Recipient has no email or phone | `RECIPIENT_CONTACT_REQUIRED` |
| Budget has room for one more, 10 customers at once | Exactly one order; held plus spent never passes the cap |
| Last unit of stock, 10 customers at once | Exactly one order |
| Seller pauses, or an admin stops the program | New gifts refused; confirmed gifts are still fulfilled |
| Seller lowers the budget below held plus spent | Refused by the CHECK; the API returns a clear message |
| Cancel before dispatch | Budget, stock and all 5 rights given back, once |
| Account deleted, then registered again | Phone and email keys stay, so the free gift is not reset |
| Paid order | Unchanged: same totals, status and rows as today |
| Flag off | Every free gift route returns `FREE_GIFT_DISABLED` |

The parallel tests need the CI Postgres. They cannot be trusted from unit tests.

---

## 10. Phases

| Phase | Work | Done when |
|---|---|---|
| 0 | Decisions in section 11. Search every order status check | Written answers |
| 1 | Migration, models, config flag | Migrates up and down cleanly; paid orders unchanged |
| 2 | Seller program and product routes | A seller can set a budget and choose products |
| 3 | Shared order helpers, identity keys, `Send`, the status route | A verified customer can send one, and only one |
| 4 | Cancel and delivered hooks | Budget, stock and rights add up after each |
| 5 | Customer and seller screens | Works at phone width |
| 6 | Admin list and stop switch | An admin can stop one seller |
| 7 | Pilot: one seller, small budget, flag on in staging only | Sign-off recorded |

---

## 11. Decisions needed before building

1. **Existing customers.** Do customers who registered before launch get the free
   gift too? Recommended: yes, the rule is simply "has not sent one yet".
2. **Recipient contact.** Recommended: the recipient must have both an email and
   a phone. This does not prove the recipient owns them, but it stops many
   accounts sending free gifts to one person.
3. **Cancellation.** Recommended: cancelling before dispatch gives the free gift
   back to the sender and the recipient; after dispatch it cannot be cancelled.
4. **Delivery cost.** Recommended: the seller pays it, inside the budget.
5. **Budget.** A limit the seller sets (recommended), or money paid to the
   platform up front?
6. **Platform fee.** Does the platform charge the seller anything on a free gift?
7. **Seller refusal.** Sellers cannot reject an order today. If a seller cannot
   fulfil a free gift, who cancels it, and are the rights given back?
8. **Approval.** Does a seller's program go live at once, or after an admin
   approves it?
9. **Countries, and expiry.** Which countries, and does an unused free gift ever
   expire?
10. **Seller terms text and version.**

---

## 12. Later, not in this plan

- **Recipient claim page.** The recipient opens a link and proves their phone or
  email with a code. Makes the recipient limit real.
- **Stock reservation for paid orders.**
- **Email aliases.** Treating `name+tag@gmail.com` and `name@gmail.com` as one.
- **Moving this into the wider promotions design** (`free-gift-program.md`), where
  the free gift becomes one offer type.

---

## 13. As built: where the code differs from this plan

**Section 11 decisions the build assumed.** Change any of these before the pilot.

| # | Decision | What the code does |
|---|---|---|
| 1 | Existing customers | They have the free gift too: the rule is "no `send` claim yet" |
| 2 | Recipient contact | Email and a readable phone are both required |
| 3 | Cancellation | Before dispatch gives everything back; after dispatch it is refused |
| 4 | Delivery cost | The seller pays it, inside the budget |
| 5 | Budget | A limit the seller sets. No money moves |
| 6 | Platform fee | None is charged or recorded |
| 7 | Seller refusal | **Not built.** A seller still cannot reject an order |
| 8 | Approval | A program is live as soon as the seller turns it on. The admin stop switch is the only control |
| 9 | Countries, expiry | No country limit and no expiry |
| 10 | Terms | Version from `FREE_GIFT_TERMS_VERSION` (default `v1`). The wording on the seller page is a placeholder |

**Differences in behaviour.**

| Plan | As built |
|---|---|
| 5.4 `PATCH /admin/free-gifts/sellers/{sellerID}` | Body is `{"stopped": true \| false}`. Re-allowing sets the program to `paused`; the seller turns it on again |
| 5.3 `GET /sellers/me/free-gift-program` before setup | Returns `status: "not_set"` with zero amounts, not a 404 |
| 5.6 status response | Also has `locked` (the email-change hold) |
| 5.7 error codes | Adds `IDEMPOTENCY_KEY_REQUIRED` (400), `OUTSIDE_DELIVERY_ZONE` (422), and for sellers `PROGRAM_REQUIRED`, `TERMS_REQUIRED`, `PROGRAM_STOPPED`, `BUDGET_BELOW_COMMITTED`, `CURRENCY_LOCKED`, `PRODUCT_NOT_ELIGIBLE`, `CAP_BELOW_COUNT` |
| 6.4 cancel before dispatch | The order header stays `confirmed` while its line moves, so a free gift is refused cancellation once its line is `dispatched`. Paid orders keep today's rule |
| 6.5 steps 7, 8 and 12 | Claims are inserted first with no `order_id`, then linked to the order in the same transaction |
| 6.5 double click | A second request with the same key that loses the race gets the first order back, not `ALREADY_USED` |
| 3.4 rule 3, public list | A product is listed while the budget covers its price plus the shop's cheapest delivery zone. The exact cost needs the recipient's address, so the send can still answer `FREE_GIFT_UNAVAILABLE` |
| 3.4 rule 5, stock | A product with no inventory row counts as out of stock for free gifts |
| 3.4 currency | Fixed once the program has products or gifts |
| 4.2 status CHECK | Dropped by what it checks, not by the name `orders_status_check` |
| 7.2 `order_service.go` | Not changed. `FreeGiftService` calls `priceShopDeliveries`, `checkOrderDate` and `shopDeliveriesFromZones` directly |
| 9 out of stock | A loser of the last-unit race is told `OUT_OF_STOCK`, or `DELIVERY_DATE_UNAVAILABLE` when the shared date check sees the empty stock first |

**Not built.** Phase 7 (pilot), an audit log entry for the admin stop switch, and
a free gift version of the order confirmation email (it reuses the paid one, so
it shows the subtotal, the delivery and a total of 0).

**Run the tests.**

```
TEST_DATABASE_URL=postgres://user:pass@localhost:5432/scratch_db?sslmode=disable \
    go test ./internal/services -run FreeGift -v
```

The scratch database needs one row in `core.countries`.
