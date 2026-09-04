# Shippo + Order flow — Postman test guide

End-to-end test: **place a customer order**, then **seller buys a Shippo label**.

Base URL: `http://localhost:8081`

## Before you start

1. API running: `go run ./cmd/api`
2. `.env` configured:

```env
SHIPPO_API_KEY=shippo_test_your_key_here
S3_BUCKET=your-s3-bucket
SHIPPO_LABEL_BUCKET=your-s3-bucket
```

3. Postman headers (when noted):

| Header | Value |
|---|---|
| `Content-Type` | `application/json` |
| `Authorization` | `Bearer <token>` |

---

## Variables to save

As you go, copy IDs from responses into these placeholders:

| Variable | From |
|---|---|
| `admin_token` | Admin login |
| `us_country_id` | Create US country |
| `seller_token` | Seller login |
| `seller_address_id` | Seller warehouse address |
| `shop_id` | Create shop |
| `product_id` | Create product |
| `customer_token` | Customer login |
| `recipient_id` | Create recipient |
| `order_id` | Place order → `id` |
| `order_item_id` | Place order → `items[0].id` (or seller GET order-items) |
| `rate_object_id` | Get shipping rates → `rates[0].object_id` |

---

## Part A — Setup (admin + seller + product)

### A1. Create admin (first time only)

**POST** `http://localhost:8081/api/v1/admin/register`

Body:

```json
{
  "email": "admin@sendagift.com",
  "password": "password123",
  "display_name": "Super Admin",
  "image_url": "https://res.cloudinary.com/demo/image/upload/v1/admin.jpg"
}
```

If admin already exists, skip to A2.

---

### A2. Admin login

**POST** `http://localhost:8081/api/v1/auth/login`

Body:

```json
{
  "email": "admin@sendagift.com",
  "password": "password123"
}
```

Save `token` → `admin_token`

---

### A3. Create United States country (for Shippo test rates)

**POST** `http://localhost:8081/api/v1/admin/countries`

Headers: `Authorization: Bearer {{admin_token}}`

Body:

```json
{
  "iso_code": "US",
  "name": "United States",
  "default_currency": "USD",
  "default_timezone": "America/Los_Angeles",
  "status": "full"
}
```

Save `id` → `us_country_id`

> Shippo test mode works best with **US addresses**. Skip if you already have a US country — use `GET /api/v1/countries` to find its `id`.

---

### A4. Register seller

**POST** `http://localhost:8081/api/v1/sellers/register`

Body:

```json
{
  "country_id": "{{us_country_id}}",
  "seller_type": "individual",
  "legal_name": "Shippo Test Seller",
  "trading_name": "Shippo Gifts",
  "email": "shippo-seller@test.com",
  "password": "password123",
  "phone": "+14155550100",
  "image_url": "https://res.cloudinary.com/demo/image/upload/v1/seller.jpg"
}
```

---

### A5. Seller login

**POST** `http://localhost:8081/api/v1/sellers/login`

Body:

```json
{
  "email": "shippo-seller@test.com",
  "password": "password123"
}
```

Save `token` → `seller_token`

---

### A6. Seller ship-from address (warehouse)

**POST** `http://localhost:8081/api/v1/sellers/me/addresses`

Headers: `Authorization: Bearer {{seller_token}}`

Body:

```json
{
  "country_id": "{{us_country_id}}",
  "label": "Warehouse",
  "address_type": "return",
  "line1": "215 Clayton St",
  "line2": null,
  "city": "San Francisco",
  "region": "CA",
  "postal_code": "94117",
  "latitude": 37.769,
  "longitude": -122.429,
  "is_default": true
}
```

Save `id` → `seller_address_id`

---

### A7. Create shop (linked to warehouse address)

**POST** `http://localhost:8081/api/v1/sellers/me/shops`

Headers: `Authorization: Bearer {{seller_token}}`

Body:

```json
{
  "name": "Shippo Gifts",
  "slug": "shippo-gifts",
  "description": "Test shop for Shippo",
  "return_address_mode": "shop",
  "customer_visible_location": "San Francisco",
  "status": "active",
  "address_id": "{{seller_address_id}}",
  "image_url": "https://res.cloudinary.com/demo/image/upload/v1/shop.jpg"
}
```

Save `id` → `shop_id`

---

### A8. Create product (published, USD)

**POST** `http://localhost:8081/api/v1/sellers/me/shops/{{shop_id}}/products`

