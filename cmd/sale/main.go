package main

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"agora/internal/authn"
	"agora/internal/events"
	"agora/internal/httpx"
	"agora/internal/sale"
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
	sub, _ := fs.Sub(migrations.FS, "sale")
	if err := pg.Migrate(ctx, pool, "sale", sub); err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}

	auth := authn.New(
		env("ID_JWKS_URL", "http://localhost:8081/.well-known/jwks.json"),
		env("ID_ISSUER", "http://localhost:8081"))
	rdb := sale.NewRedis(env("REDIS_ADDR", "localhost:6379"))
	if err := rdb.Ping(ctx); err != nil {
		slog.Error("redis connect", "err", err)
		os.Exit(1)
	}
	market := &sale.MarketClient{BaseURL: env("MARKET_URL", "http://localhost:8082")}
	secret := os.Getenv("SALE_TOKEN_SECRET")
	if secret == "" {
		secret = "dev-insecure-sale-token-secret"
	}

	brokers := os.Getenv("REDPANDA_BROKERS")
	if err := events.StartRelay(ctx, pool, "sale", brokers); err != nil {
		slog.Error("relay", "err", err)
		os.Exit(1)
	}
	var brokerList []string
	if brokers != "" {
		brokerList = strings.Split(brokers, ",")
	}

	srv := sale.NewServer(pool, auth, rdb, market, []byte(secret))
	if err := sale.StartMarketConsumer(ctx, brokerList, srv); err != nil {
		slog.Error("market consumer", "err", err)
		os.Exit(1)
	}
	srv.StartSweeper(ctx, 15*time.Second)

	addr := ":" + env("PORT", "8085")
	slog.Info("sale listening", "addr", addr)
	if err := http.ListenAndServe(addr, httpx.Wrap(srv)); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}
