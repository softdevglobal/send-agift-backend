# Progressive Prize Engine

Implements *SendAGift – Progressive Prize Game Engine, Developer Specification v1.0* on top of the existing skill competitions. It covers every one of the sixteen skill games (2048, Snake, Fruit Slice, …). The game only decides the score. Points, prize, limits, audit and settlement are handled here, once, for all of them.

## Mapping to the spec

| Spec | Here |
| --- | --- |
| game / round | `competition.competitions` (one row per round; `round_no`, `previous_round_id`) |
| game_play | `competition.competition_attempts` (+ `client_request_id`, `prize_increment_cents`, `prize_before/after_cents`, `risk_metadata`, `refunded_at`) |
| prize_ledger | `competition.prize_ledger` (append-only, trigger-enforced) |
| wallet_transactions | `points.points_ledger` (append-only) + `points.points_accounts` (balance ≥ 0) |
| game_winners | `competition.competition_winners` (+ `prize_value_cents`, `settlement_status`, `settled_at`) |
| admin_audit_log | `admin.audit_log` (existing, append-only) |
| outbox_events | `core.outbox_events` |
| game_user_counters | derived from `competition_attempts` under the round lock (indexed); no separate table |
| status ACTIVE / SETTLED | `live` / `finalised`; `paused` is new |

Migration: `000043_progressive_prize_engine`. The points tables were created in `finance` and moved to the `points` schema by `000064_move_points_to_points_schema`.

## The play transaction

`POST /api/v1/competitions/{id}/plays` with an `Idempotency-Key` header (1–128 chars; `client_request_id` in the body is accepted too). A single database transaction (`CompetitionRepository.StartPlay`):

1. Lock the round row (`SELECT … FOR UPDATE`). Every change to a round's money happens under this lock, so increments are never lost and the cap cannot be overshot.
2. If the same customer has already used this key, return that play (`200`, `"replayed": true`) and charge nothing. If the key was used on a different round, the play is refused with `IDEMPOTENCY_CONFLICT`.
3. The round must be `live` and inside its window, by the database clock.
4. Round and daily limits. The daily limit uses the calendar day in the round's time zone.
5. Lock the points account, then check the balance.
6. Work out the increment: `min(increment, max − current)`. At the cap the play adds `0`, or is refused when `continue_at_cap` is false.
7. Write the points debit, the game session, the play, the `play_increment` ledger row, the cached prize and counters, and the `PRIZE_UPDATED` and `GAME_PLAY_COMPLETED` outbox rows.

If any step fails, the whole transaction rolls back. `POST /competitions/{id}/attempts` still works for older app builds; it generates a key per request.

### Receipt (`201`)

```json
{
  "play_id": "…", "status": "COMPLETED", "points_spent": 10,
  "wallet_points_remaining": 420, "prize_before_cents": 34700,
  "prize_increment_cents": 100, "prize_after_cents": 34800,
  "prize_cap_reached": false, "played_at": "…", "replayed": false,
  "session": { "session_id": "…", "seed": "…", "config": { } },
  "attempt_id": "…", "attempt_number": 3, "attempts_remaining": 17
}
```

### Refusals

Nothing is charged for any of these. The body is `{"error", "code", …details}`.

| HTTP | code | details |
| --- | --- | --- |
| 400 | `GAME_NOT_ACTIVE` | `status` (`scheduled`/`paused`/…), `starts_at` |
| 400 | `OUTSIDE_GAME_WINDOW` | `ends_at` |
| 409 | `PLAY_LIMIT_REACHED` | `kind` (`round`/`daily`), `limit`, `next_eligible_at` |
| 409 | `IDEMPOTENCY_CONFLICT` | |
| 409 | `PRIZE_CAP_REACHED` | |
| 409 | `PLAY_BUSY` | retry |
| 403 | `NOT_ELIGIBLE` | message says why (age, country, compliance gate…) |
| 422 | `INSUFFICIENT_POINTS` | `points_required`, `points_balance` |
| 429 | `RATE_LIMITED` | `Retry-After` header (20 plays/min per customer) |
| 500 | `PLAY_TRANSACTION_FAILED` | rolled back; safe to retry with the same key |

## Live prize

`GET /api/v1/competitions/{id}/events` is a Server-Sent Events stream. It opens with `SNAPSHOT`, then sends `PRIZE_UPDATED` and `STATUS_CHANGED`, read from the outbox after their transaction commits. It sends a keep-alive comment every 15 seconds.

```json
{"event":"PRIZE_UPDATED","game_id":"…","current_prize_cents":34800,"eligible_play_count":248,"version":910}
```

Clients should ignore events whose `version` is older than what they show, and should re-read `GET /competitions/{id}` now and then; the mobile app does so every 30 seconds. Live events older than seven days are pruned.

## Prize arithmetic

