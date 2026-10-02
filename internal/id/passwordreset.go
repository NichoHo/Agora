package id

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"agora/internal/httpx"
)

const passwordResetTTL = time.Hour

// handleForgotPassword always responds 204, whether or not the email
// matches an account: an OIDC/auth error response must never leak whether a
// username exists (AGORA_SPEC.md section 12). On a match it mints a
// single-use token, stores its hash (same shape as recovery codes), and
// "sends" the reset link the only way this project sends any email —
// logged, per "vault spec.md" section 2's "console-log email" decision,
// not a real mailer.
func (s *Server) handleForgotPassword(w http.ResponseWriter, r *http.Request) {
	var in struct{ Email string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpx.Error(w, 400, "bad json")
		return
	}
	email := strings.ToLower(strings.TrimSpace(in.Email))
	var userID string
	err := s.pool.QueryRow(r.Context(),
		`SELECT id FROM id.users WHERE email = $1 AND is_active`, email).Scan(&userID)
	if err != nil {
		// no such account (or deactivated): burn comparable time to a real
		// lookup + token mint so the response time doesn't leak existence.
		HashPassword("time-parity-dummy")
		w.WriteHeader(204)
		return
	}
	token := randToken(32)
	if _, err := s.pool.Exec(r.Context(),
		`INSERT INTO id.password_reset_tokens (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		hashCode(token), userID, time.Now().Add(passwordResetTTL)); err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	link := s.webURL + "/auth/reset?token=" + token
	slog.Info("email", "to", email, "subject", "Reset your password", "body", link)
	s.audit(r.Context(), userID, "password.reset.request", nil)
	w.WriteHeader(204)
}

func (s *Server) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	var in struct{ Token, NewPassword string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpx.Error(w, 400, "bad json")
		return
	}
	if len(in.NewPassword) < 8 {
		httpx.Error(w, 400, "password must be at least 8 characters")
		return
	}
	// single-use, atomically claimed: the guarded UPDATE both burns the
	// token and serializes concurrent submissions of it, same idiom as
	// TOTP/recovery-code consumption in mfa.go.
	var userID string
	err := s.pool.QueryRow(r.Context(),
		`UPDATE id.password_reset_tokens SET used_at = now()
		 WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
		 RETURNING user_id`, hashCode(in.Token)).Scan(&userID)
	if err != nil {
		httpx.Error(w, 400, "invalid or expired token")
		return
	}
	newHash, err := HashPassword(in.NewPassword)
	if err != nil {
		httpx.Error(w, 500, "hash failed")
		return
	}
	if _, err := s.pool.Exec(r.Context(),
		`UPDATE id.credentials SET password_hash = $2, updated_at = now() WHERE user_id = $1`,
		userID, newHash); err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	if err := s.revokeAllRefreshTokens(r.Context(), userID); err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	s.audit(r.Context(), userID, "password.reset", nil)
	w.WriteHeader(204)
}
