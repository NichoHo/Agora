package main

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"os"

	"agora/internal/httpx"
	"agora/internal/id"
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
	sub, _ := fs.Sub(migrations.FS, "id")
	if err := pg.Migrate(ctx, pool, "id", sub); err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}
	signer, err := id.LoadOrCreateSigner(ctx, pool)
	if err != nil {
		slog.Error("signer", "err", err)
		os.Exit(1)
	}

	srv := id.NewServer(pool, signer,
		env("ID_ISSUER", "http://localhost:8081"),
		env("WEB_URL", "http://localhost:3000"))

	addr := ":" + env("PORT", "8081")
	slog.Info("id listening", "addr", addr)
	if err := http.ListenAndServe(addr, httpx.Wrap(srv)); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}