Headers: `Authorization: Bearer {{seller_token}}`

Body:

```json
{
  "name": "Test Gift Box",
  "slug": "test-gift-box",
  "description": "Small gift box for Shippo test",
  "product_type": "gift",
  "price_amount": 2500,
  "currency": "USD",
  "status": "published",
  "occasion_tags": ["birthday"],
  "customer_type_visibility": "both",
  "points_display_enabled": false,
  "prep_minutes": 60,
  "image_url": "https://res.cloudinary.com/demo/image/upload/v1/product.jpg",
  "inventory": {
    "available_qty": 50,
    "reserved_qty": 0,
    "low_stock_threshold": 5,
    "unavailable_dates": []
  }
}
```

Save `id` → `product_id`

> `price_amount` is in **minor units** (`2500` = USD 25.00). Product must be `published` and shop `active` for checkout.

---

## Part B — Customer places order

### B1. Register customer

**POST** `http://localhost:8081/api/v1/customers/register`

Body:

```json
{
  "country_id": "{{us_country_id}}",
  "email": "shippo-customer@test.com",
  "password": "password123",
  "phone": "+14155550200",
  "display_name": "Shippo Buyer",
  "customer_type": "individual",
  "date_of_birth": "1990-01-15",
  "image_url": "https://res.cloudinary.com/demo/image/upload/v1/customer.jpg",
  "addresses": []
}
```

---

### B2. Customer login

**POST** `http://localhost:8081/api/v1/customers/login`

Body:

```json
{
  "email": "shippo-customer@test.com",
  "password": "password123"
}
```

Save `token` → `customer_token`

---

### B3. Create recipient with US shipping address

**POST** `http://localhost:8081/api/v1/customers/me/recipients`

Headers: `Authorization: Bearer {{customer_token}}`

Body:

```json
{
  "name": "Jane Receiver",
  "relationship": "friend",
  "email": "jane.receiver@example.com",
  "phone": "+14155550300",
  "image_url": "https://res.cloudinary.com/demo/image/upload/v1/recipient.jpg",
  "preferences": {},
  "addresses": [
    {
      "country_id": "{{us_country_id}}",
      "label": "Home",
      "address_type": "shipping",
      "line1": "965 Mission St",
      "line2": null,
      "city": "San Francisco",
      "region": "CA",
      "postal_code": "94103",
      "latitude": 37.782,
      "longitude": -122.408,
      "is_default": true
    }
  ]
}
```

Save `id` → `recipient_id`

---

### B3a. List recipients (customer)

**GET** `http://localhost:8081/api/v1/customers/me/recipients`

Headers: `Authorization: Bearer {{customer_token}}`

Body: none

**Response 200** — array of recipients (no nested `addresses` on list).

```json
[
  {
    "id": "recipient-uuid",
    "customer_id": "customer-uuid",
    "name": "Jane Receiver",
    "relationship": "friend",
    "email": "jane.receiver@example.com",
    "phone": "+14155550300",
    "default_address_id": "recipient-address-uuid",
    "preferences": {},
    "created_at": "...",
    "updated_at": "..."
  }
]
```

---

### B3b. Get one recipient (with shipping address)

**GET** `http://localhost:8081/api/v1/customers/me/recipients/{{recipient_id}}`

Headers: `Authorization: Bearer {{customer_token}}`

Body: none

**Response 200** — `RecipientDetails` (recipient + `addresses[]`). Confirm `address_type: "shipping"` and US `country_id` before placing an order.

```json
{
  "id": "recipient-uuid",
  "customer_id": "customer-uuid",
  "name": "Jane Receiver",
  "relationship": "friend",
  "email": "jane.receiver@example.com",
  "phone": "+14155550300",
  "default_address_id": "recipient-address-uuid",
  "preferences": {},
  "created_at": "...",
  "updated_at": "...",
  "addresses": [
    {
      "id": "recipient-address-uuid",
      "recipient_id": "recipient-uuid",
      "country_id": "us-country-uuid",
      "label": "Home",
      "address_type": "shipping",
      "line1": "965 Mission St",
      "line2": null,
      "city": "San Francisco",
      "region": "CA",
      "postal_code": "94103",
      "latitude": 37.782,
      "longitude": -122.408,
      "is_default": true,
      "created_at": "...",
      "updated_at": "..."
    }
  ]
}
```

This is the **ship-to** address Shippo uses (default or first recipient address).

