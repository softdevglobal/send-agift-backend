# SendAGift API Reference

How every route works, how the layers connect, **which file calls which** (diagrams for
login, admin register, orders, shipping, reels, …), why every internal and external API
exists, and request/response bodies. `README.md` is the Postman walkthrough;
`DATABASE_SCHEMA.md` covers tables and keys.

- [1. Stack and entry point](#1-stack-and-entry-point)
- [2. Folder structure](#2-folder-structure)
- [3. Files used and why](#3-files-used-and-why)
  - [3.1 Entry points and infrastructure](#31-entry-points-and-infrastructure)
  - [3.2 Routes → handlers → services → repositories](#32-routes--handlers--services--repositories)
  - [3.3 Feature → file map](#33-feature--file-map)
  - [3.4 Shared utilities and stubs](#34-shared-utilities-and-stubs)
  - [3.5 Per-API call chains (which file does what)](#35-per-api-call-chains-which-file-does-what)
  - [3.6 Every endpoint → file call chain (all ~82)](#36-every-endpoint--file-call-chain-all-82)
- [4. All APIs used and why](#4-all-apis-used-and-why)
  - [4.1 Internal APIs (our `/api/v1` routes)](#41-internal-apis-our-apiv1-routes)
  - [4.2 External HTTP / cloud APIs](#42-external-http--cloud-apis)
  - [4.3 Go module dependencies](#43-go-module-dependencies)
  - [4.4 Env vars that unlock each API](#44-env-vars-that-unlock-each-api)
- [5. How a request flows](#5-how-a-request-flows)
- [6. Conventions that apply to every endpoint](#6-conventions-that-apply-to-every-endpoint)
- [7. Complete route map](#7-complete-route-map)
- [8. Endpoints in detail](#8-endpoints-in-detail)
  - [8.1 Health](#81-health)
  - [8.2 Auth and bootstrap](#82-auth-and-bootstrap)
  - [8.3 Admin profile](#83-admin-profile)
  - [8.4 Countries and country capabilities](#84-countries-and-country-capabilities)
  - [8.5 Customers](#85-customers)
  - [8.6 Recipients](#86-recipients)
  - [8.7 Saved gifts](#87-saved-gifts)
  - [8.8 Customer orders](#88-customer-orders)
  - [8.9 Sellers, addresses, shops](#89-sellers-addresses-shops)
  - [8.10 Products and inventory](#810-products-and-inventory)
  - [8.11 Seller order items](#811-seller-order-items)
  - [8.12 Shipping (Shippo)](#812-shipping-shippo)
  - [8.13 Media (S3 presign)](#813-media-s3-presign)
  - [8.14 Reels](#814-reels)
  - [8.15 Public storefront browsing](#815-public-storefront-browsing)
  - [8.16 Places (Google proxy)](#816-places-google-proxy)
- [9. Where each request/response struct lives](#9-where-each-requestresponse-struct-lives)
- [10. Cross-cutting flows](#10-cross-cutting-flows)
- [11. Error catalogue](#11-error-catalogue)
- [12. Known gaps and sharp edges](#12-known-gaps-and-sharp-edges)

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

## 2. Folder structure

```
SendAGift_GO/
├── cmd/                          # process entry points (thin main packages)
│   ├── api/main.go               # HTTP API server (primary entry)
│   └── migrate/main.go           # optional standalone migrator
├── internal/                     # private app code (cannot be imported by other modules)
│   ├── config/                   # .env → typed Config
│   ├── database/                 # pool + embedded migrations
│   │   └── migrations/           # numbered *.up.sql / *.down.sql
│   ├── routes/                   # chi route registration (URL → handler)
│   ├── middleware/               # JWT, role, IP rate limit
│   ├── handlers/                 # HTTP decode / status mapping only
│   ├── services/                 # business rules + external API clients
│   ├── repository/               # SQL / pgx only
│   ├── models/                   # DB row + response DTOs (JSON tags)
│   └── utils/                    # JWT, bcrypt, JSON helpers
├── pkg/validator/                # reserved shared package (stub today)
├── .env.example                  # required env template
├── go.mod / go.sum               # module deps
├── README.md                     # Postman-style endpoint walkthrough
├── API_REFERENCE.md              # this file
├── DATABASE_SCHEMA.md            # tables, FKs, ER diagrams
└── SHIPPO_POSTMAN_TEST.md        # shipping-focused Postman script
```

Why this layout:

| Folder | Why it exists |
| --- | --- |
| `cmd/` | Keep `main` packages tiny — wire deps and listen; all logic lives under `internal/` |
| `internal/` | Go convention: code here is private to this module, so handlers/services cannot be accidentally imported elsewhere |
| `routes/` separate from `handlers/` | URLs and middleware groups stay in one place; handlers stay free of path registration |
| `services/` vs `repository/` | Validation, ownership, status machines, and Shippo/S3/Google live in services; repositories only run SQL |
| `models/` | One place for JSON shapes shared by handlers and repos |
| `database/migrations/` | Schema versioned with the code; API startup applies pending migrations |
| docs at repo root | Frontend / QA can read contracts without opening Go files |

Temporary / debug folders under `cmd/` (`checkdb`, `debugship`, `tmpcleanup`, `tmpverify`) are local utilities — they are not part of the production API surface.

---

## 3. Files used and why

### 3.1 Entry points and infrastructure

| File | Why it is used |
| --- | --- |
| `cmd/api/main.go` | Boots config → DB → migrate → repos → services → handlers → `routes.New` → `ListenAndServe` |
| `cmd/migrate/main.go` | Run migrations without starting the HTTP server |
| `internal/config/config.go` | Loads `.env`; fails fast if JWT/DB/S3/Google keys are missing |
| `internal/database/db.go` | Creates the `pgxpool` from `Config.DSN()` |
| `internal/database/migrate.go` | Embeds `migrations/*.sql`, applies pending `*.up.sql` into `schema_migrations` |
| `internal/database/migrations/*.sql` | Creates/alters every schema table the API depends on |
| `internal/routes/router.go` | Global middleware (CORS, RequestID, RealIP, Logger, Recoverer), `/health`, mounts all `/api/v1` groups |

### 3.2 Routes → handlers → services → repositories

Each feature is a vertical slice. The route file only mounts URLs; the handler only parses HTTP; the service owns rules; the repository owns SQL.

| Concern | Routes file | Handler | Service | Repository | Models |
| --- | --- | --- | --- | --- | --- |
| Auth / bootstrap | `auth_routes.go`, `admin_register_routes.go` | `auth_handler.go` | `auth_service.go` | `admin_repository.go`, `customer_repository.go`, `seller_repository.go` | `admin.go`, `customer.go`, `seller.go` |
| Admin profile | `admin_routes.go` | `admin_handler.go` | `admin_service.go` | `admin_repository.go` | `admin.go` |
| Countries | `country_routes.go` | `country_handler.go` | `country_service.go` | `country_repository.go` | `country.go` |
| Country capabilities | `country_routes.go` | `country_capability_handler.go` | `country_capability_service.go` | `country_capability_repository.go` | `country_capability.go` |
| Customers / addresses / recipients / wishlist | `customer_routes.go` | `customer_handler.go` | `customer_service.go` | `customer_repository.go` (+ `product_repository.go` for wishlist product check) | `customer.go`, `recipient.go`, `product.go` |
| Customer orders | `customer_routes.go` | `order_handler.go` | `order_service.go` | `order_repository.go`, `customer_repository.go`, `country_repository.go` | `order.go` |
| Sellers / shops / addresses | `seller_routes.go` | `seller_handler.go` | `seller_service.go` | `seller_repository.go` | `seller.go` |
| Products / inventory | `seller_routes.go` | `product_handler.go` | `product_service.go` | `product_repository.go`, `seller_repository.go` | `product.go` |
| Seller order items | `seller_routes.go` | `seller_order_handler.go` | `order_service.go` | `order_repository.go` | `order.go` |
| Public shops / products | `marketplace_routes.go` | `shops_handler.go` | `shop_marketplace_service.go` | `seller_repository.go`, `product_repository.go` | `seller.go`, `product.go` |
| Reels (public + seller) | `reel_routes.go` | `reel_handler.go` | `reel_service.go` | `reel_repository.go`, `seller_repository.go` | `reel.go`, `media_asset.go` |
| Media presign | `media_routes.go` | `media_handler.go` | `s3_service.go` | — (S3 only; DB rows created later by reels/shipping) | — |
| Shipping + Shippo webhook | `shipping_routes.go` | `shipping_handler.go` | `shipping_service.go`, `shippo_client.go`, `shipping_inputs.go` | `shipment_repository.go`, `idempotency_repository.go`, `media_repository.go` | `shipment.go`, `media_asset.go` |
| Google Places proxy | `places_routes.go` | `places_handler.go` | `places_service.go` | — | — |

Middleware used on those groups:

| File | Why |
| --- | --- |
| `middleware/auth_middleware.go` | Parse `Authorization: Bearer`, put `user_id` / `admin_id` / `role` on context |
| `middleware/role_middleware.go` | Enforce `customer` / `seller` / `admin` (treats `superadmin` as admin) |
| `middleware/rate_limit_middleware.go` | Cap Places calls at 120/min per IP so an open endpoint cannot burn the Google bill |
| `middleware/logger_middleware.go` | Package stub — logging is done by chi's built-in `Logger` in `router.go` |

### 3.3 Feature → file map

| Feature / API area | Files involved | Why those files |
| --- | --- | --- |
| Login + JWT | `auth_handler.go`, `auth_service.go`, `utils/jwt.go`, `utils/hash.go`, admin/customer/seller repos | One login path checks all three account tables; bcrypt verify; issue HS256 token |
| Bootstrap first admin | `admin_register_routes.go`, `auth_handler.go`, `auth_service.go`, `admin_repository.go` | One-time create gated by `X-Bootstrap-Secret` |
| Country feature flags | `country_*` handler/service/repo pairs | Registration refuses when capability flags are off |
| Wishlist | `customer_handler.go` + `customer_service.go` + `saved_gifts` SQL in `customer_repository.go` | Join table only; product existence checked via `product_repository.go` |
| Checkout pricing | `order_service.go` + `order_repository.GetCheckoutProduct` | Server snapshots price/seller/shop; client never sends amounts |
| Multi-seller fulfilment | `seller_order_handler.go`, `order_service.go`, `shipping_service.go`, `shipment_repository.go` | Sellers act on `order_items.id`; webhook completes one line then maybe the order |
| Reels + S3 files | `media_handler.go` → client PUT to S3 → `reel_service.go` → `reel_repository.go` + `media.media_assets` | Presign keeps the API off the upload bandwidth path; DB stores keys + `cdn_url` |
| Product reviews | `product_review_*` + `media_handler` (`review-photo`) + `order_repository.GetItemForCustomer` | Verified purchase (delivered line); photos via `media.media_assets`; helpful votes |
| Label PDF storage | `shipping_service.go` + `s3_service.Upload` + `media_repository.go` | Download Shippo PDF, store in S3, save `media_assets` row, link `shipments.label_media_id` |
| Address autocomplete | `places_*` | API key stays on server; frontend only sees place_id / address fields |

### 3.4 Shared utilities and stubs

| File | Status | Why |
| --- | --- | --- |
| `internal/utils/response.go` | used | Every success/error JSON response |
| `internal/utils/jwt.go` | used | Sign / parse bearer tokens |
| `internal/utils/hash.go` | used | bcrypt for passwords |
| `internal/utils/token_generator.go` | present | helper for random tokens if needed |
| `internal/handlers/verification_handler.go` | stub | package only — no KYC routes yet |
| `internal/services/verification_service.go` | stub | same |
| `internal/services/email_service.go` | stub | no outbound email yet |
| `internal/repository/user_repository.go` | stub | no unified `users` table |
| `internal/models/user.go` | stub | same |
| `pkg/validator/validator.go` | stub | reserved for shared validation helpers |

### 3.5 Per-API call chains (which file does what)

Every request walks the same layers. Example for login:

```text
Route → Handler → Service → Repository → DB
                      ↘ utils (hash / jwt / response)
```

Paths below are under `internal/` unless noted. Arrows mean “calls”.

§3.5 = **diagrams for shared stacks** (one per feature family).  
§3.6 = **all 82 endpoints** listed one-by-one with the same call chain.

---

#### A. Login — `POST /auth/login` (also `/customers/login`, `/sellers/login`)

```mermaid
flowchart LR
  R["routes/auth_routes.go<br/>registers POST"] --> H["handlers/auth_handler.go<br/>decode body, map errors"]
  H --> S["services/auth_service.go<br/>try admin → customer → seller"]
  S --> HA["utils/hash.go<br/>CheckPassword"]
  S --> JA["utils/jwt.go<br/>GenerateJWT"]
  S --> RA["repository/admin_repository.go<br/>GetByEmail"]
  S --> RC["repository/customer_repository.go<br/>GetByEmail"]
  S --> RS["repository/seller_repository.go<br/>GetByEmail"]
  H --> U["utils/response.go<br/>JSON / Error"]
  RA --> DB[(PostgreSQL)]
  RC --> DB
  RS --> DB
```

| File | Job on this API |
| --- | --- |
| `routes/auth_routes.go` | Mounts the three login URLs onto `AuthHandler.Login` |
| `handlers/auth_handler.go` | Decode `{email,password}`; call service; write `{token,role}` or `401` |
| `services/auth_service.go` | Look up email in admin, then customer, then seller; verify password; build JWT |
| `repository/admin_repository.go` | `SELECT` from `admin.admin_users` by email |
| `repository/customer_repository.go` | `SELECT` from `customer.customers` by email |
| `repository/seller_repository.go` | `SELECT` from `seller.sellers` by email |
| `utils/hash.go` | `bcrypt.CompareHashAndPassword` |
| `utils/jwt.go` | Sign HS256 token (`sub` = account id, `role`, `email`, `exp`) |
| `utils/response.go` | Write JSON response |
| `models/admin.go` / `customer.go` / `seller.go` | Row shapes scanned from DB |

Protected APIs after login always add:

```mermaid
flowchart LR
  REQ[Request with Bearer token] --> MW1["middleware/auth_middleware.go<br/>ParseJWT → context user_id + role"]
  MW1 --> MW2["middleware/role_middleware.go<br/>require customer/seller/admin"]
  MW2 --> H[Handler]
```

| File | Job |
| --- | --- |
| `middleware/auth_middleware.go` | Read `Authorization: Bearer`; `utils.ParseJWT`; put id + role on context |
| `middleware/role_middleware.go` | Reject wrong role with `403` |
| `utils/jwt.go` | `ParseJWT` validation |

---

#### B. Admin register — `POST /admin/register`

```mermaid
flowchart LR
  R["routes/admin_register_routes.go"] --> H["handlers/auth_handler.go<br/>Bootstrap"]
  H --> S["services/auth_service.go<br/>Bootstrap"]
  S --> HA["utils/hash.go<br/>HashPassword"]
  S --> RA["repository/admin_repository.go<br/>CountAdmins + CreateAdmin"]
  H --> U["utils/response.go"]
  RA --> DB[(admin.admin_users)]
```

| File | Job |
| --- | --- |
| `routes/admin_register_routes.go` | Mounts `POST /admin/register` |
| `handlers/auth_handler.go` | Read body + `X-Bootstrap-Secret` header |
| `services/auth_service.go` | Allow only when no admin exists (or secret matches); hash password; create `superadmin` |
| `repository/admin_repository.go` | Count + insert |
| `utils/hash.go` | bcrypt hash |
| `models/admin.go` | Admin row |

---

#### C. Admin profile — `GET/PUT /admin/me`

```mermaid
flowchart LR
  R["routes/admin_routes.go"] --> MW["auth_middleware"]
  MW --> H["handlers/admin_handler.go"]
  H --> S["services/admin_service.go"]
  S --> RA["repository/admin_repository.go<br/>GetByID / Update"]
  H --> U["utils/response.go"]
  RA --> DB[(admin.admin_users)]
```

| File | Job |
| --- | --- |
| `routes/admin_routes.go` | JWT group; `GET/PUT /admin/me` |
| `handlers/admin_handler.go` | Read `admin_id` from context; decode update body |
| `services/admin_service.go` | Load/update display_name, image_url |
| `repository/admin_repository.go` | SQL for admin row |
| `models/admin.go` | Response shape |

---

#### D. Countries — public read + admin write

```mermaid
flowchart LR
  R["routes/country_routes.go"] --> H["handlers/country_handler.go"]
  H --> S["services/country_service.go<br/>validate ISO / currency"]
  S --> RP["repository/country_repository.go"]
  RP --> DB[(core.countries)]
```

| File | Job |
| --- | --- |
| `routes/country_routes.go` | Public `GET`; admin group for `POST/PUT/DELETE` |
| `handlers/country_handler.go` | Decode country JSON; map not-found / conflict |
| `services/country_service.go` | Require fields; validate ISO2 + currency code |
| `repository/country_repository.go` | CRUD SQL |
| `models/country.go` | Country DTO |

Capabilities (same route file, separate stack):

```mermaid
flowchart LR
  R["routes/country_routes.go"] --> H["handlers/country_capability_handler.go"]
  H --> S["services/country_capability_service.go"]
  S --> RC["repository/country_capability_repository.go"]
  S --> RP["repository/country_repository.go<br/>country must exist"]
  RC --> DB[(core.country_capabilities)]
```

| File | Job |
| --- | --- |
| `handlers/country_capability_handler.go` | Decode flag booleans |
| `services/country_capability_service.go` | Create/update/delete 1:1 flags per country |
| `repository/country_capability_repository.go` | SQL on `country_capabilities` |
| `models/country_capability.go` | Capability + `CountryCapabilityDetails` |

---

#### E. Customer register — `POST /customers/register`

```mermaid
flowchart LR
  R["routes/customer_routes.go"] --> H["handlers/customer_handler.go<br/>Register"]
  H --> S["services/customer_service.go<br/>Register"]
  S --> HA["utils/hash.go"]
  S --> RP["repository/country_repository.go"]
  S --> CAP["services/country_capability_service.go<br/>registration enabled?"]
  S --> RC["repository/customer_repository.go<br/>create customer + addresses"]
  H --> U["utils/response.go"]
  RC --> DB[(customer.*)]
```

| File | Job |
| --- | --- |
| `routes/customer_routes.go` | Public register; JWT+role group for `/me/*` |
| `handlers/customer_handler.go` | Decode register/update/address/recipient bodies |
| `services/customer_service.go` | Validate; hash password; check country + capability; create rows |
| `repository/customer_repository.go` | Inserts into `customers` / `customer_addresses` |
| `repository/country_repository.go` | Prove `country_id` exists |
| `services/country_capability_service.go` | Gate `customer_registration_enabled` |
| `utils/hash.go` | Hash password |
| `models/customer.go` | `Customer` / `CustomerDetails` |

Same vertical slice (handler → `customer_service` → `customer_repository`) for:

- profile `GET/PUT/DELETE /customers/me`
- addresses
- recipients + recipient addresses
- saved gifts (also uses `product_repository.ExistsByID`)

---

#### F. Create order — `POST /customers/me/orders`

```mermaid
flowchart LR
  R["routes/customer_routes.go"] --> MW["auth + role=customer"]
  MW --> H["handlers/order_handler.go"]
  H --> S["services/order_service.go<br/>snapshot prices, build items"]
  S --> RC["repository/customer_repository.go<br/>customer + recipient"]
  S --> RP["repository/country_repository.go"]
  S --> RO["repository/order_repository.go<br/>GetCheckoutProduct + Create"]
  H --> U["utils/response.go"]
  RO --> DB[(marketplace.orders + order_items)]
```

| File | Job |
| --- | --- |
| `handlers/order_handler.go` | Decode `OrderCreateInput`; list/get/cancel |
| `services/order_service.go` | Validate date/type; load products; snapshot amounts; generate `order_number` |
| `repository/order_repository.go` | Checkout product join; insert order + items; cancel |
| `repository/customer_repository.go` | Customer + recipient ownership |
| `repository/country_repository.go` | Valid `country_id` |
| `models/order.go` | `Order`, `OrderItem`, `OrderDetails` |

---

#### G. Seller register / shops / products

**Register `POST /sellers/register`**

```mermaid
flowchart LR
  R["routes/seller_routes.go"] --> H["handlers/seller_handler.go"]
  H --> S["services/seller_service.go"]
  S --> HA["utils/hash.go"]
  S --> CAP["country_capability_service"]
  S --> RS["repository/seller_repository.go<br/>seller + addresses + shop"]
  RS --> DB[(seller.*)]
```

**Create product `POST /sellers/me/shops/{shopID}/products`**

```mermaid
flowchart LR
  R["routes/seller_routes.go"] --> MW["auth + role=seller"]
  MW --> H["handlers/product_handler.go"]
  H --> S["services/product_service.go"]
  S --> RS["repository/seller_repository.go<br/>shop owned by seller?"]
  S --> RP["repository/product_repository.go<br/>product + inventory"]
  RP --> DB[(seller.products + inventory)]
```

| File | Job |
| --- | --- |
| `handlers/seller_handler.go` | Register, me, addresses, shops |
| `services/seller_service.go` | Seller/shop/address rules + slug uniqueness |
| `repository/seller_repository.go` | SQL for sellers, addresses, shops |
| `handlers/product_handler.go` | Product + inventory HTTP |
| `services/product_service.go` | Product/inventory validation |
| `repository/product_repository.go` | SQL for products + inventory |
| `models/seller.go` / `product.go` | Response DTOs |

---

#### H. Seller accept + shipping rates + label

```mermaid
flowchart TB
  subgraph accept [Accept line]
    A1["seller_routes.go"] --> A2["seller_order_handler.go"]
    A2 --> A3["order_service.go AcceptItemForSeller"]
    A3 --> A4["order_repository.go"]
  end
  subgraph rates [Get rates]
    B1["shipping_routes.go"] --> B2["shipping_handler.go GetRates"]
    B2 --> B3["shipping_service.go"]
    B3 --> B4["shipment_repository.go GetShippingContext + UpsertQuote"]
    B3 --> B5["shippo_client.go<br/>customs + shipments"]
    B5 --> SHIPPO[Shippo API]
  end
  subgraph label [Buy label]
    C1["shipping_handler.go BuyLabel"] --> C2["shipping_service.go"]
    C2 --> C3["idempotency_repository.go"]
    C2 --> C4["shippo_client.go transactions"]
    C2 --> C5["s3_service.go Upload PDF"]
    C2 --> C6["media_repository.go Create label asset"]
    C2 --> C7["shipment_repository.go CompleteLabel"]
  end
  subgraph hook [Webhook]
    D1["shipping_routes.go"] --> D2["shipping_handler.go ShippoWebhook"]
    D2 --> D3["shipping_service.go HandleTrackingWebhook"]
    D3 --> D4["shipment_repository.go<br/>UpdateTracking + MarkOrderItemDelivered + MarkOrderDeliveredIfComplete"]
  end
```

| File | Job |
| --- | --- |
| `handlers/seller_order_handler.go` | List/get/accept order items for this seller |
| `services/order_service.go` | Accept only when status allows |
| `repository/order_repository.go` | Seller-scoped item queries + accept update |
| `handlers/shipping_handler.go` | Decode rates/label/webhook bodies |
| `services/shipping_inputs.go` | Parcel + customs request structs / validation helpers |
| `services/shipping_service.go` | Orchestrate Shippo + DB shipment row + multi-seller delivery |
| `services/shippo_client.go` | HTTP to `api.goshippo.com` |
| `repository/shipment_repository.go` | Ship-from/to context, quote, label, tracking, order complete |
| `repository/idempotency_repository.go` | Prevent double label purchase |
| `repository/media_repository.go` | Insert label `media_assets` row |
| `services/s3_service.go` | Upload label PDF |
| `models/shipment.go` / `order.go` / `media_asset.go` | Shapes |

---

#### I. Media presign — `POST /media/presign-upload`

```mermaid
flowchart LR
  R["routes/media_routes.go"] --> MW["auth_middleware"]
  MW --> H["handlers/media_handler.go"]
  H --> S["services/s3_service.go<br/>PresignPutURL + PublicURL"]
  S --> S3[(AWS S3)]
  H --> U["utils/response.go"]
```

| File | Job |
| --- | --- |
| `routes/media_routes.go` | JWT-required media routes |
| `handlers/media_handler.go` | Validate folder whitelist; build object key `prefix/uuid-filename` |
| `services/s3_service.go` | AWS SDK presign PUT/GET; public URL; Upload/Delete |
| No repository | DB row is created later (reel create or label buy) |

---

#### J. Create reel — `POST /sellers/me/shops/{shopID}/reels`

```mermaid
flowchart LR
  R["routes/reel_routes.go"] --> MW["auth + role=seller"]
  MW --> H["handlers/reel_handler.go"]
  H --> S["services/reel_service.go<br/>build media_assets + publish rules"]
  S --> RS["repository/seller_repository.go<br/>shop ownership"]
  S --> RR["repository/reel_repository.go<br/>insert assets + reel + reel_media"]
  S --> S3S["s3_service.go PublicURL"]
  RR --> DB[(seller.reels + reel_media + media.media_assets)]
```

| File | Job |
| --- | --- |
| `routes/reel_routes.go` | Public feed routes + seller CRUD |
| `handlers/reel_handler.go` | Decode `ReelInput`; feed query params; map errors |
| `services/reel_service.go` | Validate media MIME; derive `reel_type`; set ready/approved; cursor encode |
| `repository/reel_repository.go` | Transaction: media_assets → reels → reel_media; feed queries; view_count++ |
| `repository/seller_repository.go` | Shop belongs to seller |
| `services/s3_service.go` | Fill `cdn_url` for `public/` keys; delete objects on reel delete |
| `models/reel.go` / `media_asset.go` | `ReelDetails`, feed page |

Public feed `GET /reels` skips auth middleware and uses the same handler → `reel_service.Feed` → `reel_repository.ListPublicFeed`.

---

#### K. Public storefront — `GET /shops`, `/products/{id}`

```mermaid
flowchart LR
  R["routes/marketplace_routes.go"] --> H["handlers/shops_handler.go"]
  H --> S["services/shop_marketplace_service.go"]
  S --> RS["repository/seller_repository.go<br/>active shops"]
  S --> RP["repository/product_repository.go<br/>published products"]
  RS --> DB[(seller.shops)]
  RP --> DB
```

| File | Job |
| --- | --- |
| `routes/marketplace_routes.go` | Public browse URLs (no JWT) |
| `handlers/shops_handler.go` | Path/query (`customer_type`); 404 for inactive/unpublished |
| `services/shop_marketplace_service.go` | Normalize customer_type; call repos |
| `repository/seller_repository.go` | `ListActiveShops`, `GetActiveShopByID` |
| `repository/product_repository.go` | Published-by-shop / published-by-id |
| `models/seller.go` / `product.go` | `Shop`, `Product`, `PublicProduct` |

---

#### L. Places — `GET /places/autocomplete`, `/places/details`

```mermaid
flowchart LR
  R["routes/places_routes.go"] --> RL["middleware/rate_limit_middleware.go<br/>120/min/IP"]
  RL --> H["handlers/places_handler.go"]
  H --> S["services/places_service.go"]
  S --> G[Google Places API]
  H --> U["utils/response.go"]
```

| File | Job |
| --- | --- |
| `routes/places_routes.go` | Public group + rate limit |
| `middleware/rate_limit_middleware.go` | Per-IP counter so open endpoints cannot burn Google quota |
| `handlers/places_handler.go` | Read query params; return suggestions / details |
| `services/places_service.go` | HTTP to Places API (New); map to address fields |
| No repository | Nothing stored |

---

#### M. Boot wiring — who creates whom (`cmd/api/main.go`)

```mermaid
flowchart TB
  MAIN["cmd/api/main.go"] --> CFG["config/config.go"]
  MAIN --> DB["database/db.go + migrate.go"]
  MAIN --> REPOS["All *Repository constructors"]
  MAIN --> SVCS["All *Service constructors"]
  MAIN --> HDRS["All *Handler constructors"]
  MAIN --> RT["routes/router.go New(...)"]
  RT --> HTTP[net/http ListenAndServe]
```

`main.go` is the only place that wires `AuthService(admins, customers, sellers, jwt…)`, `ReelService(reels, sellers, s3…)`, `ShippingService(shippo, shipments, idempotency, media, s3…)`, etc. Handlers never construct repositories themselves.

---

#### Quick legend (every API)

| Layer | Folder | Always does | Never does |
| --- | --- | --- | --- |
| Route | `routes/*.go` | URL + middleware attach | Business rules |
| Middleware | `middleware/*.go` | Auth / role / rate limit | SQL |
| Handler | `handlers/*.go` | Decode JSON, path params, HTTP status | SQL, Shippo/S3 calls (except via service) |
| Service | `services/*.go` | Validation, ownership, orchestration | `http.ResponseWriter` |
| Repository | `repository/*.go` | SQL / transactions | HTTP |
| Utils | `utils/*.go` | JWT, bcrypt, JSON helpers | Feature logic |
| Models | `models/*.go` | Structs + `json` tags | Logic |

### 3.6 Every endpoint → file call chain (all ~82)

**Honest answer:** §3.5 has diagrams for **feature families** (login, orders, shipping, …), not 82 separate mermaid charts — many endpoints share the exact same stack (e.g. all recipient routes use the same handler/service/repo).

This section lists **every registered route** with its full call chain. Paths are under `internal/` unless noted. Middleware abbreviations:

- `Auth` = `middleware/auth_middleware.go` → `utils/jwt.go` (`ParseJWT`)
- `Role(X)` = `middleware/role_middleware.go` (require role `X`)
- `RL` = `middleware/rate_limit_middleware.go`
- `Resp` = `utils/response.go` (always at the end of the handler)

Shared utils used when noted: `Hash` = `utils/hash.go`, `JWT` = `utils/jwt.go`.

| # | Method | Path | Middleware | Route file | Handler method | Service method / package | Repository / external | Model |
| ---: | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | GET | `/health` | — | `routes/router.go` (inline) | — | — | — | — |
| 2 | POST | `/admin/register` | Bootstrap secret header | `admin_register_routes.go` | `auth_handler.Bootstrap` | `auth_service.Bootstrap` | `admin_repository` + `Hash` | `models.Admin` |
| 3 | POST | `/auth/login` | — | `auth_routes.go` | `auth_handler.Login` | `auth_service.Login` | `admin` → `customer` → `seller` repos + `Hash` + `JWT` | `LoginResult` |
| 4 | POST | `/customers/login` | — | `auth_routes.go` | same as #3 | same | same | same |
| 5 | POST | `/sellers/login` | — | `auth_routes.go` | same as #3 | same | same | same |
| 6 | GET | `/admin/me` | Auth | `admin_routes.go` | `admin_handler.Me` | `admin_service.GetByID` | `admin_repository` | `Admin` |
| 7 | PUT | `/admin/me` | Auth | `admin_routes.go` | `admin_handler.UpdateMe` | `admin_service.Update` | `admin_repository` | `Admin` |
| 8 | GET | `/countries` | — | `country_routes.go` | `country_handler.List` | `country_service.List` | `country_repository` | `[]Country` |
| 9 | GET | `/countries/{id}` | — | `country_routes.go` | `country_handler.GetByID` | `country_service.GetByID` | `country_repository` | `Country` |
| 10 | POST | `/admin/countries` | Auth + Role(admin) | `country_routes.go` | `country_handler.Create` | `country_service.Create` | `country_repository` | `Country` |
| 11 | PUT | `/admin/countries/{id}` | Auth + Role(admin) | `country_routes.go` | `country_handler.Update` | `country_service.Update` | `country_repository` | `Country` |
| 12 | DELETE | `/admin/countries/{id}` | Auth + Role(admin) | `country_routes.go` | `country_handler.Delete` | `country_service.Delete` | `country_repository` | message |
| 13 | GET | `/admin/country-capabilities` | Auth + Role(admin) | `country_routes.go` | `country_capability_handler.List` | `country_capability_service.List` | `country_capability_repository` | `[]CountryCapabilityDetails` |
| 14 | GET | `/admin/countries/{id}/capabilities` | Auth + Role(admin) | `country_routes.go` | `…GetByCountryID` | `…GetByCountryID` | capability + country repos | `CountryCapabilityDetails` |
| 15 | POST | `/admin/countries/{id}/capabilities` | Auth + Role(admin) | `country_routes.go` | `…Create` | `…Create` | capability + country repos | `CountryCapabilityDetails` |
| 16 | PUT | `/admin/countries/{id}/capabilities` | Auth + Role(admin) | `country_routes.go` | `…Update` | `…Update` | capability + country repos | `CountryCapabilityDetails` |
| 17 | DELETE | `/admin/countries/{id}/capabilities` | Auth + Role(admin) | `country_routes.go` | `…Delete` | `…Delete` | capability + country repos | message |
| 18 | POST | `/customers/register` | — | `customer_routes.go` | `customer_handler.Register` | `customer_service.Register` | `customer` + `country` repos + capability service + `Hash` | `CustomerDetails` |
| 19 | GET | `/customers/me` | Auth + Role(customer) | `customer_routes.go` | `customer_handler.Me` | `customer_service.GetDetails` | `customer_repository` | `CustomerDetails` |
| 20 | PUT | `/customers/me` | Auth + Role(customer) | `customer_routes.go` | `customer_handler.UpdateMe` | `customer_service.Update` | `customer_repository` | `Customer` |
| 21 | DELETE | `/customers/me` | Auth + Role(customer) | `customer_routes.go` | `customer_handler.DeleteMe` | `customer_service.Delete` | `customer_repository` (soft) | message |
| 22 | POST | `/customers/me/addresses` | Auth + Role(customer) | `customer_routes.go` | `customer_handler.AddAddress` | `customer_service.AddAddress` | `customer_repository` | `CustomerAddress` |
| 23 | DELETE | `/customers/me/addresses/{id}` | Auth + Role(customer) | `customer_routes.go` | `customer_handler.DeleteAddress` | `customer_service.DeleteAddress` | `customer_repository` | message |
| 24 | GET | `/customers/me/saved-gifts` | Auth + Role(customer) | `customer_routes.go` | `customer_handler.ListSavedGifts` | `customer_service.ListSavedGifts` | `customer_repository` | `[]SavedGiftDetails` |
| 25 | POST | `/customers/me/saved-gifts` | Auth + Role(customer) | `customer_routes.go` | `customer_handler.AddSavedGift` | `customer_service.AddSavedGift` | `customer` + `product` repos | `SavedGift` |
| 26 | DELETE | `/customers/me/saved-gifts/{id}` | Auth + Role(customer) | `customer_routes.go` | `customer_handler.DeleteSavedGift` | `customer_service.DeleteSavedGift` | `customer_repository` | message |
| 27 | POST | `/customers/me/recipients` | Auth + Role(customer) | `customer_routes.go` | `customer_handler.CreateRecipient` | `customer_service.CreateRecipient` | `customer_repository` | `RecipientDetails` |
| 28 | GET | `/customers/me/recipients` | Auth + Role(customer) | `customer_routes.go` | `customer_handler.ListRecipients` | `customer_service.ListRecipients` | `customer_repository` | `[]Recipient` |
| 29 | GET | `/customers/me/recipients/{id}` | Auth + Role(customer) | `customer_routes.go` | `customer_handler.GetRecipient` | `customer_service.GetRecipient` | `customer_repository` | `RecipientDetails` |
| 30 | PUT | `/customers/me/recipients/{id}` | Auth + Role(customer) | `customer_routes.go` | `customer_handler.UpdateRecipient` | `customer_service.UpdateRecipient` | `customer_repository` | `RecipientDetails` |
| 31 | DELETE | `/customers/me/recipients/{id}` | Auth + Role(customer) | `customer_routes.go` | `customer_handler.DeleteRecipient` | `customer_service.DeleteRecipient` | `customer_repository` | message |
| 32 | POST | `/customers/me/recipients/{id}/addresses` | Auth + Role(customer) | `customer_routes.go` | `customer_handler.AddRecipientAddress` | `customer_service.AddRecipientAddress` | `customer_repository` | `RecipientAddress` |
| 33 | PUT | `/customers/me/recipients/{id}/addresses/{addressId}` | Auth + Role(customer) | `customer_routes.go` | `customer_handler.UpdateRecipientAddress` | `customer_service.UpdateRecipientAddress` | `customer_repository` | `RecipientAddress` |
| 34 | DELETE | `/customers/me/recipients/{id}/addresses/{addressId}` | Auth + Role(customer) | `customer_routes.go` | `customer_handler.DeleteRecipientAddress` | `customer_service.DeleteRecipientAddress` | `customer_repository` | message |
| 35 | POST | `/customers/me/orders` | Auth + Role(customer) | `customer_routes.go` | `order_handler.Create` | `order_service.Create` | `order` + `customer` + `country` repos | `OrderDetails` |
| 36 | GET | `/customers/me/orders` | Auth + Role(customer) | `customer_routes.go` | `order_handler.List` | `order_service.List` | `order` + `customer` repos | `[]Order` |
| 37 | GET | `/customers/me/orders/{id}` | Auth + Role(customer) | `customer_routes.go` | `order_handler.Get` | `order_service.Get` | `order_repository` | `OrderDetails` |
| 38 | POST | `/customers/me/orders/{id}/cancel` | Auth + Role(customer) | `customer_routes.go` | `order_handler.Cancel` | `order_service.Cancel` | `order_repository` | `OrderDetails` |
| 39 | POST | `/sellers/register` | — | `seller_routes.go` | `seller_handler.Register` | `seller_service.Register` | `seller` + `country` repos + capability + `Hash` | `SellerDetails` |
| 40 | GET | `/sellers/me` | Auth + Role(seller) | `seller_routes.go` | `seller_handler.Me` | `seller_service.GetDetails` | `seller_repository` | `SellerDetails` |
| 41 | PUT | `/sellers/me` | Auth + Role(seller) | `seller_routes.go` | `seller_handler.UpdateMe` | `seller_service.Update` | `seller_repository` | `Seller` |
| 42 | DELETE | `/sellers/me` | Auth + Role(seller) | `seller_routes.go` | `seller_handler.DeleteMe` | `seller_service.Delete` | `seller_repository` (soft) | message |
| 43 | POST | `/sellers/me/addresses` | Auth + Role(seller) | `seller_routes.go` | `seller_handler.AddAddress` | `seller_service.AddAddress` | `seller_repository` | `SellerAddress` |
| 44 | PUT | `/sellers/me/addresses/{id}` | Auth + Role(seller) | `seller_routes.go` | `seller_handler.UpdateAddress` | `seller_service.UpdateAddress` | `seller_repository` | `SellerAddress` |
| 45 | DELETE | `/sellers/me/addresses/{id}` | Auth + Role(seller) | `seller_routes.go` | `seller_handler.DeleteAddress` | `seller_service.DeleteAddress` | `seller_repository` | message |
| 46 | GET | `/sellers/me/shops` | Auth + Role(seller) | `seller_routes.go` | `seller_handler.ListShops` | `seller_service.ListShops` | `seller_repository` | `[]Shop` |
| 47 | POST | `/sellers/me/shops` | Auth + Role(seller) | `seller_routes.go` | `seller_handler.CreateShop` | `seller_service.CreateShop` | `seller_repository` | `Shop` |
| 48 | PUT | `/sellers/me/shops/{id}` | Auth + Role(seller) | `seller_routes.go` | `seller_handler.UpdateShop` | `seller_service.UpdateShop` | `seller_repository` | `Shop` |
| 49 | DELETE | `/sellers/me/shops/{id}` | Auth + Role(seller) | `seller_routes.go` | `seller_handler.DeleteShop` | `seller_service.DeleteShop` | `seller_repository` | message |
| 50 | GET | `/sellers/me/shops/{shopID}/products` | Auth + Role(seller) | `seller_routes.go` | `product_handler.ListByShop` | `product_service.ListByShop` | `product` + `seller` repos | `[]Product` |
| 51 | POST | `/sellers/me/shops/{shopID}/products` | Auth + Role(seller) | `seller_routes.go` | `product_handler.Create` | `product_service.Create` | `product` + `seller` repos | `ProductDetails` |
| 52 | GET | `/sellers/me/products/{id}` | Auth + Role(seller) | `seller_routes.go` | `product_handler.Get` | `product_service.Get` | `product_repository` | `ProductDetails` |
| 53 | PUT | `/sellers/me/products/{id}` | Auth + Role(seller) | `seller_routes.go` | `product_handler.Update` | `product_service.Update` | `product_repository` | `Product` |
| 54 | DELETE | `/sellers/me/products/{id}` | Auth + Role(seller) | `seller_routes.go` | `product_handler.Delete` | `product_service.Delete` | `product_repository` | message |
| 55 | GET | `/sellers/me/products/{id}/inventory` | Auth + Role(seller) | `seller_routes.go` | `product_handler.GetInventory` | `product_service.GetInventory` | `product_repository` | `Inventory` |
| 56 | PUT | `/sellers/me/products/{id}/inventory` | Auth + Role(seller) | `seller_routes.go` | `product_handler.UpdateInventory` | `product_service.UpdateInventory` | `product_repository` | `Inventory` |
| 57 | GET | `/sellers/me/order-items` | Auth + Role(seller) | `seller_routes.go` | `seller_order_handler.ListItems` | `order_service.ListItemsForSeller` | `order_repository` | `[]SellerOrderItemSummary` |
| 58 | GET | `/sellers/me/order-items/{id}` | Auth + Role(seller) | `seller_routes.go` | `seller_order_handler.GetItem` | `order_service.GetItemForSeller` | `order_repository` | `SellerOrderItemDetails` |
| 59 | PATCH | `/sellers/me/order-items/{id}/accept` | Auth + Role(seller) | `seller_routes.go` | `seller_order_handler.AcceptItem` | `order_service.AcceptItemForSeller` | `order_repository` | `OrderItem` |
| 60 | POST | `/sellers/me/order-items/{orderItemID}/shipping/rates` | Auth + Role(seller) | `shipping_routes.go` | `shipping_handler.GetRates` | `shipping_service.GetRates` | `shipment_repository` + `shippo_client` → Shippo | `ShippingRatesResult` |
| 61 | POST | `/sellers/me/order-items/{orderItemID}/shipping/labels` | Auth + Role(seller) | `shipping_routes.go` | `shipping_handler.BuyLabel` | `shipping_service.BuyLabel` | `shipment` + `idempotency` + `media` repos + `s3_service` + Shippo | `Shipment` |
| 62 | POST | `/webhooks/shippo/tracking` | — | `shipping_routes.go` | `shipping_handler.ShippoWebhook` | `shipping_service.HandleTrackingWebhook` | `shipment_repository` | `{status:ok}` |
| 63 | POST | `/media/presign-upload` | Auth | `media_routes.go` | `media_handler.PresignUpload` | `s3_service.PresignPutURL` | AWS S3 (no DB) | `{upload_url,key,public_url}` |
| 64 | GET | `/media/url` | Auth | `media_routes.go` | `media_handler.GetURL` | `s3_service.PresignGetURL` | AWS S3 (no DB) | `{url}` |
| 65 | POST | `/sellers/me/shops/{shopID}/reels` | Auth + Role(seller) | `reel_routes.go` | `reel_handler.Create` | `reel_service.Create` | `reel` + `seller` repos + `s3_service` | `ReelDetails` |
| 66 | GET | `/sellers/me/shops/{shopID}/reels` | Auth + Role(seller) | `reel_routes.go` | `reel_handler.ListByShop` | `reel_service.ListByShop` | `reel` + `seller` repos | `[]ReelDetails` |
| 67 | POST | `/sellers/me/products/{productID}/reels` | Auth + Role(seller) | `reel_routes.go` | `reel_handler.CreateForProduct` | `reel_service.CreateForProduct` | `reel_repository` + `s3_service` | `ReelDetails` |
| 68 | GET | `/sellers/me/products/{productID}/reels` | Auth + Role(seller) | `reel_routes.go` | `reel_handler.ListByProduct` | `reel_service.ListByProduct` | `reel_repository` | `[]ReelDetails` |
| 69 | GET | `/sellers/me/reels` | Auth + Role(seller) | `reel_routes.go` | `reel_handler.ListMine` | `reel_service.ListBySeller` | `reel_repository` | `[]ReelDetails` |
| 70 | GET | `/sellers/me/reels/{id}` | Auth + Role(seller) | `reel_routes.go` | `reel_handler.Get` | `reel_service.Get` | `reel_repository` | `ReelDetails` |
| 71 | PUT | `/sellers/me/reels/{id}` | Auth + Role(seller) | `reel_routes.go` | `reel_handler.Update` | `reel_service.Update` | `reel_repository` + `s3_service` | `ReelDetails` |
| 72 | DELETE | `/sellers/me/reels/{id}` | Auth + Role(seller) | `reel_routes.go` | `reel_handler.Delete` | `reel_service.Delete` | `reel_repository` + `s3_service.Delete` | message |
| 73 | GET | `/reels` | — | `reel_routes.go` | `reel_handler.Feed` | `reel_service.Feed` | `reel_repository` | `ReelFeed` |
| 74 | GET | `/reels/{id}` | — | `reel_routes.go` | `reel_handler.GetPublic` | `reel_service.GetPublic` | `reel_repository` (+ view_count) | `ReelDetails` |
| 75 | GET | `/shops/{shopId}/reels` | — | `reel_routes.go` | `reel_handler.FeedByShop` | `reel_service.Feed` | `reel_repository` | `ReelFeed` |
| 76 | GET | `/products/{productId}/reels` | — | `reel_routes.go` | `reel_handler.FeedByProduct` | `reel_service.Feed` | `reel_repository` | `ReelFeed` |
| 77 | GET | `/shops` | — | `marketplace_routes.go` | `shops_handler.ListActiveShops` | `shop_marketplace_service.ListActiveShops` | `seller_repository` | `[]Shop` |
| 78 | GET | `/shops/{shopId}` | — | `marketplace_routes.go` | `shops_handler.GetShop` | `shop_marketplace_service.GetActiveShop` | `seller_repository` | `Shop` |
| 79 | GET | `/shops/{shopId}/products` | — | `marketplace_routes.go` | `shops_handler.ListShopProducts` | `shop_marketplace_service.ListPublishedProductsByShop` | `product_repository` | `[]Product` |
| 80 | GET | `/products/{productId}` | — | `marketplace_routes.go` | `shops_handler.GetProduct` | `shop_marketplace_service.GetPublishedProduct` | `product_repository` | `PublicProduct` |
| 81 | GET | `/places/autocomplete` | RL | `places_routes.go` | `places_handler.Autocomplete` | `places_service.Autocomplete` | Google Places API | `{suggestions}` |
| 82 | GET | `/places/details` | RL | `places_routes.go` | `places_handler.Details` | `places_service.Details` | Google Places API | `PlaceDetails` |

**Count = 82** (matches every `r.Get/Post/Put/Patch/Delete` registered in `internal/routes` plus `/health`).

How to read one row (example #3 login):

```text
auth_routes.go
  → auth_handler.Login
    → auth_service.Login
      → admin_repository / customer_repository / seller_repository
      → utils/hash.go + utils/jwt.go
    → utils/response.go
```

That is the same stack as the mermaid diagram in §3.5 A. Rows that say “same as #N” share that diagram.

---

## 4. All APIs used and why

Two layers of “API”:

1. **Internal** — every HTTP route this Go app exposes under `/api/v1` (and `/health`).
   Clients (Postman, web, mobile) call these.
2. **External** — AWS S3, Shippo, Google Places that *our* services call on the server.

Below: every internal endpoint from admin register onward with **why it exists**, then the
external calls and Go libraries.

Base path unless noted: `http://localhost:$APP_PORT/api/v1`.

### 4.1 Internal APIs (our `/api/v1` routes)

Typical setup order: **admin register → login → countries/capabilities → customer/seller
register → catalogue → orders → shipping / reels / public browse**.

#### Health

| Method | Path | Auth | Why included |
| --- | --- | --- | --- |
| GET | `/health` | — | Load balancer / ops liveness check (outside `/api/v1`) |

#### Admin bootstrap + auth (start here)

| Method | Path | Auth | Files | Why included |
| --- | --- | --- | --- | --- |
| POST | `/admin/register` | `X-Bootstrap-Secret` | `admin_register_routes.go`, `auth_handler.go`, `auth_service.go`, `admin_repository.go` | Create the **first** superadmin once; platform cannot be administered without it |
| POST | `/auth/login` | — | `auth_routes.go`, `auth_handler.go`, `auth_service.go` | Shared login; returns JWT + `role` for admin/customer/seller |
| POST | `/customers/login` | — | same as above | Same handler; convenient URL for the customer app |
| POST | `/sellers/login` | — | same as above | Same handler; convenient URL for the seller app |
| GET | `/admin/me` | JWT | `admin_routes.go`, `admin_handler.go`, `admin_service.go`, `admin_repository.go` | Load logged-in admin profile |
| PUT | `/admin/me` | JWT | same | Update admin `display_name` / `image_url` |

#### Countries + capabilities (admin configures markets)

| Method | Path | Auth | Files | Why included |
| --- | --- | --- | --- | --- |
| GET | `/countries` | — | `country_routes.go`, `country_handler.go`, `country_service.go`, `country_repository.go` | Apps need country ids for register/checkout |
| GET | `/countries/{id}` | — | same | One country detail |
| POST | `/admin/countries` | admin JWT | same | Add a market (ISO, currency, timezone, status) |
| PUT | `/admin/countries/{id}` | admin JWT | same | Edit a market |
| DELETE | `/admin/countries/{id}` | admin JWT | same | Remove a market (fails if still referenced) |
| GET | `/admin/country-capabilities` | admin JWT | `country_capability_handler.go`, `country_capability_service.go`, `country_capability_repository.go` | List feature flags for every country |
| GET | `/admin/countries/{id}/capabilities` | admin JWT | same | Flags for one country |
| POST | `/admin/countries/{id}/capabilities` | admin JWT | same | Create the 1:1 capability row |
| PUT | `/admin/countries/{id}/capabilities` | admin JWT | same | Turn registration/delivery/etc. on or off |
| DELETE | `/admin/countries/{id}/capabilities` | admin JWT | same | Remove capability row |

**Why capabilities exist:** `POST /customers/register` and `POST /sellers/register` refuse
with `403` when that country’s registration flag is off.

#### Customer register + profile + addresses

| Method | Path | Auth | Files | Why included |
| --- | --- | --- | --- | --- |
| POST | `/customers/register` | — | `customer_routes.go`, `customer_handler.go`, `customer_service.go`, `customer_repository.go` | Create buyer account (+ optional addresses) |
| GET | `/customers/me` | customer JWT | same | Profile + addresses |
| PUT | `/customers/me` | customer JWT | same | Update profile fields |
| DELETE | `/customers/me` | customer JWT | same | Soft-delete account |
| POST | `/customers/me/addresses` | customer JWT | same | Add shipping address |
| DELETE | `/customers/me/addresses/{id}` | customer JWT | same | Remove one address |

#### Recipients (who receives the gift)

| Method | Path | Auth | Files | Why included |
| --- | --- | --- | --- | --- |
| POST | `/customers/me/recipients` | customer JWT | `customer_*` | Create gift recipient (+ optional addresses) |
| GET | `/customers/me/recipients` | customer JWT | same | List recipients (no nested addresses) |
| GET | `/customers/me/recipients/{id}` | customer JWT | same | One recipient + addresses |
| PUT | `/customers/me/recipients/{id}` | customer JWT | same | Replace recipient fields |
| DELETE | `/customers/me/recipients/{id}` | customer JWT | same | Delete recipient (cascades addresses) |
| POST | `/customers/me/recipients/{id}/addresses` | customer JWT | same | Add ship-to address for that person |
| PUT | `/customers/me/recipients/{id}/addresses/{addressId}` | customer JWT | same | Update ship-to |
| DELETE | `/customers/me/recipients/{id}/addresses/{addressId}` | customer JWT | same | Delete ship-to |

**Why recipients exist:** Orders point at `recipient_id`; shipping uses recipient address as
ship-to, separate from the buyer’s own address book.

#### Saved gifts (wishlist)

| Method | Path | Auth | Files | Why included |
| --- | --- | --- | --- | --- |
| GET | `/customers/me/saved-gifts` | customer JWT | `customer_*` + `product_repository.go` | Wishlist with product embedded |
| POST | `/customers/me/saved-gifts` | customer JWT | same | Save a product id |
| DELETE | `/customers/me/saved-gifts/{id}` | customer JWT | same | Remove wishlist row (`{id}` = saved-gift id) |

#### Customer orders

| Method | Path | Auth | Files | Why included |
| --- | --- | --- | --- | --- |
| POST | `/customers/me/orders` | customer JWT | `order_handler.go`, `order_service.go`, `order_repository.go` | Place gift order; server prices from published products |
| GET | `/customers/me/orders` | customer JWT | same | Order history (headers only) |
| GET | `/customers/me/orders/{id}` | customer JWT | same | Order + line items |
| POST | `/customers/me/orders/{id}/cancel` | customer JWT | same | Cancel while still allowed |

#### Seller register + profile + addresses + shops

| Method | Path | Auth | Files | Why included |
| --- | --- | --- | --- | --- |
| POST | `/sellers/register` | — | `seller_routes.go`, `seller_handler.go`, `seller_service.go`, `seller_repository.go` | Create merchant (+ optional first shop/addresses) |
| GET | `/sellers/me` | seller JWT | same | Profile + addresses + shops |
| PUT | `/sellers/me` | seller JWT | same | Update seller profile |
| DELETE | `/sellers/me` | seller JWT | same | Soft-delete seller |
| POST | `/sellers/me/addresses` | seller JWT | same | Warehouse / pickup / return address |
| PUT | `/sellers/me/addresses/{id}` | seller JWT | same | Edit address |
| DELETE | `/sellers/me/addresses/{id}` | seller JWT | same | Delete address (clears shop links first) |
| GET | `/sellers/me/shops` | seller JWT | same | All own shops (any status, including draft) |
| POST | `/sellers/me/shops` | seller JWT | same | Open another storefront |
| PUT | `/sellers/me/shops/{id}` | seller JWT | same | Edit shop (name, slug, status, address ids) |
| DELETE | `/sellers/me/shops/{id}` | seller JWT | same | Hard-delete shop (cascades products/reels) |

#### Products + inventory

| Method | Path | Auth | Files | Why included |
| --- | --- | --- | --- | --- |
| GET | `/sellers/me/shops/{shopID}/products` | seller JWT | `product_handler.go`, `product_service.go`, `product_repository.go` | Catalogue for one owned shop |
| POST | `/sellers/me/shops/{shopID}/products` | seller JWT | same | Create product (+ optional inventory) |
| GET | `/sellers/me/products/{id}` | seller JWT | same | Product + inventory |
| PUT | `/sellers/me/products/{id}` | seller JWT | same | Update product |
| DELETE | `/sellers/me/products/{id}` | seller JWT | same | Delete product (fails if on an order) |
| GET | `/sellers/me/products/{id}/inventory` | seller JWT | same | Stock row |
| PUT | `/sellers/me/products/{id}/inventory` | seller JWT | same | Set qty / blackout dates |

#### Seller order items + shipping

| Method | Path | Auth | Files | Why included |
| --- | --- | --- | --- | --- |
| GET | `/sellers/me/order-items` | seller JWT | `seller_order_handler.go`, `order_service.go`, `order_repository.go` | Only this seller’s lines across orders |
| GET | `/sellers/me/order-items/{id}` | seller JWT | same | Line + order + product + recipient + ship-to |
| PATCH | `/sellers/me/order-items/{id}/accept` | seller JWT | same | `pending` → `accepted` (required before rates) |
| POST | `/sellers/me/order-items/{orderItemID}/shipping/rates` | seller JWT | `shipping_handler.go`, `shipping_service.go`, `shippo_client.go`, `shipment_repository.go` | Quote carriers; store pending shipment + parcel/customs |
| POST | `/sellers/me/order-items/{orderItemID}/shipping/labels` | seller JWT | same + `idempotency_repository.go`, `media_repository.go`, `s3_service.go` | Buy label, store PDF, mark item `dispatched` |
| POST | `/webhooks/shippo/tracking` | — (Shippo calls us) | `shipping_handler.go`, `shipping_service.go`, `shipment_repository.go` | Tracking updates; mark item/order delivered when complete |

**Why order-item (not order) routes for sellers:** one customer order can include products
from many sellers; each seller only fulfils their own line.

#### Media (S3 presign — used by sellers/admins with any JWT)

| Method | Path | Auth | Files | Why included |
| --- | --- | --- | --- | --- |
| POST | `/media/presign-upload` | JWT | `media_routes.go`, `media_handler.go`, `s3_service.go` | Get short-lived PUT URL + object `key` for uploads |
| GET | `/media/url?key=` | JWT | same | Temporary signed GET for private objects (labels) |

#### Reels (seller write + public read)

| Method | Path | Auth | Files | Why included |
| --- | --- | --- | --- | --- |
| POST | `/sellers/me/shops/{shopID}/reels` | seller JWT | `reel_routes.go`, `reel_handler.go`, `reel_service.go`, `reel_repository.go` | Shop promo reel (`product_id` optional) |
| GET | `/sellers/me/shops/{shopID}/reels` | seller JWT | same | Seller’s reels for that shop |
| POST | `/sellers/me/products/{productID}/reels` | seller JWT | same | Product-tagged reel |
| GET | `/sellers/me/products/{productID}/reels` | seller JWT | same | Seller’s reels for that product |
| GET | `/sellers/me/reels` | seller JWT | same | All own reels |
| GET | `/sellers/me/reels/{id}` | seller JWT | same | One own reel (any status) |
| PUT | `/sellers/me/reels/{id}` | seller JWT | same | Update caption/status/media |
| DELETE | `/sellers/me/reels/{id}` | seller JWT | same | Delete reel + media + S3 objects |
| GET | `/reels` | — | same | Public TikTok-style feed (cursor) |
| GET | `/reels/{id}` | — | same | One public reel; increments `view_count` |
| GET | `/shops/{shopId}/reels` | — | same | Public reels for a shop (`?scope=shop` for shop-only) |
| GET | `/products/{productId}/reels` | — | same | Public reels for a product |

#### Public storefront (no JWT)

| Method | Path | Auth | Files | Why included |
| --- | --- | --- | --- | --- |
| GET | `/shops` | — | `marketplace_routes.go`, `shops_handler.go`, `shop_marketplace_service.go` | Browse active shops |
| GET | `/shops/{shopId}` | — | same | Shop page header |
| GET | `/shops/{shopId}/products` | — | same + `product_repository.go` | Published products for that shop |
| GET | `/products/{productId}` | — | same | Product page + shop summary |

#### Places (Google proxy — public, rate-limited)

| Method | Path | Auth | Files | Why included |
| --- | --- | --- | --- | --- |
| GET | `/places/autocomplete` | — (120/min/IP) | `places_routes.go`, `places_handler.go`, `places_service.go` | Address typeahead before login |
| GET | `/places/details` | — (120/min/IP) | same | Turn `place_id` into address fields for forms |

#### Internal API dependency chain (why order matters)

```text
POST /admin/register
  → POST /auth/login  (admin JWT)
  → POST /admin/countries
  → POST /admin/countries/{id}/capabilities
  → POST /customers/register  +  POST /sellers/register
  → POST /sellers/login
  → POST /sellers/me/shops  →  POST .../products
  → POST /media/presign-upload  →  PUT S3  →  POST .../reels
  → POST /customers/login
  → POST /customers/me/recipients
  → POST /customers/me/orders
  → PATCH /sellers/me/order-items/{id}/accept
  → POST .../shipping/rates  →  POST .../shipping/labels
  → (Shippo) POST /webhooks/shippo/tracking
```

Public browse (`/shops`, `/products`, `/reels`) can be used anytime active/published data
exists — no admin token required for reads.

### 4.2 External HTTP / cloud APIs

These are **not** called by the frontend for business logic. Our handlers/services call them.

| External API | Called from | Our internal endpoints that trigger it | Why included |
| --- | --- | --- | --- |
| **AWS S3** | `services/s3_service.go` | `POST /media/presign-upload`, `GET /media/url`, reel create/delete, shipping label buy | Store media + label PDFs; browser uploads via presigned URL |
| **Shippo** (`https://api.goshippo.com`) | `shippo_client.go` via `shipping_service.go` | `POST .../shipping/rates`, `POST .../shipping/labels`; inbound `POST /webhooks/shippo/tracking` | Rates, labels, tracking without per-carrier SDKs |
| **Google Places (New)** (`https://places.googleapis.com`) | `places_service.go` | `GET /places/autocomplete`, `GET /places/details` | Address UX; API key stays on server |

Shippo upstream calls:

| Method | Path | Why |
| --- | --- | --- |
| POST | `/customs/declarations/` | International customs before rates |
| POST | `/shipments/` | Create shipment + return rates |
| GET | `/shipments/{id}/` | Fetch shipment if needed |
| POST | `/transactions/` | Buy label for `rate_object_id` |

Google Places upstream calls:

| Method | Path | Why |
| --- | --- | --- |
| POST | `/v1/places:autocomplete` | Typeahead |
| GET | `/v1/places/{placeId}` | Resolve to address fields |

S3 operations:

| Operation | Why |
| --- | --- |
| Presign PutObject | Client upload |
| Presign GetObject | Private download (labels) |
| PutObject (`Upload`) | Server stores Shippo PDF |
| DeleteObject | Reel delete cleanup |
| Public URL builder | `cdn_url` for `public/` keys |

PostgreSQL (via `pgx`) is the only database — every repository uses it.

### 4.3 Go module dependencies

| Module | Why included |
| --- | --- |
| `github.com/go-chi/chi/v5` | Router, path params, groups, middleware |
| `github.com/go-chi/cors` | Browser frontends on another origin |
| `github.com/jackc/pgx/v5` | PostgreSQL pool |
| `github.com/golang-jwt/jwt/v5` | HS256 access tokens |
| `github.com/google/uuid` | Media keys + parse path UUIDs |
| `github.com/joho/godotenv` | Load `.env` |
| `golang.org/x/crypto` | bcrypt passwords |
| `github.com/aws/aws-sdk-go-v2` (+ config, credentials, s3) | S3 client + presigner |

Shippo and Google Places use stdlib `net/http` — no extra SDKs.

### 4.4 Env vars that unlock each API

| Env var | Unlocks |
| --- | --- |
| `DB_*` | All internal APIs that touch Postgres |
| `JWT_SECRET`, `JWT_EXPIRY_MINUTES` | Login + every protected route |
| `BOOTSTRAP_SECRET` | `POST /admin/register` |
| `AWS_*`, `S3_BUCKET` | Media + label storage |
| `SHIPPO_API_KEY`, `SHIPPO_LABEL_BUCKET` | Shipping rates/labels |
| `GOOGLE_MAPS_API_KEY` | Places proxy |

Required at boot: `JWT_SECRET`, `DB_USER`, `DB_NAME`, S3 credentials + bucket,
`GOOGLE_MAPS_API_KEY`.  
`SHIPPO_API_KEY` optional at boot — shipping returns `503` when unset.

---

## 5. How a request flows

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

## 6. Conventions that apply to every endpoint

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
[8.14](#814-reels)).

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

## 7. Complete route map

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
| GET | `/products/{productId}/reviews` | — | `ProductReviewHandler.ListByProduct` |
| GET | `/products/{productId}/reviews/summary` | — | `ProductReviewHandler.SummaryByProduct` |
| GET | `/shops/{shopId}/reviews` | — | `ProductReviewHandler.ListByShop` |
| GET | `/reviews/{id}` | — | `ProductReviewHandler.GetPublic` |
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
| POST | `/customers/me/order-items/{orderItemId}/reviews` | `ProductReviewHandler.Create` |
| GET | `/customers/me/reviews` | `ProductReviewHandler.ListMine` |
| GET | `/customers/me/reviews/{id}` | `ProductReviewHandler.GetMine` |
| PUT | `/customers/me/reviews/{id}` | `ProductReviewHandler.Update` |
| DELETE | `/customers/me/reviews/{id}` | `ProductReviewHandler.Delete` |
| PUT | `/reviews/{id}/vote` | `ProductReviewHandler.Vote` |
| DELETE | `/reviews/{id}/vote` | `ProductReviewHandler.ClearVote` |

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
| GET | `/sellers/me/reviews` | `ProductReviewHandler.ListForSeller` |
| GET | `/sellers/me/reviews/{id}` | `ProductReviewHandler.GetForSeller` |
| PUT | `/sellers/me/reviews/{id}/reply` | `ProductReviewHandler.Reply` |
| DELETE | `/sellers/me/reviews/{id}/reply` | `ProductReviewHandler.ClearReply` |

---

## 8. Endpoints in detail

### 8.1 Health

`GET /health` — outside `/api/v1`, no auth, no body.

```json
{ "status": "ok" }
```

### 8.2 Auth and bootstrap

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

### 8.3 Admin profile

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

### 8.4 Countries and country capabilities

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

### 8.5 Customers

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

### 8.6 Recipients

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

### 8.7 Saved gifts

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

### 8.8 Customer orders

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
[10.4](#104-multi-seller-completion).

### 8.9 Sellers, addresses, shops

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

### 8.10 Products and inventory

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

### 8.11 Seller order items

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

### 8.12 Shipping (Shippo)

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

### 8.13 Media (S3 presign)

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

### 8.14 Reels

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

### 8.15 Public storefront browsing

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

### 8.16 Places (Google proxy)

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

## 9. Where each request/response struct lives

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

## 10. Cross-cutting flows

### 10.1 Upload anything

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

### 10.2 Publish a product reel

1. `POST /media/presign-upload` with `folder: "reel-video"` → PUT the file.
2. Optional second presign with `folder: "reel-thumbnail"` → PUT the cover image.
3. `POST /sellers/me/products/{productID}/reels` with the returned keys and
   `status: "published"`.
4. It appears in `GET /reels`, `GET /shops/{shopId}/reels`, and
   `GET /products/{productId}/reels?scope=product`.

### 10.3 Checkout to delivery

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

### 10.4 Multi-seller completion

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

### 10.5 Idempotent label purchase

`idempotency_key` is claimed in `core.idempotency_keys` (scope + unique key) before Shippo
is called and completed with the serialized shipment afterwards. A replay of the same key
short-circuits and returns the stored response; a new key on the same order item buys
another label. Keys are scoped to label purchase only — no other endpoint reads them.

---

## 11. Error catalogue

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

## 12. Known gaps and sharp edges

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
