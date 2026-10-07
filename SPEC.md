# Agora: Marketplace Platform with Self-Built Identity, Escrow, Drops, and Risk Scoring

---

## 1. The idea

**Agora** is a C2C marketplace platform. It solves identity, money movement, and
inventory contention as separate services with real boundaries between them,
because those three problems interact in ways that isolated implementations hide.

The platform runs an OIDC provider built from the RFCs, settles money on a
double-entry escrow ledger, sells limited-inventory drops under heavy concurrency,
routes card authorization through an external payment switch written in a different
language, and scores every event through a risk plane fed by the platform's own
event stream. An AI assistant turns a photograph into a draft listing.

### Design commitments

1. **Money can never silently drift.** Every value is an integer minor unit, every
   movement is double-entry, and the signed sum of all entries is zero at all times.
2. **Every mutating operation is idempotent.** A retried request never has a second effect.
3. **State changes and their events commit together.** Transactional outbox, relayed
   at-least-once, consumed idempotently.
4. **Correctness is proven, not asserted.** Invariants are build-breaking tests,
   including under concurrency and induced failure.
5. **Behaviour under load is measured and published**, not assumed.

### Honest framing

Agora is a simulation. There is no real money, no KYC, and no compliance posture.
The identity provider is written from the RFCs deliberately, as an exercise;
production systems should use vetted libraries. Data is synthetic. `switch` accepts
only documented test BINs.

---

## 2. Scope rules

- Core pillars: identity, escrow, drops, risk, AI assistant. No chat, reviews,
  ratings, recommendations, points, or mobile client.
- Buy the boring parts: Postgres full-text search (no Elasticsearch), a plain image
  URL for item photos, console-log email.
- No new languages: Go, Python, TypeScript, and the Java in `switch`.
- No Kubernetes unless single-instance Docker Compose demonstrably fails the
  Section 11 targets.
- No ranking or personalization service.
- Every money and auth behaviour gets a test that proves it.

---

## 3. Architecture

```
                     Next.js storefront + IdP screens
                                  │
     ┌───────────┬────────────────┼──────────────┬─────────────┐
     │           │                │              │             │
   id (Go)   market (Go)      sale (Go)      pay (Go)    assist (Py)
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
                                   risk (Py)
                              anomaly detection + console

              pay ──── HTTP/JSON ────► switch (Java, separate repo)
                                       card auth, routing, 3DS, failover
                                            │
                                            └──► events back to Redpanda
```

### 3.1 Services

| Service | Language | Responsibility |
| --- | --- | --- |
| `id` | Go | OAuth 2.0 / OIDC provider |
| `market` | Go | Listings, order state machine |
| `sale` | Go | Limited-inventory drops: admission, sharded reservations |
| `pay` | Go | Double-entry escrow ledger, card authorization through `switch` |
| `assist` | Python / FastAPI | AI listing suggestions, trust scoring |
| `risk` | Python / FastAPI | Anomaly detection over the platform's event stream |
| `web` | Next.js / TS | Storefront, IdP screens, admin, risk console |
| `switch` | Java / Spring Boot | Card payment switch, a separate repository |

**Identity (`id`)**

- Authorization Code flow with mandatory PKCE, constant-time S256 verification.
- RS256 JWT signing with published JWKS and signing-key rotation.
- Consent screen, append-only audit log.
- TOTP MFA per RFC 4226/6238, single-use recovery codes, pending-MFA step-up sessions.
- Refresh token rotation with family revocation; emits `refresh.reuse_detected` on replay.
- argon2id credential hashing; per-IP and per-account rate limits.
- Password reset and self-service deactivation.

**Money (`pay`)**

- Double-entry escrow ledger: idempotent fund / release / refund.
- 10% platform fee. Money stored as integer minor units only.
- Database CHECK constraint refusing negative balances.
- Card authorization through `switch`, including `AUTH_UNKNOWN` resolution (Section 7.5).

**Orders (`market`)**

