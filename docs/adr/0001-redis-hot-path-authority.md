# ADR 0001: Redis is the hot-path reservation authority

Status: accepted. Scope: the `sale` service.

## Context

A drop sells a fixed number of units at a fixed start time. Thousands of
buyers can attempt to reserve a unit within the same few seconds. Every
attempt must get a fast, correct answer: either a unit, or a clear "sold
out". Postgres alone cannot give both. A single row's `UPDATE ... WHERE
remaining > 0` serializes every attempt on that row's lock, so throughput
drops as demand rises, exactly when it needs to hold up.

## Decision

During a drop, Redis holds the truth about which units are still available.
Postgres holds the durable record of what happened. The two are not kept in
lockstep. A background job compares them and logs the difference (spec 7.8,
`stock_reconciliation`); it never corrects Redis from Postgres, because
Redis is the one telling the truth about inventory in real time.

Each unit for a drop is an id in a Redis set, split across `shard_count`
sets (`sale:{drop}:stock:{shard}`) instead of one set. One Lua script does
the whole claim: pop one id from a shard, and if that shard is empty, try
the rest, in one atomic call. The script also writes the claim's expiry key,
so Redis's own TTL, not any server's clock, decides when a hold lapses. See
`internal/sale/redis.go`.

## Why sharding

One set for a 1,000-unit drop makes every claim contend for the same Redis
key. Splitting it N ways spreads that contention, at a cost: a shard can go
empty before the drop is actually sold out, so the script has to walk the
other shards. More shards lower contention per shard but raise the walk cost
on the sold-out tail. This repo defaults to `shard_count = 8`; tune it per
drop from the load numbers in Section 11 of `AGORA_SPEC.md`, once they
exist.

## What this costs

If the Redis process dies mid-drop, no claim can succeed until it (or a
replacement) comes back. Chaos test: kill Redis mid-drop, then check that no
new oversell happens and that no money moves incorrectly (spec 10.3). This
suite does not yet include that test; see the migration report for the
list of what's covered.

## Alternatives considered

**Postgres advisory locks, no Redis.** Simpler, one fewer moving part. Ruled
out: the shard workaround for hot-key contention needs a data structure with
cheap atomic pop, which Postgres row locking doesn't give without real
engineering (e.g. hash-partitioned counter rows, which is most of Redis's
job reimplemented on the wrong tool).

**A message queue (SQS/Kafka) as the ticketing layer.** Good at ordering,
weak at "tell me the *current* count instantly." Redis's `SPOP` is O(1) and
exact; a queue would need a separate count somewhere else anyway.
