package id

import (
	"context"
	"encoding/json"
	"net/http"

	"agora/internal/httpx"
)

// revokeAllRefreshTokens burns every refresh token family for a user, not
// just the one currently in play: a password change or deactivation should
// sign the user out everywhere, on every device and client, not just stop
// the session that triggered it.
func (s *Server) revokeAllRefreshTokens(ctx context.Context, userID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE id.refresh_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, userID)
	return err
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	u, ok := s.sessionUser(r)
	if !ok {
		httpx.Error(w, 401, "not signed in")
		return
	}
	var in struct{ CurrentPassword, NewPassword string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpx.Error(w, 400, "bad json")
		return
	}
	if len(in.NewPassword) < 8 {
		httpx.Error(w, 400, "password must be at least 8 characters")
		return
	}
	var hash string
	if err := s.pool.QueryRow(r.Context(),
		`SELECT password_hash FROM id.credentials WHERE user_id = $1`, u.ID).Scan(&hash); err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	if ok, _ := VerifyPassword(in.CurrentPassword, hash); !ok {
		s.audit(r.Context(), u.ID, "password.change.fail", nil)
		httpx.Error(w, 401, "current password is wrong")
		return
	}
	newHash, err := HashPassword(in.NewPassword)
	if err != nil {
		httpx.Error(w, 500, "hash failed")
		return
	}
	if _, err := s.pool.Exec(r.Context(),
		`UPDATE id.credentials SET password_hash = $2, updated_at = now() WHERE user_id = $1`,
		u.ID, newHash); err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	if err := s.revokeAllRefreshTokens(r.Context(), u.ID); err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	s.audit(r.Context(), u.ID, "password.change", nil)
	w.WriteHeader(204)
}

// handleDeactivate is self-service account deactivation: sets is_active
// false, revokes every refresh token so no client can mint a new access
// token past this point (handleLogin and both handleToken grant paths check
// is_active), and signs out the session that made the request. An access
// token already issued before deactivation stays valid for the rest of its
// short tokenTTL (15 minutes) — closing that window needs a per-request
// revocation check in every service's authn.Verifier, deferred; see
// docs/writeups for the trade-off.
func (s *Server) handleDeactivate(w http.ResponseWriter, r *http.Request) {
	u, ok := s.sessionUser(r)
	if !ok {
		httpx.Error(w, 401, "not signed in")
		return
	}
	if _, err := s.pool.Exec(r.Context(),
		`UPDATE id.users SET is_active = false WHERE id = $1`, u.ID); err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	if err := s.revokeAllRefreshTokens(r.Context(), u.ID); err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.pool.Exec(r.Context(), `DELETE FROM id.sessions WHERE id = $1`, c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	s.audit(r.Context(), u.ID, "account.deactivate", nil)
	w.WriteHeader(204)
}
