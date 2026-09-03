package main

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"os"

	"agora/internal/authn"
	"agora/internal/events"
	"agora/internal/httpx"
	"agora/internal/pay"
	"agora/internal/pg"
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
	pool, err := pg.Connect(ctx, env("DATABASE_URL", "postgres://vault:vault@localhost:5432/vault"))
	if err != nil {
		slog.Error("connect", "err", err)
		os.Exit(1)
	}
	sub, _ := fs.Sub(migrations.FS, "pay")
	if err := pg.Migrate(ctx, pool, "pay", sub); err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}

	if err := events.StartRelay(ctx, pool, "pay", os.Getenv("REDPANDA_BROKERS")); err != nil {
		slog.Error("relay", "err", err)
		os.Exit(1)
	}

	auth := authn.New(
		env("ID_JWKS_URL", "http://localhost:8081/.well-known/jwks.json"),
		env("ID_ISSUER", "http://localhost:8081"))

	var sw *pay.SwitchClient
	if switchURL := os.Getenv("SWITCH_URL"); switchURL != "" {
		sw = &pay.SwitchClient{BaseURL: switchURL, APIKey: os.Getenv("SWITCH_API_KEY")}
		slog.Info("card authorization via switch enabled", "url", switchURL)
	} else {
		slog.Warn("SWITCH_URL unset; escrow funds from wallet balance only, no card auth")
	}
	srv := pay.NewServer(pool, auth, os.Getenv("PAY_INTERNAL_TOKEN"), sw)

	addr := ":" + env("PORT", "8083")
	slog.Info("pay listening", "addr", addr)
	if err := http.ListenAndServe(addr, httpx.Wrap(srv)); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}