---

### B4. Place order

**POST** `http://localhost:8081/api/v1/customers/me/orders`

Headers: `Authorization: Bearer {{customer_token}}`

Body:

```json
{
  "recipient_id": "{{recipient_id}}",
  "country_id": "{{us_country_id}}",
  "customer_type": "personal",
  "delivery_date": "2026-09-15",
  "gift_message": "Happy birthday!",
  "delivery_amount": 0,
  "items": [
    {
      "product_id": "{{product_id}}",
      "quantity": 1
    }
  ]
}
```

From the response, save:

- `id` → `order_id`
- `items[0].id` → `order_item_id`
- `items[0].seller_id` (must match your seller)

Example item in response:

```json
{
  "id": "order-uuid",
  "order_number": "SAG-20260902-A1B2C3D4",
  "status": "pending_payment",
  "items": [
    {
      "id": "order-item-uuid",
      "fulfilment_status": "pending",
      "seller_id": "seller-uuid",
      "product_id": "product-uuid",
      "quantity": 1
    }
  ]
}
```

---

### B4a. List customer orders

**GET** `http://localhost:8081/api/v1/customers/me/orders`

Headers: `Authorization: Bearer {{customer_token}}`

Body: none

**Response 200** — order headers only (no `items` array).

```json
[
  {
    "id": "order-uuid",
    "order_number": "SAG-20260902-A1B2C3D4",
    "customer_id": "customer-uuid",
    "recipient_id": "recipient-uuid",
    "country_id": "us-country-uuid",
    "customer_type": "personal",
    "delivery_date": "2026-09-15T00:00:00Z",
    "status": "pending_payment",
    "subtotal_amount": 2500,
    "delivery_amount": 0,
    "total_amount": 2500,
    "currency": "USD",
    "gift_message": "Happy birthday!",
    "created_at": "...",
    "updated_at": "..."
  }
]
```

---

### B4b. Get one customer order (with items)

**GET** `http://localhost:8081/api/v1/customers/me/orders/{{order_id}}`

Headers: `Authorization: Bearer {{customer_token}}`

Body: none

**Response 200** — full order + `items[]`. Use `items[0].id` as `order_item_id` if you did not save it from B4.

```json
{
  "id": "order-uuid",
  "order_number": "SAG-20260902-A1B2C3D4",
  "status": "pending_payment",
  "currency": "USD",
  "items": [
    {
      "id": "order-item-uuid",
      "order_id": "order-uuid",
      "seller_id": "seller-uuid",
      "shop_id": "shop-uuid",
      "product_id": "product-uuid",
      "quantity": 1,
      "unit_amount": 2500,
      "total_amount": 2500,
      "fulfilment_status": "pending"
    }
  ]
}
```

---

## Part B2 — Seller views orders (before Shippo)

### B5. Seller lists order items

**GET** `http://localhost:8081/api/v1/sellers/me/order-items`

Headers: `Authorization: Bearer {{seller_token}}`

Body: none

**Response 200** — all line items for this seller (newest first).

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
    "order_number": "SAG-20260902-A1B2C3D4",
    "order_status": "pending_payment",
    "delivery_date": "2026-09-15T00:00:00Z",
    "product_name": "Gift Box USA",
    "product_slug": "gift-box-usa",
    "recipient_name": "Jane Receiver"
  }
]
```

Copy `id` where `fulfilment_status` is `pending` → `order_item_id`.

---

### B5b. Seller gets one order item (full detail)

**GET** `http://localhost:8081/api/v1/sellers/me/order-items/{{order_item_id}}`

Headers: `Authorization: Bearer {{seller_token}}`

Body: none

**Response 200** — line item + order + product + recipient + `shipping_address` (ship-to for Shippo).

```json
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
  "order": {
    "id": "order-uuid",
    "order_number": "SAG-20260902-A1B2C3D4",
    "status": "pending_payment",
    "delivery_date": "2026-09-15T00:00:00Z",
    "gift_message": "Happy birthday!",
    "currency": "USD"
  },
  "product": {
    "id": "product-uuid",
    "name": "Gift Box USA",
    "slug": "gift-box-usa",
    "price_amount": 2500,
    "currency": "USD",
    "status": "published"
  },
  "recipient": {
    "id": "recipient-uuid",
    "name": "Jane Receiver",
    "email": "jane.receiver@example.com",
    "phone": "+14155550300"
  },
  "shipping_address": {
    "id": "recipient-address-uuid",
    "line1": "965 Mission St",
    "city": "San Francisco",
    "region": "CA",
    "postal_code": "94103",
    "address_type": "shipping",
    "country_id": "us-country-uuid"
  }
}
```

