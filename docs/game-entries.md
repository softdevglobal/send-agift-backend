# Advertising packages and game entries

Points are retired. Customers earn **game entries** by buying products a
seller promotes, and spend them to play skill games in the mobile app.
Sellers promote products by buying an **advertising package** an admin
created. Games and entry earning are **off by default** and are switched on
per country, per state and per game.

## What happened to points

Migration `000082_game_entries` renames the `points` schema to
`legacy_points` and moves the competition-points and bonus tables into it.
Nothing is deleted. Customer and seller balances are frozen by a trigger and
are listed at `GET /admin/legacy-points` until the owner decides whether to
refund or convert them. No code path reads or writes the old balances.

Points columns on live tables keep their data under a `legacy_` name
(`order_items.legacy_reward_points`, `orders.legacy_gift_points`,
`products.legacy_reward_points`, `competitions.legacy_points_per_attempt`,
and so on).

## Advertising packages (`ads` schema)

An admin creates a package for one country: name, price and currency, the
entries a customer earns per purchase at each priority level (top level
first; the number of levels is the number of products it promotes), a total
entries budget (`entries_pool`) and a validity period in days. A published
package never changes: editing makes a new version, and a seller's purchase
copies the terms it was bought on.

| Who | Endpoint |
| --- | --- |
| Admin | `GET/POST /admin/advertising/packages`, `PUT/DELETE /admin/advertising/packages/{id}`, `POST /admin/advertising/packages/{id}/{publish\|retire\|new-version}` |
| Admin | `GET /admin/advertising/purchases`, `POST .../{id}/confirm`, `POST .../{id}/reminder` |
| Seller | `GET /sellers/me/advertising`, `POST /sellers/me/advertising/purchases`, `POST .../{id}/{cancel\|sync\|test-payment}`, `PUT .../{id}/promotions` |

A purchase is `pending` until Stripe confirms a paid Checkout Session whose
amount, currency and seller match it; then it is `active` for its validity
period. It ends as `exhausted` when the budget can no longer pay the
smallest level, or `expired` when its time runs out. Either way its products
leave the promoted list. Nothing is bought or renewed automatically, and
unused budget or time is not refunded.

The seller is told when 80, 85, 90, 95 and 100% of the budget is used: one
notice per mark per purchase, by email and in the dashboard. Passing several
marks at once sends only the highest, and a refund never sends a mark again.

## Game entries (`entries` schema)

1. **Checkout.** Each promoted line is promised its level's entries, held
   out of the seller's budget (`ads.purchases.entries_held`). Never a part
   award: the full amount, or none.
2. **Payment confirmed.** The hold becomes `entries_used` and the customer
   is granted the entries, in one transaction. Unpaid, abandoned and failed
   checkouts grant nothing; cancelling releases the hold.
3. **Refund or cancellation.** The grant is reversed and the seller's budget
   gets it back. Only what the customer still holds is taken; the rest is
   recorded in `entries.shortfalls` for review. A balance never goes below
   zero.

A customer earns nothing from their own products (same email as the
seller), above the daily caps of where they live, or where earning is off.
Customers cannot buy entries. Each game costs `competition.games.entry_cost`
entries per play, set by an admin.

| Who | Endpoint |
| --- | --- |
| Customer | `GET /customers/me/game-entries`, `POST /customers/me/game-entries/offers`, `POST /customers/me/game-entries/free` |
| Admin | `GET /admin/customers/{id}/game-entries`, `POST .../adjustments`, `GET /admin/game-entries/shortfalls`, `POST .../{id}/resolve` |

## States and the games gate

`core.states` lists each country's states (admin managed). Sign-up, by form
or through Google, Facebook or Apple, must give a state when the country has
states. Existing customers are asked through `GET/PUT /customers/me/state`
and cannot earn or play until they answer. The first answer is free; a
change needs the cooldown (`STATE_CHANGE_COOLDOWN_DAYS`) to have passed and
a verified phone number. The state is never taken from an IP address.

Earning and play are allowed only when all of these hold:

- the programme is on and not stopped (`entries.program`);
- the customer's country and state has a row in `entries.jurisdictions`
  with earning (or play) on and not stopped — a state never inherits from
  its country;
- the customer has a date of birth that meets that place's minimum age;
- for a play: the game is switched on there, chance games only where
  allowed, and a competition limited to states includes the customer's.

The gate is checked when entries are promised, again when they are granted,
and inside the transaction of every play. Plays are accepted from the iOS
and Android apps only.

| Who | Endpoint |
| --- | --- |
| Public | `GET /countries/{id}/states` |
| Admin | `GET /admin/games-control`, `PUT /admin/games-control/program`, `POST /admin/games-control/program/stop` |
| Admin | `PUT /admin/games-control/jurisdictions`, `POST .../{id}/stop`, `POST .../{id}/lift` |
| Admin | `GET /admin/countries/{id}/states`, `POST /admin/states`, `PUT/DELETE /admin/states/{id}` |

## Decisions still with the owner

- What to do with the frozen legacy balances (refund or convert).
- Real package prices, entry numbers, budgets and validity periods.
- Daily caps, minimum ages and official rules per country and state.
- Which countries, states and games to switch on, and whether a free entry
  route (`free_entries_per_day`) is required there.