- State machine: `pending_payment → funded → shipped → completed`, plus cancel and refund.
- 15-minute reservations with an auto-release timer.
- Postgres full-text search with keyset pagination.

**AI (`assist`)**

- Anthropic vision plus structured outputs generating title, description, category,
  and price band from a photograph. Every suggestion is editable: the AI proposes,
  the human decides.
- Heuristic fallback when the model is unavailable or the spend cap is hit.
- Price bands derived from comparable sold history.
- Kafka-driven trust scoring feeding an admin review queue.

**Events**

- Transactional outbox written in the same DB transaction as the state change.
- `outboxkit` relay using `FOR UPDATE SKIP LOCKED`, publishing after commit.
- Redpanda (Kafka-compatible) bus, consumed idempotently for exactly-once effects.

### 3.2 Data model

```
id:      users, credentials, totp_secrets, recovery_codes, sessions, oauth_clients,
         auth_codes, refresh_tokens(family_id), consents, signing_keys,
         audit_events, password_reset_tokens, outbox
market:  categories, listings, orders(state), reservations(ttl), outbox
sale:    drops, drop_units, reservations, stock_reconciliation, outbox (Section 7.8)
pay:     accounts(user | escrow | platform_revenue), transfers(idempotency_key uq),
         entries(int64 minor units), outbox
assist:  suggestions, risk_scores, comparables, consumed_events
risk:    scores, consumed_events
```

---

## 4. Pages

**Storefront:** `/` home · `/search` · `/listing/[id]` · `/sell` (drop a photo, the
assistant fills editable fields, with an "AI suggested / you edited" indicator) ·
`/checkout/[orderId]` · `/orders` · `/orders/[id]` (escrow timeline) · `/wallet`
(balance and own ledger entries, transparency as a feature).

**IdP screens (visually distinct, the IdP is its own product):** `/auth/login`
(with TOTP step-up) · `/auth/register` · `/auth/consent` · `/auth/mfa` ·
`/auth/forgot` · `/auth/reset`.

**Admin:** `/admin` (acceptance rates, trust review queue) · `/admin/risk` (risk console).

---

## 5. Design system

Documented in [`DESIGN.md`](DESIGN.md); tokens live in `web/app/globals.css`. Server components own data fetching, auth cookies are `httpOnly` and `SameSite`, and every money value renders from integer minor units with tabular numerals.

---

## 6. Service boundaries and non-goals

- **Do not rewrite `id`, `market`, `pay`, or `assist`.** They are invariant-tested
  foundations. New capability integrates through their existing APIs and the event
  bus, it does not modify their core flows.
- **`switch` stays a separate repository.** The cross-language service boundary is
  the point of integrating it. Merging it into this monorepo deletes the value.
- **`pay` is the only ledger.** No second ledger anywhere in the platform.
- **Dispatch and logistics are out of scope.** A single optional call on order
  confirmation is acceptable; anything more is a different product.
- **No real payment provider, no KYC, no compliance claims.**

---

## 7. The `sale` service

The service where throughput, contention, admission control, and graceful
degradation live.

### 7.1 Problem

A drop makes N units of a listing available at a fixed start time. Tens of
thousands of clients attempt to purchase within the first seconds. The system must
sell exactly N units, never more, keep every request bounded in latency, and
degrade to a clean refusal rather than a collapse.

### 7.2 Admission control

- On drop open, requests receive a queue token rather than immediate checkout access.
- Redis sorted set per drop, scored by arrival timestamp.
- An admission worker releases users in batches sized to remaining stock times a
  configurable overshoot factor, covering admitted users who never complete.
- Client receives position updates over SSE.
- Queue tokens are signed and carry drop ID, position, and expiry.

### 7.3 Inventory reservation (the core decision)

- **Redis is the hot-path authority during a drop. PostgreSQL is the durable source
  of truth, reconciled asynchronously.** The trade-off is recorded in
  [ADR 0001](docs/adr/0001-redis-hot-path-authority.md).
