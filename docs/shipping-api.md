# Shipping + checkout API (Option A)

Amounts on orders/quotes are **minor units (cents)** unless noted.  
`amount: 7951` → **$79.51**. Shippo `rates[].amount` is a decimal string (`"79.51"`).

Shippo **creates new `rate_object_id` values on every** `POST .../shipping/rates`.  
Always buy the label with `recommended_rate_object_id` from the **same / latest** rates response (**Option A**).

---

## End-to-end flow (Option A)

```text
1. Customer  POST /customers/me/shipping/quote
2. Customer  POST /customers/me/orders  (+ shipping_quotes)
3. Seller    accept order item
4. Seller    POST .../shipping/rates
5. Seller    POST .../shipping/labels  with recommended_rate_object_id from step 4
6. Seller    GET  .../shipping/label   (PDF)
7. Shippo    POST /webhooks/shippo/tracking
```

If seller wants a **different** courier → message the customer first. API returns **409** otherwise.

---

## 1. Product parcel (for accurate Shippo quotes)

### `POST /sellers/me/shops/{shopID}/products`  
### `PUT /sellers/me/products/{id}`

**Body (add `parcel`):**
```json
{
  "name": "Gift Box USA",
  "slug": "gift-box-usa",
  "price_amount": 32400,
  "currency": "USD",
  "status": "published",
  "parcel": {
    "length": "20",
    "width": "15",
    "height": "10",
    "distance_unit": "cm",
    "weight": "1.200",
    "mass_unit": "kg"
  }
}
```

**Response includes:**
```json
{
  "id": "...",
  "parcel": {
    "length": "20",
    "width": "15",
    "height": "10",
    "distance_unit": "cm",
    "weight": "1.200",
    "mass_unit": "kg"
  }
}
```

---

## 2. Customer quote (show delivery like AliExpress)

### `POST /customers/me/shipping/quote`

**Body:**
```json
{
  "recipient_id": "...",
  "delivery_date": "2026-09-25",
  "items": [{ "product_id": "...", "quantity": 1 }]
}
```

**Response:**
```json
{
  "shops": [
    {
      "shop_id": "...",
      "shop_name": "Bay Area Gifts",
      "shipment_object_id": "shippo_shipment_xxx",
      "options": [
        {
          "provider": "USPS",
          "service_name": "Priority Mail International",
          "amount": 7951,
          "currency": "USD",
          "estimated_days": 6,
          "days_available": 6,
          "recommended": true,
          "rate_object_id": "shippo_rate_checkout_xxx",
          "shipment_object_id": "shippo_shipment_xxx"
        }
      ]
    }
  ],
  "shipments": [
    {
      "shop_id": "...",
      "shop_name": "Bay Area Gifts",
      "provider": "USPS",
      "service_name": "Priority Mail International",
      "amount": 7951,
      "currency": "USD",
      "estimated_days": 6,
      "days_available": 6,
      "misses_delivery_date": false,
      "rate_object_id": "shippo_rate_checkout_xxx",
      "shipment_object_id": "shippo_shipment_xxx"
    }
  ],
  "amount": 7951,
  "currency": "USD",
  "complete": true
}
```

UI: list `shops[].options` with **price** (`amount / 100`) and **`days_available`**.

---

## 3. Place order (save shipment plan)

### `POST /customers/me/orders`

**Body (add `shipping_quotes` — echo selected option):**
```json
{
  "country_id": "...",
  "recipient_id": "...",
  "delivery_date": "2026-09-25",
  "customer_type": "personal",
  "items": [{ "product_id": "...", "quantity": 1 }],
  "shipping_quotes": [
    {
      "shop_id": "...",
      "rate_object_id": "shippo_rate_checkout_xxx",
      "shipment_object_id": "shippo_shipment_xxx",
      "provider": "USPS",
      "service_name": "Priority Mail International",
      "amount": 7951,
      "currency": "USD"
    }
  ]
}
```

**Response (totals include shipping):**
```json
{
  "order_number": "SAG-...",
  "subtotal_amount": 32400,
  "delivery_amount": 7951,
  "total_amount": 40351,
  "currency": "USD",
  "items": []
}
```

Display: items **$324.00** + shipping **$79.51** = **$403.51**.

---

## 4. Seller get rates

### `POST /sellers/me/order-items/{orderItemID}/shipping/rates`

**Body (international example):**
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
        "description": "test",
        "quantity": 1,
        "net_weight": "1.200",
        "mass_unit": "kg",
        "value_amount": "324.00",
        "value_currency": "USD",
        "origin_country": "US"
      }
    ]
  }
}
```

Domestic: parcel optional (prefills from product). Customs required only when international.

**Response:**
```json
{
  "shipment_object_id": "shippo_shipment_fresh_xxx",
  "international": true,
  "rates": [
    {
      "object_id": "shippo_rate_fresh_xxx",
      "provider": "USPS",
      "service_name": "Priority Mail International",
      "amount": "79.51",
      "currency": "USD",
      "estimated_days": 6
    }
  ],
  "checkout_selected": {
    "provider": "USPS",
    "service_name": "Priority Mail International",
    "amount": 7951,
    "amount_major": "79.51",
    "currency": "USD",
    "rate_object_id": "shippo_rate_checkout_xxx"
  },
  "recommended_rate_object_id": "shippo_rate_fresh_xxx",
  "customer_delivery_amount": 7951,
  "currency": "USD",
  "must_buy_customer_courier": true
}
```

Highlight `checkout_selected`. Copy **`recommended_rate_object_id`** for the next call.  
Do **not** buy with `checkout_selected.rate_object_id` (expired).

---

## 5. Buy label — Option A (required path)

### `POST /sellers/me/order-items/{orderItemID}/shipping/labels`

**Body:**
```json
{
  "rate_object_id": "shippo_rate_fresh_xxx",
  "provider": "USPS",
  "idempotency_key": "label-<orderItemID>-1"
}
```

`rate_object_id` **must** be `recommended_rate_object_id` from the **latest** rates response.

**Success `201`:**
```json
{
  "id": "...",
  "status": "label_created",
  "courier_provider": "USPS",
  "tracking_number": "...",
  "label_media_id": "..."
}
```

**Wrong / stale rate → `409`:**
```json
{
  "error": "use the customer-selected courier, or message the customer to agree a change first: customer selected USPS Priority Mail International (79.51 USD) — message the customer before changing"
}
```

Show this error **once** in the UI. Then: Get rates again → buy recommended, or chat the customer.

### Optional Option B (same URL)

If rate ids are hard to track after multiple rates calls:

```json
{
  "use_customer_selected": true,
  "idempotency_key": "label-<orderItemID>-1"
}
```

API buys the customer courier from the latest stored rates automatically.

---

## 6. Label PDF

### `GET /sellers/me/order-items/{orderItemID}/shipping/label`

**Response:**
```json
{
  "url": "https://presigned-s3-link..."
}
```

---

## Change summary for frontend

| Method + URL | Body change | Response change |
|--------------|-------------|-----------------|
| `POST /sellers/me/shops/{shopID}/products` | + `parcel` | + `parcel` |
| `PUT /sellers/me/products/{id}` | + `parcel` | + `parcel` |
| `POST /customers/me/shipping/quote` | — | + `shops`, `days_available`, rate ids |
| `POST /customers/me/orders` | + `shipping_quotes` | totals include delivery |
| `POST .../shipping/rates` | — | + `checkout_selected`, `recommended_rate_object_id`, `customer_delivery_amount`, `must_buy_customer_courier` |
| `POST .../shipping/labels` | + optional `use_customer_selected` | — |
| `GET .../shipping/label` | — | — |
