# Agora

Agora models the parts of a marketplace that are genuinely difficult to get right: delegated identity, money that must never drift, and inventory under contention. Each is built from first principles rather than assembled from frameworks, because the interactions between them are where the real problems live.

![A single trace spanning the storefront through id, sale, market, pay, switch, and risk](load/results/phase5-trace-storefront-to-risk.png)

One request, one trace ID, six services (one of them a separate Java process across a language boundary) — [ADR 0004](docs/adr/0004-distributed-tracing.md) covers how.

## Architecture

```
                     Next.js storefront + IdP screens (:3001)
                                  │
     ┌───────────┬────────────────┼──────────────┬─────────────┐
     │           │                │              │             │
   id (:8081) market (:8082)  sale (:8085)   pay (:8083)  assist (:8084)
   OIDC      listings +      drops /         escrow       AI listing
   MFA       order FSM       admission       ledger       suggestions
     │           │                │              │             │
     └───────────┴────────────────┴──────┬───────┴─────────────┘
                                         │
                                 PostgreSQL 17
                            (schema per service)
                                         │
                            transactional outbox
                                         │
                              outboxkit relay
                                         │
                                 Redpanda (Kafka)
                                         │
                                   risk (:8086)
                              anomaly detection + console (in web/app/admin/risk)

              pay ──── HTTP/JSON ────► switch (Java, separate repo)
                                       card auth, routing, 3DS, failover
                                            │
                                            └──► events back to Redpanda
```

`sale` also talks directly to Redis (`:6379`, internal-only) for the admission queue and sharded stock reservations — see [ADR 0001](docs/adr/0001-redis-hot-path-authority.md). Every hop above is traced with OpenTelemetry, exported to Jaeger (`:16686`).

## Load test results

Five k6 scenarios against the real stack (one laptop, not a cloud instance — see [the write-up](docs/writeups/05-load-test-results.md) for the honest reasoning behind every miss):

| # | Scenario | Target | Result | Verdict |
|---|----------|--------|--------|---------|
| 1 | Drop spike (10,000 VUs, 5s ramp) | p99 < 200ms, no 5xx | p99 7.0s, 0 five-xx, 0 oversell (1000/1000 units, verified in Postgres) | Miss on latency; correctness held completely |
| 2 | Drop sustained (2,000 iter/s, 60s) | p99 < 300ms, error rate < 0.1% | p99 156.65ms, error rate 0.16%, 0 oversell (2000/2000, verified) | Pass on latency, narrow miss on error rate |
| 3 | Marketplace browse (500 req/s, 60s) | p99 < 100ms | p99 1.45ms, 0% failed | Pass, by two orders of magnitude |
| 4 | Checkout end to end (100 req/s, 60s) | p99 < 800ms | p99 818ms after a connection-pool fix; ~97% rejected by switch's own 100/min rate limit | Miss, and not a code problem — switch's anti-abuse policy working as designed |
| 5 | Ledger write (500 req/s, 60s) | p99 < 150ms, invariants hold | p99 2.57s, 100% correct, all three money invariants held | Miss on latency: every `FundEscrow` call locks the same shared escrow row (see the `ponytail:` comment in [`ledger.go`](internal/pay/ledger.go)) |

## The three hardest decisions

**Redis as the hot-path reservation authority ([ADR 0001](docs/adr/0001-redis-hot-path-authority.md)).** A drop's stock lives in Redis, decremented by one atomic Lua script per shard, with PostgreSQL as the durable source of truth reconciled asynchronously rather than checked on every request. The alternative — deciding oversell in Postgres directly — would have made `sale` correct but incapable of surviving the concurrency spike it exists to survive. The trade-off is real: a killed Redis instance mid-drop needs its own recovery path, not just a retry.

**A bounded synchronous wait instead of a webhook ([ADR 0003](docs/adr/0003-pay-switch-integration.md)).** When switch reports `AUTH_UNKNOWN` — the acquirer's response was lost — `pay` doesn't return early and wait for a callback. It polls switch's own status endpoint for up to 90 seconds, because switch already runs a `StatusProbeJob` against the acquirer every 30 seconds; `pay` only needs patience, not a second resolver duplicating logic switch already owns. An order can sit in `payment_indeterminate` for real, never silently promoted to funded or silently dropped.

