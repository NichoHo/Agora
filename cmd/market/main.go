package main

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"agora/internal/authn"
	"agora/internal/events"
	"agora/internal/httpx"
	"agora/internal/market"
	"agora/internal/pg"
	"agora/internal/tracing"
	"agora/migrations"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	ctx := context.Background()
	shutdown, err := tracing.Init(ctx, "market", os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	if err != nil {
		slog.Error("tracing", "err", err)
		os.Exit(1)
	}
	defer shutdown(ctx)

	pool, err := pg.Connect(ctx, env("DATABASE_URL", "postgres://vault:vault@localhost:5432/vault"))
	if err != nil {
		slog.Error("connect", "err", err)
		os.Exit(1)
	}
	sub, _ := fs.Sub(migrations.FS, "market")
	if err := pg.Migrate(ctx, pool, "market", sub); err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}

	auth := authn.New(
		env("ID_JWKS_URL", "http://localhost:8081/.well-known/jwks.json"),
		env("ID_ISSUER", "http://localhost:8081"))
	payc := &market.PayClient{
		BaseURL: env("PAY_URL", "http://localhost:8083"),
		Token:   os.Getenv("PAY_INTERNAL_TOKEN"),
	}
	autoRelease, err := time.ParseDuration(env("AUTO_RELEASE_AFTER", "72h"))
	if err != nil {
		slog.Error("bad AUTO_RELEASE_AFTER", "err", err)
		os.Exit(1)
	}

	if err := events.StartRelay(ctx, pool, "market", os.Getenv("REDPANDA_BROKERS")); err != nil {
		slog.Error("relay", "err", err)
		os.Exit(1)
	}

	srv := market.NewServer(pool, auth, payc)
	srv.StartSweeper(ctx, 30*time.Second, autoRelease)

	addr := ":" + env("PORT", "8082")
	slog.Info("market listening", "addr", addr)
	if err := http.ListenAndServe(addr, otelhttp.NewHandler(httpx.Wrap(srv), "market")); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}
