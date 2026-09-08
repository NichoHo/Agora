// Package tracing wires OpenTelemetry the same minimal way across every Go
// service: an OTLP/HTTP exporter to Jaeger, W3C trace-context propagation
// over HTTP (via otelhttp, both server and client side), and a small helper
// to carry that same context through the one hop otelhttp can't reach: the
// transactional outbox, relayed to Kafka and picked up by a consumer in a
// different process (and, for risk, a different language). See
// docs/adr/0004-distributed-tracing.md for why the outbox needs its own
// propagation and outboxkit itself does not.
package tracing

import (
	"context"
	"log/slog"
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Client is the http.Client every inter-service client in this repo (pay's
// MarketClient/SwitchClient, market's PayClient) should use instead of
// http.DefaultClient: same client, plus a client-side span per call and a
// traceparent header injected into the outgoing request. Cheap enough to
// build fresh per client struct; otelhttp's transport holds no state worth
// sharing.
func Client() *http.Client {
	// http.DefaultTransport's MaxIdleConnsPerHost is 2, tuned for a client
	// talking to many hosts. An inter-service client talks to one host
	// repeatedly and concurrently; 2 idle conns forces constant reconnects
	// under load. See docs/writeups/05-load-test-results.md scenario 4.
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConnsPerHost = 100
	return &http.Client{Transport: otelhttp.NewTransport(t)}
}

// Init sets the global TracerProvider and propagator. otlpEndpoint is a bare
// host:port (Jaeger's OTLP/HTTP receiver, default "jaeger:4318"); tracing is
// disabled (a no-op provider) when it's empty, so services still boot
// without Jaeger running, the same dev-convenience every REDPANDA_BROKERS
// check in this repo already gives.
func Init(ctx context.Context, serviceName, otlpEndpoint string) (shutdown func(context.Context) error, err error) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	if otlpEndpoint == "" {
		slog.Warn("OTEL_EXPORTER_OTLP_ENDPOINT unset; tracing disabled", "service", serviceName)
		return func(context.Context) error { return nil }, nil
	}
	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(otlpEndpoint),
		otlptracehttp.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}
	res, err := resource.New(ctx, resource.WithAttributes(semconv.ServiceName(serviceName)))
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	slog.Info("tracing enabled", "service", serviceName, "endpoint", otlpEndpoint)
	return tp.Shutdown, nil
}

// Traceparent formats ctx's current span as a W3C traceparent header value,
// for embedding in an outbox payload (see the package doc). Empty when
// tracing is disabled or ctx carries no span.
func Traceparent(ctx context.Context) string {
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	return carrier.Get("traceparent")
}
