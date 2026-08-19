# SendAGift API

Go REST API for admin, countries, customers, sellers, shops, products, inventory, and saved gifts.

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

### GET `http://localhost:8081/api/v1/countries`

- Auth: none
- **GET body:** none

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
- **GET body:** none
- ID in URL

**Response 200** — one country object (same fields as above).

### POST `http://localhost:8081/api/v1/admin/countries`

- Auth: admin JWT
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

**Response 201** — country object with `id`. Use that `id` as `country_id` for customers and sellers.

### PUT `http://localhost:8081/api/v1/admin/countries/{id}`

Example URL: `http://localhost:8081/api/v1/admin/countries/3478b972-3c85-49ea-ac32-7afcace17129`

- Auth: admin JWT
- **PUT body:**

```json
{
  "iso_code": "LK",
  "name": "Sri Lanka",
  "default_currency": "LKR",
  "default_timezone": "Asia/Colombo",
  "status": "active"
}
```

**Response 200** — updated country object.

### DELETE `http://localhost:8081/api/v1/admin/countries/{id}`

Example URL: `http://localhost:8081/api/v1/admin/countries/3478b972-3c85-49ea-ac32-7afcace17129`

- Auth: admin JWT
- **DELETE body:** none
- ID in URL

**Response 200**

```json
{ "message": "country deleted" }
```

---

## 5. Customers

### POST `http://localhost:8081/api/v1/customers/register`

- Auth: none
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

---

## 6. Sellers

### POST `http://localhost:8081/api/v1/sellers/register`

- Auth: none
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

## 7. Products and inventory

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

| Method | Full URL | Body |
|---|---|---|
| GET | `http://localhost:8081/health` | none |
| POST | `http://localhost:8081/api/v1/admin/register` | `{ email, password, display_name, image_url }` |
| POST | `http://localhost:8081/api/v1/auth/login` | `{ email, password }` |
| POST | `http://localhost:8081/api/v1/customers/login` | `{ email, password }` |
| POST | `http://localhost:8081/api/v1/sellers/login` | `{ email, password }` |
| GET | `http://localhost:8081/api/v1/admin/me` | none |
| PUT | `http://localhost:8081/api/v1/admin/me` | `{ display_name, image_url }` |
| GET | `http://localhost:8081/api/v1/countries` | none |
| GET | `http://localhost:8081/api/v1/countries/{id}` | none (id in URL) |
| POST | `http://localhost:8081/api/v1/admin/countries` | `{ iso_code, name, default_currency, default_timezone, status }` |
| PUT | `http://localhost:8081/api/v1/admin/countries/{id}` | same as POST |
| DELETE | `http://localhost:8081/api/v1/admin/countries/{id}` | none (id in URL) |
| POST | `http://localhost:8081/api/v1/customers/register` | see customer register body |
| GET | `http://localhost:8081/api/v1/customers/me` | none |
| PUT | `http://localhost:8081/api/v1/customers/me` | `{ country_id, phone, display_name, customer_type, date_of_birth, status, image_url }` |
| DELETE | `http://localhost:8081/api/v1/customers/me` | none |
| POST | `http://localhost:8081/api/v1/customers/me/addresses` | address body |
| DELETE | `http://localhost:8081/api/v1/customers/me/addresses/{id}` | none (id in URL) |
| GET | `http://localhost:8081/api/v1/customers/me/saved-gifts` | none |
| POST | `http://localhost:8081/api/v1/customers/me/saved-gifts` | `{ product_id }` |
| DELETE | `http://localhost:8081/api/v1/customers/me/saved-gifts/{id}` | none (saved-gift id in URL) |
| POST | `http://localhost:8081/api/v1/sellers/register` | see seller register body |
| GET | `http://localhost:8081/api/v1/sellers/me` | none |
| PUT | `http://localhost:8081/api/v1/sellers/me` | `{ country_id, seller_type, legal_name, trading_name, phone, image_url }` |
| DELETE | `http://localhost:8081/api/v1/sellers/me` | none |
| POST | `http://localhost:8081/api/v1/sellers/me/addresses` | address body |
| DELETE | `http://localhost:8081/api/v1/sellers/me/addresses/{id}` | none (id in URL) |
| POST | `http://localhost:8081/api/v1/sellers/me/shops` | shop body |
| PUT | `http://localhost:8081/api/v1/sellers/me/shops/{id}` | shop body |
| DELETE | `http://localhost:8081/api/v1/sellers/me/shops/{id}` | none (id in URL) |
| GET | `http://localhost:8081/api/v1/sellers/me/shops/{shopID}/products` | none (shopID in URL) |
| POST | `http://localhost:8081/api/v1/sellers/me/shops/{shopID}/products` | product + optional inventory |
| GET | `http://localhost:8081/api/v1/sellers/me/products/{id}` | none (id in URL) |
| PUT | `http://localhost:8081/api/v1/sellers/me/products/{id}` | product body |
| DELETE | `http://localhost:8081/api/v1/sellers/me/products/{id}` | none (id in URL) |
| GET | `http://localhost:8081/api/v1/sellers/me/products/{id}/inventory` | none (id in URL) |
| PUT | `http://localhost:8081/api/v1/sellers/me/products/{id}/inventory` | `{ available_qty, reserved_qty, low_stock_threshold, unavailable_dates }` |

GET and DELETE never take a JSON body. IDs always go in the URL.