**Tracing across a language boundary, not a network boundary ([ADR 0004](docs/adr/0004-distributed-tracing.md)).** The trace ID has to survive a hop from Go to a separate Java process in a separate repository. Riding it through the outbox payload rather than a bespoke propagation header meant every service that already writes to the outbox got tracing for free, and switch needed only a standard Java agent, no code change, to join the same trace.

## Run it

Prereqs: Docker Desktop. (Local Go 1.26+ / Node 24+ only needed for development.)

```sh
make up                          # docker compose up --build -d, runs the world
docker compose run --rm seed     # demo users, categories, listings
```

Open http://localhost:3001, register (or use `alice@vault.test` / `password123!`), approve the consent screen, and sell something. The sign-in you just did was a full OAuth 2.0 Authorization Code + PKCE round trip against the project's own identity provider.

Card authorization runs in wallet-only mode (`pay` skips `authorizeCard` entirely) unless `SWITCH_URL` is set. To see a real card charge round-trip through the separate Java service, clone [switch](https://github.com/NichoHo/Switch) next to this repo (as `../Switch`) and run both in one stack:

```sh
SWITCH_URL=http://gateway:8080 \
SWITCH_API_KEY=agora-service-integration-key \
OTEL_EXPORTER_OTLP_ENDPOINT=http://jaeger:4318 \
docker compose -f docker-compose.yml -f docker-compose.switch.yml up --build -d
```

`docker-compose.switch.yml` includes switch's own compose file, so `pay` reaches it at `gateway:8080` on the internal network. `SWITCH_API_KEY` is the demo key that switch's `V10__agora_merchant.sql` migration creates. `OTEL_EXPORTER_OTLP_ENDPOINT` sends switch's spans to this stack's Jaeger, so a checkout shows as one trace across both repos. Don't point local dev at switch's Render copy: it sleeps when idle, and every session uses its free instance-hours.

## Services

| Service | Port | What |
| --- | --- | --- |
| `id` (Go) | 8081 | OIDC provider: argon2id register/login, sessions, Auth Code + PKCE, RS256 JWT + JWKS, consent, TOTP MFA + recovery codes, rotating refresh tokens with family revocation on reuse, self-service deactivation, append-only audit log |
| `market` (Go) | 8082 | Listings CRUD + FTS, order state machine (`pending_payment→funded→shipped→completed`, cancel/refund), 15-min reservations, auto-release timer, transactional outbox → Redpanda relay |
| `sale` (Go) | 8085 | Limited-inventory drops: SSE queue admission, Redis-sharded Lua reservations, TTL sweeper, per-IP/per-user rate limits on reserve, hand-off to `market`/`pay` on checkout |
| `pay` (Go) | 8083 | Double-entry ledger: idempotent escrow fund/release/refund (10% platform fee), demo deposits, wallet, card authorization through `switch` with `AUTH_UNKNOWN` resolution. Internal money endpoints are shared-token gated |
| `assist` (Python) | 8084 | AI listing suggestions (Anthropic vision + structured outputs, prompt-injection-resistant, heuristic fallback under a daily spend cap), price bands from comparable sold history, trust scoring via Kafka consumer + admin review queue |
| `risk` (Python) | 8086 | Anomaly scoring (IsolationForest + explainable rules) over `id`/`sale`/`market`/`pay` events; console lives in `web/app/admin/risk` |
| `web` (Next.js) | 3001 | Storefront, checkout + escrow timeline, wallet, AI-assisted `/sell`, IdP screens incl. MFA, admin dashboard + risk console |
| `db` (Postgres 17) | 5432 | One database, schema per service |
| `redpanda` | 9092 | Kafka-compatible event bus |
| `redis` | 6379 (internal only) | `sale`'s hot-path stock and admission-queue authority |
| `jaeger` | 16686 (UI) | Distributed trace viewer |

`switch` is a separate Java/Spring Boot repository (card auth, routing, 3DS, failover) reached over HTTP once `SWITCH_URL` is set; it's the one service boundary in this diagram that's also a language boundary, which is the point of keeping it separate rather than folding it into this monorepo.

## OSS: outboxkit

The transactional-outbox relay and idempotent-consumer guard are extracted into [`outboxkit/`](outboxkit/), a standalone Go module (`github.com/NichoHo/outboxkit`) with its own tests, CI, MIT license, and [CONTRIBUTING guide](outboxkit/CONTRIBUTING.md). Agora imports it via a `replace` directive; the module is independently versioned and publishable. See the [outboxkit README](outboxkit/README.md) for usage.

## Development

```sh
make test   # unit tests; DB-backed tests need TEST_DATABASE_URL (below)
make fmt    # gofmt
make vet    # go vet

# DB-backed tests (id + market + pay + sale integration suites):
TEST_DATABASE_URL=postgres://vault:vault@localhost:5432/vault go test -race -p 1 ./...

# assist / risk tests:
cd assist && pytest tests/
cd services/risk && pytest

# storefront dev loop (talks to the compose services):
cd web && npm run dev

# e2e (against a running compose stack):
cd web && npx playwright test
```

- Windows note: the Go toolchain lives at `~/sdk/go/bin` if it's not on PATH; no `go` binary is assumed on the host otherwise — CI and this README's DB-backed commands run against a real Postgres either way.
- Tests recreate schemas in whatever database `TEST_DATABASE_URL` points at, so re-run seed afterwards.
- CI: gofmt + `go vet` + `govulncheck` + `go test -race`, a Next.js production build + `npm audit`, and `pytest` + `pip-audit` for both `assist` and `risk`.

## What the tests prove

Identity:
- **A replayed authorization code fails**, atomically claimed with `UPDATE … WHERE used_at IS NULL`.
- **A wrong PKCE `code_verifier` fails**; PKCE is mandatory.
- **`alg: none` and tampered tokens are rejected**; JWTs only verify RS256 against the published JWKS.
- **TOTP matches the RFC 4226 test vectors**; a recovery code works exactly once; a pending-MFA session is not signed in.
- **Refresh-token reuse burns the whole family**, writing a `refresh.reuse_detected` audit event.
- **A deactivated account is rejected on its very next request, not its next token refresh** — proven across a service boundary, not just inside `id`: `TestDeactivatedAccountRejectedImmediately` in `internal/market` deactivates a user mid-session and reuses the exact same still-valid access token against `market`.

Money:
- **Every transfer's entries sum to zero, globally and per account**; `balance == SUM(entries)` always, backed by a DB CHECK that refuses negative balances.
- **Concurrent double-spends fail**: 20 goroutines racing to spend a 10k balance in 1k bites, exactly 10 succeed.
- **Escrow zeroes out** on release and refund, mutually exclusive even when raced.
- **An `AUTH_UNKNOWN` card response resolves exactly once and never double-charges.** `TestAuthorizeAndResolve_ResolvesFromAuthUnknown` and `_GivesUpAfterMaxWait` force switch's ambiguous response and prove the resolver either settles to a real state or reports `AUTH_UNKNOWN` cleanly; `TestAuthorizeCard_IndeterminateBlocksFunding` proves an unresolved response never reaches the ledger.

Drops (`sale`):
- **No oversell.** `TestNoOversell` runs 50,000 concurrent reservation attempts against 1,000 units; exactly 1,000 succeed.
- **Conservation.** `TestConservation`: confirmed + released + expired + still-reserved always equals what was decremented from Redis.
- **Idempotency and per-user caps.** `TestIdempotency`, `TestPerUserCap`.
- **TTL correctness under a race.** `TestTTLExpiryRace`: the sweeper and a manual confirm racing still release exactly once.

Distributed systems:
- **The outbox chaos test kills the relay mid-flow and proves no lost or duplicated events**, generically for every service's outbox (including `sale`'s) since they all go through the same `outboxkit.Relay`.

