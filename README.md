# SendAGift API

Go REST API for admin, countries, customers, recipients, orders, sellers, shops, products, inventory, and saved gifts.

Base URL: `http://localhost:8081`

## Run

```powershell
go run ./cmd/api
```

Migrations run on startup.

```powershell
go run ./cmd/migrate
```

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

---

## 5. Country capabilities

Admin-only CRUD for `core.country_capabilities` (country feature gates).  
One capability row per country. **`{id}` in the URL is the `country_id`** (FK to `core.countries`).

**Postman headers**

| Header | Value |
|---|---|
| `Content-Type` | `application/json` |
| `Authorization` | `Bearer <admin_token>` |

**Request body (POST and PUT)** — boolean flags only; `country_id` comes from the URL

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
| `country_id` | In URL only — do not send in body |

### GET `http://localhost:8081/api/v1/admin/country-capabilities`

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

Example URL: `http://localhost:8081/api/v1/admin/countries/3478b972-3c85-49ea-ac32-7afcace17129/capabilities`

- Auth: admin JWT
- **Body:** none
- `{id}` = `country_id`

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

Example URL: `http://localhost:8081/api/v1/admin/countries/3478b972-3c85-49ea-ac32-7afcace17129/capabilities`

- Auth: admin JWT
- **Body:** boolean flags above
- `{id}` = `country_id` (must exist in `core.countries`)

**Response 201** — created capability object.

### PUT `http://localhost:8081/api/v1/admin/countries/{id}/capabilities`

Example URL: `http://localhost:8081/api/v1/admin/countries/3478b972-3c85-49ea-ac32-7afcace17129/capabilities`

- Auth: admin JWT
- **Body:** boolean flags above
- `{id}` = `country_id`
- `rule_version` increments automatically on each update

**Response 200** — updated capability object.

### DELETE `http://localhost:8081/api/v1/admin/countries/{id}/capabilities`

Example URL: `http://localhost:8081/api/v1/admin/countries/3478b972-3c85-49ea-ac32-7afcace17129/capabilities`

- Auth: admin JWT
- **Body:** none
- `{id}` = `country_id`

**Response 200**

```json
{ "message": "country capability deleted" }
```

**Postman quick copy**

| Method | URL | Body |
|---|---|---|
| GET | `http://localhost:8081/api/v1/admin/country-capabilities` | none |
| GET | `http://localhost:8081/api/v1/admin/countries/{country_id}/capabilities` | none |
| POST | `http://localhost:8081/api/v1/admin/countries/{country_id}/capabilities` | boolean flags above |
| PUT | `http://localhost:8081/api/v1/admin/countries/{country_id}/capabilities` | boolean flags above |
| DELETE | `http://localhost:8081/api/v1/admin/countries/{country_id}/capabilities` | none |

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
| GET | `http://localhost:8081/api/v1/admin/countries/{country_id}/capabilities` | none | `CountryCapabilityDetails` |
| POST | `http://localhost:8081/api/v1/admin/countries/{country_id}/capabilities` | boolean flags (see section 5) | `CountryCapability` |
| PUT | `http://localhost:8081/api/v1/admin/countries/{country_id}/capabilities` | boolean flags (see section 5) | `CountryCapability` |
| DELETE | `http://localhost:8081/api/v1/admin/countries/{country_id}/capabilities` | none | `{ "message": "country capability deleted" }` |
| GET | `http://localhost:8081/api/v1/shops` | none | `Shop[]` |
| GET | `http://localhost:8081/api/v1/shops/{shopId}/products` | none (use `?customer_type=personal|corporate`) | `Product[]` |
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

GET and DELETE never take a JSON body. IDs always go in the URL. See each section above for full JSON examples.
