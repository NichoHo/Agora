# ADR 0002: A drop's units are minted as individual market listings

Status: accepted. Scope: the `sale` <-> `market` boundary.

## Context

`AGORA_SPEC.md` names two constraints that pull against each other:

1. `market`'s order flow is not to change (Section 6: "do not rewrite id,
   market, pay, or assist... move them, do not touch them").
2. A drop hands a successful reservation to `market` as a real order
   (Section 7.4: "sale calls market to create an order in pending_payment").

`market`'s listing model is one order per listing: `POST /orders` locks the
listing row, requires `status = 'active'`, and flips it to `reserved` on
success. A drop sells many units of one item to many different buyers. If
every buyer's order pointed at the same `listing_id`, only the first order
would ever be created; every other buyer would see 409 "listing is not
available", regardless of how many units the drop still has.

## Decision

At drop-open time (`POST /drops/{id}/open`), `sale` calls `market`'s own
`POST /listings` once per unit, using the seller's own bearer token, and
records each returned `listing_id` in `sale.drop_units`. A drop with 500
units becomes 500 ordinary market listings, all identical, each good for
exactly one order. `sale`'s Redis shards hold these listing ids, not an
abstract count, so claiming a unit and claiming a specific sellable listing
are the same atomic operation (see ADR 0001).

This needs no change to `market`. Every order a drop produces is a normal
market order, funded through the normal `pay` flow, visible in the normal
order history, cancellable through the normal endpoint.

## What this costs

Priming a 1,000-unit drop is 1,000 sequential HTTP calls to `market` before
the drop opens. That is acceptable: it runs once, well before `starts_at`,
never on the request path a buyer waits on. A very large drop (tens of
thousands of units) would want this parallelized; not done here; add it if
a real drop needs it.

**The market-status catch-up window.** `sale` releases an expired unit back
to its Redis shard as soon as *its own* TTL fires (5 minutes by default).
The `market` listing behind that unit is still `reserved` until `market`'s
own reservation sweep runs (`AUTO_RELEASE_AFTER`-independent; it's the
15-minute `market.reservations` sweep in `internal/market/orders.go`, which
this ADR does not touch). In that window, a second buyer's Redis claim can
succeed for a listing that `market` has not yet reopened. `sale` does not
try to force `market`'s hand here: doing so needs a service-level "act on
behalf of the buyer/seller" credential that `market`'s auth model has no
concept of (it authorizes strictly by `buyer_id`/`seller_id`, not by role),
and adding one means touching `market`, which Section 6 rules out.

Instead, `internal/sale/reservations.go`'s checkout step treats `market`'s
409 as expected, not fatal: it claims a different unit and retries once. A
buyer who hits this sees, at worst, one extra hundred milliseconds on
checkout; they never see the conflict. No unit and no reservation row are
ever lost by it; see `TestConservation` in
`internal/sale/reservations_test.go`.

## Alternatives considered

**One listing per drop, quantity tracked only in `sale`.** Needs `market`
to accept multi-unit orders against one listing, which is exactly the
"don't touch market" line this migration draws.

**A service-account user with elevated market permissions.** Solves the
catch-up window cleanly, but `market`'s authorization is not role-based
(Section 9.1 only defines service JWTs for *verifying who a service is*,
not for acting as an arbitrary buyer or seller), so this is new surface on
`market`, not just new surface on `sale`. Revisit if the catch-up window
ever shows up as a real user complaint rather than a documented edge case.