Ship-from for Shippo comes from the **shop** `address_id` (seller warehouse), not this object.

---

### B6. Accept order item (seller)

**PATCH** `http://localhost:8081/api/v1/sellers/me/order-items/{{order_item_id}}/accept`

Headers: `Authorization: Bearer {{seller_token}}`

Body: none

**Response 200**

```json
{
  "id": "order-item-uuid",
  "order_id": "order-uuid",
  "seller_id": "seller-uuid",
  "shop_id": "shop-uuid",
  "product_id": "product-uuid",
  "quantity": 1,
  "unit_amount": 2500,
  "total_amount": 2500,
  "fulfilment_status": "accepted",
  "created_at": "...",
  "updated_at": "..."
}
```

Only works when `fulfilment_status` is `pending`. After accept, you can call Shippo rates/labels.

---

## Part C — Seller Shippo shipping

### C1. Get shipping rates

**POST** `http://localhost:8081/api/v1/sellers/me/order-items/{{order_item_id}}/shipping/rates`

Headers: `Authorization: Bearer {{seller_token}}`

**Domestic (US → US)** — body optional (empty `{}` is fine).

**International (US → AU, etc.)** — `parcel` and `customs_declaration` are **required**:

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

Posted `parcel` and `customs_declaration` are stored on `marketplace.shipments` (`parcel_details`, `customs_declaration`) when rates are fetched.

**Response 200** (example):

```json
{
  "shipment_object_id": "abc123...",
  "rates": [
    {
      "object_id": "rate-uuid-here",
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

Save one `rates[].object_id` → `rate_object_id`

**Errors**

| Status | Fix |
|---|---|
| 503 | Set `SHIPPO_API_KEY` in `.env`, restart API |
| 409 | Run B6 accept (`PATCH .../accept`) |
| 400 | Check seller shop address + recipient shipping address exist |
| 400 | International: include `parcel` + `customs_declaration` in rates body |
| 500 | Use valid US addresses (steps A6 + B3) |

---

### C2. Buy shipping label

**POST** `http://localhost:8081/api/v1/sellers/me/order-items/{{order_item_id}}/shipping/labels`

Headers: `Authorization: Bearer {{seller_token}}`

Body:

```json
{
  "rate_object_id": "{{rate_object_id}}",
  "provider": "USPS",
  "idempotency_key": "shippo-test-label-001"
}
```

Use a **new** `idempotency_key` for each new label. Reusing the same key returns the cached shipment (no double charge).

**Response 201** (example):

```json
{
  "id": "shipment-uuid",
  "order_id": "order-uuid",
  "seller_id": "seller-uuid",
  "courier_provider": "USPS",
  "tracking_number": "9205590164917312751089",
  "label_media_id": "media-uuid",
  "delivery_mode": "courier",
  "status": "label_created",
  "provider_shipment_id": "shippo-transaction-id",
  "provider_tracking_url": "https://tools.usps.com/go/TrackConfirmAction_input?...",
  "created_at": "...",
  "updated_at": "..."
}
```

Test labels are watermarked **SAMPLE — DO NOT MAIL**. PDF is stored in S3; order item becomes `dispatched`.

---

### C3. Test tracking webhook (optional)

Shippo test mode does not push real tracking events. Simulate locally:

**POST** `http://localhost:8081/api/v1/webhooks/shippo/tracking`

Headers: `Content-Type: application/json`  
Auth: none

Body (use `tracking_number` from C2):

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

**Response 200**

```json
{ "status": "ok" }
```

---

## Quick reference — all URLs in order

