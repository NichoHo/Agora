# Vault

A compact C2C marketplace with three deliberately hard parts built from scratch: **identity** (an OAuth 2.0 / OIDC provider with TOTP MFA, refresh-token rotation, and session security), **money** (escrow checkout on a double-entry ledger), and **an AI listing assistant** (photograph an item, get suggested title/description/category/price band). Everything is invariant-tested, event-driven, and runs with one command.

> **Honest framing:** this is an educational IdP. Production systems should use vetted libraries; building one from the RFCs is the point here. Simulated deliveries, synthetic data, no real money.

## Architecture

```
             Next.js storefront + IdP screens (:3001)
               │ /idp/* rewrite (same-origin proxy)  │ server-side JSON
               ▼                                     ▼
   id (Go, :8081) ◄─JWKS── market (Go, :8082) ──X-Internal-Token──► pay (Go, :8083)
   OIDC + PKCE, TOTP MFA,  listings + FTS,                          double-entry ledger,
   refresh rotation w/     order state machine,                     idempotent escrow
   reuse detection,        reservations (15m TTL),                  fund/release/refund,
   sessions, audit log     outbox (tx) ──┐                          outbox (tx) ──┐
               │                         │                                        │
               │              ┌──────────▼────────────────────────────────────────┘
               │              │  outboxkit relay (at-least-once)
               │              ▼
               │          Redpanda (:9092)   ◄── Kafka-compatible event bus
               │              │
               │              │  market.events / pay.events topics
               │              ▼
               └──────┬───────────────────┐
          PostgreSQL 17                assist (Python/FastAPI, :8084)
          (schema per service)         Kafka consumer → trust rules,
                                       VLM listing suggestions,
                                       price bands from comparables
```

State changes and events are written in a single database transaction (the [transactional outbox pattern](docs/writeups/02-transactional-outbox-in-practice.md)). An `outboxkit` relay publishes committed rows to Redpanda; consumers achieve exactly-once *effects* via idempotent deduplication. The pattern is extracted into [`outboxkit/`](outboxkit/) as a standalone, publishable Go module.

## Services

| Service | Port | What |
| --- | --- | --- |
| `id` (Go) | 8081 | OIDC provider: argon2id register/login, sessions, Auth Code + PKCE, RS256 JWT + JWKS, consent, TOTP MFA + recovery codes, rotating refresh tokens with family revocation on reuse, append-only audit log |
| `market` (Go) | 8082 | Listings CRUD + FTS, order state machine (`pending_payment→funded→shipped→completed`, cancel/refund), 15-min reservations, auto-release timer, transactional outbox → Redpanda relay |
| `pay` (Go) | 8083 | Double-entry ledger: idempotent escrow fund/release/refund (10% platform fee), demo deposits, wallet. Internal money endpoints are shared-token gated. Transactional outbox → Redpanda relay |
| `assist` (Python) | 8084 | AI listing suggestions (Anthropic vision + structured outputs, heuristic fallback), price bands from comparable sold history, trust scoring via Kafka consumer + admin review queue |
| `web` (Next.js) | 3001 | Storefront, checkout + escrow timeline, wallet, AI-assisted `/sell`, IdP screens incl. MFA, admin dashboard |
| `db` (Postgres 17) | 5432 | One database, schema per service |
| `redpanda` | 9092 | Kafka-compatible event bus (market.events, pay.events topics) |

## OSS: outboxkit

The transactional-outbox relay and idempotent-consumer guard are extracted into [`outboxkit/`](outboxkit/), a standalone Go module (`github.com/NichoHo/outboxkit`) with its own tests, CI, MIT license, and [CONTRIBUTING guide](outboxkit/CONTRIBUTING.md). Vault imports it via a `replace` directive; the module is independently versioned and publishable. See the [outboxkit README](outboxkit/README.md) for usage.

## Run it

Prereqs: Docker Desktop. (Local Go 1.26+ / Node 24+ only needed for development.)

```sh
make up                          # docker compose up --build -d, runs the world
docker compose run --rm seed     # demo users, categories, 12 listings (or: make seed with local Go)
```

Open http://localhost:3001, register (or use `alice@vault.test` / `password123!`), approve the consent screen, and sell something. The sign-in you just did was a full OAuth 2.0 Authorization Code + PKCE round trip against the project's own identity provider.

## What the tests prove

`go test -race ./...` + `pytest assist/tests` + `npm run e2e` (CI runs Go with `-race` against a real Postgres):

Identity:
- **A replayed authorization code fails.** Codes are claimed with a single atomic `UPDATE … WHERE used_at IS NULL`; the second exchange gets `invalid_grant`.
- **A wrong PKCE `code_verifier` fails.** The S256 challenge comparison is constant-time. PKCE is mandatory: the IdP refuses requests without it.
- **An expired code fails**, and **an unregistered `redirect_uri` is rejected without redirecting**.
- **`alg: none` and tampered tokens are rejected**; JWTs only verify RS256 against the published JWKS, with expiry/issuer/audience enforced.
- **TOTP matches the RFC 4226 test vectors**; stale codes outside the ±1-step window fail; a recovery code works exactly once; a pending-MFA session is not signed in.
- **Refresh-token reuse burns the whole family.** Rotation works; presenting a rotated-away token revokes every descendant (the newest token dies too) and writes a `refresh.reuse_detected` audit event.