- Stored in integer minor units of `prize_currency`.
- `current_prize_cents` is a cache, always equal to the ledger sum. A check constraint keeps it within `[0, max_prize_cents]`.
- A fixed prize is a round with `prize_growth_enabled = false`. It still gets a `seed` entry, so every round has a ledger.
- Winners split the prize the round closed on (`final_prize_cents`) equally. Cents that don't divide evenly go to first place.

## Super Admin (`superadmin` role)

The `admin` role (support) is read-only: lists, ledger, plays, analytics, boards, review queue, freeze. Everything that changes economics or pays out needs `superadmin`, and every such action writes an audit row.

| Method | Path | |
| --- | --- | --- |
| POST / PUT / PATCH | `/admin/competitions[/{id}]` | economics fields below; `config_version` guards against stale edits (`409 CONFIG_VERSION_CONFLICT`) |
| POST | `/admin/competitions/{id}/schedule` (alias `/activate`) | publishes and posts exactly one SEED entry |
| POST | `/admin/competitions/{id}/pause` · `/resume` · `/close` | plays already started can still be scored while paused |
| POST | `/admin/competitions/{id}/cancel` | voids plays, refunds every point, and withdraws the prize with a correction entry |
| POST | `/admin/competitions/{id}/prize-adjustments` | `{amount_delta_cents, reason}` (reason required; cannot pass the cap or go below 0) |
| POST | `/admin/competitions/{id}/plays/{playID}/void` | `{reason, refund_points=true, reverse_prize=true}` |
| POST | `/admin/competitions/{id}/settle` | `{reference?}`: pays every validated, unpaid winner; retries only pay who is still pending |
| POST | `/admin/competitions/{id}/duplicate` | `{starts_at, ends_at, next_round}`: a new round never erases the old one |
| GET | `/admin/competitions/{id}/ledger` | entries plus a fresh reconciliation |
| GET | `/admin/competitions/{id}/plays` · `/analytics` | spec §10 dashboard, rejections by reason, velocity and concentration |
| POST | `/admin/competitions/{id}/reconcile` | also runs every 15 minutes in the API process |
| GET | `/admin/customers?q=` · `/admin/customers/{id}/points` | (admin) |
| POST | `/admin/customers/{id}/points/adjustments` | `{amount, reason, idempotency_key}` |

Economics fields: `prize_growth_enabled`, `start_prize_cents`, `increment_per_play_cents`, `max_prize_cents`, `continue_at_cap`, `daily_play_limit`, `min_plays_to_win`, `prize_type` (`cash`/`product`/`voucher`/`gift`/`other`), `winner_method` (`score` only, because every game here is a skill game).

Customer: `GET /api/v1/customers/me/points` returns the balance, lifetime totals and history.

## Publishing gates

On top of the existing gates (country enabled, rules published, funded reserve):

- **Compliance:** a growing prize needs the country's `progressive_prizes_enabled` capability. It is off by default and should only be turned on after legal sign-off, per the spec's compliance hook.
- A growing prize needs a `max_prize_cents`, and the funded reserve must cover it. The most the prize can reach is always pre-funded.
- A round that costs points needs `points_usage_enabled` for its country.

## Reconciliation

`Reconcile` recomputes the prize from the ledger and checks the links between plays, points and prize entries:

- cached prize = ledger sum
- running balance = ledger sum
- play counter = plays that are not voided
- one increment entry per incrementing play, with the amount matching
- reversals only on voided plays
- one points debit per charged play, and one refund per refunded play, with the amounts matching

Each run is stored in `competition.reconciliation_runs`. A discrepancy is written to the audit log and never corrected automatically.

## Tests

- `go test ./internal/repository` covers the pure prize math.
- The database-backed acceptance tests (AC-02–AC-15, including 100 concurrent plays and the cap under concurrency) need a throwaway database:

  ```sh
  TEST_DATABASE_URL=postgres://user:pass@localhost:5432/scratch_db?sslmode=disable \
      go test ./internal/services -run Prize -race
  ```

## Chance games (migration `000044`)

Five catalog games with `game_type = 'chance'`: `spin-wheel`, `scratch-card`, `treasure-hunt`, `instant-win` (winner method `instant`) and `prize-draw` (winner method `draw`). They never appear in the practice collection.

