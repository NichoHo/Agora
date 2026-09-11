// Package pg: pool connect with retry + a tiny schema-scoped migration runner.
package pg

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// maxConns caps each service's pool so a traffic spike (sale's reserve
// endpoint under load, see AGORA_SPEC.md section 11) degrades into queued
// requests instead of exhausting Postgres's own connection limit and taking
// every service down with it.
const maxConns = 20

// Connect retries for ~30s so services survive a Postgres that is still booting.
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("pg connect: %w", err)
	}
	cfg.MaxConns = maxConns
	var pool *pgxpool.Pool
	for i := 0; i < 30; i++ {
		pool, err = pgxpool.NewWithConfig(ctx, cfg)
		if err == nil {
			if err = pool.Ping(ctx); err == nil {
				return pool, nil
			}
			pool.Close()
		}
		time.Sleep(time.Second)
	}
	return nil, fmt.Errorf("pg connect: %w", err)
}

// Migrate applies every *.sql in fsys (name order) that is not yet recorded in
// <schema>.schema_migrations. Each file runs in its own transaction with
// search_path set to the schema.
func Migrate(ctx context.Context, pool *pgxpool.Pool, schema string, fsys fs.FS) error {
	if _, err := pool.Exec(ctx, fmt.Sprintf(
		`CREATE SCHEMA IF NOT EXISTS %s;
		 CREATE TABLE IF NOT EXISTS %s.schema_migrations (name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());`,
		schema, schema)); err != nil {
		return err
	}
	names, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		var done bool
		if err := pool.QueryRow(ctx, fmt.Sprintf(
			`SELECT EXISTS(SELECT 1 FROM %s.schema_migrations WHERE name=$1)`, schema), name).Scan(&done); err != nil {
			return err
		}
		if done {
			continue
		}
		sql, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		// no-arg Exec uses the simple protocol, so multi-statement files work
		if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s;\n", schema)+string(sql)); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(
			`INSERT INTO %s.schema_migrations(name) VALUES ($1)`, schema), name); err != nil {
			tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}