Assist:
- **Price bands are honest quartiles**; trust rules flag new-account high-value listings and stay quiet for normal behavior.
- **The vision model's structured output is revalidated before use**, and a seller-typed hint is passed as delimited data the prompt can't be overridden by.

End-to-end (Playwright):
- **The full happy path runs in a single Playwright test**: register → MFA enroll → TOTP step-up login → AI-assisted listing → escrow buy → ship → confirm receipt → wallet reconciles.

## Trying the features

- **Escrow demo:** sign in as `bob@vault.test` (`password123!`), buy one of alice's listings, pay from the seeded wallet, then sign in as `alice@vault.test`, ship it, switch back to bob and confirm receipt. Watch both `/wallet` pages: bob −price, alice +90%, platform +10%, escrow zero.
- **AI sell:** `/sell` → type a title → Suggest. With `ANTHROPIC_API_KEY` exported before `docker compose up`, suggestions come from Claude vision; without it, a heuristic + comparable-price band keeps the flow alive.
- **MFA:** `2FA` in the header → enroll with any TOTP app → sign out → sign in again for the step-up prompt. Recovery codes are single-use.
- **Admin:** sign in as alice → `Admin` for suggestion acceptance rates, the trust review queue, and the risk console (`/admin/risk`).
- **Drops:** `sale`'s admission/reservation/checkout flow has no storefront page yet (see Honest limitations) — exercise it directly against `POST http://localhost:8085/drops` etc., or through `TestNoOversell` and its neighbors in `internal/sale/reservations_test.go`.