| # | Method | URL | Auth | Body |
|---|---|---|---|---|
| A1 | POST | `/api/v1/admin/register` | none | admin register JSON |
| A2 | POST | `/api/v1/auth/login` | none | email + password |
| A3 | POST | `/api/v1/admin/countries` | admin | US country JSON |
| A4 | POST | `/api/v1/sellers/register` | none | seller register JSON |
| A5 | POST | `/api/v1/sellers/login` | none | email + password |
| A6 | POST | `/api/v1/sellers/me/addresses` | seller | US warehouse JSON |
| A7 | POST | `/api/v1/sellers/me/shops` | seller | shop + `address_id` |
| A8 | POST | `/api/v1/sellers/me/shops/{shop_id}/products` | seller | published product JSON |
| B1 | POST | `/api/v1/customers/register` | none | customer register JSON |
| B2 | POST | `/api/v1/customers/login` | none | email + password |
| B3 | POST | `/api/v1/customers/me/recipients` | customer | recipient + US address |
| B3a | GET | `/api/v1/customers/me/recipients` | customer | none |
| B3b | GET | `/api/v1/customers/me/recipients/{recipient_id}` | customer | none |
| B4 | POST | `/api/v1/customers/me/orders` | customer | order JSON |
| B4a | GET | `/api/v1/customers/me/orders` | customer | none |
| B4b | GET | `/api/v1/customers/me/orders/{order_id}` | customer | none |
| B5 | GET | `/api/v1/sellers/me/order-items` | seller | none |
| B5b | GET | `/api/v1/sellers/me/order-items/{order_item_id}` | seller | none |
| B6 | PATCH | `/api/v1/sellers/me/order-items/{order_item_id}/accept` | seller | none |
| C1 | POST | `/api/v1/sellers/me/order-items/{order_item_id}/shipping/rates` | seller | optional `{ parcel, customs_declaration }` (required intl) |
| C2 | POST | `/api/v1/sellers/me/order-items/{order_item_id}/shipping/labels` | seller | rate + idempotency_key |
| C3 | POST | `/api/v1/webhooks/shippo/tracking` | none | track_updated JSON |

Prefix every URL with `http://localhost:8081`.

---

## Postman collection tip

Create an environment with:

```json
{
  "base_url": "http://localhost:8081",
  "admin_token": "",
  "seller_token": "",
  "customer_token": "",
  "us_country_id": "",
  "seller_address_id": "",
  "shop_id": "",
  "product_id": "",
  "recipient_id": "",
  "order_id": "",
  "order_item_id": "",
  "rate_object_id": ""
}
```

Use `{{base_url}}/api/v1/...` in requests and paste tokens/IDs after each step.

---

## Troubleshooting

| Problem | Solution |
|---|---|
| `shipping provider not configured` | Add `SHIPPO_API_KEY` to `.env`, restart |
| `order item is not ready for shipping` | Run B6 accept endpoint |
| `shipping addresses are incomplete` | Seller shop needs `address_id`; recipient needs default shipping address |
| Empty `rates` array | Use US addresses; check Shippo dashboard / API key |
| S3 upload fails on label | Check `S3_BUCKET`, AWS keys; set `SHIPPO_LABEL_BUCKET` = same bucket |
| Registration 403 | Enable capabilities: `POST /admin/countries/{id}/capabilities` with flags `true` |

For full API docs see [README.md](./README.md).

---

## Appendix — Australia (AU) address bodies

Create AU country (admin), then use `{{au_country_id}}` instead of `{{us_country_id}}`.

**Admin — Australia country**

```json
{
  "iso_code": "AU",
  "name": "Australia",
  "default_currency": "AUD",
  "default_timezone": "Australia/Sydney",
  "status": "full"
}
```

| Step | Email / slug | Notes |
|---|---|---|
| Seller | `au-seller@test.com` / `sydney-gifts` | |
| Customer | `au-customer@test.com` | |
| Product currency | `AUD` | `price_amount`: 3500 = AUD 35.00 |

**Seller warehouse (ship-from)**

```json
{
  "country_id": "{{au_country_id}}",
  "label": "Warehouse",
  "address_type": "return",
  "line1": "100 George St",
  "line2": "Level 5",
  "city": "Sydney",
  "region": "NSW",
  "postal_code": "2000",
  "latitude": -33.8688,
  "longitude": 151.2093,
  "is_default": true
}
```

**Recipient shipping (ship-to)** — inside `POST /customers/me/recipients` `addresses[]`:

```json
{
  "country_id": "{{au_country_id}}",
  "label": "Home",
  "address_type": "shipping",
  "line1": "250 Pitt St",
  "line2": null,
  "city": "Sydney",
  "region": "NSW",
  "postal_code": "2000",
  "latitude": -33.8727,
  "longitude": 151.2069,
  "is_default": true
}
```

Then run the same **GET recipient**, **GET orders**, **GET seller order-items** steps as USA. Shippo **test** keys may still return no rates for AU → use **US** for label testing.
