# Vault — Marketplace with Self-Built Identity, Escrow Checkout, and an AI Listing Assistant

---

## 1. The Idea in One Paragraph

**Vault** is a compact C2C marketplace with three deliberately hard parts built from scratch: **identity** (a real OAuth 2.0 / OIDC provider with TOTP MFA and session security), **money** (escrow checkout on a double-entry ledger, extending Tally's proven patterns), and **an AI listing assistant** (photograph an item, get a suggested title, description, category, and price band). Everything is invariant-tested, deployed on AWS with Terraform, and demoable in a browser in under three minutes.

---

## 2. Scope Rules

* Three pillars only: identity, escrow, AI assistant. No chat, reviews, ratings, recommendations, points, mobile.
* Buy the boring parts: Postgres full-text search (no Elasticsearch), a storage bucket for images, console-log email.
* Every money/auth behavior gets a test that proves it. That's the brand Tally started; Vault continues it.
* Honest README: simulated deliveries, synthetic data, no real money, "educational IdP — production should use vetted libraries, here's what building one taught me." That framing pre-empts the only serious criticism.

---

## 3. Architecture

```
            Next.js storefront + IdP screens (TypeScript, App Router)
                                │ HTTPS/JSON
                    ┌───────────▼───────────┐
                    │ gateway (Go, chi)     │  authn middleware, rate limits,
                    └──┬─────────┬───────┬──┘  request IDs, OpenAPI
                 gRPC  │         │       │ gRPC
            ┌──────────▼──┐ ┌────▼────┐ ┌▼─────────────┐
            │ id (Go)     │ │ market  │ │ pay (Go)     │
            │ OIDC, MFA,  │ │ (Go)    │ │ escrow on    │
            │ sessions,   │ │ listings│ │ double-entry │
            │ audit log   │ │ orders  │ │ ledger       │
            └──────┬──────┘ └────┬────┘ └──────┬───────┘
                   └──────┬──────┴──────┬──────┘
                          │  outbox → Redpanda events
            ┌─────────────▼───┐   ┌────▼─────────────────────┐
            │ PostgreSQL      │   │ assist (Python, FastAPI) │
            │ (schema per     │   │ AI listing suggestions + │
            │  service)       │   │ trust scoring            │
            └─────────────────┘   └──────────────────────────┘

```

| Service | Language | Notes |
| --- | --- | --- |
| `id` | Go | Auth Code + PKCE, RS256 JWT + JWKS rotation, rotating refresh tokens with family revocation on reuse, argon2id, TOTP + recovery codes, per-IP/per-account rate limits, sessions with remote revocation, append-only audit log |
| `market` | Go | Listings (state machine: draft→active→reserved→sold|withdrawn), reserve-on-checkout with 15-min TTL, Postgres FTS search, orders |
| `pay` | Go | Tally's rules, single currency: escrow fund/release/refund as atomic double-entry transfers, idempotency keys, lock ordering, int64 minor units |
| `assist` | Python (FastAPI) | (a) Listing assistant: image → suggested title/description/category via a vision-language model API, price band from comparable sold listings (embeddings + nearest neighbors over synthetic history); (b) trust scoring on signups/orders (rules + IsolationForest) feeding an admin review queue. Every suggestion is editable — AI proposes, the human decides.
| `web` | Next.js + TS | Storefront + separate IdP screens + small admin. State management is deliberate and documented: TanStack Query owns server state (caching, invalidation, optimistic updates on listing edits), a small Zustand store owns session/UI state, and DESIGN.md explains why neither Redux nor raw context was chosen |

Events use the **transactional outbox** pattern (state change and event written in one DB transaction; relay publishes to Redpanda) — your upgrade over Tally's publish-after-commit, and a ready-made distributed-systems.

---

## 4. Pages (complete)

**Storefront:** 1. `/` home (search, categories, fresh listings) · 2. `/search` (filters, keyset pagination) · 3. `/listing/[id]` (gallery, seller card, Buy) · 4. `/sell` — the showcase page: drop a photo → assistant streams suggested title/description/category/price band into editable fields, with an "AI suggested / you edited" indicator · 5. `/checkout/[orderId]` (idempotent Pay) · 6. `/orders` (purchases/sales tabs) · 7. `/orders/[id]` (escrow timeline: funded → shipped → completed; confirm receipt; cancel/refund) · 8. `/wallet` (balance + own ledger entries — transparency as a feature) · 9. `/profile/[handle]` · 10. `/settings` (profile, EN/JA language toggle)

**IdP screens (visually distinct — the IdP is its own product):** 11. `/auth/login` (+ TOTP step-up) · 12. `/auth/register` · 13. `/auth/consent` (scope grant) · 14. `/auth/mfa` (QR enrollment, recovery codes) · 15. `/auth/sessions` (device list, revoke) · 16. `/auth/forgot`

**Admin:** 17. dashboard (GMV, orders, escrow float, AI suggestion acceptance rate) · 18. trust queue (scores + explanations, approve/reject) · 19. audit log explorer · 20. ledger browser

---

## 5. Design System — "Slate" (v2)

Supersedes the original "Ishidatami" palette (see git history). Sleek, high-trust,
light **and** dark. Fully documented in `DESIGN.md`; tokens live in
`web/app/globals.css`. Legacy Ishidatami token names (`torii`, `moss`, `kohaku`,
`sumi-*`, `paper`) are **aliased** onto the new palette so pages not yet restyled
inherit both the restyle and dark mode without a rename. Depth comes from a neutral
surface scale + soft elevation + glass, not heavy shadows.

* **Neutral surface scale (depth via surface, not shadow):** `canvas #F6F7F9` app bg · `surface #FFFFFF` cards · `surface-2 #FBFCFD` raised/alt. Text ramp: `ink #0F1524` · `ink-2 #38414F` · `muted #5B6474` · `faint #9096A2`. Lines/fills: `line #E6E8EE` hairline · `line-strong #D3D7DF` · `fill #EEF0F4`.
* **Accent (single trust hue):** `accent #4F46E5` indigo — links, primary CTAs, focus ring, security contexts · `accent-strong #4338CA` hover · `accent-tint #EEF0FE`. Semantics: `success #157A53` escrow/paid · `warning #B45309` pending · `danger #C81E14` errors/destructive/negative money, each with a `-tint` surface. Solid fills always take `text-on-solid` (never `text-white`) so they survive the dark-theme flip.
* **Type:** Plus Jakarta Sans via `next/font`; scale 12/14/16/20/24/32/48; weights 400/500/600/700; `tabular-nums` on all money; tight tracking on headings.
* **Shape/space:** 4px grid, radii 10/14px (`control`/`card`), hero 20px. Generous rhythm — `max-w-6xl`, responsive `px-4→8`, section `gap-12→16`.
* **Elevation & glass:** three soft layered shadows (`sm`/`md`/`lg`) for cards + hover lift; frosted `glass` (blur + translucent surface) on sticky chrome; `wash-accent` gradient for hero/feature surfaces.
* **Components:** Button, Input, Select, Modal, Toast, Tabs, Badge (fixed state-color map), Card, ListingCard (surface card, soft hover lift + image zoom), PriceTag, Timeline, DataTable, Skeleton, EmptyState, SuggestionField (the AI-prefilled input).
* **Motion:** 150–200ms hover/press, 250ms modal/toast, 500ms image zoom, suggestion fields fade in as they stream. Honors `prefers-reduced-motion`.
* **Dark mode:** ships, driven by `prefers-color-scheme`. Only base tokens are overridden in one media block — aliases follow through `var()`, so no component carries a `dark:` variant.

---

## 6. Data Model (core)

```
id:      users, credentials, totp_secrets, recovery_codes, sessions,
         oauth_clients, auth_codes, refresh_tokens(family_id), consents,
         signing_keys, audit_events, outbox
market:  listings, listing_images, categories, orders(state), reservations(ttl), outbox
pay:     accounts(user | escrow | platform_revenue), transfers(idempotency_key uq),
         entries(int64 minor units), outbox
assist:  suggestions(listing_id, model, prompt_hash, accepted_fields), risk_scores,
         consumed_events, comparables(embedding vector)

```

---

## 7. Phases (vs. the actual selection calendar)

Budget ~10–12 hrs/week. Start early August 2026.

**Phase 0 — Skeleton (1 wk, early Aug):** monorepo, `docker compose up` runs everything, CI (lint/test/race), seed script, Terraform stub. *Done = one command runs the world.*

**Phase 1 — Identity + storefront skeleton (3 wks, Aug):** register/login (argon2id), sessions, Auth Code + PKCE, JWT + JWKS, consent screen, audit log; storefront signs in via your own IdP; listings CRUD without payments.

**Phase 2 — Escrow + orders (3 wks, Sept):** pay service with fund/release/refund, order state machine, reservation TTL, wallet page, invariant suite (escrow zeroes out on completion; concurrent double-spends fail; timer-vs-manual confirm releases exactly once; race detector clean).

**Phase 3 — AI assistant + trust (2–3 wks, Oct):** `/sell` suggestion flow (VLM API + price band from embedded comparables), acceptance-rate metric on admin, trust scoring + review queue, TOTP MFA + refresh rotation with reuse detection.

**Phase 4 — Ship it + OSS layer (2 wks, late Oct/Nov):** `outboxkit` extraction and publication, the three engineering write-ups, AWS deploy via Terraform (small instance or ECS free tier), seeded live demo + reset cron, Playwright e2e (register → MFA → AI-assisted listing → buy → confirm → wallet reconciles), README with architecture diagram + "What the tests prove" + honest limitations, DESIGN.md, 3-minute demo GIF, OpenAPI spec.

---

## 8. Testing (the differentiator, restated)

Invariant suite in CI with `-race`; OIDC misuse tests (wrong `code_verifier`, replayed auth codes, expired codes, JWKS rotation without downtime); property tests on the escrow state machine; one chaos test (kill the outbox relay mid-flow, prove no lost/duplicated events); Playwright e2e happy path. README section: **"What the tests prove."** Both companies name testing explicitly in their listings; almost no student demonstrates it. This is your loudest signal.

---

## 9. Open-Source & Community Layer

**9.1 Extract and publish a real package: `outboxkit`.**
The transactional outbox relay and idempotent-consumer helpers get pulled out of Vault into a standalone Go module, published properly: semver tags, godoc, README with usage examples, CI badge, MIT license, CONTRIBUTING.md, 2–3 good-first-issues. Vault then imports it like any other dependency, which is itself the story: "I extracted the reusable part of my project and maintain it as a library." A focused single-purpose package beats a sprawling one; outbox/idempotency is genuinely underserved in the Go ecosystem and matches your correctness brand.

**9.2 Write it up (3 posts, on your portfolio site or dev.to).**

1. "Building an OIDC provider from the RFCs (and what I got wrong first)".
2. "The transactional outbox pattern in practice" — doubles as `outboxkit` documentation.
3. "What my tests prove: invariant testing for money code" — continues the Tally voice.
Each post is ~1,500 words, concrete, with code.

**10.3 One community act.**
Present Vault or the outbox pattern at a HIMTI session (you're already in the web development division — this converts an existing membership into visible activity) or a local/online Go or security meetup. One talk, recorded or with slides published, is enough to answer the question honestly in an interview.

**Cost:** ~1 week of effort spread across Phases 2–4 (extraction is mostly moving code you already wrote; posts are written after the code works). **Payoff:** turns an essential-qualification "no" into a differentiated "yes" that almost no student applicant has.

*Spec version 1.0 — July 18, 2026*