Money:
- **Every transfer's entries sum to zero, globally and per account**, so value is conserved; `balance == SUM(entries)` always, backed by a DB CHECK that refuses negative balances.
- **Concurrent double-spends fail**: 20 goroutines racing to spend a 10k balance in 1k bites, exactly 10 succeed, final balance exactly 0.
- **Escrow zeroes out** on release (seller +90%, platform +10%) and on refund (buyer whole), and release/refund are mutually exclusive even when raced.
- **Timer-vs-manual confirm releases exactly once**: the auto-release sweeper and the buyer's confirm racing produce exactly one `escrow_release` transfer (idempotency keys + a status-guarded UPDATE).
- **Concurrent buys of one listing produce exactly one order** (`SELECT … FOR UPDATE`); expired reservations re-activate the listing; strangers get 404s on orders they're not part of.

Distributed systems:
- **The outbox chaos test kills the relay mid-flow and proves no lost or duplicated events.** A `recordingPublisher` injects a crash on the 2nd batch; the relay retries and drains all 25 events. An `Idempotent.Guard` consumer then proves exactly 25 effects fired: redelivery happened, but deduplication held.
- **The relay's at-least-once contract:** `FOR UPDATE SKIP LOCKED` + in-transaction publish means a crash after publish but before COMMIT re-publishes, never loses.

Assist:
- **Price bands are honest quartiles** with sane single-comparable behavior; **trust rules flag** new-account high-value listings and rapid listing bursts, and stay quiet for normal behavior.

End-to-end (Playwright):
- **The full happy path runs in a single Playwright test**: register → MFA enroll → TOTP step-up login → AI-assisted listing → escrow buy → ship → confirm receipt → wallet reconciles (bob −$50, seller +$45 after 10% fee).

## Development

```sh
make test   # unit tests; DB-backed tests need TEST_DATABASE_URL (below)
make fmt    # gofmt
make vet    # go vet

# DB-backed tests (id + market + pay integration suites):
TEST_DATABASE_URL=postgres://vault:vault@localhost:5432/vault go test -race ./...

# assist tests:
cd assist && pytest tests/

# storefront dev loop (talks to the compose services):
cd web && npm run dev

# e2e (against a running compose stack):
cd web && npx playwright test
```

- Windows note: the Go toolchain lives at `~/sdk/go/bin` if it's not on PATH.
- Tests recreate schemas in whatever database `TEST_DATABASE_URL` points at, so re-run seed afterwards.
- CI: gofmt + `go vet` + `go test -race` against Postgres 17, plus a Next.js production build and `pytest`.

## Trying the features

- **Escrow demo:** sign in as `bob@vault.test` (`password123!`), buy one of alice's listings, pay from the seeded wallet, then sign in as `alice@vault.test`, ship it, switch back to bob and confirm receipt. Watch both `/wallet` pages: bob −price, alice +90%, platform +10%, escrow zero.
- **AI sell:** `/sell` → type a title → Suggest. With `ANTHROPIC_API_KEY` exported before `docker compose up`, suggestions come from Claude vision; without it, a heuristic + comparable-price band keeps the flow alive. Fields keep a coloured edge until you edit them; acceptance is measured per field on `/admin`.
- **MFA:** `2FA` in the header → enroll with any TOTP app (manual key entry) → sign out → sign in again for the step-up prompt. Recovery codes are single-use.
- **Admin:** sign in as alice → `Admin` for suggestion acceptance rates and the trust review queue (try listing something expensive on a brand-new account).

## Design

See [DESIGN.md](DESIGN.md) for the Nova design system: color palette, typography, spacing, components, and motion (light-only).

## Engineering write-ups

1. [Building an OIDC provider from the RFCs](docs/writeups/01-oidc-from-the-rfcs.md).
2. [The transactional outbox pattern in practice](docs/writeups/02-transactional-outbox-in-practice.md), which doubles as `outboxkit` documentation.
3. [What my tests prove: invariant testing for money code](docs/writeups/03-what-my-tests-prove.md).

## Deploy

See [`deploy/terraform/`](deploy/terraform/) for a single-instance AWS deploy via Terraform. Cost: ~$15/month for a `t3.small`. **Not applied here**, since it needs your AWS credentials.

## Honest limitations

- **Educational IdP.** Production systems should use vetted identity libraries. Building one from the RFCs is the learning exercise.
- **Single-instance deploy.** No ASG, ALB, or managed RDS. Swap in ECS + RDS if the demo needs HA.
- **Demo GIF.** Needs manual screen capture (deferred).
- **`terraform apply`.** Needs the user's AWS credentials (not run in CI).
- **outboxkit publishing.** Lives as a monorepo submodule via `replace`; mirror-publishing to its own GitHub repo for real semver tags is the next step.

## License

MIT
