# SendAGift API Reference

How every route works, how the layers connect, and what each request and response body
looks like. This is the "how it fits together" document — `README.md` stays the
step-by-step Postman walkthrough, and `DATABASE_SCHEMA.md` covers tables and keys.

- [1. Stack and entry point](#1-stack-and-entry-point)
- [2. How a request flows](#2-how-a-request-flows)
- [3. Conventions that apply to every endpoint](#3-conventions-that-apply-to-every-endpoint)
- [4. Complete route map](#4-complete-route-map)
- [5. Endpoints in detail](#5-endpoints-in-detail)
  - [5.1 Health](#51-health)
  - [5.2 Auth and bootstrap](#52-auth-and-bootstrap)
  - [5.3 Admin profile](#53-admin-profile)
  - [5.4 Countries and country capabilities](#54-countries-and-country-capabilities)
  - [5.5 Customers](#55-customers)
  - [5.6 Recipients](#56-recipients)
  - [5.7 Saved gifts](#57-saved-gifts)
  - [5.8 Customer orders](#58-customer-orders)
  - [5.9 Sellers, addresses, shops](#59-sellers-addresses-shops)
  - [5.10 Products and inventory](#510-products-and-inventory)
  - [5.11 Seller order items](#511-seller-order-items)
  - [5.12 Shipping (Shippo)](#512-shipping-shippo)
  - [5.13 Media (S3 presign)](#513-media-s3-presign)
  - [5.14 Reels](#514-reels)
  - [5.15 Public storefront browsing](#515-public-storefront-browsing)
  - [5.16 Places (Google proxy)](#516-places-google-proxy)
- [6. Where each request/response struct lives](#6-where-each-requestresponse-struct-lives)
- [7. Cross-cutting flows](#7-cross-cutting-flows)
- [8. Error catalogue](#8-error-catalogue)
- [9. Known gaps and sharp edges](#9-known-gaps-and-sharp-edges)

---

## 1. Stack and entry point

| Piece | Choice |
| --- | --- |
| Language | Go (module name `myapp`) |
| Router | `github.com/go-chi/chi/v5` |
| Database | PostgreSQL via `pgx/v5` connection pool |
| Auth | HS256 JWT (`github.com/golang-jwt/jwt/v5`) |
| Files | AWS S3 (presigned PUT/GET, public `public/` prefix) |
| Shipping | Shippo REST API |
| Addresses | Google Places (proxied so the key never reaches the browser) |
| Config | `.env` via `godotenv`, read once in `internal/config` |

`cmd/api/main.go` is the only entry point. On boot it:

1. loads config (`config.Load`) and fails fast if `JWT_SECRET`, `DB_USER`, `DB_NAME`,
   `S3_BUCKET`, the AWS keys, or `GOOGLE_MAPS_API_KEY` are missing;
2. opens the pgx pool;
3. runs `database.MigrateUp` — embedded `*.up.sql` files applied in filename order and
   recorded in `schema_migrations`, so starting the API migrates the database;
4. constructs repositories → services → handlers → router, in that order;
5. serves on `:$APP_PORT` (default `8080`).

Base URL for everything except `/health` is `http://localhost:$APP_PORT/api/v1`.

---

## 2. How a request flows

```mermaid
flowchart LR
    C[Client] --> R[chi router<br/>internal/routes]
    R --> M[Global middleware<br/>CORS, RequestID, RealIP, Logger, Recoverer]
    M --> G{Route group}
    G -->|public| H[Handler]
    G -->|protected| A[RequireAuth + RequireRole]
    A --> H
    H --> S[Service<br/>validation + business rules]
    S --> P[Repository<br/>SQL only]
    P --> DB[(PostgreSQL)]
    S --> X[S3 / Shippo / Google Places]
```

Every layer has exactly one job:

| Layer | Package | Responsibility | Never does |
| --- | --- | --- | --- |
| Routes | `internal/routes` | URL → handler, attaches middleware | validation, SQL |
| Middleware | `internal/middleware` | JWT parsing, role gate, per-IP rate limit | business rules |
| Handler | `internal/handlers` | decode JSON, read path/query params, map service errors to HTTP status codes | SQL, business rules |
| Service | `internal/services` | validation, ownership checks, status transitions, external APIs | HTTP concerns |
| Repository | `internal/repository` | SQL and transactions, returns sentinel errors like `ErrShopNotFound` | validation |
| Models | `internal/models` | row structs and response DTOs with JSON tags | logic |

Wiring is explicit in `main.go` — no DI container. A handler only ever receives the one
service it needs:

| Handler | Service(s) | Repositories reached |
| --- | --- | --- |
| `AuthHandler` | `AuthService` | admins, customers, sellers |
| `AdminHandler` | `AdminService` | admins |
| `CountryHandler`, `CountryCapabilityHandler` | `CountryService`, `CountryCapabilityService` | countries, country_capabilities |
| `CustomerHandler` | `CustomerService` | customers, countries, products |
| `OrderHandler`, `SellerOrderHandler` | `OrderService` | orders, customers, countries |
| `ShopsHandler` | `MarketplaceService` | sellers, products |
| `SellerHandler` | `SellerService` | sellers, countries |
| `ProductHandler` | `ProductService` | products, sellers |
| `ReelHandler` | `ReelService` | reels, sellers, S3 |
| `MediaHandler` | `S3Service` | — (S3 only) |
| `ShippingHandler` | `ShippingService` | shipments, idempotency_keys, media_assets, S3, Shippo |
| `PlacesHandler` | `PlacesService` | — (Google only) |

Route groups are registered in `internal/routes/router.go` in this order, all under
`/api/v1`: marketplace → admin bootstrap → auth → admin protected → countries →
customers → sellers → reels → media → places → shipping.

---

## 3. Conventions that apply to every endpoint

**Content type.** Requests and responses are `application/json`. Every response is
written by `utils.JSON`, which sets the header and status then encodes the body.

**Authentication.** Send the JWT from a login call:

```
Authorization: Bearer eyJhbGciOiJIUzI1NiIs...
```

`RequireAuth` rejects a missing prefix or bad token with `401`
`{"error":"missing bearer token"}` / `{"error":"invalid or expired token"}`. On success it
puts the token subject on the request context under both `admin_id` and `user_id`, plus
the `role` claim. Handlers read the caller's own id from that context — that's why the
`/customers/me`, `/sellers/me`, `/admin/me` routes never take an id in the URL.

**Roles.** The JWT `role` claim is one of `superadmin`, `admin`, `customer`, `seller`.
`RequireRole("seller")` returns `403 {"error":"insufficient role"}` for anyone else;
`superadmin` is accepted anywhere `admin` is allowed.

**Token contents.** `{sub, email, role, iat, exp}`, HS256, signed with `JWT_SECRET`, TTL
from `JWT_EXPIRY_MINUTES` (default 1440). There is no refresh endpoint and no
server-side revocation — a token stays valid until it expires.

**Errors.** Always the same envelope, one string field:

```json
{ "error": "shop not found" }
```

| Status | When |
| --- | --- |
| `400` | malformed JSON, failed validation, bad query param |
| `401` | missing/expired token, or an admin id that no longer exists |
| `403` | wrong role, disabled country capability, bootstrap already done |
| `404` | not found, or found but not owned by the caller |
| `409` | uniqueness conflict, or an illegal status transition |
| `405` | right path, wrong method (chi) |
| `502` / `503` | upstream provider failed / not configured |
| `500` | unexpected; the real error is logged, not returned |

**Ownership is enforced as 404, not 403.** Asking for another seller's product returns
`product not found` so the API never confirms that a foreign id exists.

**Identifiers.** All ids are UUIDs. Path params are named per route file
(`{id}`, `{shopId}`, `{shopID}`, `{productId}`, `{productID}`, `{orderItemID}`,
`{addressId}`) — the casing differs between public and seller routes, which matters only
if you read the handler source.

**Money.** Integer minor units (cents), never floats. `price_amount: 4500` with
`currency: "USD"` is $45.00. `total_amount = subtotal_amount + delivery_amount`.

**Dates and times.** Timestamps are RFC 3339 UTC (`2026-09-08T10:15:00Z`). Calendar
dates in request bodies are `YYYY-MM-DD` (`delivery_date`, `date_of_birth`,
`unavailable_dates`).

**Nulls.** Optional response fields use `omitempty`, so they are absent rather than
`null`. `password_hash` is tagged `json:"-"` and never leaves the process.

**PUT is a full replace.** `PUT /sellers/me/products/{id}`, `PUT /customers/me/recipients/{id}`
and friends overwrite with what you send; omitted fields become empty or default. The one
exception is a reel's `media[]`, which is left alone when the key is absent (see
[5.14](#514-reels)).

**Deletes are not all equal.**

| Endpoint | Effect |
| --- | --- |
| `DELETE /customers/me` | soft: `deleted_at = now()`, `status = 'deleted'` |
| `DELETE /sellers/me` | soft: `status = 'deleted'` |
| `DELETE /sellers/me/shops/{id}` | hard row delete (cascades to products, reels) |
| `DELETE /sellers/me/products/{id}` | hard row delete (cascades to inventory) |
| `DELETE /sellers/me/reels/{id}` | hard delete of the reel, its media rows, and the S3 objects |
| everything else | hard row delete |

**No pagination anywhere except the reel feed.** List endpoints return the full array.
`GET /reels` uses an opaque keyset cursor.

---

## 4. Complete route map

Auth column: `—` public, `JWT` any valid token, `role` a required role claim.

### Public

| Method | Path | Auth | Handler |
| --- | --- | --- | --- |
| GET | `/health` | — | inline in `router.go` |
| POST | `/admin/register` | `X-Bootstrap-Secret` header | `AuthHandler.Bootstrap` |
| POST | `/auth/login` | — | `AuthHandler.Login` |
| POST | `/customers/login` | — | `AuthHandler.Login` |
| POST | `/sellers/login` | — | `AuthHandler.Login` |
| POST | `/customers/register` | — | `CustomerHandler.Register` |
| POST | `/sellers/register` | — | `SellerHandler.Register` |
| GET | `/countries` | — | `CountryHandler.List` |
| GET | `/countries/{id}` | — | `CountryHandler.GetByID` |
| GET | `/shops` | — | `ShopsHandler.ListActiveShops` |
| GET | `/shops/{shopId}` | — | `ShopsHandler.GetShop` |
| GET | `/shops/{shopId}/products` | — | `ShopsHandler.ListShopProducts` |
| GET | `/products/{productId}` | — | `ShopsHandler.GetProduct` |
| GET | `/reels` | — | `ReelHandler.Feed` |
| GET | `/reels/{id}` | — | `ReelHandler.GetPublic` |
| GET | `/shops/{shopId}/reels` | — | `ReelHandler.FeedByShop` |
| GET | `/products/{productId}/reels` | — | `ReelHandler.FeedByProduct` |
| GET | `/places/autocomplete` | — (120 req/min per IP) | `PlacesHandler.Autocomplete` |
| GET | `/places/details` | — (120 req/min per IP) | `PlacesHandler.Details` |
| POST | `/webhooks/shippo/tracking` | — | `ShippingHandler.ShippoWebhook` |

### Any authenticated caller

| Method | Path | Handler |
| --- | --- | --- |
| GET | `/admin/me` | `AdminHandler.Me` |
| PUT | `/admin/me` | `AdminHandler.UpdateMe` |
| POST | `/media/presign-upload` | `MediaHandler.PresignUpload` |
| GET | `/media/url?key=` | `MediaHandler.GetURL` |

### role = admin (or superadmin)

| Method | Path | Handler |
| --- | --- | --- |
| POST | `/admin/countries` | `CountryHandler.Create` |
| PUT | `/admin/countries/{id}` | `CountryHandler.Update` |
| DELETE | `/admin/countries/{id}` | `CountryHandler.Delete` |
| GET | `/admin/country-capabilities` | `CountryCapabilityHandler.List` |
| GET | `/admin/countries/{id}/capabilities` | `CountryCapabilityHandler.GetByCountryID` |
| POST | `/admin/countries/{id}/capabilities` | `CountryCapabilityHandler.Create` |
| PUT | `/admin/countries/{id}/capabilities` | `CountryCapabilityHandler.Update` |
| DELETE | `/admin/countries/{id}/capabilities` | `CountryCapabilityHandler.Delete` |

### role = customer

| Method | Path | Handler |
| --- | --- | --- |
| GET | `/customers/me` | `CustomerHandler.Me` |
| PUT | `/customers/me` | `CustomerHandler.UpdateMe` |
| DELETE | `/customers/me` | `CustomerHandler.DeleteMe` |
| POST | `/customers/me/addresses` | `CustomerHandler.AddAddress` |
| DELETE | `/customers/me/addresses/{id}` | `CustomerHandler.DeleteAddress` |
| GET | `/customers/me/saved-gifts` | `CustomerHandler.ListSavedGifts` |
| POST | `/customers/me/saved-gifts` | `CustomerHandler.AddSavedGift` |
| DELETE | `/customers/me/saved-gifts/{id}` | `CustomerHandler.DeleteSavedGift` |
| POST | `/customers/me/recipients` | `CustomerHandler.CreateRecipient` |
| GET | `/customers/me/recipients` | `CustomerHandler.ListRecipients` |
| GET | `/customers/me/recipients/{id}` | `CustomerHandler.GetRecipient` |
| PUT | `/customers/me/recipients/{id}` | `CustomerHandler.UpdateRecipient` |
| DELETE | `/customers/me/recipients/{id}` | `CustomerHandler.DeleteRecipient` |
| POST | `/customers/me/recipients/{id}/addresses` | `CustomerHandler.AddRecipientAddress` |
| PUT | `/customers/me/recipients/{id}/addresses/{addressId}` | `CustomerHandler.UpdateRecipientAddress` |
| DELETE | `/customers/me/recipients/{id}/addresses/{addressId}` | `CustomerHandler.DeleteRecipientAddress` |
| POST | `/customers/me/orders` | `OrderHandler.Create` |
| GET | `/customers/me/orders` | `OrderHandler.List` |
| GET | `/customers/me/orders/{id}` | `OrderHandler.Get` |
| POST | `/customers/me/orders/{id}/cancel` | `OrderHandler.Cancel` |

### role = seller

| Method | Path | Handler |
| --- | --- | --- |
| GET | `/sellers/me` | `SellerHandler.Me` |
| PUT | `/sellers/me` | `SellerHandler.UpdateMe` |
| DELETE | `/sellers/me` | `SellerHandler.DeleteMe` |
| POST | `/sellers/me/addresses` | `SellerHandler.AddAddress` |
| PUT | `/sellers/me/addresses/{id}` | `SellerHandler.UpdateAddress` |
| DELETE | `/sellers/me/addresses/{id}` | `SellerHandler.DeleteAddress` |
| GET | `/sellers/me/shops` | `SellerHandler.ListShops` |
| POST | `/sellers/me/shops` | `SellerHandler.CreateShop` |
| PUT | `/sellers/me/shops/{id}` | `SellerHandler.UpdateShop` |
| DELETE | `/sellers/me/shops/{id}` | `SellerHandler.DeleteShop` |
| GET | `/sellers/me/shops/{shopID}/products` | `ProductHandler.ListByShop` |
| POST | `/sellers/me/shops/{shopID}/products` | `ProductHandler.Create` |
| GET | `/sellers/me/products/{id}` | `ProductHandler.Get` |
| PUT | `/sellers/me/products/{id}` | `ProductHandler.Update` |
| DELETE | `/sellers/me/products/{id}` | `ProductHandler.Delete` |
| GET | `/sellers/me/products/{id}/inventory` | `ProductHandler.GetInventory` |
| PUT | `/sellers/me/products/{id}/inventory` | `ProductHandler.UpdateInventory` |
| GET | `/sellers/me/order-items` | `SellerOrderHandler.ListItems` |
| GET | `/sellers/me/order-items/{id}` | `SellerOrderHandler.GetItem` |
| PATCH | `/sellers/me/order-items/{id}/accept` | `SellerOrderHandler.AcceptItem` |
| POST | `/sellers/me/order-items/{orderItemID}/shipping/rates` | `ShippingHandler.GetRates` |
| POST | `/sellers/me/order-items/{orderItemID}/shipping/labels` | `ShippingHandler.BuyLabel` |
| POST | `/sellers/me/shops/{shopID}/reels` | `ReelHandler.Create` |
| GET | `/sellers/me/shops/{shopID}/reels` | `ReelHandler.ListByShop` |
| POST | `/sellers/me/products/{productID}/reels` | `ReelHandler.CreateForProduct` |
| GET | `/sellers/me/products/{productID}/reels` | `ReelHandler.ListByProduct` |
| GET | `/sellers/me/reels` | `ReelHandler.ListMine` |
| GET | `/sellers/me/reels/{id}` | `ReelHandler.Get` |
| PUT | `/sellers/me/reels/{id}` | `ReelHandler.Update` |
| DELETE | `/sellers/me/reels/{id}` | `ReelHandler.Delete` |

---

## 5. Endpoints in detail

### 5.1 Health

`GET /health` — outside `/api/v1`, no auth, no body.

```json
{ "status": "ok" }
```

### 5.2 Auth and bootstrap

#### `POST /admin/register`

Creates the very first superadmin and then refuses to run again. Requires the header
`X-Bootstrap-Secret: <BOOTSTRAP_SECRET from .env>`.

```json
{
  "email": "root@sendagift.test",
  "password": "supersecret123",
  "display_name": "Root Admin",
  "image_url": null
}
```

`201`:

```json
{ "message": "superadmin created", "id": "4f1c…" }
```

`403 bootstrap already completed` once an admin exists or the secret is wrong.
`400` if the email is empty or the password is shorter than 8 characters.

#### `POST /auth/login`, `POST /customers/login`, `POST /sellers/login`

All three are the same handler and the same lookup order (admin → customer → seller); the
three paths exist only so each frontend can call a familiar URL. The `role` in the
response is what decides which routes the token opens.

```json
{ "email": "seller@shop.test", "password": "supersecret123" }
```

`200`:

```json
{ "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9…", "role": "seller" }
```

`401 invalid email or password` — same message for unknown email and wrong password.

### 5.3 Admin profile

`GET /admin/me` returns the admin row for the token subject; `PUT /admin/me` updates the
two editable fields.

```json
{ "display_name": "Platform Admin", "image_url": "https://…/admin.png" }
```

`200`:

```json
{
  "id": "4f1c…",
  "email": "root@sendagift.test",
  "display_name": "Platform Admin",
  "role": "superadmin",
  "mfa_required": false,
  "status": "active",
  "created_at": "2026-08-01T09:00:00Z",
  "updated_at": "2026-09-08T04:12:00Z",
  "image_url": "https://…/admin.png"
}
```

These two routes are gated by `RequireAuth` only, with no role check — a customer or
seller token reaches the handler, but the subject is not an admin id, so the lookup fails
and the response is `401 unauthorized`.

### 5.4 Countries and country capabilities

Countries are the platform's root reference data: currency, timezone, and a `status` that
says how much of the product is switched on there. Capabilities are a 1:1 feature-flag row
per country, checked during registration.

Reads are public:

```
GET /countries
GET /countries/{id}
```

```json
[
  {
    "id": "3478b972-3c85-49ea-ac32-7afcace17129",
    "iso_code": "US",
    "name": "United States",
    "default_currency": "USD",
    "default_timezone": "America/New_York",
    "status": "full",
    "created_at": "2026-08-01T09:00:00Z",
    "updated_at": "2026-08-01T09:00:00Z"
  }
]
```

Writes are admin-only. `POST /admin/countries` and `PUT /admin/countries/{id}` take:

```json
{
  "iso_code": "US",
  "name": "United States",
  "default_currency": "USD",
  "default_timezone": "America/New_York",
  "status": "full"
}
```

`status` ∈ `full | marketplace | customer_only | browse_only | blocked`. `iso_code` must
be two letters and unique (`409 iso_code already exists`); `default_currency` must be a
known ISO code.

Capabilities use the **country id** in the path, not a capability id:

```
GET    /admin/country-capabilities            → [{ country, capability }, …]
GET    /admin/countries/{id}/capabilities     → { country, capability }
POST   /admin/countries/{id}/capabilities
PUT    /admin/countries/{id}/capabilities
DELETE /admin/countries/{id}/capabilities
```

Body — all ten flags are plain booleans, and because Go zero-values a missing bool, any
key you omit is stored as `false`:

```json
{
  "customer_registration_enabled": true,
  "seller_registration_enabled": true,
  "seller_payouts_enabled": true,
  "domestic_delivery_enabled": true,
  "international_delivery_enabled": true,
  "memberships_enabled": true,
  "points_earning_enabled": true,
  "points_usage_enabled": true,
  "skill_competitions_enabled": false,
  "app_store_available": true
}
```

`200` / `201`:

```json
{
  "country": { "id": "3478b972…", "iso_code": "US", "…": "…" },
  "capability": {
    "id": "b91d…",
    "country_id": "3478b972…",
    "customer_registration_enabled": true,
    "seller_registration_enabled": true,
    "rule_version": 1,
    "created_at": "2026-08-01T09:05:00Z",
    "updated_at": "2026-08-01T09:05:00Z"
  }
}
```

`409` if a capability row already exists for that country (there is a `UNIQUE` on
`country_id`). These flags are enforced at registration: `customer_registration_enabled = false`
turns `POST /customers/register` into `403 customer registration is disabled for this country`,
and the same applies to sellers.

### 5.5 Customers

#### `POST /customers/register` (public)

One call creates the customer and, optionally, their addresses.

```json
{
  "country_id": "3478b972-3c85-49ea-ac32-7afcace17129",
  "email": "gifter@example.com",
  "password": "supersecret123",
  "phone": "+14155550123",
  "display_name": "Ava Gifter",
  "customer_type": "individual",
  "date_of_birth": "1994-04-12",
  "image_url": null,
  "addresses": [
    {
      "country_id": "3478b972-3c85-49ea-ac32-7afcace17129",
      "label": "Home",
      "address_type": "shipping",
      "line1": "221B Baker Street",
      "line2": null,
      "city": "San Francisco",
      "region": "CA",
      "postal_code": "94105",
      "latitude": 37.7749,
      "longitude": -122.4194,
      "is_default": true
    }
  ]
}
```

`201` returns `CustomerDetails` — the customer plus every address:

```json
{
  "id": "9c2f…",
  "country_id": "3478b972…",
  "email": "gifter@example.com",
  "phone": "+14155550123",
  "display_name": "Ava Gifter",
  "customer_type": "individual",
  "date_of_birth": "1994-04-12T00:00:00Z",
  "status": "active",
  "created_at": "2026-09-08T04:20:00Z",
  "updated_at": "2026-09-08T04:20:00Z",
  "addresses": [
    {
      "id": "1a7e…",
      "customer_id": "9c2f…",
      "country_id": "3478b972…",
      "label": "Home",
      "address_type": "shipping",
      "line1": "221B Baker Street",
      "city": "San Francisco",
      "region": "CA",
      "postal_code": "94105",
      "latitude": 37.7749,
      "longitude": -122.4194,
      "is_default": true,
      "created_at": "2026-09-08T04:20:00Z",
      "updated_at": "2026-09-08T04:20:00Z"
    }
  ]
}
```

Errors: `400 invalid country_id`, `400 address requires country_id, line1, and city`,
`403 customer registration is disabled for this country`, `409 email already registered`.

#### `GET /customers/me`

Same `CustomerDetails` shape as above.

#### `PUT /customers/me`

Full replace of the editable profile fields; returns the bare `Customer` (no addresses).

```json
{
  "country_id": "3478b972…",
  "phone": "+14155550999",
  "display_name": "Ava G.",
  "customer_type": "individual",
  "date_of_birth": "1994-04-12",
  "status": "active",
  "image_url": "https://bucket.s3.region.amazonaws.com/public/customers/…jpg"
}
```

#### `DELETE /customers/me`

```json
{ "message": "customer deleted" }
```

Soft delete. The token issued before the call keeps working until it expires.

#### Addresses

`POST /customers/me/addresses` takes one `AddressInput` (the object shown inside
`addresses[]` above) and returns `201` with the created `CustomerAddress`.
`DELETE /customers/me/addresses/{id}` returns `{"message":"address deleted"}`, or
`404 address not found` if the row belongs to someone else.

### 5.6 Recipients

A recipient is the person receiving the gift — stored under the customer, with its own
addresses, and referenced by `orders.recipient_id`.

`POST /customers/me/recipients` and `PUT /customers/me/recipients/{id}`:

```json
{
  "name": "Maya Fernando",
  "relationship": "sister",
  "email": "maya@example.com",
  "phone": "+94711234567",
  "image_url": null,
  "default_address_id": null,
  "preferences": { "likes": ["chocolate"], "allergies": ["nuts"] },
  "addresses": [
    {
      "country_id": "3478b972…",
      "label": "Apartment",
      "address_type": "shipping",
      "line1": "42 Galle Road",
      "city": "Colombo",
      "region": "Western",
      "postal_code": "00300",
      "is_default": true
    }
  ]
}
```

`201` / `200` return `RecipientDetails`:

```json
{
  "id": "63dce1d5-5cfe-4e46-9e51-d6cb2474dde4",
  "customer_id": "9c2f…",
  "name": "Maya Fernando",
  "relationship": "sister",
  "email": "maya@example.com",
  "phone": "+94711234567",
  "default_address_id": "8bb1…",
  "preferences": { "likes": ["chocolate"], "allergies": ["nuts"] },
  "created_at": "2026-09-08T04:30:00Z",
  "updated_at": "2026-09-08T04:30:00Z",
  "addresses": [ { "id": "8bb1…", "recipient_id": "63dce1d5…", "line1": "42 Galle Road", "…": "…" } ]
}
```

`GET /customers/me/recipients` returns a flat `[]Recipient` **without** the `addresses`
array — fetch one recipient by id when you need addresses.

Address sub-routes take the same `AddressInput` and return the `RecipientAddress`:

```
POST   /customers/me/recipients/{id}/addresses                 → 201
PUT    /customers/me/recipients/{id}/addresses/{addressId}     → 200
DELETE /customers/me/recipients/{id}/addresses/{addressId}     → { "message": "address deleted" }
```

Deleting the address that was the default clears `recipients.default_address_id`
(`ON DELETE SET NULL`). Setting `default_address_id` to an address that belongs to another
recipient is `400 default_address_id must belong to this recipient`.

### 5.7 Saved gifts

A wishlist join row. There is no update — change means delete then create.

```
POST   /customers/me/saved-gifts        { "product_id": "317ae580…" }   → 201
GET    /customers/me/saved-gifts                                        → 200
DELETE /customers/me/saved-gifts/{id}                                   → 200
```

`POST` returns the join row only:

```json
{ "id": "aa10…", "customer_id": "9c2f…", "product_id": "317ae580…", "created_at": "2026-09-08T04:40:00Z" }
```

`GET` returns `[]SavedGiftDetails`, each with the product embedded:

```json
[
  {
    "id": "aa10…",
    "customer_id": "9c2f…",
    "product_id": "317ae580…",
    "created_at": "2026-09-08T04:40:00Z",
    "product": {
      "id": "317ae580…",
      "shop_id": "d40b…",
      "name": "Gift Box USA",
      "slug": "gift-box-usa",
      "product_type": "gift",
      "price_amount": 4500,
      "currency": "USD",
      "status": "published",
      "occasion_tags": ["birthday"],
      "customer_type_visibility": "both",
      "points_display_enabled": false,
      "prep_minutes": 30,
      "created_at": "2026-08-20T10:00:00Z",
      "updated_at": "2026-08-20T10:00:00Z",
      "image_url": "https://…/box.jpg"
    }
  }
]
```

Note the `{id}` in the DELETE is the **saved-gift id**, not the product id.
`409 product already saved` on a duplicate (`UNIQUE (customer_id, product_id)`).

### 5.8 Customer orders

#### `POST /customers/me/orders`

The server prices the order — you send products and quantities, never amounts.

```json
{
  "recipient_id": "63dce1d5-5cfe-4e46-9e51-d6cb2474dde4",
  "country_id": "3478b972-3c85-49ea-ac32-7afcace17129",
  "customer_type": "personal",
  "delivery_date": "2026-09-25",
  "gift_message": "Sending love from San Francisco!",
  "media_greeting_id": null,
  "delivery_amount": 1200,
  "items": [
    { "product_id": "317ae580-1771-4cf1-b6d9-dad9eaf01b23", "quantity": 2 }
  ]
}
```

For each line the service snapshots the live product, so a later price change cannot alter
a placed order. It rejects the call unless the product is `published`, its shop is
`active`, its `customer_type_visibility` matches, and every line shares one currency.
`order_number` is generated as `SAG-YYYYMMDD-XXXXXXXX`, and the order starts at
`pending_payment` with every item at `pending`.

`201` returns `OrderDetails`:

```json
{
  "id": "c7f9…",
  "order_number": "SAG-20260908-9F2C1A4E",
  "customer_id": "9c2f…",
  "recipient_id": "63dce1d5…",
  "country_id": "3478b972…",
  "customer_type": "personal",
  "delivery_date": "2026-09-25T00:00:00Z",
  "status": "pending_payment",
  "subtotal_amount": 9000,
  "delivery_amount": 1200,
  "total_amount": 10200,
  "currency": "USD",
  "gift_message": "Sending love from San Francisco!",
  "created_at": "2026-09-08T04:50:00Z",
  "updated_at": "2026-09-08T04:50:00Z",
  "items": [
    {
      "id": "60fca39c-9818-456a-a0a0-8c753c39182b",
      "order_id": "c7f9…",
      "seller_id": "77aa…",
      "shop_id": "d40b…",
      "product_id": "317ae580…",
      "quantity": 2,
      "unit_amount": 4500,
      "total_amount": 9000,
      "fulfilment_status": "pending",
      "created_at": "2026-09-08T04:50:00Z",
      "updated_at": "2026-09-08T04:50:00Z"
    }
  ]
}
```

Errors: `400 items required; delivery_date YYYY-MM-DD; customer_type personal or corporate`,
`400 product not found, not published, or shop is not active`,
`400 product is not available for this customer_type`,
`400 all items must use the same currency`, `404 recipient not found`.

#### The other three

| Route | Response |
| --- | --- |
| `GET /customers/me/orders` | `[]Order` — headers only, no items |
| `GET /customers/me/orders/{id}` | `OrderDetails` — header + `items[]` |
| `POST /customers/me/orders/{id}/cancel` | `OrderDetails` after cancelling; `409 order cannot be cancelled in its current status` once fulfilment has started |

For a multi-seller order, `order.status` is the header and each `items[].fulfilment_status`
is one seller's progress. Read the items to answer "is my gift done?" — see
[7.4](#74-multi-seller-completion).

### 5.9 Sellers, addresses, shops

#### `POST /sellers/register` (public)

Creates the seller, their addresses, and optionally a first shop in one call.

```json
{
  "country_id": "3478b972-3c85-49ea-ac32-7afcace17129",
  "seller_type": "individual",
  "legal_name": "Bay Area Gifts LLC",
  "trading_name": "Bay Area Gifts",
  "email": "seller@shop.test",
  "password": "supersecret123",
  "phone": "+14155550100",
  "image_url": null,
  "addresses": [
    {
      "country_id": "3478b972…",
      "label": "Warehouse",
      "address_type": "both",
      "line1": "1 Market Street",
      "city": "San Francisco",
      "region": "CA",
      "postal_code": "94105",
      "is_default": true
    }
  ],
  "shop": {
    "name": "Bay Area Gifts",
    "slug": "bay-area-gifts",
    "description": "Curated gift boxes",
    "customer_visible_location": "San Francisco, CA",
    "status": "active",
    "address_id": null,
    "return_address_id": null,
    "image_url": null
  }
}
```

`address_type` for a seller address must be `pickup | return | both` (unlike customer
addresses). Omit `shop` entirely to register without one. `201` returns `SellerDetails`:

```json
{
  "id": "77aa…",
  "country_id": "3478b972…",
  "seller_type": "individual",
  "legal_name": "Bay Area Gifts LLC",
  "trading_name": "Bay Area Gifts",
  "email": "seller@shop.test",
  "phone": "+14155550100",
  "verification_status": "unverified",
  "status": "active",
  "created_at": "2026-09-08T05:00:00Z",
  "updated_at": "2026-09-08T05:00:00Z",
  "addresses": [ { "id": "5c31…", "seller_id": "77aa…", "address_type": "both", "…": "…" } ],
  "shops": [ { "id": "d40b…", "seller_id": "77aa…", "name": "Bay Area Gifts", "slug": "bay-area-gifts", "status": "active", "…": "…" } ]
}
```

Errors: `400 legal_name, email required; password must be at least 8 characters`,
`403 seller registration is disabled for this country`, `409 email already registered`,
`409 shop slug already exists` (slug is globally unique).

#### Profile

| Route | Body | Response |
| --- | --- | --- |
| `GET /sellers/me` | — | `SellerDetails` (addresses + shops) |
| `PUT /sellers/me` | `country_id`, `seller_type`, `legal_name`, `trading_name`, `phone`, `image_url` | `Seller` |
| `DELETE /sellers/me` | — | `{ "message": "seller deleted" }` (soft) |

#### Addresses

```
POST   /sellers/me/addresses          → 201 SellerAddress
PUT    /sellers/me/addresses/{id}     → 200 SellerAddress
DELETE /sellers/me/addresses/{id}     → { "message": "address deleted" }
```

Deleting an address first clears any `shops.address_id` pointing at it, then removes the
row.

#### Shops

```
GET    /sellers/me/shops         → []Shop  (every status, including draft)
POST   /sellers/me/shops         → 201 Shop
PUT    /sellers/me/shops/{id}    → 200 Shop
DELETE /sellers/me/shops/{id}    → { "message": "shop deleted" }
```

`POST`/`PUT` body is the `shop` object from registration. `address_id` is the ship-from
address; `return_address_id` overrides it for returns, and shipping prefers
`return_address_id` when both are set. Only `status: "active"` shops appear in the public
`GET /shops`, and `DELETE` is a hard delete that cascades to products and reels.

### 5.10 Products and inventory

#### `POST /sellers/me/shops/{shopID}/products`

Inventory can be created inline:

```json
{
  "name": "Gift Box USA",
  "slug": "gift-box-usa",
  "description": "Chocolate, candle, and a card.",
  "product_type": "gift",
  "price_amount": 4500,
  "currency": "USD",
  "status": "published",
  "occasion_tags": ["birthday", "thank-you"],
  "customer_type_visibility": "both",
  "points_display_enabled": false,
  "prep_minutes": 30,
  "image_url": "https://bucket.s3.region.amazonaws.com/public/products/…jpg",
  "inventory": {
    "available_qty": 25,
    "reserved_qty": 0,
    "low_stock_threshold": 5,
    "unavailable_dates": ["2026-12-24", "2026-12-25"]
  }
}
```

`status` ∈ `draft | published | paused | rejected`; `customer_type_visibility` ∈
`personal | corporate | both`; `price_amount` and `prep_minutes` must be ≥ 0; `currency`
must be a known ISO code. `201` returns `ProductDetails` (product + `inventory`):

```json
{
  "id": "317ae580-1771-4cf1-b6d9-dad9eaf01b23",
  "shop_id": "d40b…",
  "name": "Gift Box USA",
  "slug": "gift-box-usa",
  "price_amount": 4500,
  "currency": "USD",
  "status": "published",
  "occasion_tags": ["birthday", "thank-you"],
  "customer_type_visibility": "both",
  "points_display_enabled": false,
  "prep_minutes": 30,
  "created_at": "2026-09-08T05:10:00Z",
  "updated_at": "2026-09-08T05:10:00Z",
  "image_url": "https://…jpg",
  "inventory": {
    "id": "ee54…",
    "product_id": "317ae580…",
    "available_qty": 25,
    "reserved_qty": 0,
    "low_stock_threshold": 5,
    "unavailable_dates": ["2026-12-24T00:00:00Z", "2026-12-25T00:00:00Z"],
    "updated_at": "2026-09-08T05:10:00Z"
  }
}
```

`409 product slug already exists for this shop` — slugs are unique per shop, not globally.

#### The rest

| Route | Body | Response |
| --- | --- | --- |
| `GET /sellers/me/shops/{shopID}/products` | — | `[]Product` |
| `GET /sellers/me/products/{id}` | — | `ProductDetails` |
| `PUT /sellers/me/products/{id}` | same as create | `Product` |
| `DELETE /sellers/me/products/{id}` | — | `{ "message": "product deleted" }` |
| `GET /sellers/me/products/{id}/inventory` | — | `Inventory` |
| `PUT /sellers/me/products/{id}/inventory` | the `inventory` object above | `Inventory` |

`unavailable_dates` go in as `"YYYY-MM-DD"` strings and come back as full timestamps.

### 5.11 Seller order items

A seller works on **order items**, never whole orders, because one customer order can span
several sellers. `{id}` here is always an `order_items.id`.

| Route | Response |
| --- | --- |
| `GET /sellers/me/order-items` | `[]SellerOrderItemSummary` — line + order number/status, delivery date, product name/slug/image, recipient name |
| `GET /sellers/me/order-items/{id}` | `SellerOrderItemDetails` — line + full `order`, `product`, `recipient`, `shipping_address` |
| `PATCH /sellers/me/order-items/{id}/accept` | the updated `OrderItem` |

Accept takes no body and moves `fulfilment_status` from `pending` to `accepted`; anything
else is `409 order item cannot be accepted in its current status`. Accepting is the gate
for shipping — rates are refused unless the item is `accepted`, `preparing`, or `ready`.

```json
{
  "id": "60fca39c-9818-456a-a0a0-8c753c39182b",
  "order_id": "c7f9…",
  "seller_id": "77aa…",
  "shop_id": "d40b…",
  "product_id": "317ae580…",
  "quantity": 2,
  "unit_amount": 4500,
  "total_amount": 9000,
  "fulfilment_status": "accepted",
  "created_at": "2026-09-08T04:50:00Z",
  "updated_at": "2026-09-08T05:20:00Z"
}
```

### 5.12 Shipping (Shippo)

Two seller calls plus one provider callback. Both seller routes are keyed by
`order_item_id`, and both write to the same `marketplace.shipments` row.

#### `POST /sellers/me/order-items/{orderItemID}/shipping/rates`

Ship-from (shop address) and ship-to (recipient address) are read from the database — you
only post the box and, for international, the customs form. The body may be omitted
entirely for domestic.

```json
{
  "parcel": {
    "length": "20", "width": "15", "height": "10", "distance_unit": "cm",
    "weight": "1.200", "mass_unit": "kg"
  },
  "customs_declaration": {
    "contents_type": "MERCHANDISE",
    "contents_explanation": "Gift box",
    "non_delivery_option": "RETURN",
    "certify_signer": "Bay Area Gifts",
    "eel_pfc": "",
    "incoterm": "",
    "items": [
      {
        "description": "Gift Box USA",
        "quantity": 1,
        "net_weight": "1.2",
        "mass_unit": "kg",
        "value_amount": "45.00",
        "value_currency": "USD",
        "origin_country": "US",
        "tariff_number": ""
      }
    ]
  }
}
```

International is decided by comparing ship-from and ship-to ISO2 codes, and then both
`parcel` and `customs_declaration` are mandatory. Whatever you post is stored on the
pending shipment row, so a retry can omit it. `200`:

```json
{
  "shipment_object_id": "a884e22e…",
  "customs_declaration_id": "7c1f…",
  "international": true,
  "rates": [
    {
      "object_id": "299a3ab049a541d8a56fc668f531cec5",
      "provider": "USPS",
      "amount": "28.45",
      "currency": "USD",
      "estimated_days": 6,
      "duration_terms": "6 to 10 days",
      "service_name": "Priority Mail International"
    }
  ]
}
```

Errors: `409 order item is not ready for shipping`, `400` with the missing-address detail,
`400 parcel is required for international shipments`, `502` with the carrier's own text,
`503 shipping provider not configured` when `SHIPPO_API_KEY` is unset.

#### `POST /sellers/me/order-items/{orderItemID}/shipping/labels`

```json
{
  "rate_object_id": "299a3ab049a541d8a56fc668f531cec5",
  "provider": "USPS",
  "idempotency_key": "us-label-001"
}
```

Pick a `rate_object_id` from the rates response. `idempotency_key` must be unique per real
purchase: the key is claimed in `core.idempotency_keys` before Shippo is called, and
replaying it returns the stored shipment instead of buying a second label. The label PDF
is downloaded from Shippo, uploaded to S3, and recorded as a `media.media_assets` row of
`asset_type = 'label'`, whose id lands on `shipments.label_media_id`. The order item moves
to `dispatched`.

`201`:

```json
{
  "order_id": "00000000-0000-0000-0000-000000000000",
  "seller_id": "00000000-0000-0000-0000-000000000000",
  "is_international": false,
  "courier_provider": "USPS",
  "tracking_number": "CA004982114US",
  "label_media_id": "5f3b…",
  "delivery_mode": "",
  "status": "label_created",
  "provider_shipment_id": "71a26df8…",
  "provider_tracking_url": "https://tools.usps.com/go/TrackConfirmAction?tLabels=CA004982114US",
  "provider_metadata": { "…": "raw Shippo transaction" },
  "created_at": "0001-01-01T00:00:00Z",
  "updated_at": "0001-01-01T00:00:00Z"
}
```

The response is the patch that was applied, not a re-read of the row, so `order_id`,
`delivery_mode`, and the timestamps are zero values here. Fetch the order item to see
committed state.

#### `POST /webhooks/shippo/tracking` (public)

```json
{
  "event": "track_updated",
  "data": {
    "tracking_number": "CA004982114US",
    "tracking_status": { "status": "DELIVERED", "status_date": "2026-09-04T14:30:00Z" }
  }
}
```

Any `event` other than `track_updated` is acknowledged and ignored. Statuses map
`PRE_TRANSIT → label_created`, `TRANSIT → in_transit`, `DELIVERED → delivered`,
`FAILURE → failed`, `RETURNED → returned`, anything else → `in_transit`. Always `200`:

```json
{ "status": "ok" }
```

This endpoint has no signature verification — anyone who knows a tracking number can post
to it.

### 5.13 Media (S3 presign)

The API never proxies file bytes. It hands out a short-lived URL and the client uploads
straight to S3.

#### `POST /media/presign-upload` (any valid token)

```json
{ "filename": "unboxing.mp4", "content_type": "video/mp4", "folder": "reel-video" }
```

`folder` must be one of these, which fixes the key prefix:

| `folder` | Prefix | Public? |
| --- | --- | --- |
| `seller-profile` | `public/sellers` | yes |
| `shop-image` | `public/shops` | yes |
| `product-image` | `public/products` | yes |
| `reel-video` | `public/reels/videos` | yes |
| `reel-photo` | `public/reels/photos` | yes |
| `reel-thumbnail` | `public/reels/thumbnails` | yes |

Anything else is `400 unsupported folder`. `200`:

```json
{
  "upload_url": "https://bucket.s3.region.amazonaws.com/public/reels/videos/9f2c1a4e-unboxing.mp4?X-Amz-Algorithm=…",
  "key": "public/reels/videos/9f2c1a4e-unboxing.mp4",
  "public_url": "https://bucket.s3.region.amazonaws.com/public/reels/videos/9f2c1a4e-unboxing.mp4"
}
```

The server generates `key` as `<prefix>/<uuid>-<filename>` — you cannot choose it. Upload
with a plain `PUT` to `upload_url`, sending the same `Content-Type` you presigned and the
raw file as the body (no multipart, no `Authorization` header). The URL expires after 10
minutes; `public_url` is only present for `public/` prefixes.

#### `GET /media/url?key=<object key>` (any valid token)

```json
{ "url": "https://bucket.s3.region.amazonaws.com/labels/…pdf?X-Amz-Algorithm=…" }
```

A 15-minute signed GET, for private objects such as label PDFs. The URL is signed for
`GET` specifically — a `HEAD` against it returns `403`.

### 5.14 Reels

A reel is a short video (or photo carousel) that a seller posts. It always belongs to a
shop; tagging a product is optional:

- **shop reel** — the shop's own promo, `product_id` null, surfaced by `?scope=shop`
- **product reel** — tagged to one product, tappable to buy, surfaced by `?scope=product`

The reel row lives in `seller.reels`; the files live in `media.media_assets` and are linked
in order through `seller.reel_media`.

#### Creating

```
POST /sellers/me/shops/{shopID}/reels        product_id in the body is optional
POST /sellers/me/products/{productID}/reels  product taken from the URL, body product_id ignored
```

Both take the same body. Upload each file first via `/media/presign-upload`, then post the
returned `key` as `object_path`:

```json
{
  "product_id": null,
  "caption": "Check out our Gift Box USA",
  "hashtags": ["#GiftBox", "birthday"],
  "visibility": "public",
  "status": "published",
  "duration_ms": 15000,
  "thumbnail": {
    "object_path": "public/reels/thumbnails/1b7d-cover.jpg",
    "mime_type": "image/jpeg",
    "size_bytes": 84213,
    "metadata": { "width": 1080, "height": 1920 }
  },
  "media": [
    {
      "object_path": "public/reels/videos/9f2c1a4e-unboxing.mp4",
      "mime_type": "video/mp4",
      "size_bytes": 2481920,
      "metadata": { "width": 1080, "height": 1920, "duration_ms": 15000 }
    }
  ]
}
```

Rules the service enforces:

- `media` needs at least 1 and at most 10 items; each needs a non-empty `object_path` and
  an `image/*` or `video/*` `mime_type`. `size_bytes` and `metadata` are informational —
  the frontend reads them from the `File` object and the `<video>`/`<img>` element.
- `reel_type` is derived, not sent: any video item makes the reel a `video`, otherwise
  `photo`.
- `thumbnail` must be an image.
- `visibility` ∈ `public | private` (default `public`); `status` ∈
  `draft | published | archived` (default `draft`). Setting `published` stamps
  `published_at` once; it is never re-stamped.
- `hashtags` are lowercased, `#` stripped, duplicates removed.
- A `product_id` that is not in the reel's shop is `400 product_id must belong to this shop`.
- `processing_status` and `moderation_status` on the created assets are set by the server
  to `ready` / `approved`. They are not part of any request body.
- `cdn_url` is filled only when `object_path` starts with `public/`. Posting a placeholder
  string instead of a real key yields a reel with no playable URL.

`201` returns `ReelDetails`:

```json
{
  "id": "e51a…",
  "seller_id": "77aa…",
  "shop_id": "d40b…",
  "product_id": null,
  "thumbnail_media_id": "b220…",
  "reel_type": "video",
  "caption": "Check out our Gift Box USA",
  "hashtags": ["giftbox", "birthday"],
  "visibility": "public",
  "status": "published",
  "duration_ms": 15000,
  "view_count": 0,
  "published_at": "2026-09-08T05:40:00Z",
  "created_at": "2026-09-08T05:40:00Z",
  "updated_at": "2026-09-08T05:40:00Z",
  "media": [
    {
      "media_asset_id": "a91c…",
      "position": 0,
      "asset_type": "video",
      "bucket": "sendagift-media",
      "object_path": "public/reels/videos/9f2c1a4e-unboxing.mp4",
      "cdn_url": "https://sendagift-media.s3.ap-southeast-2.amazonaws.com/public/reels/videos/9f2c1a4e-unboxing.mp4",
      "mime_type": "video/mp4",
      "size_bytes": 2481920,
      "metadata": { "width": 1080, "height": 1920, "duration_ms": 15000 }
    }
  ],
  "thumbnail": {
    "media_asset_id": "b220…",
    "position": 0,
    "asset_type": "image",
    "bucket": "sendagift-media",
    "object_path": "public/reels/thumbnails/1b7d-cover.jpg",
    "cdn_url": "https://…/public/reels/thumbnails/1b7d-cover.jpg",
    "mime_type": "image/jpeg",
    "size_bytes": 84213,
    "metadata": { "width": 1080, "height": 1920 }
  },
  "shop": { "id": "d40b…", "name": "Bay Area Gifts", "slug": "bay-area-gifts", "image_url": null },
  "product": null
}
```

`media[]` carries `media_asset_id` only — a thumbnail has no `seller.reel_media` row, so a
single `id` field would have meant two different things.

#### Seller reads and writes

| Route | Response |
| --- | --- |
| `GET /sellers/me/reels` | `[]ReelDetails`, all statuses, newest first |
| `GET /sellers/me/shops/{shopID}/reels` | `[]ReelDetails` for that shop |
| `GET /sellers/me/products/{productID}/reels` | `[]ReelDetails` tagged to that product |
| `GET /sellers/me/reels/{id}` | `ReelDetails`, any status, owner only, no view counted |
| `PUT /sellers/me/reels/{id}` | `ReelDetails` |
| `DELETE /sellers/me/reels/{id}` | `{ "message": "reel deleted" }` |

`PUT` uses the same body as create with one difference: **omit `media` to keep the current
files**, send it to replace them all (an empty array is `400`). Sending `media: []` is
invalid; sending a new array deletes the old assets, which cascades the old
`seller.reel_media` rows away. `DELETE` removes the reel, its media rows, and best-effort
deletes the S3 objects.

#### Public feed

```
GET /reels?scope=&shop_id=&product_id=&limit=&cursor=
GET /shops/{shopId}/reels?scope=shop
GET /products/{productId}/reels
GET /reels/{id}
```

Query params:

| Param | Meaning |
| --- | --- |
| `shop_id`, `product_id` | filter (the path param wins when both are present) |
| `scope` | `all` (default), `shop` = no product tagged, `product` = product tagged; anything else is `400 scope must be all, shop, or product` |
| `limit` | 1–50, default 20; non-numeric or ≤ 0 is `400 limit must be a positive number` |
| `cursor` | `next_cursor` from the previous page; a corrupt value is `400 invalid cursor` |

Only reels that are `published` **and** `public` **and** whose shop is `active` are ever
returned. `200`:

```json
{
  "items": [ { "…": "ReelDetails as above" } ],
  "next_cursor": "MjAyNi0wOS0wOFQwNTo0MDowMFp8ZTUxYQ"
}
```

`next_cursor` is absent on the last page. It is base64url of `published_at|id`, used for
keyset paging (`(published_at, id) < (cursor)` ordered descending) so new posts never
shift a page. `GET /reels/{id}` returns one `ReelDetails` and increments `view_count` in
`seller.reels`, returning the already-incremented number.

### 5.15 Public storefront browsing

No JWT. Inactive shops and unpublished products are `404`, never a hint that they exist.

| Route | Response |
| --- | --- |
| `GET /shops` | `[]Shop` where `status = 'active'`, oldest first |
| `GET /shops/{shopId}` | one active `Shop` |
| `GET /shops/{shopId}/products` | `[]Product`, published, from an active shop |
| `GET /products/{productId}` | `PublicProduct` — product + `shop` summary |

Both product routes take `?customer_type=personal|corporate` (default `personal`), which
keeps out products whose `customer_type_visibility` targets the other audience;
anything else is `400 customer_type must be personal or corporate`.

`GET /products/{productId}`:

```json
{
  "id": "317ae580…",
  "shop_id": "d40b…",
  "name": "Gift Box USA",
  "slug": "gift-box-usa",
  "description": "Chocolate, candle, and a card.",
  "product_type": "gift",
  "price_amount": 4500,
  "currency": "USD",
  "status": "published",
  "occasion_tags": ["birthday"],
  "customer_type_visibility": "both",
  "points_display_enabled": false,
  "prep_minutes": 30,
  "created_at": "2026-08-20T10:00:00Z",
  "updated_at": "2026-08-20T10:00:00Z",
  "image_url": "https://…/box.jpg",
  "shop": {
    "id": "d40b…",
    "name": "Bay Area Gifts",
    "slug": "bay-area-gifts",
    "image_url": null,
    "customer_visible_location": "San Francisco, CA"
  }
}
```

A storefront page is these plus the matching reel feed: `/shops/{shopId}` +
`/shops/{shopId}/products` + `/shops/{shopId}/reels`, and `/products/{productId}` +
`/products/{productId}/reels`.

### 5.16 Places (Google proxy)

Public because address pickers run on registration and checkout forms before any token
exists, so both routes sit behind a 120-request-per-minute-per-IP limit.

`GET /places/autocomplete?input=221b+baker&session=<uuid>&country=US&language=en&types=address`
(`q` also works instead of `input`):

```json
{
  "suggestions": [
    {
      "place_id": "ChIJ…",
      "description": "221B Baker Street, London, UK",
      "main_text": "221B Baker Street",
      "secondary_text": "London, UK"
    }
  ]
}
```

`GET /places/details?place_id=ChIJ…&session=<same uuid>&language=en`:

```json
{
  "place_id": "ChIJ…",
  "name": "221B Baker Street",
  "formatted_address": "221B Baker St, London NW1 6XE, UK",
  "line1": "221B Baker Street",
  "city": "London",
  "region": "England",
  "postal_code": "NW1 6XE",
  "country_code": "GB",
  "country_name": "United Kingdom",
  "latitude": 51.5238,
  "longitude": -0.1586
}
```

Reuse one `session` token across the keystrokes and the follow-up details call — Google
bills them as a single session. The fields map straight onto `AddressInput`
(`line1`, `line2`, `city`, `region`, `postal_code`, `latitude`, `longitude`);
`country_code` still has to be translated to your `core.countries.id`.
`400 place_id is required`, `404 place not found`, `502` on a Google failure.

---

## 6. Where each request/response struct lives

Request bodies are decoded either into a private struct in the handler (when the HTTP shape
differs from the service input) or straight into the service input type. Responses are
always `internal/models` types.

### Request types

| Endpoint group | Struct | File |
| --- | --- | --- |
| Admin bootstrap | `bootstrapRequest` | `internal/handlers/auth_handler.go` |
| Login | `loginRequest` | `internal/handlers/auth_handler.go` |
| Admin profile | `services.AdminUpdateInput` | `internal/services/admin_service.go` |
| Countries | `countryRequest` → `services.CountryInput` | `internal/handlers/country_handler.go`, `internal/services/country_service.go` |
| Capabilities | `countryCapabilityRequest` → `services.CountryCapabilityInput` | `internal/handlers/country_capability_handler.go`, `internal/services/country_capability_service.go` |
| Customer register/update | `customerRegisterRequest`, `customerUpdateRequest` → `services.CustomerRegisterInput`, `CustomerUpdateInput` | `internal/handlers/customer_handler.go`, `internal/services/customer_service.go` |
| Any address | `services.AddressInput` | `internal/services/customer_service.go` |
| Recipients | `services.RecipientInput` | `internal/services/customer_service.go` |
| Saved gifts | `savedGiftRequest` | `internal/handlers/customer_handler.go` |
| Orders | `services.OrderCreateInput`, `OrderItemInput` | `internal/services/order_service.go` |
| Seller register/update | `sellerRegisterRequest`, `sellerUpdateRequest` → `services.SellerRegisterInput`, `SellerUpdateInput` | `internal/handlers/seller_handler.go`, `internal/services/seller_service.go` |
| Seller address | `services.SellerAddressInput` | `internal/services/seller_service.go` |
| Shops | `services.ShopInput` | `internal/services/seller_service.go` |
| Products / inventory | `services.ProductInput`, `InventoryInput` | `internal/services/product_service.go` |
| Reels | `services.ReelInput`, `ReelMediaInput` | `internal/services/reel_service.go` |
| Reel feed params | `services.ReelFeedFilter` | `internal/services/reel_service.go` |
| Shipping rates | `services.ShippingShipmentInput`, `ParcelInput`, `CustomsDeclarationInput`, `CustomsItemInput` | `internal/services/shipping_inputs.go` |
| Shipping labels | `buyLabelRequest` → `services.BuyLabelInput` | `internal/handlers/shipping_handler.go`, `internal/services/shipping_service.go` |
| Shippo webhook | `shippoWebhookPayload` | `internal/services/shipping_service.go` |
| Media presign | `presignUploadRequest` | `internal/handlers/media_handler.go` |
| Places | `services.AutocompleteParams` (from query string) | `internal/services/places_service.go` |

### Response types

| Response | Struct | File |
| --- | --- | --- |
| Login | `services.LoginResult` | `internal/services/auth_service.go` |
| Admin | `models.Admin` | `internal/models/admin.go` |
| Country / capability | `models.Country`, `CountryCapability`, `CountryCapabilityDetails` | `internal/models/country.go`, `country_capability.go` |
| Customer | `models.Customer`, `CustomerAddress`, `CustomerDetails` | `internal/models/customer.go` |
| Saved gift | `models.SavedGift`, `SavedGiftDetails` | `internal/models/customer.go` |
| Recipient | `models.Recipient`, `RecipientAddress`, `RecipientDetails` | `internal/models/recipient.go` |
| Seller | `models.Seller`, `SellerAddress`, `Shop`, `SellerDetails` | `internal/models/seller.go` |
| Product | `models.Product`, `Inventory`, `ProductDetails`, `ProductShopSummary`, `PublicProduct` | `internal/models/product.go` |
| Order | `models.Order`, `OrderItem`, `OrderDetails`, `SellerOrderItemSummary`, `SellerOrderItemDetails` | `internal/models/order.go` |
| Reel | `models.Reel`, `ReelMediaItem`, `ReelShopSummary`, `ReelProductSummary`, `ReelDetails`, `ReelFeed` | `internal/models/reel.go` |
| Media asset | `models.MediaAsset` | `internal/models/media_asset.go` |
| Shipment | `models.Shipment` | `internal/models/shipment.go` |
| Rates | `services.ShippingRatesResult`, `ShippoRate` | `internal/services/shipping_service.go`, `shippo_client.go` |
| Presign | `presignUploadResponse` | `internal/handlers/media_handler.go` |
| Places | `services.PlaceSuggestion`, `PlaceDetails` | `internal/services/places_service.go` |

Naming pattern worth knowing: a bare model (`Product`) is the row; `…Details` embeds the
row plus children (`ProductDetails` adds inventory); `Public…` / `…Summary` are trimmed
DTOs for public or nested use.

---

## 7. Cross-cutting flows

### 7.1 Upload anything

```mermaid
sequenceDiagram
    participant FE as Client
    participant API
    participant S3
    FE->>API: POST /media/presign-upload {filename, content_type, folder}
    API-->>FE: {upload_url, key, public_url}
    FE->>S3: PUT upload_url (raw bytes, same Content-Type)
    S3-->>FE: 200
    FE->>API: POST resource with object_path = key
```

Three destinations for a key: `image_url` on a seller/shop/product (public URL), a reel's
`media[].object_path` (which creates the `media_assets` row), or a private object read back
through `GET /media/url`.

### 7.2 Publish a product reel

1. `POST /media/presign-upload` with `folder: "reel-video"` → PUT the file.
2. Optional second presign with `folder: "reel-thumbnail"` → PUT the cover image.
3. `POST /sellers/me/products/{productID}/reels` with the returned keys and
   `status: "published"`.
4. It appears in `GET /reels`, `GET /shops/{shopId}/reels`, and
   `GET /products/{productId}/reels?scope=product`.

### 7.3 Checkout to delivery

```mermaid
sequenceDiagram
    participant Cust as Customer
    participant API
    participant Sel as Seller
    participant Shippo
    Cust->>API: POST /customers/me/orders
    API-->>Cust: order pending_payment, items pending
    Sel->>API: GET /sellers/me/order-items
    Sel->>API: PATCH /order-items/{id}/accept
    Sel->>API: POST /order-items/{id}/shipping/rates
    API->>Shippo: shipment (+ customs if international)
    Sel->>API: POST /order-items/{id}/shipping/labels
    API->>Shippo: buy label
    API-->>Sel: label_created, item dispatched
    Shippo->>API: POST /webhooks/shippo/tracking (DELIVERED)
    API-->>API: item delivered; order delivered only if all items resolved
```

### 7.4 Multi-seller completion

One order, two sellers, two independent lines:

| Step | Item A | Item B | `orders.status` |
| --- | --- | --- | --- |
| placed | `pending` | `pending` | `pending_payment` |
| both accept | `accepted` | `accepted` | unchanged |
| A buys label | `dispatched` | `accepted` | unchanged |
| A delivered webhook | `delivered` | `accepted` | unchanged |
| B delivered webhook | `delivered` | `delivered` | `delivered` |

The webhook marks the one item delivered, then only flips the order header when every item
is `delivered` or `cancelled`. So a customer UI should read `items[].fulfilment_status` for
per-seller progress and treat `order.status = delivered` as "the whole gift landed".

### 7.5 Idempotent label purchase

`idempotency_key` is claimed in `core.idempotency_keys` (scope + unique key) before Shippo
is called and completed with the serialized shipment afterwards. A replay of the same key
short-circuits and returns the stored response; a new key on the same order item buys
another label. Keys are scoped to label purchase only — no other endpoint reads them.

---

## 8. Error catalogue

Message strings are exactly what the API returns, so they can be matched in tests.

| Status | Message | Raised by |
| --- | --- | --- |
| 401 | `missing bearer token` / `invalid or expired token` | `RequireAuth` |
| 403 | `insufficient role` | `RequireRole` |
| 400 | `invalid request body` | any handler, on malformed JSON |
| 403 | `bootstrap already completed` | `POST /admin/register` |
| 401 | `invalid email or password` | login |
| 400 | `iso_code must be a 2-letter country code` / `default_currency must be a known ISO currency code` | country write |
| 409 | `iso_code already exists` | country write |
| 404 | `country not found` / `country capability not found` | country + capability routes |
| 409 | `country capability already exists for this country` | capability create |
| 400 | `email required, password must be at least 8 characters` | customer register |
| 400 | `invalid country_id` | customer/seller/order create |
| 403 | `customer registration is disabled for this country` / `seller registration is disabled for this country` | register |
| 400 | `address requires country_id, line1, and city` | customer address |
| 400 | `address requires country_id, line1, city; address_type must be pickup\|return\|both` | seller address |
| 409 | `email already registered` | register |
| 404 | `customer not found` / `seller not found` / `address not found` / `recipient not found` | profile routes |
| 400 | `name is required; preferences must be valid JSON` | recipient write |
| 400 | `default_address_id must belong to this recipient` | recipient write |
| 409 | `product already saved` | saved gifts |
| 400 | `invalid or missing product_id` | saved gifts |
| 404 | `saved gift not found` | saved gifts |
| 400 | `shop name is required` | shop write |
| 409 | `shop slug already exists` | shop write |
| 404 | `shop not found` | shop, product, reel routes |
| 400 | `name, currency required; status must be draft\|published\|paused\|rejected; visibility personal\|corporate\|both; amounts >= 0` | product write |
| 400 | `qty fields must be >= 0; unavailable_dates must be YYYY-MM-DD` | inventory write |
| 409 | `product slug already exists for this shop` | product write |
| 404 | `product not found` / `inventory not found` | product routes |
| 400 | `items required; delivery_date YYYY-MM-DD; customer_type personal or corporate` | order create |
| 400 | `product not found, not published, or shop is not active` | order create |
| 400 | `product is not available for this customer_type` | order create |
| 400 | `all items must use the same currency` | order create |
| 409 | `order cannot be cancelled in its current status` | order cancel |
| 404 | `order not found` / `order item not found` | order routes |
| 409 | `order item cannot be accepted in its current status` | accept |
| 400 | `customer_type must be personal or corporate` | public browsing |
| 400 | `media[] requires object_path and an image/* or video/* mime_type; visibility public\|private; status draft\|published\|archived` | reel write |
| 400 | `product_id must belong to this shop` | reel write |
| 400 | `scope must be all, shop, or product` / `invalid cursor` / `limit must be a positive number` | reel feed |
| 404 | `reel not found` | reel routes |
| 400 | `filename and content_type are required` / `unsupported folder` / `key is required` | media |
| 503 | `shipping provider not configured` | shipping |
| 409 | `order item is not ready for shipping` | shipping |
| 502 | carrier text passed through | shipping |
| 404 | `place not found` | places |

---

## 9. Known gaps and sharp edges

Documented so nobody rediscovers them the hard way:

1. **No payment step.** Orders are created as `pending_payment` and nothing moves them to
   `paid`. Sellers can accept regardless.
2. **No inventory reservation at checkout.** `inventory.reserved_qty` and
   `unavailable_dates` exist but ordering neither decrements nor reserves stock.
3. **Soft delete leaves data public.** `DELETE /sellers/me` sets `status = 'deleted'` on
   the seller only; their `active` shops still appear in `GET /shops` and their JWT stays
   valid until it expires.
4. **`/admin/me` has no role gate.** It is protected by `RequireAuth` alone, so any valid
   token reaches the handler and gets `401` from the failed admin lookup rather than `403`.
5. **The Shippo webhook is unauthenticated and unverified.** No signature check, no replay
   protection.
6. **`BuyLabel` returns the patch, not the row.** `order_id`, `delivery_mode`, and the
   timestamps come back as zero values.
7. **Booleans in capability bodies are all-or-nothing.** An omitted flag is stored
   `false`, so always send all ten on `PUT`.
8. **Lists are unbounded** except the reel feed. `GET /shops`, `GET /reels`-adjacent seller
   lists, order lists, and product lists return everything.
9. **`orders.media_greeting_id` has no foreign key.** The value is accepted as any UUID
   and never validated against `media.media_assets`.
10. **Empty placeholder files.** `internal/handlers/verification_handler.go`,
    `internal/services/verification_service.go`, `internal/repository/user_repository.go`,
    `internal/services/email_service.go`, and `internal/models/user.go` contain only a
    package clause — there is no verification or email feature yet.
