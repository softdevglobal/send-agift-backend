# Competition points

How the backend implements the *SendAGift Competition Points Developer
Handover, version 4* (26 October 2026). Read this before switching anything
on.

## What it is

- **Competition points** are a new unit. Customers earn them from qualifying
  completed gifts and spend them on skill competitions. The **legacy points**
  customers already hold (`points.points_accounts`) are a separate unit and are
  never converted into competition points.
- **Ordinary points:** every eligible sender, member or not, earns them from
  each qualifying gift, under the published reward policy.
- **Member bonus:** each paid membership period has one extra bonus. The first
  qualifying gift in that period unlocks it. Paying for the membership credits
  nothing by itself.
- **Entries:** a competition entry reserves points, captures them once when the
  attempt starts, or releases them once if it never starts.

## Nothing is on until it is approved

Every customer operation goes through the eligibility engine. The engine
answers *unavailable* unless all of these hold:

1. **An approval record** covers the operation in the customer's country and
   channel, plus the payment provider for operations that use one. The record
   must be active and inside its effective dates. It is requested by one admin
   and approved by a different one.
2. **No emergency stop** covers the operation in that country.
3. **Residence evidence:** for sales, entries and payouts, the account needs a
   verified mobile number from the account's country.
4. **Minimum age:** if the approval sets one, the account's date of birth must
   meet it.

| Operation | Channel to approve | Provider |
|---|---|---|
| `membership_sale` | `web` (or the app channel, if one is ever accepted) | `stripe` (`test` in a sandbox) |
| `membership_renewal` | `any` | `stripe` / `test` |
| `gift_points_earning` | `any` (runs on the server) | none |
| `competition_entry` | `web` | none |
| `winner_payout`, `seller_campaign_funding` | as approved | required |

A renewing membership whose renewal is no longer approved, or is stopped, is
set to end at its paid period end by the background job. Its benefits are
kept.

## Setting up a market (Super Admin, needs a password re-confirmation)

All under `/api/v1/admin/competition-points`.

1. **Approvals:** `POST /approvals` with the operation, country, channel,
   provider, an evidence reference and an expiry. A second admin then calls
   `POST /approvals/{id}/approve`.
2. **Reward policy:** `POST /policies` (fixed award, or points per currency
   unit, with an optional minimum spend, cap and expiry). Then
   `POST /policies/{id}/submit`, and a second admin calls
   `POST /policies/{id}/approve`, which publishes it. `POST /policies/preview`
   shows the ordinary and member scenarios before you publish.
3. **Membership offer:** `POST /offers` with the tier, price, the true interval
   and the *extra* bonus quantity, then submit and approve the same way.
4. **A competition on competition points:** create or edit it with
   `"points_unit": "competition"` and `"entry_cost_points": N`. Optionally set
   `"cp_allowed_sources"`. Only skill games won by the best verified score are
   accepted.

A published offer or policy never changes. A new price or rule is a new
version, and orders and periods keep the version they were sold under.

## Rules as implemented (proposed defaults from the handover)

- **Period anchor:** the gift's payment capture time (`finance.payments.paid_at`).
  Periods are half-open, `start <= anchor < end`, so a gift paid exactly at a
  boundary belongs to the next period.
- **Qualifying gift:** one paid order. Rewards are released when it is
  delivered. Free-gift orders and orders below the minimum spend do not
  qualify. Eligible spend is the order subtotal, without delivery.
- **First gift:** the first completed gift that claims the period's slot gets
  the bonus. A membership confirmed late is reconciled by anchor.
- **Refunds:**
  - Before award: the pending reward is cancelled.
  - After award: the gift's own unused points are reversed.
  - Points held by an entry: the reversal waits until the entry settles.
  - Points already spent: a review case is opened and the balance never goes
    negative.
  - A refunded bonus gift: its slot stays *reversed*, with no automatic second
    bonus.
- **Spend order:** soonest expiry first, then the oldest lot.
- **Restoration window:** `CP_RESTORE_WINDOW_HOURS` (sandbox default 24) keeps
  units released after their lot expired usable for that long.
- **Holds:** a reserved entry waits `CP_ENTRY_HOLD_MINUTES` (default 10) to
  start, then is released.
- **Voided attempt:** the lots it used are restored as new lots that name the
  originals, with the same source and expiry.

The values Appendix B leaves to the owner are listed by
`GET /admin/competition-points/settings` (`open_decisions`). Record each
decision in the release review.

## Customer API

| Endpoint | What it does |
|---|---|
| `GET /customers/me/eligibility` | Decision and reason code for sale, earning and entry |
| `GET /customers/me/membership/offers` | Offers on sale here, with the decision |
| `POST /customers/me/membership/checkout` | `{offer_id, renewal_consent: true}` with an `Idempotency-Key`; returns the provider's checkout URL |
| `GET /customers/me/membership` | State, paid periods, bonus slot, actual next charge |
| `POST /customers/me/membership/cancel` | Stops renewal at period end |
| `GET /customers/me/competition-points` | Lots, held entries, pending gifts, activity |
| `GET /competitions/{id}/entry-quote` | Cost, eligible balance, signed reference |
| `POST /competitions/{id}/entries` | `{reference}` with an `Idempotency-Key`; reserves |
| `POST /competitions/{id}/entries/{entryID}/start` | Captures and starts the attempt |
| `POST /competitions/{id}/entries/{entryID}/cancel` | Releases an entry that has not started |

Refusals carry a stable `code` (`LOCATION_UNAVAILABLE`,
`VERIFICATION_REQUIRED`, `CHANNEL_UNAVAILABLE`, `OPERATION_STOPPED`,
`INSUFFICIENT_ELIGIBLE_POINTS`, `QUOTE_EXPIRED`, and so on), a safe message and
a `next_action`. Native apps send `X-Client-Channel: ios` or `android`.

## Stripe

Memberships use Stripe Billing, through Checkout in subscription mode with the
price given inline. Add these events to the platform webhook endpoint:

- `checkout.session.completed`
- `customer.subscription.created`, `customer.subscription.updated`, `customer.subscription.deleted`
- `invoice.paid`
- `charge.refunded`, `charge.dispute.created`

Only a paid invoice creates a paid period. The redirect back from Stripe
activates nothing.

The handover notes that Stripe's restricted-business list includes skill
competitions with prizes. Do not approve `membership_sale` for `stripe` until
Stripe has accepted the exact model.

In a sandbox without Stripe, `MEMBERSHIP_TEST_PAYMENTS=true` enables
`POST /customers/me/membership/test-payment`. Never set it in production.

## Operations

- **Background job:** runs every minute on one server. It qualifies paid
  gifts, awards completed ones, awards deferred bonuses, reverses refunds,
  expires lots, releases lapsed holds and re-checks renewals.
  `POST /admin/competition-points/run` runs one pass at once.
- **Reconciliation:** `GET /admin/competition-points/reconciliation` compares
  every lot with its postings and every hold with its entry. It reports and
  never repairs.
- **Review cases:** `GET /admin/competition-points/cases`. Resolve them with a
  written decision. Any change to points is a separately approved grant.
- **Emergency stops:** a Super Admin can activate one at once, with
  `POST /admin/competition-points/stops`; lifting one needs a password
  re-confirmation. A stop refuses new affected actions on the server. Refunds
  and existing obligations continue.

## Not built yet

Each of these needs approvals the handover leaves open:

- seller campaign funding and pay-per-participation billing
- operated events, terminals and QR admission (online admission only, as in
  the initial release)
- head-to-head entries
- sponsored allocations

The schema reserves the lot source `sponsored` and the approval operation
`seller_campaign_funding` for them.