- Stock decrement is one Lua script, atomic by construction: verify remaining > 0,
  decrement, write a reservation marker with TTL, return a reservation token.
- **Hot key mitigation:** stock is split across `sale:{drop}:stock:{shard}` for N
  shards. Clients hit a random shard. When a shard is empty the script walks the
  remaining shards before declaring sold out. The shard-count versus
  sold-out-tail-latency trade-off is in the ADR.
- Reservation TTL (default 5 minutes) is enforced by **Redis expiry, not application
  clock**, so clock skew cannot leak inventory.

### 7.4 Reservation to order handoff

1. On successful reservation, `sale` calls `market` to create an order in `pending_payment`.
2. `market` calls `pay` to fund escrow.
3. `pay` calls `switch` for card authorization.
4. Every hop carries the reservation's idempotency key.
5. On TTL expiry with no completed payment, a sweeper releases the unit back to its
   shard and emits `sale.reservation_expired`.

### 7.5 The interesting failure: payment ambiguity

`switch` models `AUTH_UNKNOWN` (acquirer response lost). When that reaches `sale`,
the reservation must be neither released nor confirmed. It enters
`payment_indeterminate`, and a resolver polls `switch`'s status probe until it
settles, then confirms the order or releases the unit. See
[ADR 0003](docs/adr/0003-pay-switch-integration.md).

### 7.6 Abuse detection

- Per-user and per-IP reservation velocity limits.
- Device fingerprint clustering within a single drop.
- Account age at first purchase attempt.
- Suspicious attempts are **shadow-queued** (admitted but placed at the back) rather
  than hard-blocked, so scripted buyers cannot learn the detection threshold.
- Signals published to Redpanda for `risk` to score.

### 7.7 Cache strategy

- Drop landing page pre-warmed into Redis before `starts_at`.
- Single-flight guard on cache miss so a cold key cannot stampede Postgres.

### 7.8 Data model (`sale` schema)

```sql
drops
  id, listing_id, starts_at, ends_at, total_units, price_minor,
  units_per_user, shard_count, status

reservations
  id, drop_id, user_id, units, state, idempotency_key,
  reserved_at, expires_at, order_id NULL
  -- state: reserved | payment_pending | payment_indeterminate
  --      | confirmed | expired | released

stock_reconciliation
  drop_id, checked_at, redis_remaining, postgres_reserved, drift
```

### 7.9 Redis keys

```
sale:{drop}:stock:{shard}      integer, Lua-decremented
sale:{drop}:queue              zset, score = arrival timestamp
sale:{drop}:reservation:{id}   string with TTL, reservation marker
sale:{drop}:admitted           set of admitted user ids
```

---

## 8. The `risk` service

### 8.1 Role

Scores every platform event with an IsolationForest anomaly model blended with
explainable rules, mapping to allow / review / block. The model trains on synthetic
data and is documented as illustrative. The ledger lives in `pay` only; `risk`
never moves money.

### 8.2 Components

- The Python scoring service: IsolationForest plus explainable rules.
- Kafka consumer scaffolding with idempotent consumption.
- A risk console in the web client (`/admin/risk`).

### 8.3 Event inputs

| Topic | Source | Signal |
|---|---|---|
| `auth.login` | `id` | Geo velocity, unrecognised device |
| `auth.refresh_reuse_detected` | `id` | Token theft |
| `sale.reservation_created` | `sale` | Scalper velocity |
| `sale.reservation_expired` | `sale` | Abandonment patterns |
| `order.confirmed` | `market` | Purchase behaviour |
| `payment.authorized` | `pay` | Amount anomalies |
| `payment.risk_decision` | `switch` | Card-testing patterns |

### 8.4 Output

`risk.decision` events carrying allow / review / block plus a per-rule score
breakdown, consumed by `sale` for shadow-queueing and by the console's review queue.

### 8.5 Risk console

A live view: drop metrics, anomaly feed, per-rule score breakdown for any flagged
event, and a review queue with allow and block actions. Gated on the server by an
admin role.

---