- **Instant:** inside the play transaction the server draws a uniform integer in `[0, win_odds)` with `crypto/rand`; `0` wins. The result (`mechanic`, `won`, `odds`, `draw`, `algorithm`, plus the reveal details — wheel segment, scratch symbols, chest contents) is stored on the play (`result_payload`) and returned as `result` in the receipt. A win records the winner at the prize the play left and closes the round under the same lock, so there is exactly one winner. `win_odds` is required; winners is fixed at 1. Finalise from `closed`.
- **Draw:** every play is one entry. When closed, `POST /admin/competitions/{id}/draw` samples entries without replacement with `crypto/rand`, one win per customer, skipping (and recording) anyone no longer eligible. The ordered entry list, its SHA-256, every random value and every pick are stored in `competition.prize_draws` (append-only); winners split the prize; the round is finalised in the same transaction. `GET …/draw` returns the record.
- **Gate:** the country's `chance_games_enabled` capability, off by default, is required to publish and to play. Paid chance games with prizes are regulated as gambling or lotteries in most places — legal sign-off first.

## Points earning

`points.points_earning_rules`, one per country, set by a Super Admin (`GET /admin/points/earning-rules`, `PUT /admin/points/earning-rules/{countryID}`): `points_per_unit` per whole unit of the order currency (rounded down; zero-decimal currencies handled) and a `signup_bonus`. A job every 5 minutes (or `POST /admin/points/earning-runs`):

- awards delivered orders (`order_reward`, key `order:<id>`) — only orders delivered after the rule was switched on;
- takes points back from refunded or cancelled orders (`order_reversal`), up to what the customer still holds;
- pays the sign-up bonus to customers who joined after it was switched on (`signup_bonus`).

Every award is keyed, so passes can repeat or overlap without paying twice. Requires the country's `points_earning_enabled`. Customers: `GET /customers/me/points/earning`.

## Re-authentication

`POST /admin/reauth {password}` returns a 5-minute `reauth_token` (role `reauth`, useless as a login). Cancel, prize adjustments, voids, settlement, draws, points adjustments and earning rules also require it in `X-Reauth-Token`, else `403 REAUTH_REQUIRED`. The admin app asks for the password once and retries.

## Fraud controls

Plays are limited per account (20/min), per device (`X-Device-Id`, 20/min) and per network address (120/min, high so shared networks aren't blocked). Each play stores its device id and network prefix (/24 or /48 — not the exact address) in `risk_metadata`; analytics lists devices and networks used by several accounts.

## Quiz (migration `000045`)

`quiz` is a skill game (winner method `score`) whose content must stay secret, so each round keeps its own questions in `competition.quiz_questions`: prompt, 2–6 options, the correct option and a 5–120 second limit, 1–50 questions, set in the round's `quiz_questions` and editable until it starts.

- A play's session is dealt the prompts, options and limits only — never the answers.
- Moves are `question:option:ms` (option `-1` = time ran out). The server scores them from its own copy: 100 per right answer plus up to 50 for speed, `50 × (limit − ms) / limit`. The fastest possible play is 80% of the answer times, checked against the real session length like every game.
- The same server-side scoring re-verifies the leading scores at finalisation.
- The app sends no client score for a quiz (it cannot know it).

## Spec coverage

| Spec section | Where it lives |
| --- | --- |
| §1 System overview, prize as a ledger, §1.3 calculation | `competition.prize_ledger`, `postPrize`, `Reconcile` |
| §2 Configuration & Super Admin controls | `CompetitionInput`, admin competition form; start/increment/max/cap, per-user and daily limits, min plays to win, winner count and method, prize type, growth on/off, statuses incl. `paused` |
| §2.1 Admin actions | create/edit (versioned)/duplicate/new round, schedule (= activate), pause/resume/close/cancel, prize adjustments with reason, analytics |
| §3 Data schema | migrations `000043`–`000045` (see mapping above) |
| §4 Atomic play, §4.2 idempotency | `StartPlay`; `Idempotency-Key`, unique per customer |
| §5 API contract, §5.3 failure codes, §5.4 live updates | routes above; `PlayRefusal`; SSE `/competitions/{id}/events` |
| §6 Screens & UI states | mobile: game card, detail, play, receipt, insufficient points, limit reached (reset time), paused, closed, cap reached, win/claim, chance reveals, quiz; admin: list, editor, ledger, dashboard |
| §7 Integrity & fraud | server authority, one transaction, row lock, idempotency, per user/device/IP limits, superadmin RBAC + re-auth, append-only audit and ledgers, secure random with stored metadata, reconciliation job |
| Compliance hook | per-country `progressive_prizes_enabled` and `chance_games_enabled`, off by default |
| §8 Edge cases | integration tests: concurrent plays, double tap, partial increment at cap, cap reached, failure rollback, void/refund, $200→$250 adjustment, pause boundary, server-time window, settlement retries |
| §9 Automations | seed on publish, play, close by clock, adjustment, refund/void, winner/settlement, reconciliation, points earning |
| §10 Analytics | `/admin/competitions/{id}/analytics` and the dashboard cards |
| §11 AC-01 – AC-15 | `internal/services/prize_integration_test.go` (database-backed) |
| Game types | skill games (16), SPIN, SCRATCH, TREASURE, INSTANT_WIN, DRAW, QUIZ |
