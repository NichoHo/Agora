# outboxkit

[![CI](https://github.com/NichoHo/outboxkit/actions/workflows/ci.yml/badge.svg)](https://github.com/NichoHo/outboxkit/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/NichoHo/outboxkit.svg)](https://pkg.go.dev/github.com/NichoHo/outboxkit)
[![MIT License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

A small, focused Go library for the **transactional outbox** pattern on
PostgreSQL: a relay that drains an outbox table to a message sink *at-least-once*,
and an idempotent consumer guard that turns at-least-once delivery into
*exactly-once effects*.

```
at-least-once delivery  +  idempotent consumer  =  exactly-once effects
```

## Why

If a service writes a row **and** publishes an event in two separate steps, a
crash between them either loses the event or emits one for a change that rolled
back. This is the *dual-write problem*. Writing the event to an **outbox table in the
same transaction** as the state change makes the two atomic. A relay then
publishes committed outbox rows out-of-band. outboxkit is that relay plus the
consumer-side dedup that makes redelivery safe.

The core package has **no message-broker dependency**. It talks to a
`Publisher` interface, so it's trivially testable with a fake and you bring your
own sink. A `kafkapub` subpackage provides a Kafka/Redpanda publisher.

## Install

```sh
go get github.com/NichoHo/outboxkit
```

## Producer side: the relay

Your service writes to `<schema>.outbox` in the same transaction as its state
change (outboxkit does not prescribe how; it's your `INSERT`). The table needs
at least:

```sql
CREATE TABLE app.outbox (
    id           bigserial PRIMARY KEY,
    topic        text NOT NULL,
    payload      jsonb NOT NULL,
    published_at timestamptz
);
```

Then run a relay in the background:

```go
pub, err := kafkapub.New([]string{"localhost:9092"}, "app.events")
if err != nil {
    log.Fatal(err)
}
relay := &outboxkit.Relay{Pool: pool, Schema: "app", Pub: pub}
go relay.Run(ctx) // drains until ctx is cancelled
```

The relay `SELECT ... WHERE published_at IS NULL ... FOR UPDATE SKIP LOCKED`,
publishes the batch, then marks the rows published, all in one transaction. A
crash after publishing but before commit re-publishes those rows on the next
drain. `SKIP LOCKED` lets you run multiple relay instances without them
blocking each other.

## Consumer side: idempotent effects

Delivery is at-least-once, so consumers must be idempotent. Wrap your side
effect in `Guard`, which records the event id in `<schema>.consumed_events` in
the **same transaction** as the effect:

```sql
CREATE TABLE app.consumed_events (
    source   text NOT NULL,
    event_id bigint NOT NULL,
    PRIMARY KEY (source, event_id)
);
```

```go
idem := &outboxkit.Idempotent{Pool: pool, Schema: "app", Source: "orders"}

applied, err := idem.Guard(ctx, event.ID, func(tx pgx.Tx) error {
    _, err := tx.Exec(ctx, `UPDATE app.inventory SET reserved = reserved + 1 WHERE sku = $1`, sku)
    return err
})
// applied == false means this event was already processed, so it's a safe no-op.
```

If the effect returns an error, the dedup marker rolls back with it, so a retry
runs the effect again. First delivery applies exactly once; every redelivery is
a no-op.

## What's tested

The suite includes a **chaos test** that seeds events, injects a relay crash
mid-flow (publisher succeeds, then the drain fails before commit), drains to
completion, and asserts: every event was delivered at least once (no loss), the
crash really did cause redelivery, and an `Idempotent` consumer applied each
event exactly once (no duplicate effects). It runs under `-race`.

```sh
TEST_DATABASE_URL=postgres://user:pass@localhost:5432/db go test -race ./...
```

## License

MIT. See [LICENSE](LICENSE).

Extracted from [Agora](https://github.com/NichoHo/Agora), a marketplace with a
self-built OIDC identity provider and escrow payments.
