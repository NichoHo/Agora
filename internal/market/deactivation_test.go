package market

import (
	"context"
	"io/fs"
	"testing"

	"agora/internal/pg"
	"agora/migrations"
)

// TestDeactivatedAccountRejectedImmediately proves AGORA_SPEC.md section 12's
// "account deactivation takes effect on the next request, not the next token
// refresh" across a service boundary: market never talks to id's database
// directly except through authn.Verifier's cross-schema is_active check.
// Every other market test uses a bare fabricated sub with no id.users row at
// all (see testEnv), which is exactly the case authn.Verifier.active must
// tolerate (undefined_table) for those to keep passing; this test is the one
// place a real id.users row exists, so it's the one place the check itself
// is actually exercised end to end.
func TestDeactivatedAccountRejectedImmediately(t *testing.T) {
	e := testEnv(t)
	ctx := context.Background()
	pool := e.srv.pool

	sub, _ := fs.Sub(migrations.FS, "id")
	if err := pg.Migrate(ctx, pool, "id", sub); err != nil {
		t.Fatal(err)
	}
	// Every other test in this package relies on authn.Verifier.active's
	// undefined_table fallback (no id schema in their database at all, see
	// testEnv/the comment above) — leaving this schema behind would make
	// their fabricated subs look like real-but-missing users instead
	// (pgx.ErrNoRows, not undefined_table), turning every one of their
	// authenticated requests into a spurious 401.
	t.Cleanup(func() { pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS id CASCADE`) })
	const userID = "33333333-3333-3333-3333-333333333333"
	if _, err := pool.Exec(ctx,
		`INSERT INTO id.users (id, email, handle) VALUES ($1, 'dee@test.dev', 'dee')`, userID); err != nil {
		t.Fatal(err)
	}

	at := e.token(t, userID)
	create := map[string]any{"title": "x", "price_minor": 100}

	// active: the token works on an authenticated route
	if resp := e.do(t, "POST", "/listings", at, create); resp.StatusCode != 201 {
		t.Fatalf("active user: want 201, got %d", resp.StatusCode)
	}

	// deactivate — no new token minted, the exact same one is reused below
	if _, err := pool.Exec(ctx, `UPDATE id.users SET is_active = false WHERE id = $1`, userID); err != nil {
		t.Fatal(err)
	}

	// same still-unexpired token, next request: rejected immediately
	if resp := e.do(t, "POST", "/listings", at, create); resp.StatusCode != 401 {
		t.Fatalf("deactivated user, same token, next request: want 401, got %d", resp.StatusCode)
	}
}