## 9. Cross-cutting concerns

### 9.1 Service-to-service authentication

Internal calls carry a service-account JWT issued by `id`, verified via JWKS.

The outbound call to `switch` crosses a repository and language boundary. It uses a
shared API key plus optional mTLS. The simplification is documented honestly rather
than implying production-grade mutual auth.

### 9.2 Distributed tracing

OpenTelemetry across every service, exported to Jaeger. A single trace ID follows
the whole path:

```
storefront click
  → id      (token verification)
  → sale    (queue admission → Lua reservation)
  → market  (order creation)
  → pay     (escrow ledger write)
  → switch  (card authorization, separate Java process)
  → outbox → Redpanda → risk (scoring)
```

See [ADR 0004](docs/adr/0004-distributed-tracing.md).

### 9.3 Event bus

Transactional outbox written in the same transaction as the state change, relayed
at-least-once by `outboxkit`, consumed idempotently for exactly-once effects. Every
service that emits events uses the same mechanism.

### 9.4 Deployment

- Docker Compose locally. `make up` starts everything.
- Terraform (AWS) or Oracle Always Free for a single-instance public demo.
- A seed script that runs a scripted drop, so a visitor sees the system work
  without needing ten thousand simultaneous users.

---

## 10. Test strategy

Every item is a build-breaking test.

### 10.1 Identity

- Replayed authorization codes fail via atomic single-claim `UPDATE`.
- Wrong or absent PKCE verifiers fail.
- `alg:none` and tampered JWTs are rejected.
- TOTP matches RFC test vectors; recovery codes work exactly once.
- Refresh token reuse burns the entire family.
- A deactivated account is refused on its next request, across service boundaries.

### 10.2 Money

- Signed sum of all ledger entries is exactly zero, platform-wide.
- Per-account cached balance equals the sum of its entries.
- 20 goroutines racing a 10,000 balance yield exactly 10 successes, final balance 0.
- Release and refund are mutually exclusive under race.
- Duplicate idempotency keys move money exactly once.
- Auto-release sweeper and manual confirm release exactly once.
- Concurrent buys of one listing produce exactly one order.

### 10.3 Drop invariants

- **No oversell.** 50,000 concurrent reservation attempts against 1,000 units yield
  exactly 1,000 successes, under the Go race detector.
- **Conservation.** For any drop, `confirmed + released + expired + still_reserved`
  equals the total decremented from Redis.
- **Idempotency.** One reservation key replayed 20 times produces exactly one order
  and one escrow funding.
- **Per-user cap.** A user exceeding `units_per_user` is refused even when racing
  themselves across N connections.
- **TTL correctness.** An expired reservation is released exactly once, even when
  the sweeper and a manual confirm race.

### 10.4 Chaos

- **Kill Redis mid-drop.** No oversell, clean refusal to new requests, existing
  reservations resolve from Postgres, no money moved incorrectly.
- **Kill the outbox relay mid-flow.** No lost or duplicated events, for every
  service's outbox.
- **Force `AUTH_UNKNOWN` from `switch`.** The reservation resolves exactly once, no
  double charge, no leaked inventory.

### 10.5 End to end

```
register → MFA enroll → TOTP step-up login → AI-assisted listing → escrow buy
→ ship → confirm receipt → wallet reconciles
```

The drop path (queue → reserve → pay via `switch` → risk console shows the scored
event) is covered at the API layer and by the load suite.

---

## 11. Load test plan

Publish real numbers from a single instance and state the hardware. Modest and
honest beats inflated.

| Scenario | Load | Target |
|---|---|---|
| Drop spike | 10,000 VUs arriving within 5s, 1,000 units | Zero oversell. p99 admission < 200ms. No 5xx. |
| Drop sustained | 2,000 RPS reservation attempts, 60s | p99 < 300ms, error rate < 0.1% |
| Marketplace browse | 500 RPS on the listings read path | p99 < 100ms |
| Checkout end to end | 100 RPS through `pay` → `switch` | p99 < 800ms including the Java hop |
| Ledger write | 500 TPS escrow transfers | p99 < 150ms, invariants hold throughout |

