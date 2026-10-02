# Agora — Migration Spec (transitional)

**Status:** transitional. This document exists to drive one migration and is then
deleted.

**What it is for:** turning the existing `vault` repository into `agora` by
absorbing `tally`, adding one new service, integrating the existing `switch`
service across a language boundary, and producing deployment and load evidence.

**What it is not:** the project's permanent documentation. After migration, the
canonical document is `vault`'s own blueprint/README, updated per Section 12.
This file is scaffolding. Delete it when Section 12 is complete.

Each section below is tagged:

- `[CARRIES OVER]` — the content belongs in the permanent project doc.
- `[TRANSITIONAL]` — migration-only. Must not appear in the permanent doc.

---

## Table of contents

1. [Current state](#1-current-state) `[TRANSITIONAL]`
2. [Overlap analysis](#2-overlap-analysis-vault-vs-tally) `[TRANSITIONAL]`
3. [What is missing today](#3-what-is-missing-today) `[TRANSITIONAL]`
4. [Target state](#4-target-state) `[CARRIES OVER]`
5. [Architecture delta](#5-architecture-delta) `[TRANSITIONAL]`
6. [Scope and non-goals](#6-scope-and-non-goals) `[TRANSITIONAL]`
7. [The `sale` service](#7-the-sale-service-new-build) `[CARRIES OVER]`
8. [The `risk` service](#8-the-risk-service-salvage-and-repoint) `[CARRIES OVER]`
9. [Cross-cutting concerns](#9-cross-cutting-concerns) `[CARRIES OVER]`
10. [Test strategy](#10-test-strategy) `[CARRIES OVER]`
11. [Load test plan](#11-load-test-plan) `[CARRIES OVER]`
12. [Phases](#12-phases) `[TRANSITIONAL]`
13. [Documentation migration](#13-documentation-migration) `[TRANSITIONAL]`
14. [Portfolio positioning](#14-portfolio-positioning) `[TRANSITIONAL]`

---

## 1. Current state

`[TRANSITIONAL]`

### 1.1 `vault` — as it stands today

A compact C2C marketplace ("micro-Mercari") built around three deliberately hard
subsystems written from scratch.

**Services**

| Service | Language | Responsibility |
|---|---|---|
| `id` | Go | OAuth 2.0 / OIDC provider |
| `market` | Go | Listings, order state machine |
| `pay` | Go | Double-entry escrow ledger |
| `assist` | Python / FastAPI | AI listing suggestions, trust scoring |
| storefront | Next.js / TS | Storefront plus IdP screens |

**Identity (`id`)**

- Authorization Code flow with mandatory PKCE, constant-time S256 verification.
- RS256 JWT signing with published JWKS.
- Consent screen, append-only audit log.
- TOTP MFA per RFC 4226/6238, single-use recovery codes, pending-MFA step-up sessions.
- Refresh token rotation with family revocation; emits `refresh.reuse_detected` on replay.
- argon2id credential hashing.

**Money (`pay`)**

- Double-entry escrow ledger: idempotent fund / release / refund.
- 10% platform fee.
- Money stored as integer minor units only.
- Database CHECK constraint refusing negative balances.

**Orders (`market`)**

- State machine: `pending_payment → funded → shipped → completed`, plus cancel and refund.
- 15-minute reservations with an auto-release timer.

**Events**

- Transactional outbox written in the same DB transaction as the state change.
- `outboxkit` relay using `FOR UPDATE SKIP LOCKED`, publishing after commit.
- Redpanda (Kafka-compatible) bus, consumed idempotently for exactly-once effects.
- `outboxkit` is extracted as a standalone, independently versioned Go module with
  its own tests, CI, and MIT license. Currently a monorepo submodule via a `replace`
  directive; mirror-publishing to its own repo is pending.

**AI (`assist`)**

- Anthropic vision plus structured outputs generating title, description, category,
  and price band from a photograph.
- Heuristic fallback when the model is unavailable.
- Price bands derived from comparable sold history.
- Kafka-driven trust scoring feeding an admin review queue.

**Test suite (existing, must keep passing)**

- Replayed authorization codes fail via atomic single-claim `UPDATE`.
- Wrong or absent PKCE verifiers fail.
- `alg:none` and tampered JWTs rejected.
- TOTP matches RFC test vectors; recovery codes work exactly once.
- Refresh token reuse burns the entire family.
- Every transfer's entries sum to zero, globally and per account.
- 20 goroutines racing a 10,000 balance yield exactly 10 successes, final balance 0.
- Release and refund are mutually exclusive under race.
- Auto-release sweeper and manual confirm release exactly once.
- Concurrent buys of one listing produce exactly one order.
- Chaos: relay killed mid-flow, proving no lost or duplicated events.
- Playwright e2e: register → MFA enroll → TOTP step-up → AI listing → escrow buy →
  ship → confirm receipt → wallet reconciles.

**Infrastructure**

- PostgreSQL 17 via pgx, schema per service.
- Docker and docker-compose; `make up` starts everything.
- Terraform for a single-instance AWS deploy. **Written but never applied.**
- GitHub Actions CI.

**Known gaps**

- No live demo. Nothing deployed.
- No git remote set on the local repo.
- `outboxkit` not yet mirror-published.
- No load or throughput evidence of any kind.
- No dark mode (deliberate).

### 1.2 `tally` — as it stands today

A payments ledger backend simulating money movement in a banking or e-wallet app,
with a fraud-scoring service and a dashboard.

**Services:** Go ledger core, Go REST/gRPC gateway, Python fraud service, Next.js dashboard.

**Capabilities**

- Double-entry ledger: matching debit and credit in one atomic transaction.
- Money as integer minor units (`int64` / `BIGINT`), never floats.
- Idempotent transfers via client `Idempotency-Key`.
- Consistent account lock ordering (`SELECT ... FOR UPDATE`).
- Ledger publishes `transfers.completed` to Redpanda only after commit; Python
  service scores each transfer, writes `fraud_scores`, publishes `fraud.scored`.
- IsolationForest anomaly model blended with explainable rules, mapping to
  allow / review / block. Trained on synthetic data, documented as illustrative.
- Dashboard: stat cards, 7-day volume chart, account and transfer browsing with
  running balances, idempotency-key transfer form, transfer detail view showing
  both ledger entries side by side.

**Test suite**

- Debits equal credits equal the transfer amount, per transfer.
- Cached balances equal balances recomputed from entries.
- Signed sum of every ledger entry system-wide is exactly zero.
- Duplicate keys move money once.
- 50 concurrent transfers never lose an update, under the Go race detector.

**Stack additions not present in `vault`:** gRPC with protobuf, chi, golang-migrate,
scikit-learn, recharts, Kubernetes manifests.

**Known gaps:** no live demo, no load evidence.

### 1.3 `switch` — as it stands today (external, mostly unchanged)

A card payment switch: the authorization engine between a merchant and acquirers.
Java 21, Spring Boot, PostgreSQL 16, multi-module Maven.

Relevant capabilities for this migration:

- 13-state by 8-operation payment FSM, exhaustively tested at 104 generated cases.
- Idempotency via `INSERT ON CONFLICT` key claiming, verified under 20 threads.
- Card vault: tokenization, Luhn, AES-GCM with key versioning, PAN fingerprinting,
  isolated by an ArchUnit rule, with a log-scrubbing test.
- Acquirer routing with Resilience4j circuit breaking, retry-safety classification,
  and **`AUTH_UNKNOWN` resolution via a status-probe job when a response is lost.**
- Double-entry ledger with a Postgres deferred constraint trigger.
- Risk engine: ten weighted rules producing explainable ALLOW / CHALLENGE / DENY.
- 3-D Secure simulation against a fake ACS.
- Transactional outbox for webhook delivery.
- Property-based tests: 2,000 generated legal operation sequences via jqwik.
- PIT mutation testing, Testcontainers, WireMock.

`switch` stays a separate repository in Java. The only change is an inbound API
contract so `pay` can call it. See Section 9.

**Known gaps:** no live demo.

### 1.4 Cross-repo state

| | `vault` | `tally` | `switch` |
|---|---|---|---|
| Deployed | No | No | No |
| Live demo | No | No | No |
| Load evidence | No | No | No |
| Correctness evidence | Extensive | Good | Extensive |
| Talks to any other repo | No | No | No |

---

## 2. Overlap analysis: `vault` vs `tally`

`[TRANSITIONAL]`

This table is the justification for the merge. `tally` reimplements most of
`pay` and does it less thoroughly.

| Concern | `vault` | `tally` | Verdict |
|---|---|---|---|
| Double-entry ledger | `pay` | ledger service | **Duplicate.** Keep `pay`, delete tally's. |
| Integer minor units | Yes | Yes | Duplicate. |
| Idempotency keys | Yes | Yes | Duplicate. |
| Lock ordering under race | Yes | Yes | Duplicate. |
| Conservation invariant tests | Yes | Yes | Duplicate. Merge suites, keep the stronger assertions from each. |
| Event publishing | Transactional outbox + relay | Direct publish after commit | `vault`'s is strictly stronger. Keep outbox. |
| Fraud / anomaly scoring | Basic trust scoring in `assist` | **IsolationForest + explainable rules** | **`tally`'s is better. This is what survives.** |
| Analytics dashboard | Admin review queue | **Charts, stat cards, running balances** | **`tally`'s is the better base for the risk console.** |
| gRPC + protobuf | Internal only | Yes | Minor. Fold into `contracts`. |
| Kubernetes manifests | No | Yes | Out of scope per Section 6. Archive, do not use. |

**Conclusion:** roughly 70% of `tally` is redundant. Its surviving value is the
IsolationForest pipeline and the dashboard. Both become the `risk` service.

---

## 3. What is missing today

`[TRANSITIONAL]`

The migration exists to close these five gaps, in priority order.

1. **Nothing is deployed.** Three complete systems, zero running instances.
2. **No throughput evidence anywhere.** Every proof is a correctness proof.
   Nothing demonstrates behaviour under contention or load.
3. **No integration between systems.** `vault` and `switch` model adjacent layers
   of the same domain and have never exchanged a request.
4. **Duplicated money logic** across two repositories.
5. **No distributed tracing**, so multi-service behaviour is not observable.

Everything in Sections 7 through 11 exists to close one of these.

---

## 4. Target state

`[CARRIES OVER]`

**Agora** is a C2C marketplace platform. It solves identity, money movement, and
inventory contention as separate services with real boundaries between them,
because those three problems interact in ways that isolated implementations hide.

The platform runs an OIDC provider built from the RFCs, settles money on a
double-entry escrow ledger, sells limited-inventory drops under heavy concurrency,
routes card authorization through an external payment switch written in a different
language, and scores every event through a risk plane fed by the platform's own
event stream.

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
production systems should use vetted libraries. Data is synthetic. `switch`
accepts only documented test BINs.

---

## 5. Architecture delta

`[TRANSITIONAL]`

### Before

```
vault (repo)                     tally (repo)              switch (repo)
  id ──┐                           ledger ──┐                gateway
  market ├─ Postgres               gateway  ├─ Postgres       acquirer-sim
  pay ──┘                          fraud ───┘                 (Java)
  assist                           dashboard
  storefront
       │
   outbox → Redpanda

        (no connection)          (no connection)         (no connection)
```

Three isolated systems. Two independent double-entry ledgers. Nothing deployed.

### After

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
                                   risk (Py + Next.js)
                              anomaly detection + console

              pay ──── HTTP/JSON ────► switch (Java, separate repo)
                                       card auth, routing, 3DS, failover
                                            │
                                            └──► events back to Redpanda
```

### Change summary

| Change | Type | Effort |
|---|---|---|
| Move `vault` services into `agora/services/` | Move | Low |
| Delete `tally` ledger, gateway, transfer UI | **Delete** | Low |
| Move `tally` fraud service → `services/risk/` | Move + rewire | Medium |
| Move `tally` dashboard → `web/risk-console/` | Move + rebuild | Medium |
| Build `sale` service | **New** | High |
| Wire `pay` → `switch` | **New** | Medium |
| Add OpenTelemetry across all hops | **New** | Medium |
| Apply Terraform, deploy publicly | **New** | Low |
| Write and run k6 load suite | **New** | Medium |
| Rewrite project documentation | **New** | Low |

---

## 6. Scope and non-goals

`[TRANSITIONAL]`

### In scope

Everything in the change summary above.

### Non-goals

Each is a real temptation. Each costs time without adding signal.

- **Do not rewrite `id`, `market`, `pay`, or `assist`.** They work and are
  invariant-tested. They are inputs, not work items. Move them, do not touch them.
- **Do not merge `switch` into the monorepo.** The cross-language service boundary
  is the point of integrating it. Merging deletes the value.
- **Do not merge `sigap`** (dispatch) into Agora. Different domain, stays standalone.
  An optional one-call integration on order confirmation is acceptable; anything
  more is scope creep.
- **Do not build a ranking or personalization service.** Cut.
- **Do not introduce Kubernetes** unless single-instance Docker Compose demonstrably
  fails the Section 11 targets. `tally`'s manifests are archived, not adopted.
- **No new languages.** Go, Python, TypeScript, and the existing Java in `switch`.
- **No mobile client.**
- **No real payment provider, no KYC, no compliance claims.**
- **Do not preserve `tally`'s ledger "just in case."** Deleting it is the point of
  the merge. Two ledgers in one repo is worse than two repos with one ledger each.

---

## 7. The `sale` service (new build)

`[CARRIES OVER]`

The only service requiring a full build. Everything Agora currently lacks lives
here: throughput, contention, admission control, and graceful degradation.

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
  of truth, reconciled asynchronously.** Document this trade-off in an ADR; it is the
  single most discussable decision in the service.
- Stock decrement is one Lua script, atomic by construction: verify remaining > 0,
  decrement, write a reservation marker with TTL, return a reservation token.
- **Hot key mitigation:** stock is split across `sale:{drop}:stock:{shard}` for N
  shards. Clients hit a random shard. When a shard is empty the script walks the
  remaining shards before declaring sold out. Record the shard-count versus
  sold-out-tail-latency trade-off in the ADR.
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

`switch` already models `AUTH_UNKNOWN` (acquirer response lost). When that reaches
`sale`, the reservation must be neither released nor confirmed. It enters
`payment_indeterminate`, and a resolver job polls `switch`'s status probe until it
settles, then confirms the order or releases the unit.

**This is the strongest system design story in the platform.** Write the ADR and
the test before writing the code.

### 7.6 Abuse detection (folded in, not a separate project)

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
sale_drops
  id, listing_id, starts_at, ends_at, total_units, price_minor,
  units_per_user, shard_count, status

sale_reservations
  id, drop_id, user_id, units, state, idempotency_key,
  reserved_at, expires_at, order_id NULL
  -- state: reserved | payment_pending | payment_indeterminate
  --      | confirmed | expired | released

sale_queue_tokens
  id, drop_id, user_id, position, issued_at, expires_at, consumed_at

sale_stock_reconciliation
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

## 8. The `risk` service (salvage and repoint)

`[CARRIES OVER]`

### 8.1 Delete from `tally`

- The entire Go ledger service and its Postgres schema. `pay` is the ledger.
- The REST/gRPC gateway.
- The transfer-creation UI in the dashboard.

This deletion is not optional. It is the reason the merge improves the codebase
rather than enlarging it.

### 8.2 Keep and repoint

- The Python scoring service: IsolationForest plus explainable rules.
- The Kafka consumer scaffolding with idempotent consumption.
- The Next.js dashboard, rebuilt as a cross-platform risk console.

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
breakdown. Consumed by `sale` for shadow-queueing and by the console's review queue.

### 8.5 Risk console

A live cross-platform view: drop metrics, anomaly feed, per-rule score breakdown
for any flagged event, and a review queue with allow and block actions. This is a
demoable surface. Treat it as a real interface, not an afterthought.

---

## 9. Cross-cutting concerns

`[CARRIES OVER]`

### 9.1 Service-to-service authentication

Internal calls carry a service-account JWT issued by `id`, verified via JWKS.
This is already the pattern in `vault`; extend it to `sale`.

The outbound call to `switch` crosses a repository and language boundary. Use a
shared secret plus optional mTLS, and document the simplification honestly rather
than implying production-grade mutual auth.

### 9.2 Distributed tracing

OpenTelemetry across every service, exported to Jaeger. A single trace ID must
follow the whole path:

```
storefront click
  → id      (token verification)
  → sale    (queue admission → Lua reservation)
  → market  (order creation)
  → pay     (escrow ledger write)
  → switch  (card authorization — separate Java process)
  → outbox → Redpanda → risk (scoring)
```

**A screenshot of that trace, spanning a Go monorepo and an external Java service,
is the single strongest visual artifact this project can produce.** It belongs at
the top of the README.

### 9.3 Event bus

Unchanged from `vault`: transactional outbox written in the same transaction as the
state change, relayed at-least-once by `outboxkit`, consumed idempotently for
exactly-once effects. `sale` adopts the existing pattern. Introduce no new mechanism.

### 9.4 Deployment

- Docker Compose locally. Preserve the `make up` single-command convention.
- Terraform to a single cloud instance for the public demo. `vault`'s Terraform
  exists and has never been applied; applying it is Phase 0.
- A seed script that runs a scripted drop, so a visitor sees the system work
  without needing ten thousand simultaneous friends.

---

## 10. Test strategy

`[CARRIES OVER]`

Extend the existing invariant style. Every item is a build-breaking test.

### 10.1 Money invariants (existing, must keep passing after the merge)

- Signed sum of all ledger entries is exactly zero, platform-wide.
- Per-account cached balance equals the sum of its entries.
- Release and refund are mutually exclusive under race.
- Duplicate idempotency keys move money exactly once.

### 10.2 New drop invariants

- **No oversell.** 50,000 concurrent reservation attempts against 1,000 units yield
  exactly 1,000 successes. Run under the Go race detector.
- **Conservation.** For any drop, `confirmed + released + expired + still_reserved`
  equals the total decremented from Redis.
- **Idempotency.** One reservation key replayed 20 times produces exactly one order
  and one escrow funding.
- **Per-user cap.** A user exceeding `units_per_user` is refused even when racing
  themselves across N connections.
- **TTL correctness.** An expired reservation is released exactly once, even when
  the sweeper and a manual confirm race.

### 10.3 Chaos

- **Kill Redis mid-drop.** Assert: no oversell, clean refusal to new requests,
  existing reservations resolve from Postgres, no money moved incorrectly.
- **Kill the outbox relay mid-flow.** Assert: no lost or duplicated events. This
  test exists in `vault`; extend it to `sale` topics.
- **Force `AUTH_UNKNOWN` from `switch`.** Assert: the reservation resolves exactly
  once, no double charge, no leaked inventory.

### 10.4 End to end

Extend the existing Playwright run:

```
register → MFA enroll → TOTP step-up login → join drop queue → admitted
→ reserve → pay via switch → order confirmed → wallet reconciles
→ risk console shows the scored event
```

---

## 11. Load test plan

`[CARRIES OVER]`

Publish real numbers from a single instance. State the hardware in the README.
Modest and honest beats inflated.

| Scenario | Load | Target |
|---|---|---|
| Drop spike | 10,000 VUs arriving within 5s, 1,000 units | Zero oversell. p99 admission < 200ms. No 5xx. |
| Drop sustained | 2,000 RPS reservation attempts, 60s | p99 < 300ms, error rate < 0.1% |
| Marketplace browse | 500 RPS on the listings read path | p99 < 100ms |
| Checkout end to end | 100 RPS through `pay` → `switch` | p99 < 800ms including the Java hop |
| Ledger write | 500 TPS escrow transfers | p99 < 150ms, invariants hold throughout |

Tooling: k6. Commit results as JSON plus a rendered chart. Re-run on a CI schedule
so the numbers do not silently rot.

**If a target is missed, publish the miss and the reason.** A documented bottleneck
with a named fix is a better artifact than a passing number.

---

## 12. Phases

`[TRANSITIONAL]`

Ordered by value per hour. Do not reorder.

### Phase 0 — Deploy what already exists

No new code. Apply `vault`'s Terraform. Get a public URL serving the current
storefront with a working OAuth round trip. Do the same for `switch`. Set the git
remote on `vault`.

This is the largest single gap in the current state and it costs a weekend.

**Exit criteria:** three public URLs, all reachable, OAuth round trip works in a
browser.

### Phase 1 — Monorepo merge and deletion

Move `vault` services into `agora/services/`. Move `tally`'s Python scorer and
dashboard. Delete `tally`'s ledger, gateway, and transfer UI. Update module paths.
One `make up` brings up everything.

**Exit criteria:** existing test suites pass unchanged, `make up` works, `tally`'s
ledger code no longer exists in the tree.

### Phase 2 — Build `sale`

Admission control, sharded Lua reservation, TTL sweeper, order handoff, abuse
signals. All invariants from 10.2. Largest single chunk of work.

**Exit criteria:** the 50,000-against-1,000 oversell test passes under the race detector.

### Phase 3 — Wire `switch`

`pay` calls `switch` for card authorization. Handle `AUTH_UNKNOWN` end to end.
Write the ADR. Cross-language integration test.

**Exit criteria:** the forced `AUTH_UNKNOWN` chaos test passes.

### Phase 4 — Repoint `risk`

New consumers, new detections, risk console rebuild.

**Exit criteria:** a scalper simulation produces visible detections in the console.

### Phase 5 — Tracing, load, write-up

OpenTelemetry across all hops. Run the Section 11 plan. Publish results. Write one
long-form engineering post on the hardest problem encountered. Candidates: the
`AUTH_UNKNOWN` resolver, the hot key shard walk, or the Redis-death chaos case.

**Exit criteria:** a single Jaeger trace spanning storefront to `risk`, screenshotted;
load results committed; post published.

### Phase 6 — Security and hardening

**Tier: T2, Internal/Team.** Reasoning: the demo is public and the money is
synthetic, but three things push it past T1. The OIDC provider stores real
credentials for demo accounts (people reuse passwords). The `assist` service
calls a paid LLM API, so abuse has a dollar cost. And there are two admin
surfaces (the review queue and the risk console) that must be gated on the
server, not hidden in the UI. Re-check the tier if Agora ever takes real card
data or real listings from real sellers; either moves it to T3.

**Scaling: none.** Portfolio demo with a fixed, small audience. The Section 11
load tests are a demonstrated feature, not production scale-out, and need none
of the scaling checklist. Re-check if the demo is ever put in front of real
traffic.

**Secrets and keys**

- Every secret stays server-side: Postgres and Redis credentials, Redpanda
  credentials, the Anthropic API key, the RS256 private signing key for `id`,
  and the shared secret between `pay` and `switch`. The storefront and risk
  console bundle nothing secret; they only call the backend.
- `.env` and any key files are in `.gitignore` before the first `git add`. If
  the RS256 signing key is ever committed, treat it as burned: rotate it and
  revoke every token signed with it.
- `id` supports signing key rotation: publish the new key in JWKS, sign with
  it, keep the old key in JWKS until every token signed with it has expired,
  then retire it. Schedule the rotation.
- Rotate the Anthropic key and the `pay`→`switch` shared secret on a schedule
  and immediately on any suspected leak.

**Database and data access**

- Every user can only reach their own listings, orders, wallet, reservations,
  and MFA settings. The check is ownership on the server, not "is logged in."
  This applies to every read and write endpoint in `market`, `pay`, and `sale`.
- All Postgres access through `pgx` parameterized queries. No SQL by string
  concatenation, including in the reconciliation jobs and the risk consumers.
- Postgres and Redis bind only to the Docker network. Only the storefront, the
  public API surface, and the risk console are exposed on the instance.
  Redpanda is not reachable from outside.
- TOTP secrets in `id` are encrypted at rest (they must be recoverable, so
  hashing is not an option). Recovery codes are hashed (single-use, never need
  to be read back). `switch` already encrypts PANs with AES-GCM; confirm the
  key is not in the repo.

**Auth and access control**

- Every mutating endpoint enforces auth and authorization on the server.
  Internal money endpoints stay shared-token gated. Cross-service calls verify
  the caller's service JWT via JWKS.
- Mass assignment is rejected on every create and update: listing endpoints
  refuse `seller_id`, `status`, `trust_score`; order endpoints refuse `state`;
  profile endpoints refuse `role`, `is_admin`, `mfa_enrolled`; reservation
  endpoints refuse `state` and `expires_at`.
- argon2id stays. Keep the honest-framing note that a production system should
  delegate to a managed provider.
- Session and refresh tokens live in secure, httpOnly, SameSite cookies.
  Nothing auth-related in `localStorage`.
- The `assist` admin review queue and the `risk` console require an admin
  role, checked on the server on every request. Verify by hand that an
  unauthenticated request to their API routes is refused, not just that the
  page hides.
- Password change triggers refresh-token family revocation (the mechanism
  already exists; wire it). Account deactivation takes effect on the next
  request, not the next token refresh.

**Rate limiting and abuse**

- Server-side rate limits, tighter on: login, registration, TOTP verify,
  recovery-code use, password reset, and `POST /sale/.../reserve`. The `sale`
  abuse detection in Section 7.6 is not a substitute for plain per-IP and
  per-user limits.
- Billing cap and usage alert on the Anthropic account. `assist` enforces a
  per-user daily cap and a global daily cap server-side, so a bug or an
  abusive account is a notification, not an invoice.
- Billing alerts on the hosting account.

**Input and output handling**

- Validate every field server-side: listing title and description length,
  price within bounds, category from the allowed set, image count and size
  for `assist`, idempotency key present and bounded.
- Listing titles and descriptions render through React's default escaping. No
  `dangerouslySetInnerHTML` anywhere in the storefront or risk console. This
  includes AI-generated descriptions.
- Never write raw user strings (titles, idempotency keys, TOTP input) into log
  lines unescaped.
- Item photos for `assist`: validate type and size server-side, allowlist
  `jpg`/`png`/`webp` by content sniffing not extension, strip EXIF before
  storage (photo GPS is personal data), store in a private bucket served
  through signed URLs, `Content-Disposition: attachment` on any raw download.
- API responses return only the fields the client needs. Never a whole
  `users` row, never a password hash, never a TOTP secret, never another
  user's wallet, never an internal trust score on a public listing endpoint.

**Dependencies and supply chain**

- `govulncheck`, `pip-audit`, and `npm audit` before the first public deploy
  and on the CI schedule. Patch what has a non-breaking fix; schedule the rest.
- Before installing any package suggested during development, confirm it
  exists on the registry with real download counts and a real repo. This
  applies to Go modules, PyPI packages, and npm packages alike.

**AI/LLM features (`assist`)**

- User-supplied content (the photo, any seller-typed hints) is passed as
  data, structurally separated from the system instructions, so text inside
  an image or a hint field cannot override the listing-generation prompt.
- Model output is untrusted. Validate the structured output against the
  expected schema server-side before storing it. Escape it before display.
  Never use it to build a query or a shell command.
- The heuristic fallback is the path taken when the model call fails or the
  per-user cap is hit, so the feature degrades rather than errors.

**Payments**

- `pay` verifies the signature on every webhook from `switch` and rejects
  anything that does not verify. `switch`'s outbox already delivers webhooks;
  the receiving side must check them.
- Prices are read server-side from `listings.price_minor` and
  `sale_drops.price_minor`. A price or amount in the request body is ignored.
  Platform fee is computed server-side.
- Keep the honest-framing note: test BINs only, no PCI scope, no real card
  data.

**Performance choices with security consequences**

- Every `pgx` pool has an explicit max connection limit sized so a `sale`
  spike degrades into queued requests rather than exhausting Postgres and
  taking every service down.
- No permission, role, or admin-flag data is cached across requests without a
  short TTL and explicit invalidation on change. Redis holds stock and surge,
  not authorization.
- Any cache key for a per-user view includes the user ID.

**Deployment and operations**

- HTTPS only; HTTP redirects.
- Standard headers on the storefront and risk console: `Content-Security-Policy`,
  `X-Frame-Options` or `frame-ancestors`, `X-Content-Type-Options: nosniff`,
  `Referrer-Policy`, `Strict-Transport-Security`.
- Debug mode off. pprof off. No source maps served. `.git` absent from every
  image.
- Clients receive generic error messages. Stack traces and internal errors go
  to logs only. This includes OIDC error responses, which must not leak
  whether a username exists.
- Error and suspicious-activity logging is on and reviewed. Confirm by grep
  that access tokens, refresh tokens, TOTP codes, recovery codes, and the
  signing key never appear in any log line.
- Two-factor auth on every account that can affect Agora: hosting, Postgres if
  managed, domain registrar, the Anthropic account, GitHub, and every admin
  account inside Agora itself (dogfood the TOTP you built).

**Exit criteria:** every row above verified by hand or by an automated scan;
`govulncheck`/`pip-audit`/`npm audit` clean or their findings triaged and
scheduled; an unauthenticated request to the `assist` review queue and the
`risk` console API routes returns 401/403, not data; a grep of the logs turns
up no access tokens, refresh tokens, TOTP codes, recovery codes, or signing
key; HTTPS enforced with the standard headers present; 2FA enabled on every
account listed above.

### If time runs short

Phases 0 through 3 finished and documented beats all seven half-built. Phase 0 and
Phase 5 carry the most demonstrable value per hour; Phase 2 carries the most
engineering value.

---

## 13. Documentation migration

`[TRANSITIONAL]`

**The permanent document is `vault`'s existing blueprint/README, updated as below
and renamed to Agora. This spec is deleted once these edits land.**

### 13.1 Remove all application-oriented framing

Search every doc, README, and code comment across `vault`, `tally`, and `switch`
for text that positions the project as a job-seeking instrument. Remove all of it.

Known instances to delete:

- From `tally`: *"a backend-correctness showcase built to demonstrate fintech
  fundamentals ... for backend/full-stack roles at fintech and marketplace companies."*
- From `switch`: *"Built for payment-platform engineering roles
  (Adyen/N26/Trade Republic/SAP-style stacks)."*

Patterns to search for and strip:

```
built for ... roles
for ... engineering roles
portfolio project
showcase
demonstrates ... for employers
to apply to
target companies
resume / CV / hiring
```

**Why this matters:** a project that announces it exists to get someone hired reads
as resume-driven engineering, and reviewers discount everything after that sentence.
The work is strong enough to stand on the problem it solves. Let it.

### 13.2 Replace with problem-motivated framing

Use Section 4 as the source. The opening should say what the system does and why
the problem is hard, never who it is meant to impress.

Acceptable replacement for the deleted `tally` and `switch` notes:

> Agora models the parts of a marketplace that are genuinely difficult to get right:
> delegated identity, money that must never drift, and inventory under contention.
> Each is built from first principles rather than assembled from frameworks, because
> the interactions between them are where the real problems live.

### 13.3 Keep the honest-framing notes

Do **not** remove the existing disclaimers about simulation, synthetic data,
educational IdP, test BINs, or absence of KYC and compliance. Those are integrity
notes, not marketing. They read as maturity and they stay.

### 13.4 Structural edits to the permanent doc

**Rename**

- Repository `vault` → `agora`. Update the Go module path and all imports.
- `vault` survives as the internal name of the identity subsystem, preserving
  continuity with existing commits and the `id` service.

**Sections to add**

| Section | Source |
|---|---|
| Target state and design commitments | Section 4 of this spec |
| `sale` service | Section 7 |
| `risk` service | Section 8 |
| Cross-cutting: auth, tracing, events, deployment | Section 9 |
| Test strategy, extended | Section 10 merged with the existing suite description |
| Load results | Section 11, with real numbers once Phase 5 completes |
| Security & hardening | Section 12, Phase 6, as its own section, not folded into Section 9's cross-cutting concerns |

**Sections to update**

- Architecture diagram → the "After" diagram in Section 5.
- Service table → add `sale` and `risk`.
- Run instructions → confirm `make up` still covers everything.
- Known limitations → remove "never applied" once Terraform is applied; add the
  Redis-as-hot-path-authority trade-off.

**Sections to delete**

- Anything describing `tally` as a separate project.
- The `tally` ledger documentation in full.

### 13.5 `tally` repository disposition

Archive it. Do not delete the GitHub repo (commit history is real work), but mark
it archived with a README pointing to `agora`. Remove it from the featured portfolio
list.

### 13.6 README ordering for the permanent doc

A reviewer gives it ninety seconds. Order accordingly.

1. One-sentence description and the end-to-end trace screenshot.
2. Architecture diagram.
3. Load test results table.
4. The three hardest decisions, one paragraph each, linked to their ADRs.
5. `make up` quickstart.
6. Invariant test summary: what is proven, and how.
7. Honest-framing note.

### 13.7 Deletion checklist

- [ ] All application-oriented framing removed from `vault`, `tally`, `switch`.
- [ ] Replacement opening written from Section 4.
- [ ] Honest-framing notes preserved.
- [ ] Repo renamed, module path updated.
- [ ] Architecture diagram replaced.
- [ ] `sale` and `risk` documented.
- [ ] `tally` archived with a pointer README.
- [ ] Security phase rows verified before the public demo goes live.
- [ ] **This spec file deleted.**

---

## 14. Portfolio positioning

`[TRANSITIONAL]` — planning context only. **Must not appear in any project doc.**

Featured projects reduce from eleven to five:

| Entry | Role |
|---|---|
| **Agora** | Flagship. Distributed systems, money correctness, contention under load. |
| **Switch** | Depth in payments infrastructure. Exhaustive FSM, property-based and mutation testing. |
| **Sigap** | Geospatial dispatch at throughput. Different problem class. |
| **Signlingo** | Applied ML and computer vision, linked to the published thesis. |
| **Maravellante** | Frontend and design capability. |

Everything else moves to a compact secondary list. `vault` and `tally` disappear
as standalone entries because they are now Agora. Fewer entries, each deeper, all
with working links and published numbers.
