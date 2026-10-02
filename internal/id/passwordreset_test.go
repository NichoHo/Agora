package id

import (
	"context"
	"testing"
	"time"
)

func TestForgotPasswordDoesNotLeakAccountExistence(t *testing.T) {
	ts, c, _ := testEnv(t)
	register(t, c, ts, "hasaccount@test.dev", "hasaccount")

	real := postJSON(t, c, ts.URL+"/password/forgot", map[string]string{"Email": "hasaccount@test.dev"})
	fake := postJSON(t, c, ts.URL+"/password/forgot", map[string]string{"Email": "nobody@test.dev"})
	if real.StatusCode != 204 || fake.StatusCode != 204 {
		t.Fatalf("want 204/204 regardless of existence, got %d/%d", real.StatusCode, fake.StatusCode)
	}
}

func TestResetPasswordSingleUseAndExpiry(t *testing.T) {
	ts, c, pool := testEnv(t)
	ctx := context.Background()
	register(t, c, ts, "reset@test.dev", "reset")

	var userID string
	if err := pool.QueryRow(ctx, `SELECT id FROM id.users WHERE email = 'reset@test.dev'`).Scan(&userID); err != nil {
		t.Fatal(err)
	}

	seedToken := func(raw string, expiresAt time.Time) {
		if _, err := pool.Exec(ctx,
			`INSERT INTO id.password_reset_tokens (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
			hashCode(raw), userID, expiresAt); err != nil {
			t.Fatal(err)
		}
	}

	// expired token rejected
	seedToken("expired-token", time.Now().Add(-time.Minute))
	if resp := postJSON(t, c, ts.URL+"/password/reset",
		map[string]string{"Token": "expired-token", "NewPassword": "newpassword123"}); resp.StatusCode != 400 {
		t.Fatalf("expired token: want 400, got %d", resp.StatusCode)
	}

	// valid token: resets the password and revokes existing sessions
	seedToken("good-token", time.Now().Add(time.Hour))
	if resp := postJSON(t, c, ts.URL+"/password/reset",
		map[string]string{"Token": "good-token", "NewPassword": "newpassword123"}); resp.StatusCode != 204 {
		t.Fatalf("valid token: want 204, got %d", resp.StatusCode)
	}

	// old password no longer works, new one does
	if resp := postJSON(t, c, ts.URL+"/login",
		map[string]string{"email": "reset@test.dev", "password": "password123!"}); resp.StatusCode != 401 {
		t.Fatalf("old password after reset: want 401, got %d", resp.StatusCode)
	}
	if resp := postJSON(t, c, ts.URL+"/login",
		map[string]string{"email": "reset@test.dev", "password": "newpassword123"}); resp.StatusCode != 200 {
		t.Fatalf("new password after reset: want 200, got %d", resp.StatusCode)
	}

	// the same token cannot be replayed
	if resp := postJSON(t, c, ts.URL+"/password/reset",
		map[string]string{"Token": "good-token", "NewPassword": "anotherpassword123"}); resp.StatusCode != 400 {
		t.Fatalf("reused token: want 400, got %d", resp.StatusCode)
	}
}
