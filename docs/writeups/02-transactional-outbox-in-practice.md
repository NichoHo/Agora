# The transactional outbox pattern in practice

Here is a bug that looks correct in code review and fails in production:

```go
order := createOrder(ctx, db)          // 1. write to the database
events.Publish("order.created", order) // 2. publish an event
```

Two writes to two systems, no transaction spanning them. The process can die
between line 1 and line 2: order created, no event, and a downstream consumer
(inventory, email, fraud scoring) never hears about it. Or line 2 succeeds and
the surrounding transaction rolls back, leaving an event for an order that
doesn't exist. This is the **dual-write problem**, and you cannot fix it by reordering
the two lines or wrapping them in a retry. There is no ordering of two
independent commits that is crash-safe.

Building [Vault](https://github.com/NichoHo/Agora), a marketplace where the
trust-scoring service reacts to every listing and order, I hit this and solved it
with the transactional outbox, then extracted the reusable core into a small Go
library, [outboxkit](https://github.com/NichoHo/outboxkit). This is how it works
and why the guarantee holds.

## One transaction, two rows

The insight: you already have one system that can make writes atomic, and that
is your database. So make the event a *database row*, written in the **same transaction**
as the state change. Either both commit or neither does.

In Vault's market service, creating a listing and recording its event are one
transaction:

```go
tx, _ := s.pool.Begin(r.Context())
defer tx.Rollback(r.Context())

l, _ := scanListing(tx.QueryRow(r.Context(),
    `INSERT INTO market.listings (...) VALUES (...) RETURNING ...`, ...))

// same tx: the event can't outlive a rolled-back listing, and vice versa
outboxTx(r.Context(), tx, "listing.created", map[string]any{
    "listing_id": l.ID, "seller_id": l.SellerID, "price_minor": l.PriceMinor})

tx.Commit(r.Context())
```

The `outbox` table is deliberately dumb:

```sql
CREATE TABLE outbox (
    id           bigserial PRIMARY KEY,
    topic        text NOT NULL,
    payload      jsonb NOT NULL,
    published_at timestamptz          -- NULL until a relay ships it
);
```

After commit, the database holds a durable, ordered log of events that is exactly
consistent with the state changes, because it *is* the state changes' commit.
Nothing has been published yet. That's the relay's job.

## The relay: at-least-once by construction

A separate process (or goroutine) drains unpublished rows to the real message
bus, which is Redpanda in Vault's case. This is outboxkit's `Relay`, and the
entire correctness argument lives in one method:

```go
func (r *Relay) DrainOnce(ctx context.Context) (int, error) {
    tx, _ := r.Pool.Begin(ctx)
    defer tx.Rollback(ctx)

    rows, _ := tx.Query(ctx,
        `SELECT id, topic, payload FROM `+r.Schema+`.outbox
         WHERE published_at IS NULL ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED`,
        r.batch())
    // ... scan into msgs, ids ...

    if err := r.Pub.Publish(ctx, msgs); err != nil {
        return 0, err // rows stay unpublished; retried next drain
    }
    tx.Exec(ctx,
        `UPDATE `+r.Schema+`.outbox SET published_at = now() WHERE id = ANY($1)`, ids)
    return len(msgs), tx.Commit(ctx)
}
```

Walk the crash points:

- **Crash before `Publish`** → nothing sent, rows still `NULL`, redriven next
  time. No loss.
- **`Publish` returns an error** → `Rollback`, rows still `NULL`, retried. No loss.
- **Crash after `Publish` succeeds but before `COMMIT`** → the message *is* on the
  bus, but `published_at` was never set, so the next drain publishes it **again**.
  A duplicate.

That last case is the crux: **you cannot get exactly-once delivery here.** The
publish and the mark-as-published are writes to two different systems, which is
the same dual-write problem one level down. So the relay doesn't pretend to. It
commits to **at-least-once** and makes duplicates the consumer's problem, which
turns out to be solvable.

Two details earn their place. `FOR UPDATE SKIP LOCKED` lets you run multiple relay
instances that never block each other, since each grabs a disjoint batch. And holding
the transaction open across `Publish` is deliberate: it's what makes a crash
leave the rows unpublished instead of marked-but-unsent.

## The consumer: at-least-once + idempotent = exactly-once effects

At-least-once delivery is only useful if consumers are idempotent. outboxkit ships
the other half of the pattern for that: `Idempotent.Guard`, which records the
event id in a `consumed_events` table **in the same transaction as the effect**:

```go
func (i *Idempotent) Guard(ctx, eventID int64, effect func(pgx.Tx) error) (bool, error) {
    tx, _ := i.Pool.Begin(ctx)
    defer tx.Rollback(ctx)

    tag, _ := tx.Exec(ctx,
        `INSERT INTO `+i.Schema+`.consumed_events (source, event_id) VALUES ($1, $2)
         ON CONFLICT DO NOTHING`, i.Source, eventID)
    if tag.RowsAffected() == 0 {
        return false, nil // already consumed, so this is a safe no-op
    }
    if err := effect(tx); err != nil {
        return false, err // marker rolls back with the effect
    }
    return true, tx.Commit(ctx)
}
```

The `ON CONFLICT DO NOTHING` on a primary key is the dedup. First delivery
inserts the marker and runs the effect, atomically. A redelivery finds the marker
already there, does nothing, returns `false`. And if the effect fails, the marker
rolls back *with* it, so a failed effect isn't falsely remembered as done.

Compose the two halves and you get the property everyone actually wants:

> **at-least-once delivery + idempotent consumer = exactly-once effects**

Not exactly-once *delivery*, which is a distributed-systems unicorn, but
exactly-once *effect*, which is what "the seller gets charged once" really needs.

## Proving it: the chaos test

The pattern's whole selling point is crash safety, so the test injects a crash.
outboxkit's chaos test seeds events, uses a publisher that records everything it's
handed but **fails on the second batch after "publishing" it** (the exact
crash-after-send-before-commit case), drains to completion tolerating the failure,
and asserts three things:

```go
// NO LOSS: every distinct event id reached the publisher at least once
if len(delivered) != total { t.Fatalf("lost events") }

// the crash really did cause re-delivery, or the test proves nothing
if len(pub.got) <= total { t.Fatalf("crash did not cause re-delivery") }

// NO DUPLICATE EFFECTS: an Idempotent consumer applies each id exactly once
for _, m := range pub.got {
    applied, _ := idem.Guard(ctx, m.ID, func(pgx.Tx) error { return nil })
    if applied { effects++ }
}
if effects != total { t.Fatalf("duplicate effects") }
```

It's deterministic (no `sleep`, no real broker) and runs under `-race`. It's the
single most valuable test in the module, because it exercises the one scenario the
pattern exists to survive. It also asserts the redelivery *happened*, so a version
that silently lost the crash would fail rather than pass green.

## Why extract it

The relay and the guard are maybe 150 lines. But they're 150 lines that are easy
to get subtly wrong: hold the transaction the wrong way and you lose events,
forget `ON CONFLICT` and you double-charge. Pulling them into a versioned module
with a chaos test means the correctness argument lives in one place, tested once,
reused by both of Vault's event-producing services. The core has **no
message-broker dependency**. It talks to a `Publisher` interface, so it's
testable with a fake and you bring your own sink (a `kafkapub` subpackage wraps
Redpanda). Outbox/idempotency is genuinely underserved in Go, and it's exactly the
kind of small, correctness-shaped thing that's worth doing once and well.

*outboxkit: [github.com/NichoHo/outboxkit](https://github.com/NichoHo/outboxkit).
Used in Vault by the market and pay services.*