Tooling: k6. Results are committed under `load/results/`.

**If a target is missed, publish the miss and the reason.** A documented bottleneck
with a named fix is a better artifact than a passing number.

---

## 12. Security and hardening

**Tier: T2, Internal/Team.** The demo is public and the money is synthetic, but
three things push it past T1. The OIDC provider stores credentials for demo
accounts (people reuse passwords). The `assist` service calls a paid LLM API, so
abuse has a dollar cost. And there are two admin surfaces (the review queue and
the risk console) that must be gated on the server, not hidden in the UI. Re-check
the tier if Agora ever takes real card data or real listings from real sellers;
either moves it to T3.

**Scaling: none.** Fixed, small audience. The Section 11 load tests are a
demonstrated feature, not production scale-out. Re-check if the demo is ever put in
front of real traffic.

**Secrets and keys**

- Every secret stays server-side: Postgres and Redis credentials, Redpanda
  credentials, the Anthropic API key, the RS256 private signing key for `id`, and
  the shared secret between `pay` and `switch`. The storefront bundles nothing
  secret; it only calls the backend.
- `.env` and any key files are in `.gitignore` before the first `git add`. If the
  RS256 signing key is ever committed, treat it as burned: rotate it and revoke
  every token signed with it.
- `id` supports signing key rotation: publish the new key in JWKS, sign with it,
  keep the old key in JWKS until every token signed with it has expired, then
  retire it.
- Rotate the Anthropic key and the `pay`→`switch` shared secret on a schedule and
  immediately on any suspected leak.

**Database and data access**

- Every user can only reach their own listings, orders, wallet, reservations, and
  MFA settings. The check is ownership on the server, not "is logged in." This
  applies to every read and write endpoint in `market`, `pay`, and `sale`.
- All Postgres access through `pgx` parameterized queries. No SQL by string
  concatenation.
- Postgres and Redis bind only to the Docker network. Only the storefront and the
  public API surface are exposed. Redpanda is not reachable from outside.
- TOTP secrets in `id` are encrypted at rest (they must be recoverable). Recovery
  codes are hashed. `switch` encrypts PANs with AES-GCM; the key is not in the repo.

**Auth and access control**

- Every mutating endpoint enforces auth and authorization on the server. Internal
  money endpoints stay shared-token gated. Cross-service calls verify the caller's
  service JWT via JWKS.
- Mass assignment is rejected on every create and update: listing endpoints refuse
  `seller_id`, `status`, `trust_score`; order endpoints refuse `state`; profile
  endpoints refuse `role`, `is_admin`, `mfa_enrolled`; reservation endpoints refuse
  `state` and `expires_at`.
- argon2id stays. A production system should delegate to a managed provider.
- Session and refresh tokens live in secure, httpOnly, SameSite cookies. Nothing
  auth-related in `localStorage`.
- The `assist` review queue and the `risk` console require an admin role, checked on
  the server on every request. An unauthenticated request to their API routes is
  refused, not just hidden in the UI.
- Password change triggers refresh-token family revocation. Account deactivation
  takes effect on the next request, not the next token refresh.

**Rate limiting and abuse**

- Server-side rate limits, tighter on: login, registration, TOTP verify,
  recovery-code use, password reset, and `POST /sale/.../reserve`. The Section 7.6
  abuse detection is not a substitute for plain per-IP and per-user limits.
- Billing cap and usage alert on the Anthropic account. `assist` enforces a per-user
  daily cap and a global daily cap server-side.
- Billing alerts on the hosting account.

**Input and output handling**

- Validate every field server-side: listing title and description length, price
  within bounds, category from the allowed set, idempotency key present and bounded.
- Listing titles and descriptions render through React's default escaping. No
  `dangerouslySetInnerHTML` anywhere, including AI-generated descriptions.
- Never write raw user strings (titles, idempotency keys, TOTP input) into log lines
  unescaped.
