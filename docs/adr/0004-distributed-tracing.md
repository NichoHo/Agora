# ADR 0004: tracing rides the outbox payload across Kafka, and a Java agent covers switch

Status: accepted. Scope: every service, plus switch's deployment config.

## Context

`AGORA_SPEC.md` section 9.2 wants one trace ID following a single request
through `id`, `sale`, `market`, `pay`, `switch` (a separate Java process in
a separate repository), the transactional outbox, Redpanda, and `risk`.
OpenTelemetry's standard propagation (W3C Trace Context, a `traceparent`
header) covers direct HTTP calls for free once each server and client is
instrumented. It does not cover the two hops in that list that are not
HTTP: outbox row to Kafka message, and Kafka message to consumer.

## Decision

**HTTP hops:** every service wraps its handler in
`otelhttp.NewHandler` (server-side spans, extracts `traceparent` from
incoming requests) and every inter-service client
(`MarketClient`, `PayClient`, `SwitchClient`) uses an `http.Client` built on
`otelhttp.NewTransport` (client-side spans, injects `traceparent` into
outgoing requests). This is the same propagation mechanism regardless of
which side of the `pay` → `switch` language boundary a hop crosses; W3C
Trace Context is not Go-specific.

**The outbox hop:** `internal/tracing.Traceparent(ctx)` formats the
current span as a `traceparent` string, and each service's own outbox
writer (`market` and `sale`'s `outboxTx`, `pay`'s literal `INSERT`, `id`'s
`emit`) adds it to the JSON payload under a reserved `_trace` key, right
alongside the domain fields it already writes. `risk`'s consumer (and
`assist`'s, for the topics it reads) pulls `_trace` back out and extracts
it into a `Context` before creating its own span, so that span becomes a
real child of the one that wrote the outbox row, not a disconnected trace.

This is deliberately not a change to `outboxkit` or `kafkapub`. Both stay
exactly what they are: a generic, broker-level at-least-once relay that has
no opinion about payload shape. Tracing metadata travels the one channel
already guaranteed to reach the consumer intact, the JSON payload itself,
instead of asking a standalone, independently-versioned, MIT-licensed
library to grow an OpenTelemetry dependency for one caller's use case.

**switch (Java, separate repo):** the OpenTelemetry Java agent
(`opentelemetry-javaagent.jar`), attached via `-javaagent` in switch's own
`gateway`/`acquirer-sim` Dockerfiles, with `OTEL_EXPORTER_OTLP_ENDPOINT`
pointed at agora's Jaeger over `host.docker.internal` (mirroring how `pay`
already reaches switch's gateway the same way, from ADR 0003). Zero
switch source code changes: the agent auto-instruments Spring's incoming
requests (extracting the `traceparent` `pay` already sends) and its
outgoing calls to `acquirer-sim` (continuing the same trace one hop
further, for free). This is the smallest possible footprint on a
repository this migration otherwise doesn't touch.

## What this costs

A span linked via `_trace` in a Kafka payload is exactly as reliable as
the outbox itself: at-least-once. A redelivered event creates a
DB-idempotent no-op in every consumer here (Conservation and Idempotency
are proven in the sale and pay test suites), but OpenTelemetry has no way
to know that and will show a second (very short, immediately-returning)
child span for the replay. Cosmetic, not a correctness problem: the trace
viewer shows an extra span, not extra state.

## Verification

A real drop purchase (storefront checkout, funded through switch) produces
one trace spanning `sale`, `market`, `assist`, `pay`, `switch-gateway`,
`switch-acquirer-sim`, and `risk`, 122 spans across 7 services in one
call: [`load/results/phase5-trace-storefront-to-risk.png`](../../load/results/phase5-trace-storefront-to-risk.png).
That screenshot is the second capture, not the first: the first one was
missing every `switch-gateway`/`switch-acquirer-sim` span past tokenize,
because `SwitchClient.authorize()` sent its request through bare
`http.DefaultClient` instead of the traced client every other method here
uses, so it carried no `traceparent`. See
[write-up 05](../writeups/05-load-test-results.md) for how load testing
surfaced that before this verification step did.

## Alternatives considered

**Add a `trace_context` column to every outbox table and a header in
kafkapub instead of a payload key.** More "correct" in the sense of
keeping tracing metadata out of the domain payload, but it means
`outboxkit`'s `Message` type and `kafkapub`'s `Publish` need to know
OpenTelemetry exists, which the ADR's whole point is to avoid. The payload
key costs one line per outbox writer and touches nothing shared.

**OpenTelemetry Collector as a sidecar, instead of exporting straight to
Jaeger.** Standard production topology, and worth adopting if this ever
needs sampling policy, multiple backends, or metrics/logs correlation
alongside traces. For a single demo instance, one more container buys
nothing yet; Jaeger accepts OTLP directly.
