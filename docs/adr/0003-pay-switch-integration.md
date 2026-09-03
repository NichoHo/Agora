# ADR 0003: pay authorizes cards through switch with a bounded synchronous wait

Status: accepted. Scope: `pay`, and one migration in the separate `switch` repo.

## Context

Phase 3 wires `pay` to `switch` for card authorization (`AGORA_SPEC.md`
section 9.1 and 7.4 step 3), and switch already models the hard case: an
acquirer response that never arrives. Its payment can land in
`AUTH_UNKNOWN`, neither approved nor declined.

Two constraints shape the design:

1. `market` and `sale` must not change (`AGORA_SPEC.md` section 6). Market's
   `PayClient.Fund` already only understands three outcomes: 200 success,
   402 insufficient funds, 409 already settled. Anything else it treats as
   a generic failure and leaves the order in `pending_payment`, unharmed.
2. `switch` already resolves `AUTH_UNKNOWN` on its own. Its
   `StatusProbeJob` polls the acquirer every 30 seconds (skipping payments
   under 10 seconds old) and settles the payment without anyone outside
   `switch` asking it to.

## Decision

`pay`'s `handleFund` gets one new step before its existing (untouched)
`Ledger.FundEscrow` call: `authorizeCard`. It calls switch's
`POST /v1/payments`, and if the state comes back `AUTH_UNKNOWN`, it polls
switch's own `GET /v1/payments/{id}` every 3 seconds for up to 90 seconds,
the same read `StatusProbeJob` itself relies on to have resolved by then.
See `internal/pay/switchclient.go`.

The HTTP request from `market` to `pay` simply takes longer when this
happens. Nothing in `market` or `sale` needs to know a wait occurred; both
already tolerate `pay` being slow or unreachable (`market`'s `handlePayOrder`
already logs and returns 502 on any `pay` error; `sale`'s reservation
already waits on `market`'s own order-funded event before confirming). No
new state, no new consumer, no new endpoint needed on either.

A declined outcome (`AUTH_DECLINED`, `RISK_DECLINED`,
`AUTHENTICATION_FAILED`) maps onto `pay`'s existing `ErrInsufficientFunds`,
so it reaches `market` as the same 402 a real declined wallet already
produces. Not a perfect label (a real fintech would want to distinguish
"your wallet is empty" from "your card was declined"), but exact under this
repo's own honest-simulation framing: there is no real card, only a
documented test BIN.

## What this costs

A `pay` request can now legitimately take up to 90 seconds, holding one
goroutine and one HTTP connection (no database transaction; `FundEscrow`
only starts after `authorizeCard` returns). A production system would push
this to a callback or a queue instead of blocking a request thread that
long. Not done here: it would mean giving `market` a way to learn about a
payment that resolves after its own request already returned, which is
exactly the endpoint `market` isn't getting (constraint 1, above).

## Making `pay` a real switch merchant

Switch's own `/admin/seed` endpoint creates a demo merchant whose
`api_key_hash` is the literal string `"hashed-key"`, not a real SHA-256
digest of anything, so nothing can authenticate as it. `pay` needs a
merchant it can actually log in as, so this migration adds one:
`Switch/gateway/src/main/resources/db/migration/V10__agora_merchant.sql`.
Raw key `agora-service-integration-key` (same trust tier as this repo's
other `dev-insecure-*` defaults), stored as its real SHA-256 hex digest.
This is the "inbound API contract" section 9.1 names as the one allowed
change to `switch`: a row, not a line of Java.

## Alternatives considered

**Return 202 immediately and let a webhook or event notify `market`
later.** Cleaner under load, but needs a new consumer somewhere in `market`
to act on it, which is the change section 6 rules out. Revisit if `pay`
ever needs to shed the 90-second hold under real concurrency; the 90-second
number is arbitrary today, not load-tested.

**Give `pay` its own resolver, polling the acquirer-sim directly instead of
switch.** Duplicates `StatusProbeJob`'s job on the wrong side of a trust
boundary (`pay` has no business talking to `switch`'s acquirer). Reading
switch's own resolved payment state is the correct boundary to poll.