- Item photos: validate type and size server-side, allowlist `jpg`/`png`/`webp` by
  content sniffing, strip EXIF before storage, serve through signed URLs from a
  private bucket.
- API responses return only the fields the client needs. Never a whole `users` row,
  a password hash, a TOTP secret, another user's wallet, or an internal trust score
  on a public listing endpoint.

**Dependencies and supply chain**

- `govulncheck`, `pip-audit`, and `npm audit` on every CI run and before any public
  deploy. Patch what has a non-breaking fix; schedule the rest.
- Before installing any suggested package, confirm it exists on the registry with
  real download counts and a real repo.

**AI/LLM features (`assist`)**

- User-supplied content (the photo, any seller-typed hints) is passed as data,
  structurally separated from the system instructions.
- Model output is untrusted. Validate the structured output against the expected
  schema before storing it. Escape it before display. Never use it to build a query
  or a shell command.
- The heuristic fallback is the path taken when the model call fails or the cap is
  hit, so the feature degrades rather than errors.

**Payments**

- `pay` verifies the signature on every webhook from `switch` and rejects anything
  that does not verify.
- Prices are read server-side from `listings.price_minor` and `drops.price_minor`. A
  price or amount in the request body is ignored. Platform fee is computed server-side.
- Test BINs only, no PCI scope, no real card data.

**Performance choices with security consequences**

- Every `pgx` pool has an explicit max connection limit sized so a `sale` spike
  degrades into queued requests rather than exhausting Postgres.
- No permission, role, or admin-flag data is cached across requests without a short
  TTL and explicit invalidation. Redis holds stock and surge, not authorization.
- Any cache key for a per-user view includes the user ID.

**Deployment and operations**

- HTTPS only; HTTP redirects.
- Standard headers on the storefront: `Content-Security-Policy`, `frame-ancestors`,
  `X-Content-Type-Options: nosniff`, `Referrer-Policy`, `Strict-Transport-Security`.
- Debug mode off. pprof off. No source maps served. `.git` absent from every image.
- Clients receive generic error messages. Stack traces go to logs only. OIDC error
  responses must not leak whether a username exists.
- Access tokens, refresh tokens, TOTP codes, recovery codes, and the signing key
  never appear in any log line.
- Two-factor auth on every account that can affect Agora: hosting, Postgres if
  managed, domain registrar, the Anthropic account, GitHub, and every admin account
  inside Agora itself.

---

## 13. Open-source layer and write-ups

**`outboxkit`.** The transactional outbox relay and idempotent-consumer helpers live
in [`outboxkit/`](outboxkit/), a standalone Go module with semver tags, godoc,
README, CI, MIT license, and CONTRIBUTING guide. Agora imports it like any other
dependency. A focused single-purpose package beats a sprawling one; outbox and
idempotency are underserved in the Go ecosystem.

**Write-ups** live in [`docs/writeups/`](docs/writeups/): the OIDC provider, the
outbox in practice, what the tests prove, the `AUTH_UNKNOWN` resolver, load test
results, and a rate-limiter incident.

**Decision records** live in [`docs/adr/`](docs/adr/).

---

## 14. Build order

0. **Skeleton.** Monorepo, `docker compose up` runs everything, CI, seed script, Terraform.
1. **Identity and storefront.** `id`, consent, audit log, listings without payments.
2. **Escrow and orders.** `pay`, order state machine, reservation TTL, wallet, money invariants.
3. **AI and trust.** `/sell` suggestions, trust scoring, review queue, TOTP MFA, refresh rotation.
4. **Drops.** `sale`: admission, sharded Lua reservation, TTL sweeper, order handoff, abuse signals.
5. **Wire `switch`.** `pay` calls `switch`; `AUTH_UNKNOWN` handled end to end.
6. **Risk.** New consumers, new detections, the risk console.
7. **Tracing, load, write-ups.** OpenTelemetry across all hops, the Section 11 plan.
8. **Security and hardening.** Section 12.