## Design

See [DESIGN.md](DESIGN.md) for the Nova design system: color palette, typography, spacing, components, and motion (light-only).

## Engineering write-ups

1. [Building an OIDC provider from the RFCs](docs/writeups/01-oidc-from-the-rfcs.md).
2. [The transactional outbox pattern in practice](docs/writeups/02-transactional-outbox-in-practice.md), which doubles as `outboxkit` documentation.
3. [What my tests prove: invariant testing for money code](docs/writeups/03-what-my-tests-prove.md).
4. [The answer that never comes: resolving a payment neither approved nor declined](docs/writeups/04-the-answer-that-never-comes.md).
5. [Load testing Agora: three real bugs and one honest miss](docs/writeups/05-load-test-results.md).
6. [A rate limiter that would have locked out real users](docs/writeups/06-a-rate-limiter-that-would-have-locked-out-real-users.md).

## Deploy

Two options exist, neither applied from this session:

- [`deploy/terraform/`](deploy/terraform/): a single-instance AWS deploy via Terraform. Cost: ~$15/month for a `t3.small`. Needs your AWS credentials.
- [`deploy/oracle/`](deploy/oracle/): Oracle Cloud's Always Free Ampere A1 tier (no ongoing cost), including a documented VCN-setup gotcha and a capacity-retry script for the "out of host capacity" error that tier is known for.

## Honest limitations

- **Educational IdP.** Production systems should use vetted identity libraries. Building one from the RFCs is the learning exercise.
- **Simulation throughout.** No real money, no real cards (switch accepts only its own documented test BINs), no KYC, no compliance posture.
- **Redis is a hot-path authority, not just a cache, for `sale`.** A Redis outage mid-drop needs the recovery path in [ADR 0001](docs/adr/0001-redis-hot-path-authority.md), not a plain retry — see that ADR before treating `sale` as stateless.
- **No storefront UI for drops.** The `sale` service (admission, reservation, checkout) is fully built and tested at the API layer; there's no `/drops` page in `web` yet.
- **No image upload pipeline.** `/sell` takes a pasted image URL, not an uploaded file — there's no content-sniffing, EXIF stripping, or private bucket to speak of because there's no upload endpoint at all yet.
- **Two pip-audit findings (`starlette`, `protobuf`) are pinned, not patched.** Both require bumping `fastapi`/`opentelemetry` past a version pip's resolver rejects at the versions this repo currently pins — see the comments in `assist/requirements.txt` and `services/risk/requirements.txt`.
- **Not deployed.** Both deploy paths above are written and unapplied; there's no live public URL from this session.
- **`outboxkit` publishing.** Lives as a monorepo submodule via `replace`; mirror-publishing to its own GitHub repo for real semver tags is the next step.

## License

MIT
