# SendAGift API

Go REST API for admin, countries, customers, recipients, orders, sellers, shops, products, inventory, and saved gifts.

Base URL: `http://localhost:8081`

This file is the endpoint-by-endpoint Postman guide. Two companion documents cover the
design:

| Document | Covers |
|---|---|
| [`API_REFERENCE.md`](API_REFERENCE.md) | how the layers connect, every route in one map, request/response structures and where they are defined, shared flows, the full error catalogue |
| [`DATABASE_SCHEMA.md`](DATABASE_SCHEMA.md) | every table and column, primary/foreign keys, ER diagram per section and for the whole database, cascade behaviour, indexes, migration history |

## Run

```powershell
go run ./cmd/api
```

Migrations run on startup.

```powershell
go run ./cmd/migrate
```

Copy `.env.example` to `.env` and fill in values. Shippo uses a **test API key** (`shippo_test_...`) in development; the app sends it as `Authorization: ShippoToken <key>` on every Shippo request ([auth docs](https://docs.goshippo.com/docs/guides_general/authentication/)).

| Variable | Purpose |
|---|---|
| `SHIPPO_API_KEY` | Shippo test or live API token |
| `SHIPPO_LABEL_BUCKET` | S3 bucket name stored on label records — set to the **same value as `S3_BUCKET`** |

Webhook URL for tracking updates (configure in the [Shippo API portal](https://docs.goshippo.com/docs/tracking/webhooks/)):

`POST http://localhost:8081/api/v1/webhooks/shippo/tracking`

Event type: `track_updated`. Respond with `2xx` within 3 seconds.

## Postman rules

| | |
|---|---|
| POST / PUT | Header `Content-Type: application/json` + JSON body |
| GET / DELETE | **No body** (IDs go in the URL) |
| Protected routes | Header `Authorization: Bearer <token>` |
| IDs | `customer_id` / `seller_id` / admin id come from JWT `sub` — do not send in body |
| Errors | `{ "error": "message" }` |
| Passwords | never returned (`password_hash` is hidden) |

CORS is enabled for browser frontends (React/Vite on another port). **Postman does not need CORS** — it is not a browser, so requests already work.

---

## 1. Health

### GET `http://localhost:8081/health`

- Auth: none
- **GET body:** none

**Response 200**

```json
{ "status": "ok" }
```

---

## 2. Auth

### POST `http://localhost:8081/api/v1/admin/register`

Create first superadmin. After one admin exists, add header `X-Bootstrap-Secret`.

- Auth: none (then bootstrap secret)
- **POST body:**

```json
{
  "email": "admin@sendagift.com",
  "password": "password123",
  "display_name": "Super Admin",
  "image_url": "https://res.cloudinary.com/demo/image/upload/v1/admin.jpg"
}
```

**Response 201**

```json
{
  "message": "superadmin created",
  "id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890"
}
```

### POST `http://localhost:8081/api/v1/auth/login`

Also:

- `POST http://localhost:8081/api/v1/customers/login`
- `POST http://localhost:8081/api/v1/sellers/login`

Same **POST body** for all three:

```json
{
  "email": "jane@example.com",
  "password": "password123"
}
```

**Response 200**

```json
{
  "token": "eyJhbGciOiJIUzI1NiIs...",
  "role": "customer"
}
```

`role` is `admin` / `superadmin` / `customer` / `seller`. Copy `token` for later requests.

---

## 3. Admin

JWT: admin / superadmin  
Header: `Authorization: Bearer <admin_token>`

### GET `http://localhost:8081/api/v1/admin/me`

- **GET body:** none

**Response 200**

```json
{
  "id": "admin-uuid",
  "email": "admin@sendagift.com",
  "display_name": "Super Admin",
  "role": "superadmin",
  "mfa_required": true,
  "status": "active",
  "created_at": "2026-08-11T08:00:00Z",
  "updated_at": "2026-08-11T08:00:00Z",
  "image_url": "https://res.cloudinary.com/demo/image/upload/v1/admin.jpg"
}
```

### PUT `http://localhost:8081/api/v1/admin/me`

- **PUT body:**

```json
{
  "display_name": "Super Admin",
  "image_url": "https://res.cloudinary.com/demo/image/upload/v1/admin-new.jpg"
}
```

**Response 200** — same shape as GET `/admin/me`.

---

## 4. Countries

Public read endpoints (no JWT). Create, update, and delete require admin JWT.

**Postman headers (admin write routes only)**

| Header | Value |
|---|---|
| `Content-Type` | `application/json` |
| `Authorization` | `Bearer <admin_token>` |

Get `<admin_token>` from `POST /api/v1/auth/login` (admin account).

### GET `http://localhost:8081/api/v1/countries`

- Auth: none
- **Headers:** none required
- **Body:** none

**Response 200**

```json
[
  {
    "id": "3478b972-3c85-49ea-ac32-7afcace17129",
    "iso_code": "LK",
    "name": "Sri Lanka",
    "default_currency": "LKR",
    "default_timezone": "Asia/Colombo",
    "status": "active",
    "created_at": "2026-08-11T08:00:00Z",
    "updated_at": "2026-08-11T08:00:00Z"
  }
]
```

### GET `http://localhost:8081/api/v1/countries/{id}`

Example URL: `http://localhost:8081/api/v1/countries/3478b972-3c85-49ea-ac32-7afcace17129`

- Auth: none
- **Headers:** none required
- **Body:** none
- ID in URL

**Response 200** — one country object (same fields as above).

### POST `http://localhost:8081/api/v1/admin/countries`

- Auth: admin JWT (`admin` or `superadmin`)
- **Headers:** `Content-Type: application/json`, `Authorization: Bearer <admin_token>`
- **POST body:**

```json
{
  "iso_code": "LK",
  "name": "Sri Lanka",
  "default_currency": "LKR",
  "default_timezone": "Asia/Colombo",
  "status": "active"
}
```

| Field | Required | Notes |
|---|---|---|
| `iso_code` | yes | 2-letter country code (e.g. `LK`, `US`) |
| `name` | yes | Country display name |
| `default_currency` | yes | ISO 4217 code: `USD`, `EUR`, `GBP`, `INR`, `LKR`, `AUD`, `CAD`, `SGD`, `AED`, `JPY`, `CNY`, `CHF`, `NZD`, `HKD`, `MYR`, `THB`, `IDR`, `PHP`, `PKR`, `BDT`, `SAR` |
| `default_timezone` | yes | IANA timezone (e.g. `Asia/Colombo`) |
| `status` | no | Defaults to `active` if omitted |

**Response 201** — country object with `id`. Use that `id` as `country_id` for customers and sellers.

```json
{
  "id": "3478b972-3c85-49ea-ac32-7afcace17129",
  "iso_code": "LK",
  "name": "Sri Lanka",
  "default_currency": "LKR",
  "default_timezone": "Asia/Colombo",
  "status": "active",
  "created_at": "2026-08-11T08:00:00Z",
  "updated_at": "2026-08-11T08:00:00Z"
}
```

### PUT `http://localhost:8081/api/v1/admin/countries/{id}`

Example URL: `http://localhost:8081/api/v1/admin/countries/3478b972-3c85-49ea-ac32-7afcace17129`

- Auth: admin JWT
- **Headers:** `Content-Type: application/json`, `Authorization: Bearer <admin_token>`
- **PUT body:** same shape as POST (all fields required)

```json
{
  "iso_code": "LK",
  "name": "Sri Lanka",
  "default_currency": "LKR",
  "default_timezone": "Asia/Colombo",
  "status": "inactive"
}
```

**Response 200** — updated country object (same shape as POST response).

### DELETE `http://localhost:8081/api/v1/admin/countries/{id}`

Example URL: `http://localhost:8081/api/v1/admin/countries/3478b972-3c85-49ea-ac32-7afcace17129`

- Auth: admin JWT
- **Headers:** `Authorization: Bearer <admin_token>`
- **Body:** none
- ID in URL

**Response 200**

```json
{ "message": "country deleted" }
```

**Postman quick copy**

| Method | URL | Body |
|---|---|---|
| GET | `http://localhost:8081/api/v1/countries` | none |
| GET | `http://localhost:8081/api/v1/countries/{id}` | none |
| POST | `http://localhost:8081/api/v1/admin/countries` | `{ "iso_code": "LK", "name": "Sri Lanka", "default_currency": "LKR", "default_timezone": "Asia/Colombo", "status": "active" }` |
| PUT | `http://localhost:8081/api/v1/admin/countries/{id}` | same as POST |
| DELETE | `http://localhost:8081/api/v1/admin/countries/{id}` | none |

Country capability routes (same admin JWT) — see **section 5**.

---

## 5. Country capabilities

Admin-only routes for `core.country_capabilities` (country feature gates).  
Registered in `country_routes.go` together with country routes. All require admin JWT.

**Routes** (`{id}` = `country_id` from `core.countries`)

| Method | URL | Body |
|---|---|---|
| GET | `http://localhost:8081/api/v1/admin/country-capabilities` | none |
| GET | `http://localhost:8081/api/v1/admin/countries/{id}/capabilities` | none |
| POST | `http://localhost:8081/api/v1/admin/countries/{id}/capabilities` | boolean flags below |
| PUT | `http://localhost:8081/api/v1/admin/countries/{id}/capabilities` | boolean flags below |
| DELETE | `http://localhost:8081/api/v1/admin/countries/{id}/capabilities` | none |

One capability row per country (`country_id` is unique in the database).

**Postman headers** (all routes above)

| Header | Value |
|---|---|
| `Content-Type` | `application/json` |
| `Authorization` | `Bearer <admin_token>` |

Get `<admin_token>` from `POST /api/v1/auth/login` (admin account).

**Request body (POST and PUT only)** — boolean flags; do **not** send `country_id` in the body

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

| Field | Notes |
|---|---|
| boolean flags | Set each gate explicitly on POST and PUT |
| `rule_version` | Read-only; starts at `1`, auto-incremented on PUT |
| `{id}` in URL | The country's `id` (same as `country_id` FK) |

### GET `http://localhost:8081/api/v1/admin/country-capabilities`

List all countries that have capability rows, each with nested `country` + `capability`.

- Auth: admin JWT
- **Body:** none

**Response 200**

```json
[
  {
    "country": {
      "id": "3478b972-3c85-49ea-ac32-7afcace17129",
      "iso_code": "LK",
      "name": "Sri Lanka",
      "default_currency": "LKR",
      "default_timezone": "Asia/Colombo",
      "status": "active",
      "created_at": "2026-08-11T08:00:00Z",
      "updated_at": "2026-08-11T08:00:00Z"
    },
    "capability": {
      "id": "eb840357-5918-4b3a-a95f-b04096f9b52b",
      "country_id": "3478b972-3c85-49ea-ac32-7afcace17129",
      "customer_registration_enabled": false,
      "seller_registration_enabled": true,
      "seller_payouts_enabled": true,
      "domestic_delivery_enabled": true,
      "international_delivery_enabled": true,
      "memberships_enabled": true,
      "points_earning_enabled": true,
      "points_usage_enabled": true,
      "skill_competitions_enabled": false,
      "app_store_available": true,
      "rule_version": 2,
      "created_at": "2026-08-28T10:23:18.184418+05:30",
      "updated_at": "2026-08-28T10:24:23.909594+05:30"
    }
  }
]
```

### GET `http://localhost:8081/api/v1/admin/countries/{id}/capabilities`

Example: `http://localhost:8081/api/v1/admin/countries/3478b972-3c85-49ea-ac32-7afcace17129/capabilities`

- Auth: admin JWT
- **Body:** none
- `{id}` = country `id`

**Response 200**

```json
{
  "country": {
    "id": "3478b972-3c85-49ea-ac32-7afcace17129",
    "iso_code": "LK",
    "name": "Sri Lanka",
    "default_currency": "LKR",
    "default_timezone": "Asia/Colombo",
    "status": "active",
    "created_at": "2026-08-11T08:00:00Z",
    "updated_at": "2026-08-11T08:00:00Z"
  },
  "capability": {
    "id": "eb840357-5918-4b3a-a95f-b04096f9b52b",
    "country_id": "3478b972-3c85-49ea-ac32-7afcace17129",
    "customer_registration_enabled": false,
    "seller_registration_enabled": true,
    "seller_payouts_enabled": true,
    "domestic_delivery_enabled": true,
    "international_delivery_enabled": true,
    "memberships_enabled": true,
    "points_earning_enabled": true,
    "points_usage_enabled": true,
    "skill_competitions_enabled": false,
    "app_store_available": true,
    "rule_version": 2,
    "created_at": "2026-08-28T10:23:18.184418+05:30",
    "updated_at": "2026-08-28T10:24:23.909594+05:30"
  }
}
```

### POST `http://localhost:8081/api/v1/admin/countries/{id}/capabilities`

Example: `http://localhost:8081/api/v1/admin/countries/3478b972-3c85-49ea-ac32-7afcace17129/capabilities`

- Auth: admin JWT
- **Body:** boolean flags above
- `{id}` = country `id` (must exist in `core.countries`)

**Response 201** — `capability` object (flags + `rule_version`, not nested with country).

### PUT `http://localhost:8081/api/v1/admin/countries/{id}/capabilities`

Example: `http://localhost:8081/api/v1/admin/countries/3478b972-3c85-49ea-ac32-7afcace17129/capabilities`

- Auth: admin JWT
- **Body:** boolean flags above
- `{id}` = country `id`
- `rule_version` increments automatically on each update

**Response 200** — updated `capability` object.

### DELETE `http://localhost:8081/api/v1/admin/countries/{id}/capabilities`

Example: `http://localhost:8081/api/v1/admin/countries/3478b972-3c85-49ea-ac32-7afcace17129/capabilities`

- Auth: admin JWT
- **Body:** none
- `{id}` = country `id`

**Response 200**

```json
{ "message": "country capability deleted" }
```

**Postman quick copy**

| Method | URL | Body | Response |
|---|---|---|---|
| GET | `http://localhost:8081/api/v1/admin/country-capabilities` | none | `CountryCapabilityDetails[]` |
| GET | `http://localhost:8081/api/v1/admin/countries/{id}/capabilities` | none | `CountryCapabilityDetails` |
| POST | `http://localhost:8081/api/v1/admin/countries/{id}/capabilities` | boolean flags above | `CountryCapability` |
| PUT | `http://localhost:8081/api/v1/admin/countries/{id}/capabilities` | boolean flags above | `CountryCapability` |
| DELETE | `http://localhost:8081/api/v1/admin/countries/{id}/capabilities` | none | `{ "message": "country capability deleted" }` |

**Registration gates:** `customer_registration_enabled` and `seller_registration_enabled` are checked on `POST /customers/register` and `POST /sellers/register` (403 when `false`).

---

## 6. Customers

### POST `http://localhost:8081/api/v1/customers/register`

- Auth: none
- Blocked with **403** when `customer_registration_enabled` is `false` for `country_id` in country capabilities
- Does not return a token — login after
- **POST body:**

```json
{
  "country_id": "3478b972-3c85-49ea-ac32-7afcace17129",
  "email": "jane@example.com",
  "password": "password123",
  "phone": "+94771234567",
  "display_name": "Jane Doe",
  "customer_type": "individual",
  "date_of_birth": "1995-04-12",
  "image_url": "https://res.cloudinary.com/demo/image/upload/v1/customer.jpg",
  "addresses": [
    {
      "country_id": "3478b972-3c85-49ea-ac32-7afcace17129",
      "label": "Home",
      "address_type": "shipping",
      "line1": "12 Galle Road",
      "line2": "Apt 4",
      "city": "Colombo",
      "region": "Western",
      "postal_code": "00300",
      "latitude": 6.9271,
      "longitude": 79.8612,
      "is_default": true
    }
  ]
}
```

`addresses` can be `[]`. Password min 8 characters.

**Response 201**

```json
{
  "id": "customer-uuid",
  "country_id": "3478b972-3c85-49ea-ac32-7afcace17129",
  "email": "jane@example.com",
  "phone": "+94771234567",
  "display_name": "Jane Doe",
  "customer_type": "individual",
  "date_of_birth": "1995-04-12T00:00:00Z",
  "status": "active",
  "created_at": "...",
  "updated_at": "...",
  "image_url": "https://res.cloudinary.com/demo/image/upload/v1/customer.jpg",
  "addresses": [
    {
      "id": "address-uuid",
      "customer_id": "customer-uuid",
      "country_id": "3478b972-3c85-49ea-ac32-7afcace17129",
      "label": "Home",
      "address_type": "shipping",
      "line1": "12 Galle Road",
      "line2": "Apt 4",
      "city": "Colombo",
      "region": "Western",
      "postal_code": "00300",
      "latitude": 6.9271,
      "longitude": 79.8612,
      "is_default": true,
      "created_at": "...",
      "updated_at": "..."
    }
  ]
}
```

### GET `http://localhost:8081/api/v1/customers/me`

- Auth: customer JWT
- **GET body:** none

**Response 200** — same shape as register (profile + `addresses`).

### PUT `http://localhost:8081/api/v1/customers/me`

- Auth: customer JWT
- **PUT body:**

```json
{
  "country_id": "3478b972-3c85-49ea-ac32-7afcace17129",
  "phone": "+94770000000",
  "display_name": "Jane D.",
  "customer_type": "individual",
  "date_of_birth": "1995-04-12",
  "status": "active",
  "image_url": "https://res.cloudinary.com/demo/image/upload/v1/customer-new.jpg"
}
```

**Response 200** — customer profile **without** `addresses`.

### DELETE `http://localhost:8081/api/v1/customers/me`

- Auth: customer JWT
- **DELETE body:** none

**Response 200**

```json
{ "message": "customer deleted" }
```

### POST `http://localhost:8081/api/v1/customers/me/addresses`

- Auth: customer JWT
- **POST body:**

```json
{
  "country_id": "3478b972-3c85-49ea-ac32-7afcace17129",
  "label": "Office",
  "address_type": "shipping",
  "line1": "88 Union Place",
  "line2": null,
  "city": "Colombo",
  "region": "Western",
  "postal_code": "00200",
  "latitude": 6.917,
  "longitude": 79.865,
  "is_default": false
}
```

**Response 201** — one address object (includes `id`).

### DELETE `http://localhost:8081/api/v1/customers/me/addresses/{id}`

Example URL: `http://localhost:8081/api/v1/customers/me/addresses/address-uuid`

- Auth: customer JWT
- **DELETE body:** none
- Address id in URL

**Response 200**

```json
{ "message": "address deleted" }
```

### GET `http://localhost:8081/api/v1/customers/me/saved-gifts`

- Auth: customer JWT
- **GET body:** none
- Joins product details from `seller.products`

**Response 200**

```json
[
  {
    "id": "saved-gift-uuid",
    "customer_id": "customer-uuid",
    "product_id": "product-uuid",
    "created_at": "2026-08-17T10:18:00+05:30",
    "product": {
      "id": "product-uuid",
      "shop_id": "shop-uuid",
      "name": "Rose Bouquet",
      "slug": "rose-bouquet",
      "description": "Fresh roses",
      "product_type": "gift",
      "price_amount": 250000,
      "currency": "LKR",
      "status": "draft",
      "occasion_tags": ["birthday"],
      "customer_type_visibility": "both",
      "points_display_enabled": false,
      "prep_minutes": 60,
      "created_at": "...",
      "updated_at": "...",
      "image_url": "https://res.cloudinary.com/demo/image/upload/v1/product.jpg"
    }
  }
]
```

### POST `http://localhost:8081/api/v1/customers/me/saved-gifts`

- Auth: customer JWT (`customer_id` from token)
- **POST body:**

```json
{
  "product_id": "product-uuid"
}
```

**Response 201**

```json
{
  "id": "saved-gift-uuid",
  "customer_id": "customer-uuid",
  "product_id": "product-uuid",
  "created_at": "2026-08-17T10:18:00+05:30"
}
```

No PUT for saved gifts. Change = DELETE + POST.

### DELETE `http://localhost:8081/api/v1/customers/me/saved-gifts/{id}`

Example URL: `http://localhost:8081/api/v1/customers/me/saved-gifts/saved-gift-uuid`

- Auth: customer JWT
- **DELETE body:** none
- `{id}` is the **saved gift** id, not the product id

**Response 200**

```json
{ "message": "saved gift deleted" }
```

### POST `http://localhost:8081/api/v1/customers/me/recipients`

- Auth: customer JWT
- Creates a gift recipient (person you send gifts to)
- Optional `addresses` on create only — first address becomes default if `is_default` is not set
- **POST body:**

```json
{
  "name": "Amma",
  "relationship": "mother",
  "email": "amma@example.com",
  "phone": "+94771234567",
  "image_url": "https://res.cloudinary.com/demo/image/upload/v1/recipient.jpg",
  "preferences": {
    "favorite_colors": ["red", "gold"],
    "no_alcohol": true
  },
  "addresses": [
    {
      "country_id": "3478b972-3c85-49ea-ac32-7afcace17129",
      "label": "Home",
      "address_type": "shipping",
      "line1": "12 Temple Road",
      "line2": null,
      "city": "Colombo",
      "region": "Western",
      "postal_code": "00300",
      "latitude": 6.927,
      "longitude": 79.861,
      "is_default": true
    }
  ]
}
```

**Response 201** — **Return type:** `RecipientDetails` (same JSON shape as GET `/recipients/{id}`)

```json
{
  "id": "recipient-uuid",
  "customer_id": "customer-uuid",
  "name": "Amma",
  "relationship": "mother",
  "email": "amma@example.com",
  "phone": "+94771234567",
  "image_url": "https://res.cloudinary.com/demo/image/upload/v1/recipient.jpg",
  "default_address_id": "recipient-address-uuid",
  "preferences": {
    "favorite_colors": ["red", "gold"],
    "no_alcohol": true
  },
  "created_at": "2026-08-19T10:00:00+05:30",
  "updated_at": "2026-08-19T10:00:00+05:30",
  "addresses": [
    {
      "id": "recipient-address-uuid",
      "recipient_id": "recipient-uuid",
      "country_id": "3478b972-3c85-49ea-ac32-7afcace17129",
      "label": "Home",
      "address_type": "shipping",
      "line1": "12 Temple Road",
      "city": "Colombo",
      "region": "Western",
      "postal_code": "00300",
      "is_default": true,
      "created_at": "...",
      "updated_at": "..."
    }
  ]
}
```

### GET `http://localhost:8081/api/v1/customers/me/recipients`

- Auth: customer JWT
- **GET body:** none
- **Return type:** `Recipient[]` — array of recipient objects **without** nested `addresses`

**Response 200**

```json
[
  {
    "id": "recipient-uuid",
    "customer_id": "customer-uuid",
    "name": "Amma",
    "relationship": "mother",
    "email": "amma@example.com",
    "phone": "+94771234567",
    "image_url": "https://res.cloudinary.com/demo/image/upload/v1/recipient.jpg",
    "default_address_id": "recipient-address-uuid",
    "preferences": {},
    "created_at": "2026-08-19T10:00:00+05:30",
    "updated_at": "2026-08-19T10:00:00+05:30"
  }
]
```

**Recipient fields:** `id`, `customer_id`, `name`, `relationship`, `email`, `phone`, `image_url`, `default_address_id`, `preferences`, `created_at`, `updated_at`

### GET `http://localhost:8081/api/v1/customers/me/recipients/{id}`

Example URL: `http://localhost:8081/api/v1/customers/me/recipients/recipient-uuid`

- Auth: customer JWT
- **GET body:** none
- **Return type:** `RecipientDetails` — one recipient + `addresses[]`

**Response 200**

```json
{
  "id": "recipient-uuid",
  "customer_id": "customer-uuid",
  "name": "Amma",
  "relationship": "mother",
  "email": "amma@example.com",
  "phone": "+94771234567",
  "image_url": "https://res.cloudinary.com/demo/image/upload/v1/recipient.jpg",
  "default_address_id": "recipient-address-uuid",
  "preferences": {
    "favorite_colors": ["red", "gold"],
    "no_alcohol": true
  },
  "created_at": "2026-08-19T10:00:00+05:30",
  "updated_at": "2026-08-19T10:00:00+05:30",
  "addresses": [
    {
      "id": "recipient-address-uuid",
      "recipient_id": "recipient-uuid",
      "country_id": "3478b972-3c85-49ea-ac32-7afcace17129",
      "label": "Home",
      "address_type": "shipping",
      "line1": "12 Temple Road",
      "line2": null,
      "city": "Colombo",
      "region": "Western",
      "postal_code": "00300",
      "latitude": 6.927,
      "longitude": 79.861,
      "is_default": true,
      "created_at": "2026-08-19T10:00:00+05:30",
      "updated_at": "2026-08-19T10:00:00+05:30"
    }
  ]
}
```

**RecipientAddress fields:** `id`, `recipient_id`, `country_id`, `label`, `address_type`, `line1`, `line2`, `city`, `region`, `postal_code`, `latitude`, `longitude`, `is_default`, `created_at`, `updated_at`

### PUT `http://localhost:8081/api/v1/customers/me/recipients/{id}`

Example URL: `http://localhost:8081/api/v1/customers/me/recipients/recipient-uuid`

- Auth: customer JWT
- Updates the **person** only (name, contact, image, preferences, default address pointer)
- `addresses` in body is **ignored** on PUT — use recipient address routes below to change street/city
- **PUT body:**

```json
{
  "name": "Amma Perera",
  "relationship": "mother",
  "email": "amma.new@example.com",
  "phone": "+94771234567",
  "image_url": "https://res.cloudinary.com/demo/image/upload/v1/recipient-new.jpg",
  "default_address_id": "recipient-address-uuid",
  "preferences": {
    "favorite_colors": ["blue"],
    "no_alcohol": true
  }
}
```

`default_address_id` must already exist on this recipient. Omit or send `null` to clear it.

**Response 200** — **Return type:** `RecipientDetails` (same JSON shape as GET `/recipients/{id}`)

### DELETE `http://localhost:8081/api/v1/customers/me/recipients/{id}`

Example URL: `http://localhost:8081/api/v1/customers/me/recipients/recipient-uuid`

- Auth: customer JWT
- **DELETE body:** none
- Cascades delete of all recipient addresses

**Response 200**

```json
{ "message": "recipient deleted" }
```

### POST `http://localhost:8081/api/v1/customers/me/recipients/{id}/addresses`

Example URL: `http://localhost:8081/api/v1/customers/me/recipients/recipient-uuid/addresses`

- Auth: customer JWT
- Adds a delivery address for this recipient
- If `is_default: true`, sets `recipients.default_address_id` automatically
- **POST body:** same fields as customer address

```json
{
  "country_id": "3478b972-3c85-49ea-ac32-7afcace17129",
  "label": "Office",
  "address_type": "shipping",
  "line1": "45 Galle Road",
  "line2": "Floor 2",
  "city": "Colombo",
  "region": "Western",
  "postal_code": "00300",
  "latitude": 6.927,
  "longitude": 79.861,
  "is_default": false
}
```

**Response 201** — **Return type:** `RecipientAddress` object

```json
{
  "id": "recipient-address-uuid",
  "recipient_id": "recipient-uuid",
  "country_id": "3478b972-3c85-49ea-ac32-7afcace17129",
  "label": "Office",
  "address_type": "shipping",
  "line1": "45 Galle Road",
  "line2": "Floor 2",
  "city": "Colombo",
  "region": "Western",
  "postal_code": "00300",
  "latitude": 6.927,
  "longitude": 79.861,
  "is_default": false,
  "created_at": "2026-08-19T10:00:00+05:30",
  "updated_at": "2026-08-19T10:00:00+05:30"
}
```

### PUT `http://localhost:8081/api/v1/customers/me/recipients/{id}/addresses/{addressId}`

Example URL: `http://localhost:8081/api/v1/customers/me/recipients/recipient-uuid/addresses/recipient-address-uuid`

- Auth: customer JWT
- Updates one recipient address (use this to change line1, city, etc.)
- **PUT body:** same as POST address above

**Response 200** — **Return type:** `RecipientAddress` (same fields as POST address response above)

### DELETE `http://localhost:8081/api/v1/customers/me/recipients/{id}/addresses/{addressId}`

Example URL: `http://localhost:8081/api/v1/customers/me/recipients/recipient-uuid/addresses/recipient-address-uuid`

- Auth: customer JWT
- **DELETE body:** none
- If deleted address was the default, DB sets `default_address_id` to null (`ON DELETE SET NULL`)

**Response 200**

```json
{ "message": "address deleted" }
```

---

## 7. Customer orders

Auth: customer JWT. `customer_id` comes from the token.

One checkout = one `marketplace.orders` row + one `marketplace.order_items` row per product. Prices are copied from the product at checkout (`unit_amount` / `total_amount`) so later catalog price changes do not change past orders.

- Order `status` starts as `pending_payment`
- Each item `fulfilment_status` starts as `pending`
- Product must be `published` and its shop `active`
- `customer_type_visibility` on the product must be `both` or match `customer_type`
- All items must share the same `currency`
- `recipient_id` is optional but must belong to this customer if sent
- `delivery_amount` defaults to `0` (minor units)

### POST `http://localhost:8081/api/v1/customers/me/orders`

- **POST body:**

```json
{
  "recipient_id": "recipient-uuid",
  "country_id": "3478b972-3c85-49ea-ac32-7afcace17129",
  "customer_type": "personal",
  "delivery_date": "2026-08-25",
  "gift_message": "Happy birthday Amma",
  "delivery_amount": 50000,
  "items": [
    {
      "product_id": "product-uuid",
      "quantity": 2
    }
  ]
}
```

`recipient_id` and `gift_message` can be omitted. `customer_type`: `personal` | `corporate`.

**Response 201** — **Return type:** `OrderDetails` (order + `items[]`)

```json
{
  "id": "order-uuid",
  "order_number": "SAG-20260820-A1B2C3D4",
  "customer_id": "customer-uuid",
  "recipient_id": "recipient-uuid",
  "country_id": "3478b972-3c85-49ea-ac32-7afcace17129",
  "customer_type": "personal",
  "delivery_date": "2026-08-25T00:00:00Z",
  "status": "pending_payment",
  "subtotal_amount": 500000,
  "delivery_amount": 50000,
  "total_amount": 550000,
  "currency": "LKR",
  "gift_message": "Happy birthday Amma",
  "created_at": "...",
  "updated_at": "...",
  "items": [
    {
      "id": "order-item-uuid",
      "order_id": "order-uuid",
      "seller_id": "seller-uuid",
      "shop_id": "shop-uuid",
      "product_id": "product-uuid",
      "quantity": 2,
      "unit_amount": 250000,
      "total_amount": 500000,
      "fulfilment_status": "pending",
      "created_at": "...",
      "updated_at": "..."
    }
  ]
}
```

### GET `http://localhost:8081/api/v1/customers/me/orders`

- **GET body:** none
- **Return type:** `Order[]` — headers only, no `items`

**Response 200** — array of order objects (same fields as the header above, without `items`).

### GET `http://localhost:8081/api/v1/customers/me/orders/{id}`

Example URL: `http://localhost:8081/api/v1/customers/me/orders/order-uuid`

- **GET body:** none
- **Return type:** `OrderDetails` (same as POST create)

**Response 200** — one order + `items[]`.

### POST `http://localhost:8081/api/v1/customers/me/orders/{id}/cancel`

Example URL: `http://localhost:8081/api/v1/customers/me/orders/order-uuid/cancel`

- Auth: customer JWT
- **POST body:** none
- Sets order `status` to `cancelled` and item `fulfilment_status` to `cancelled`
- Allowed only when order status is `draft`, `pending_payment`, `paid`, `accepted`, or `preparing`
- Blocked when already `dispatched`, `delivered`, `cancelled`, or `refunded` (409)

**Response 200** — **Return type:** `OrderDetails` (same as GET one, with cancelled statuses)

**Response 409**

```json
{ "error": "order cannot be cancelled in its current status" }
```

---

## 8. Sellers

### POST `http://localhost:8081/api/v1/sellers/register`

- Auth: none
- Blocked with **403** when `seller_registration_enabled` is `false` for `country_id` in country capabilities
- Does not return a token — login after
- **POST body:**

```json
{
  "country_id": "3478b972-3c85-49ea-ac32-7afcace17129",
  "seller_type": "individual",
  "legal_name": "Nimal Perera",
  "trading_name": "Nimal Gifts",
  "email": "nimal@shop.com",
  "password": "password123",
  "phone": "+94711111111",
  "image_url": "https://res.cloudinary.com/demo/image/upload/v1/seller.jpg",
  "addresses": [
    {
      "country_id": "3478b972-3c85-49ea-ac32-7afcace17129",
      "label": "Warehouse",
      "address_type": "both",
      "line1": "45 Baseline Road",
      "line2": null,
      "city": "Colombo",
      "region": "Western",
      "postal_code": "00900",
      "latitude": 6.91,
      "longitude": 79.86,
      "is_default": true
    }
  ],
  "shop": {
    "name": "Nimal Gifts",
    "slug": "nimal-gifts",
    "description": "Handmade gifts",
    "return_address_mode": "shop",
    "customer_visible_location": "Colombo",
    "status": "active",
    "address_id": null,
    "image_url": "https://res.cloudinary.com/demo/image/upload/v1/shop.jpg"
  }
}
```

`address_type`: `pickup` | `return` | `both`  
`shop` can be omitted.

**Response 201**

```json
{
  "id": "seller-uuid",
  "country_id": "3478b972-3c85-49ea-ac32-7afcace17129",
  "seller_type": "individual",
  "legal_name": "Nimal Perera",
  "trading_name": "Nimal Gifts",
  "email": "nimal@shop.com",
  "phone": "+94711111111",
  "verification_status": "unverified",
  "status": "active",
  "created_at": "...",
  "updated_at": "...",
  "image_url": "https://res.cloudinary.com/demo/image/upload/v1/seller.jpg",
  "addresses": [
    {
      "id": "address-uuid",
      "seller_id": "seller-uuid",
      "country_id": "3478b972-3c85-49ea-ac32-7afcace17129",
      "label": "Warehouse",
      "address_type": "both",
      "line1": "45 Baseline Road",
      "city": "Colombo",
      "region": "Western",
      "postal_code": "00900",
      "latitude": 6.91,
      "longitude": 79.86,
      "is_default": true,
      "created_at": "...",
      "updated_at": "..."
    }
  ],
  "shops": [
    {
      "id": "shop-uuid",
      "seller_id": "seller-uuid",
      "name": "Nimal Gifts",
      "slug": "nimal-gifts",
      "description": "Handmade gifts",
      "return_address_mode": "shop",
      "customer_visible_location": "Colombo",
      "status": "active",
      "address_id": null,
      "created_at": "...",
      "updated_at": "...",
      "image_url": "https://res.cloudinary.com/demo/image/upload/v1/shop.jpg"
    }
  ]
}
```

### GET `http://localhost:8081/api/v1/sellers/me`

- Auth: seller JWT
- **GET body:** none

**Response 200** — same shape as register. Copy `shops[].id` and `addresses[].id`.

### PUT `http://localhost:8081/api/v1/sellers/me`

- Auth: seller JWT
- **PUT body:**

```json
{
  "country_id": "3478b972-3c85-49ea-ac32-7afcace17129",
  "seller_type": "individual",
  "legal_name": "Nimal Perera",
  "trading_name": "Nimal Gifts Co",
  "phone": "+94712222222",
  "image_url": "https://res.cloudinary.com/demo/image/upload/v1/seller-new.jpg"
}
```

**Response 200** — seller profile **without** `addresses` and `shops`.

### DELETE `http://localhost:8081/api/v1/sellers/me`

- Auth: seller JWT
- **DELETE body:** none

**Response 200**

```json
{ "message": "seller deleted" }
```

### POST `http://localhost:8081/api/v1/sellers/me/addresses`

- Auth: seller JWT
- **POST body:**

```json
{
  "country_id": "3478b972-3c85-49ea-ac32-7afcace17129",
  "label": "Returns",
  "address_type": "return",
  "line1": "10 Main Street",
  "line2": null,
  "city": "Kandy",
  "region": "Central",
  "postal_code": "20000",
  "latitude": 7.2906,
  "longitude": 80.6337,
  "is_default": false
}
```

**Response 201** — address object. Use `id` as shop `address_id`.

### DELETE `http://localhost:8081/api/v1/sellers/me/addresses/{id}`

Example URL: `http://localhost:8081/api/v1/sellers/me/addresses/address-uuid`

- Auth: seller JWT
- **DELETE body:** none

**Response 200**

```json
{ "message": "address deleted" }
```

### GET `http://localhost:8081/api/v1/sellers/me/shops`

- Auth: seller JWT
- **GET body:** none

Returns every shop you own, in **any** status (`draft`, `active`, `suspended`), unlike the public
`GET /shops` which only lists `active` ones.

**Response 200**

```json
[
  {
    "id": "shop-uuid",
    "seller_id": "seller-uuid",
    "name": "Nimal Gifts",
    "slug": "nimal-gifts",
    "status": "active",
    "customer_visible_location": "Colombo",
    "created_at": "...",
    "updated_at": "..."
  }
]
```

### POST `http://localhost:8081/api/v1/sellers/me/shops`

- Auth: seller JWT
- **POST body:**

```json
{
  "name": "Nimal Gifts",
  "slug": "nimal-gifts",
  "description": "Handmade gifts",
  "return_address_mode": "shop",
  "customer_visible_location": "Colombo",
  "status": "active",
  "address_id": "address-uuid",
  "image_url": "https://res.cloudinary.com/demo/image/upload/v1/shop.jpg"
}
```

`address_id` can be `null`.

**Response 201**

```json
{
  "id": "shop-uuid",
  "seller_id": "seller-uuid",
  "name": "Nimal Gifts",
  "slug": "nimal-gifts",
  "description": "Handmade gifts",
  "return_address_mode": "shop",
  "customer_visible_location": "Colombo",
  "status": "active",
  "address_id": "address-uuid",
  "created_at": "...",
  "updated_at": "...",
  "image_url": "https://res.cloudinary.com/demo/image/upload/v1/shop.jpg"
}
```

### PUT `http://localhost:8081/api/v1/sellers/me/shops/{id}`

Example URL: `http://localhost:8081/api/v1/sellers/me/shops/shop-uuid`

- Auth: seller JWT
- **PUT body:**

```json
{
  "name": "Nimal Gifts",
  "slug": "nimal-gifts",
  "description": "Handmade gifts",
  "return_address_mode": "shop",
  "customer_visible_location": "Colombo",
  "status": "active",
  "address_id": "address-uuid",
  "image_url": "https://res.cloudinary.com/demo/image/upload/v1/shop-new.jpg"
}
```

**Response 200** — shop object (same fields as create).

### DELETE `http://localhost:8081/api/v1/sellers/me/shops/{id}`

Example URL: `http://localhost:8081/api/v1/sellers/me/shops/shop-uuid`

- Auth: seller JWT
- **DELETE body:** none

**Response 200**

```json
{ "message": "shop deleted" }
```

---

## 9. Products and inventory

Auth: seller JWT  
`seller_id` from token. Shop must belong to that seller.

`price_amount` = minor units (`250000` = LKR 2500.00)  
`status`: `draft` | `published` | `paused` | `rejected`  
`customer_type_visibility`: `personal` | `corporate` | `both`

### GET `http://localhost:8081/api/v1/sellers/me/shops/{shopID}/products`

Example URL: `http://localhost:8081/api/v1/sellers/me/shops/shop-uuid/products`

- **GET body:** none

**Response 200**

```json
[
  {
    "id": "product-uuid",
    "shop_id": "shop-uuid",
    "name": "Rose Bouquet",
    "slug": "rose-bouquet",
    "description": "Fresh red roses",
    "product_type": "gift",
    "price_amount": 250000,
    "currency": "LKR",
    "status": "draft",
    "occasion_tags": ["birthday", "thank-you"],
    "customer_type_visibility": "both",
    "points_display_enabled": false,
    "prep_minutes": 60,
    "created_at": "...",
    "updated_at": "...",
    "image_url": "https://res.cloudinary.com/demo/image/upload/v1/product.jpg"
  }
]
```

### POST `http://localhost:8081/api/v1/sellers/me/shops/{shopID}/products`

Example URL: `http://localhost:8081/api/v1/sellers/me/shops/shop-uuid/products`

Creates product + inventory (`inventory` optional, defaults to 0).

- **POST body:**

```json
{
  "name": "Rose Bouquet",
  "slug": "rose-bouquet",
  "description": "Fresh red roses",
  "product_type": "gift",
  "price_amount": 250000,
  "currency": "LKR",
  "status": "draft",
  "occasion_tags": ["birthday", "thank-you"],
  "customer_type_visibility": "both",
  "points_display_enabled": false,
  "prep_minutes": 60,
  "image_url": "https://res.cloudinary.com/demo/image/upload/v1/product.jpg",
  "inventory": {
    "available_qty": 20,
    "reserved_qty": 0,
    "low_stock_threshold": 5,
    "unavailable_dates": ["2026-12-25"]
  }
}
```

**Response 201**

```json
{
  "id": "product-uuid",
  "shop_id": "shop-uuid",
  "name": "Rose Bouquet",
  "slug": "rose-bouquet",
  "description": "Fresh red roses",
  "product_type": "gift",
  "price_amount": 250000,
  "currency": "LKR",
  "status": "draft",
  "occasion_tags": ["birthday", "thank-you"],
  "customer_type_visibility": "both",
  "points_display_enabled": false,
  "prep_minutes": 60,
  "created_at": "...",
  "updated_at": "...",
  "image_url": "https://res.cloudinary.com/demo/image/upload/v1/product.jpg",
  "inventory": {
    "id": "inventory-uuid",
    "product_id": "product-uuid",
    "available_qty": 20,
    "reserved_qty": 0,
    "low_stock_threshold": 5,
    "unavailable_dates": ["2026-12-25T00:00:00Z"],
    "updated_at": "..."
  }
}
```

Save `id` for product URLs below.

### GET `http://localhost:8081/api/v1/sellers/me/products/{id}`

Example URL: `http://localhost:8081/api/v1/sellers/me/products/product-uuid`

- **GET body:** none

**Response 200** — same as POST create (product + `inventory`).

### PUT `http://localhost:8081/api/v1/sellers/me/products/{id}`

Example URL: `http://localhost:8081/api/v1/sellers/me/products/product-uuid`

Updates product fields only (not inventory).

- **PUT body:**

```json
{
  "name": "Rose Bouquet Deluxe",
  "slug": "rose-bouquet-deluxe",
  "description": "Premium roses",
  "product_type": "gift",
  "price_amount": 300000,
  "currency": "LKR",
  "status": "published",
  "occasion_tags": ["birthday"],
  "customer_type_visibility": "both",
  "points_display_enabled": false,
  "prep_minutes": 90,
  "image_url": "https://res.cloudinary.com/demo/image/upload/v1/product-new.jpg"
}
```

**Response 200** — product object **without** `inventory`.

### DELETE `http://localhost:8081/api/v1/sellers/me/products/{id}`

Example URL: `http://localhost:8081/api/v1/sellers/me/products/product-uuid`

- **DELETE body:** none
- Also deletes inventory (CASCADE)

**Response 200**

```json
{ "message": "product deleted" }
```

### GET `http://localhost:8081/api/v1/sellers/me/products/{id}/inventory`

Example URL: `http://localhost:8081/api/v1/sellers/me/products/product-uuid/inventory`

- **GET body:** none

**Response 200**

```json
{
  "id": "inventory-uuid",
  "product_id": "product-uuid",
  "available_qty": 20,
  "reserved_qty": 0,
  "low_stock_threshold": 5,
  "unavailable_dates": ["2026-12-25T00:00:00Z"],
  "updated_at": "..."
}
```

### PUT `http://localhost:8081/api/v1/sellers/me/products/{id}/inventory`

Example URL: `http://localhost:8081/api/v1/sellers/me/products/product-uuid/inventory`

- **PUT body:**

```json
{
  "available_qty": 15,
  "reserved_qty": 2,
  "low_stock_threshold": 5,
  "unavailable_dates": ["2026-12-25", "2027-01-01"]
}
```

**Response 200** — inventory object (same fields as GET inventory).

---

## 10. Seller order items

Auth: seller JWT. Lists `marketplace.order_items` for the logged-in seller, joined with order, product, and recipient shipping address.

Typical flow: customer places order → seller **lists** items → **accepts** item → calls Shippo **rates** / **labels** (section 11).

### GET `http://localhost:8081/api/v1/sellers/me/order-items`

- Auth: seller JWT
- **GET body:** none

**Response 200** — `SellerOrderItemSummary[]`

```json
[
  {
    "id": "order-item-uuid",
    "order_id": "order-uuid",
    "seller_id": "seller-uuid",
    "shop_id": "shop-uuid",
    "product_id": "product-uuid",
    "quantity": 1,
    "unit_amount": 2500,
    "total_amount": 2500,
    "fulfilment_status": "pending",
    "created_at": "...",
    "updated_at": "...",
    "order_number": "SAG-20260902-A1B2C3D4",
    "order_status": "pending_payment",
    "delivery_date": "2026-09-15T00:00:00Z",
    "product_name": "Test Gift Box",
    "product_slug": "test-gift-box",
    "product_image_url": "https://res.cloudinary.com/demo/image/upload/v1/product.jpg",
    "recipient_name": "Jane Receiver"
  }
]
```

### GET `http://localhost:8081/api/v1/sellers/me/order-items/{id}`

Example URL: `http://localhost:8081/api/v1/sellers/me/order-items/order-item-uuid`

- Auth: seller JWT
- **GET body:** none

**Response 200** — `SellerOrderItemDetails` (line item + `order` + `product` + `recipient` + `shipping_address`)

### PATCH `http://localhost:8081/api/v1/sellers/me/order-items/{id}/accept`

Example URL: `http://localhost:8081/api/v1/sellers/me/order-items/order-item-uuid/accept`

- Auth: seller JWT
- **PATCH body:** none
- Sets `fulfilment_status` from `pending` → `accepted`
- Required before Shippo rates/labels

**Response 200** — updated `OrderItem`

**Response 409**

```json
{ "error": "order item cannot be accepted in its current status" }
```

---

## 10b. Seller reels (product videos + photos)

Short-form posts shown to customers in a TikTok-style feed. Sellers upload a video (or a photo carousel) and publish. Customers browse a public feed — no login required.

### Shop reels vs product reels

Every reel belongs to a **shop**. Tagging a **product** is optional, which gives you two kinds of reel:

| Kind | How to create it | `product_id` |
|---|---|---|
| **Shop reel** — shop tour, behind the scenes, brand video | `POST /sellers/me/shops/{shopID}/reels` with no `product_id` | `null` |
| **Product reel** — demo/unboxing of one product | `POST /sellers/me/products/{productID}/reels` | set from the URL |

You can also post to the shop route **with** `product_id` in the body — same result as the product route. The product route is just shorter and validates ownership from the product itself.

Filter either kind out of the feed with `?scope=`:

| `scope` | Returns |
|---|---|
| `all` (default) | Both kinds |
| `shop` | Only reels with **no** product tagged |
| `product` | Only reels tagged to a product |

### How the data is stored

| Table | Holds |
|---|---|
| `media.media_assets` | The **files** (video/image), bucket, object path, CDN URL, mime, size |
| `seller.reels` | The **post**: caption, hashtags, visibility, status, tagged product, view count |
| `seller.reel_media` | Ordered link rows (`position` 0,1,2…) joining a reel to its files |

The reel row never stores raw file data — it points at `media.media_assets`. Deleting a reel deletes its media rows and its S3 objects.

### Upload first, then create the reel

Reel files go up through the presigned-upload endpoint, so bytes never pass through this API.

**Step 1 —** `POST http://localhost:8081/api/v1/media/presign-upload` (any JWT)

```json
{
  "filename": "reel1.mp4",
  "content_type": "video/mp4",
  "folder": "reel-video"
}
```

Valid reel folders:

| `folder` | Storage prefix |
|---|---|
| `reel-video` | `public/reels/videos` |
| `reel-photo` | `public/reels/photos` |
| `reel-thumbnail` | `public/reels/thumbnails` |

**Response 200** — `{ "upload_url": "...", "key": "public/reels/videos/uuid-reel1.mp4", "public_url": "..." }`

**Step 2 —** `PUT` the raw file to `upload_url` (header `Content-Type` must match `content_type`).

**Step 3 —** Create the reel, passing each `key` as `object_path`.

### POST `http://localhost:8081/api/v1/sellers/me/shops/{shopID}/reels`

Also: **POST** `http://localhost:8081/api/v1/sellers/me/products/{productID}/reels` — same body, but omit `product_id` (it comes from the URL and the shop is derived from the product).

- Auth: seller JWT (the shop / product must belong to you)
- **POST body:**

```json
{
  "product_id": "product-uuid-in-this-shop",
  "caption": "Unboxing our chocolate gift box!",
  "hashtags": ["#Gift", "chocolate"],
  "visibility": "public",
  "status": "published",
  "duration_ms": 15000,
  "thumbnail": {
    "object_path": "public/reels/thumbnails/cover.jpg",
    "mime_type": "image/jpeg",
    "size_bytes": 20480
  },
  "media": [
    {
      "object_path": "public/reels/videos/reel1.mp4",
      "mime_type": "video/mp4",
      "size_bytes": 8241234,
      "metadata": { "width": 1080, "height": 1920 }
    },
    {
      "object_path": "public/reels/photos/shot1.jpg",
      "mime_type": "image/jpeg",
      "size_bytes": 320145
    }
  ]
}
```

| Field | Rules |
|---|---|
| `media[]` | **Required**, 1–10 items. `mime_type` must be `image/*` or `video/*` |
| `product_id` | Optional on the shop route (must belong to that shop, else 400). Ignored on the product route |
| `caption` | Optional, trimmed |
| `hashtags` | Optional. Lowercased, `#` stripped, de-duplicated |
| `visibility` | `public` (default) or `private` |
| `status` | `draft` (default), `published`, `archived` |
| `thumbnail` | Optional cover image; must be `image/*` |

Derived automatically: `reel_type` is `video` when any file is a video (else `photo`), and `published_at` is stamped the first time `status` becomes `published`.

**Response 201** — the reel with `media[]`, `thumbnail`, `shop`, and `product` attached.

### Other seller endpoints

| Method | URL | Notes |
|---|---|---|
| GET | `/sellers/me/reels` | All your reels, any status |
| GET | `/sellers/me/shops/{shopID}/reels` | Reels for one of your shops |
| GET | `/sellers/me/products/{productID}/reels` | Reels tagged to one of your products |
| GET | `/sellers/me/reels/{id}` | One reel, any status |
| PUT | `/sellers/me/reels/{id}` | Update. Omit `media` to keep files; send `media` to **replace** them |
| DELETE | `/sellers/me/reels/{id}` | Deletes reel, media rows, and S3 objects |

### Public customer feed (no JWT)

### GET `http://localhost:8081/api/v1/reels`

Newest published reels first. Only reels that are `status: published`, `visibility: public`, and whose **shop is active** appear.

Query params: `limit` (default 20, max 50), `cursor`, `shop_id`, `product_id`, `scope` (`all` | `shop` | `product`)

**Response 200**

```json
{
  "items": [
    {
      "id": "reel-uuid",
      "reel_type": "video",
      "caption": "Unboxing our chocolate gift box!",
      "hashtags": ["gift", "chocolate"],
      "view_count": 12,
      "media": [
        {
          "media_asset_id": "asset-uuid",
          "position": 0,
          "asset_type": "video",
          "cdn_url": "https://bucket.s3.region.amazonaws.com/public/reels/videos/reel1.mp4",
          "mime_type": "video/mp4"
        }
      ],
      "shop": { "id": "shop-uuid", "name": "Reel Gift Shop", "slug": "reel-gift-shop" },
      "product": { "id": "product-uuid", "name": "Chocolate Reel Box", "price_amount": 2500, "currency": "USD" }
    }
  ],
  "next_cursor": "MjAyNi0wOS0wN1QwOTo0ODo0Ni42NDJa..."
}
```

Pass `next_cursor` back as `?cursor=` for the next page. When `next_cursor` is absent you have reached the end.

| Method | URL | Notes |
|---|---|---|
| GET | `/reels/{id}` | One published reel; **increments `view_count`** |
| GET | `/shops/{shopId}/reels` | Public reels for one shop (add `?scope=shop` for shop-only videos) |
| GET | `/products/{productId}/reels` | Public reels for one product |

Drafts, private, and archived reels return **404** on public routes.

---

## 10c. Public storefront browsing (no JWT)

The read-only endpoints a customer-facing website needs. No token, no role — but they only ever expose **active shops** and **published products**. Anything else returns 404, so a draft product can't be discovered by guessing its id.

| Method | URL | Purpose |
|---|---|---|
| GET | `/shops` | Shop directory — every `active` shop |
| GET | `/shops/{shopId}` | Shop page header |
| GET | `/shops/{shopId}/products` | Products in that shop |
| GET | `/products/{productId}` | Product page |

Both product endpoints take `?customer_type=personal|corporate` (defaults to `personal`) and honour each product's `customer_type_visibility`, so a `corporate`-only product is invisible to a personal shopper. An unknown or invalid value returns 400.

### GET `http://localhost:8081/api/v1/shops/{shopId}`

Example URL: `http://localhost:8081/api/v1/shops/shop-uuid`

**Response 200** — a `Shop` object (same shape as the seller-side shop).

```json
{
  "id": "shop-uuid",
  "seller_id": "seller-uuid",
  "name": "John Gift Shop",
  "slug": "john-gift-shop-10",
  "description": "Handmade gifts",
  "customer_visible_location": "Colombo",
  "status": "active",
  "created_at": "...",
  "updated_at": "...",
  "image_url": "https://.../shop.jpg"
}
```

Inactive or unknown shop → **404** `{ "error": "shop not found" }`.

### GET `http://localhost:8081/api/v1/products/{productId}`

Example URL: `http://localhost:8081/api/v1/products/product-uuid?customer_type=personal`

Returns the product **plus a `shop` block**, so the product page can render "sold by" without a second request.

```json
{
  "id": "product-uuid",
  "shop_id": "shop-uuid",
  "name": "Fresh Flower Basket",
  "slug": "fresh-flower-basket",
  "description": "Seasonal blooms",
  "product_type": "physical",
  "price_amount": 550000,
  "currency": "LKR",
  "status": "published",
  "occasion_tags": ["birthday"],
  "customer_type_visibility": "both",
  "points_display_enabled": false,
  "prep_minutes": 60,
  "image_url": "https://.../product.jpg",
  "created_at": "...",
  "updated_at": "...",
  "shop": {
    "id": "shop-uuid",
    "name": "John Gift Shop",
    "slug": "john-gift-shop-10",
    "image_url": "https://.../shop.jpg",
    "customer_visible_location": "Colombo"
  }
}
```

Not published, shop not active, hidden from this `customer_type`, or unknown id → **404** `{ "error": "product not found" }`.

Pair these with the reel feeds from section 10b: `/shops/{shopId}/reels` for a shop page and `/products/{productId}/reels` for a product page.

---

## 11. Shippo shipping (seller)

> **Full Postman walkthrough:** [SHIPPO_POSTMAN_TEST.md](./SHIPPO_POSTMAN_TEST.md) — copy-paste bodies for order → accept → rates → label.

Seller JWT required. Uses [Shippo test mode](https://docs.goshippo.com/docs/guides_general/testing/) when `SHIPPO_API_KEY` starts with `shippo_test_` (free, watermarked labels).

### Prerequisites

1. `.env` has `SHIPPO_API_KEY`, `S3_BUCKET`, and `SHIPPO_LABEL_BUCKET` (same bucket name as `S3_BUCKET`).
2. Restart API: `go run ./cmd/api`
3. Data from earlier sections:
   - Admin: country (for Shippo test, create **US** — see below)
   - Seller: register → login → **ship-from address** → shop linked to that address → published product
   - Customer: register → login → recipient with **shipping address** → place order
4. Copy `order-item-uuid` from the customer order response, or from `GET /sellers/me/order-items`.
5. Accept the item: `PATCH /sellers/me/order-items/{id}/accept` (section 10).

Typical flow: customer places order → seller **lists** items → **accepts** item → **POST rates** (with parcel + customs for international) → **POST label**.

### Where parcel and customs are stored

Seller-posted `parcel` and `customs_declaration` are **not** stored on products or order items. They are saved on **`marketplace.shipments`** when you call **POST `/shipping/rates`**:

| DB column | What you POST |
|---|---|
| `parcel_details` | Full `parcel` JSON object |
| `customs_declaration` | Full `customs_declaration` JSON object (international only) |
| `is_international` | `true` when ship-from country ≠ ship-to country |
| `order_item_id` | Links shipment to the order line |
| `status` | `pending` after rates → `label_created` after label |
| `provider_shipment_id` | Shippo `shipment_object_id` |
| `provider_customs_declaration_id` | Shippo customs object id (international) |

Calling **rates** again for the same order item **updates** the existing `pending` shipment row. **Labels** updates that same row with tracking and label PDF.

Verify in SQL:

```sql
SELECT id, order_item_id, status, is_international,
       parcel_details, customs_declaration,
       provider_shipment_id, provider_customs_declaration_id
FROM marketplace.shipments
WHERE order_item_id = '<order-item-uuid>'
ORDER BY created_at DESC;
```

### International vs domestic

| | Domestic (e.g. US → US) | International (e.g. US → AU) |
|---|---|---|
| `parcel` in POST body | Optional (defaults used) | **Required** |
| `customs_declaration` | Not needed | **Required** |
| Stored on shipment | `parcel_details` only | `parcel_details` + `customs_declaration` |

Addresses (ship-from / ship-to) always come from **seller shop address** and **recipient shipping address** in the database — not from the rates body.

### Recommended test addresses (Shippo test mode)

Shippo test rates work best with **real US addresses** ([testing guide](https://docs.goshippo.com/docs/guides_general/testing/)). Create a US country (admin), then use ISO country `US` on seller and recipient addresses.

**Seller ship-from** (`POST /sellers/me/addresses`):

```json
{
  "country_id": "<us-country-uuid>",
  "label": "Warehouse",
  "address_type": "return",
  "line1": "215 Clayton St",
  "city": "San Francisco",
  "region": "CA",
  "postal_code": "94117",
  "latitude": 37.769,
  "longitude": -122.429,
  "is_default": true
}
```

**Recipient shipping** (inside `POST /customers/me/recipients` `addresses[]`):

```json
{
  "country_id": "<us-country-uuid>",
  "label": "Home",
  "address_type": "shipping",
  "line1": "965 Mission St",
  "city": "San Francisco",
  "region": "CA",
  "postal_code": "94103",
  "latitude": 37.782,
  "longitude": -122.408,
  "is_default": true
}
```

Link the seller address to the shop via `address_id` when creating the shop (section 8).

### Step 1 — Get shipping rates

### POST `http://localhost:8081/api/v1/sellers/me/order-items/{orderItemID}/shipping/rates`

Example URL: `http://localhost:8081/api/v1/sellers/me/order-items/order-item-uuid/shipping/rates`

- Auth: seller JWT
- **POST body:** optional for domestic; **required** for international (parcel + customs)

**Domestic** — empty body is fine (default parcel is used):

```json
{}
```

**International** — parcel and customs are required:

```json
{
  "parcel": {
    "length": "20",
    "width": "15",
    "height": "10",
    "distance_unit": "cm",
    "weight": "1.200",
    "mass_unit": "kg"
  },
  "customs_declaration": {
    "contents_type": "MERCHANDISE",
    "non_delivery_option": "RETURN",
    "certify_signer": "Bay Area Gifts",
    "eel_pfc": "NOEEI_30_37_a",
    "incoterm": "DDU",
    "items": [
      {
        "description": "Gift Box USA",
        "quantity": 1,
        "net_weight": "1.200",
        "mass_unit": "kg",
        "value_amount": "25.00",
        "value_currency": "USD",
        "origin_country": "US",
        "tariff_number": "950300"
      }
    ]
  }
}
```

Customs fields: `contents_type`, `non_delivery_option`, `certify_signer`, and at least one `items[]` entry are required. `eel_pfc` and `incoterm` default to `NOEEI_30_37_a` and `DDU` when omitted (US → Canada uses `NOEEI_30_36`).

**Response 200**

```json
{
  "shipment_object_id": "shippo-shipment-object-id",
  "rates": [
    {
      "object_id": "rate-object-id-to-use-next",
      "provider": "USPS",
      "amount": "5.50",
      "currency": "USD",
      "estimated_days": 2,
      "duration_terms": "Delivery in 1 to 3 business days.",
      "service_name": "Priority Mail"
    }
  ]
}
```

Copy one `rates[].object_id` for the next step.

**Common errors**

| Status | Meaning |
|---|---|
| 503 | `SHIPPO_API_KEY` missing — check `.env` and restart |
| 409 | Order item not `accepted` — run `PATCH .../accept` (section 10) |
| 400 | Missing seller shop address or recipient shipping address |
| 400 | International shipment missing `parcel` or `customs_declaration` |
| 500 | Shippo rejected addresses — use valid US test addresses |

### Step 2 — Buy label

### POST `http://localhost:8081/api/v1/sellers/me/order-items/{orderItemID}/shipping/labels`

Example URL: `http://localhost:8081/api/v1/sellers/me/order-items/order-item-uuid/shipping/labels`

- Auth: seller JWT
- **POST body:**

```json
{
  "rate_object_id": "rate-object-id-from-step-1",
  "provider": "USPS",
  "idempotency_key": "label-test-001"
}
```

`idempotency_key` — any unique string per label purchase. Repeating the same key returns the cached shipment instead of buying twice.

**Response 201**

```json
{
  "id": "shipment-uuid",
  "order_id": "order-uuid",
  "seller_id": "seller-uuid",
  "courier_provider": "USPS",
  "tracking_number": "9205590164917312751089",
  "label_media_id": "media-asset-uuid",
  "delivery_mode": "courier",
  "status": "label_created",
  "provider_shipment_id": "shippo-transaction-id",
  "provider_tracking_url": "https://tools.usps.com/go/TrackConfirmAction_input?...",
  "created_at": "...",
  "updated_at": "..."
}
```

The API downloads the PDF from Shippo, uploads it to S3, and stores a `media.media_assets` row (`asset_type: label`). Order item `fulfilment_status` becomes `dispatched`.

Test labels are watermarked **SAMPLE — DO NOT MAIL**.

### Step 3 — Webhook (optional, local)

Shippo test mode does **not** send real tracking updates. You can still test your handler manually.

### POST `http://localhost:8081/api/v1/webhooks/shippo/tracking`

- Auth: none
- **POST body:**

```json
{
  "event": "track_updated",
  "test": true,
  "data": {
    "tracking_number": "9205590164917312751089",
    "tracking_status": {
      "status": "DELIVERED",
      "status_date": "2026-09-02T10:00:00Z"
    }
  }
}
```

Use the `tracking_number` from step 2.

**Response 200**

```json
{ "status": "ok" }
```

A `DELIVERED` event updates three things, in this order:

1. `marketplace.shipments.status` → `delivered` (plus `delivered_at`)
2. that shipment's `marketplace.order_items.fulfilment_status` → `delivered`
3. `marketplace.orders.status` → `delivered`, **but only if every line on the order is
   now `delivered` or `cancelled`** and at least one was delivered

Step 3 is what makes multi-seller orders correct. One order can hold items from several
sellers, each with its own shipment and tracking number. When seller A's parcel arrives,
only A's item is completed — the order header stays as it was until seller B's parcel is
also delivered. An order whose items were all cancelled never becomes `delivered`.

To check completion yourself:

```sql
SELECT o.id, o.status,
       count(*) AS total_items,
       count(*) FILTER (WHERE oi.fulfilment_status = 'delivered') AS delivered_items,
       count(*) FILTER (WHERE oi.fulfilment_status = 'cancelled') AS cancelled_items
FROM marketplace.orders o
JOIN marketplace.order_items oi ON oi.order_id = o.id
WHERE o.id = '<order-uuid>'
GROUP BY o.id, o.status;
```

### Shippo portal setup (production / ngrok)

For live webhooks from Shippo (not manual Postman), register in the [Shippo API portal](https://docs.goshippo.com/docs/tracking/webhooks/):

- URL: `https://<your-public-host>/api/v1/webhooks/shippo/tracking`
- Event: `track_updated`

For local dev, expose port 8081 with ngrok and use the ngrok HTTPS URL.

---

## URL + body cheat sheet

| Method | Full URL | Body | Response (GET) |
|---|---|---|---|
| GET | `http://localhost:8081/health` | none | `{ "status": "ok" }` |
| POST | `http://localhost:8081/api/v1/admin/register` | `{ email, password, display_name, image_url }` | — |
| POST | `http://localhost:8081/api/v1/auth/login` | `{ email, password }` | — |
| POST | `http://localhost:8081/api/v1/customers/login` | `{ email, password }` | — |
| POST | `http://localhost:8081/api/v1/sellers/login` | `{ email, password }` | — |
| GET | `http://localhost:8081/api/v1/admin/me` | none | Admin object |
| PUT | `http://localhost:8081/api/v1/admin/me` | `{ display_name, image_url }` | — |
| GET | `http://localhost:8081/api/v1/countries` | none | `Country[]` |
| GET | `http://localhost:8081/api/v1/countries/{id}` | none (id in URL) | `Country` |
| POST | `http://localhost:8081/api/v1/admin/countries` | `{ "iso_code": "LK", "name": "Sri Lanka", "default_currency": "LKR", "default_timezone": "Asia/Colombo", "status": "active" }` | `Country` |
| PUT | `http://localhost:8081/api/v1/admin/countries/{id}` | same as POST | `Country` |
| DELETE | `http://localhost:8081/api/v1/admin/countries/{id}` | none (id in URL) | `{ "message": "country deleted" }` |
| GET | `http://localhost:8081/api/v1/admin/country-capabilities` | none | `CountryCapabilityDetails[]` |
| GET | `http://localhost:8081/api/v1/admin/countries/{id}/capabilities` | none | `CountryCapabilityDetails` |
| POST | `http://localhost:8081/api/v1/admin/countries/{id}/capabilities` | boolean flags (see section 5) | `CountryCapability` |
| PUT | `http://localhost:8081/api/v1/admin/countries/{id}/capabilities` | boolean flags (see section 5) | `CountryCapability` |
| DELETE | `http://localhost:8081/api/v1/admin/countries/{id}/capabilities` | none | `{ "message": "country capability deleted" }` |
| GET | `http://localhost:8081/api/v1/shops` | none | `Shop[]` |
| GET | `http://localhost:8081/api/v1/shops/{shopId}` | none (id in URL) | `Shop` |
| GET | `http://localhost:8081/api/v1/shops/{shopId}/products` | none (use `?customer_type=personal|corporate`) | `Product[]` |
| GET | `http://localhost:8081/api/v1/products/{productId}` | none (use `?customer_type=personal|corporate`) | `Product` + `shop` |
| POST | `http://localhost:8081/api/v1/customers/register` | see customer register body | — |
| GET | `http://localhost:8081/api/v1/customers/me` | none | `CustomerDetails` (profile + `addresses[]`) |
| PUT | `http://localhost:8081/api/v1/customers/me` | `{ country_id, phone, display_name, customer_type, date_of_birth, status, image_url }` | — |
| DELETE | `http://localhost:8081/api/v1/customers/me` | none | — |
| POST | `http://localhost:8081/api/v1/customers/me/addresses` | address body | — |
| DELETE | `http://localhost:8081/api/v1/customers/me/addresses/{id}` | none (id in URL) | — |
| GET | `http://localhost:8081/api/v1/customers/me/saved-gifts` | none | `SavedGiftDetails[]` (each includes `product`) |
| POST | `http://localhost:8081/api/v1/customers/me/saved-gifts` | `{ product_id }` | — |
| DELETE | `http://localhost:8081/api/v1/customers/me/saved-gifts/{id}` | none (saved-gift id in URL) | — |
| POST | `http://localhost:8081/api/v1/customers/me/recipients` | recipient body + optional `addresses` | — |
| GET | `http://localhost:8081/api/v1/customers/me/recipients` | none | `Recipient[]` (no nested addresses) |
| GET | `http://localhost:8081/api/v1/customers/me/recipients/{id}` | none (recipient id in URL) | `RecipientDetails` (recipient + `addresses[]`) |
| PUT | `http://localhost:8081/api/v1/customers/me/recipients/{id}` | person fields + `default_address_id` (no address edit) | — |
| DELETE | `http://localhost:8081/api/v1/customers/me/recipients/{id}` | none (recipient id in URL) | — |
| POST | `http://localhost:8081/api/v1/customers/me/recipients/{id}/addresses` | address body | — |
| PUT | `http://localhost:8081/api/v1/customers/me/recipients/{id}/addresses/{addressId}` | address body | — |
| DELETE | `http://localhost:8081/api/v1/customers/me/recipients/{id}/addresses/{addressId}` | none (ids in URL) | `{ "message": "address deleted" }` |
| POST | `http://localhost:8081/api/v1/customers/me/orders` | `{ recipient_id, country_id, customer_type, delivery_date, gift_message, delivery_amount, items }` | `OrderDetails` |
| GET | `http://localhost:8081/api/v1/customers/me/orders` | none | `Order[]` (no items) |
| GET | `http://localhost:8081/api/v1/customers/me/orders/{id}` | none (order id in URL) | `OrderDetails` (order + `items[]`) |
| POST | `http://localhost:8081/api/v1/customers/me/orders/{id}/cancel` | none | cancelled `OrderDetails` |
| POST | `http://localhost:8081/api/v1/sellers/register` | see seller register body | — |
| GET | `http://localhost:8081/api/v1/sellers/me` | none | `SellerDetails` (profile + addresses + shops) |
| PUT | `http://localhost:8081/api/v1/sellers/me` | `{ country_id, seller_type, legal_name, trading_name, phone, image_url }` | — |
| DELETE | `http://localhost:8081/api/v1/sellers/me` | none | — |
| POST | `http://localhost:8081/api/v1/sellers/me/addresses` | address body | — |
| DELETE | `http://localhost:8081/api/v1/sellers/me/addresses/{id}` | none (id in URL) | — |
| GET | `http://localhost:8081/api/v1/sellers/me/shops` | none | `Shop[]` (your shops, any status) |
| POST | `http://localhost:8081/api/v1/sellers/me/shops` | shop body | — |
| PUT | `http://localhost:8081/api/v1/sellers/me/shops/{id}` | shop body | — |
| DELETE | `http://localhost:8081/api/v1/sellers/me/shops/{id}` | none (id in URL) | — |
| GET | `http://localhost:8081/api/v1/sellers/me/shops/{shopID}/products` | none (shopID in URL) | `Product[]` |
| POST | `http://localhost:8081/api/v1/sellers/me/shops/{shopID}/products` | product + optional inventory | — |
| GET | `http://localhost:8081/api/v1/sellers/me/products/{id}` | none (id in URL) | `Product` |
| PUT | `http://localhost:8081/api/v1/sellers/me/products/{id}` | product body | — |
| DELETE | `http://localhost:8081/api/v1/sellers/me/products/{id}` | none (id in URL) | — |
| GET | `http://localhost:8081/api/v1/sellers/me/products/{id}/inventory` | none (id in URL) | `Inventory` |
| PUT | `http://localhost:8081/api/v1/sellers/me/products/{id}/inventory` | `{ available_qty, reserved_qty, low_stock_threshold, unavailable_dates }` | — |
| GET | `http://localhost:8081/api/v1/sellers/me/order-items` | none | `SellerOrderItemSummary[]` |
| GET | `http://localhost:8081/api/v1/sellers/me/order-items/{id}` | none | `SellerOrderItemDetails` |
| PATCH | `http://localhost:8081/api/v1/sellers/me/order-items/{id}/accept` | none | `OrderItem` |
| POST | `http://localhost:8081/api/v1/media/presign-upload` | `{ filename, content_type, folder }` | `{ upload_url, key, public_url }` |
| GET | `http://localhost:8081/api/v1/media/url?key=...` | none | `{ url }` (15 min signed) |
| GET | `http://localhost:8081/api/v1/reels` | none (`?limit=&cursor=&shop_id=&product_id=&scope=`) | `{ items[], next_cursor }` |
| GET | `http://localhost:8081/api/v1/reels/{id}` | none (id in URL) | `ReelDetails` (counts a view) |
| GET | `http://localhost:8081/api/v1/shops/{shopId}/reels` | none (`?scope=shop` for shop-only) | `{ items[], next_cursor }` |
| GET | `http://localhost:8081/api/v1/products/{productId}/reels` | none | `{ items[], next_cursor }` |
| POST | `http://localhost:8081/api/v1/sellers/me/shops/{shopID}/reels` | `{ media[], product_id?, caption?, hashtags?, visibility?, status?, thumbnail? }` | `ReelDetails` |
| GET | `http://localhost:8081/api/v1/sellers/me/shops/{shopID}/reels` | none | `ReelDetails[]` |
| POST | `http://localhost:8081/api/v1/sellers/me/products/{productID}/reels` | same as shop reel (no `product_id`) | `ReelDetails` |
| GET | `http://localhost:8081/api/v1/sellers/me/products/{productID}/reels` | none | `ReelDetails[]` |
| GET | `http://localhost:8081/api/v1/sellers/me/reels` | none | `ReelDetails[]` |
| GET | `http://localhost:8081/api/v1/sellers/me/reels/{id}` | none (id in URL) | `ReelDetails` |
| PUT | `http://localhost:8081/api/v1/sellers/me/reels/{id}` | same as create (`media` optional) | `ReelDetails` |
| DELETE | `http://localhost:8081/api/v1/sellers/me/reels/{id}` | none (id in URL) | `{ "message": "reel deleted" }` |
| POST | `http://localhost:8081/api/v1/sellers/me/order-items/{orderItemID}/shipping/rates` | optional `{ parcel, customs_declaration }` (required international) | `{ shipment_object_id, rates[] }` |
| POST | `http://localhost:8081/api/v1/sellers/me/order-items/{orderItemID}/shipping/labels` | `{ rate_object_id, provider, idempotency_key }` | `Shipment` |
| POST | `http://localhost:8081/api/v1/webhooks/shippo/tracking` | Shippo `track_updated` payload | `{ "status": "ok" }` |

GET and DELETE never take a JSON body. IDs always go in the URL. See each section above for full JSON examples.
