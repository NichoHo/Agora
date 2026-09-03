package id

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"agora/internal/httpx"
)

// webPath is the dev proxy prefix the storefront mounts the IdP under.
// ponytail: hardcoded dev shape; make it config when this runs behind a real domain.
const webPath = "/idp"

func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	clientID := q.Get("client_id")
	redirectURI := q.Get("redirect_uri")

	var name string
	var uris []string
	err := s.pool.QueryRow(r.Context(),
		`SELECT name, redirect_uris FROM id.oauth_clients WHERE id = $1`, clientID).Scan(&name, &uris)
	if err != nil {
		httpx.Error(w, 400, "unknown client")
		return
	}
	// exact-match redirect_uri; on failure DO NOT redirect (RFC 6749 §3.1.2.4)
	if !slices.Contains(uris, redirectURI) {
		httpx.Error(w, 400, "redirect_uri not registered")
		return
	}
	if q.Get("response_type") != "code" {
		httpx.Error(w, 400, "response_type must be code")
		return
	}
	// PKCE S256 is mandatory for every client of this IdP
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		httpx.Error(w, 400, "PKCE S256 code_challenge required")
		return
	}

	u, ok := s.sessionUser(r)
	if !ok {
		returnTo := url.QueryEscape(webPath + "/authorize?" + r.URL.RawQuery)
		http.Redirect(w, r, s.webURL+"/auth/login?return_to="+returnTo, http.StatusFound)
		return
	}

	scope := q.Get("scope")
	if scope == "" {
		scope = "openid profile"
	}
	var consented bool
	s.pool.QueryRow(r.Context(),
		`SELECT EXISTS(SELECT 1 FROM id.consents WHERE user_id = $1 AND client_id = $2)`,
		u.ID, clientID).Scan(&consented)
	if !consented {
		returnTo := url.QueryEscape(webPath + "/authorize?" + r.URL.RawQuery)
		http.Redirect(w, r, s.webURL+"/auth/consent?client_id="+url.QueryEscape(clientID)+
			"&client_name="+url.QueryEscape(name)+"&scope="+url.QueryEscape(scope)+
			"&return_to="+returnTo, http.StatusFound)
		return
	}

	code := randToken(32)
	if _, err := s.pool.Exec(r.Context(),
		`INSERT INTO id.auth_codes (code, user_id, client_id, redirect_uri, scope, nonce, code_challenge, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		code, u.ID, clientID, redirectURI, scope, q.Get("nonce"), q.Get("code_challenge"),
		time.Now().Add(codeTTL)); err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	s.audit(r.Context(), u.ID, "code.issue", map[string]any{"client": clientID})

	loc, _ := url.Parse(redirectURI)
	lq := loc.Query()
	lq.Set("code", code)
	if state := q.Get("state"); state != "" {
		lq.Set("state", state)
	}
	loc.RawQuery = lq.Encode()
	http.Redirect(w, r, loc.String(), http.StatusFound)
}

func (s *Server) handleConsent(w http.ResponseWriter, r *http.Request) {
	u, ok := s.sessionUser(r)
	if !ok {
		httpx.Error(w, 401, "not signed in")
		return
	}
	var in struct{ ClientID, Scope string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpx.Error(w, 400, "bad json")
		return
	}
	if _, err := s.pool.Exec(r.Context(),
		`INSERT INTO id.consents (user_id, client_id, scope) VALUES ($1, $2, $3)
		 ON CONFLICT (user_id, client_id) DO UPDATE SET scope = $3, granted_at = now()`,
		u.ID, in.ClientID, in.Scope); err != nil {
		httpx.Error(w, 400, "unknown client")
		return
	}
	s.audit(r.Context(), u.ID, "consent.grant", map[string]any{"client": in.ClientID, "scope": in.Scope})
	w.WriteHeader(204)
}

func invalidGrant(w http.ResponseWriter) {
	httpx.JSON(w, 400, map[string]string{"error": "invalid_grant"})
}

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		httpx.JSON(w, 400, map[string]string{"error": "invalid_request"})
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
	case "refresh_token":
		s.handleRefreshGrant(w, r)
		return
	default:
		httpx.JSON(w, 400, map[string]string{"error": "unsupported_grant_type"})
		return
	}
	code := r.PostForm.Get("code")

	// single atomic claim: a replayed code finds used_at already set and gets nothing
	var userID, clientID, redirectURI, scope, nonce, challenge string
	var expiresAt time.Time
	err := s.pool.QueryRow(r.Context(),
		`UPDATE id.auth_codes SET used_at = now()
		 WHERE code = $1 AND used_at IS NULL
		 RETURNING user_id, client_id, redirect_uri, scope, nonce, code_challenge, expires_at`,
		code).Scan(&userID, &clientID, &redirectURI, &scope, &nonce, &challenge, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		s.audit(r.Context(), "", "token.fail", map[string]any{"reason": "unknown or replayed code"})
		invalidGrant(w)
		return
	}
	if err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	if time.Now().After(expiresAt) ||
		r.PostForm.Get("client_id") != clientID ||
		r.PostForm.Get("redirect_uri") != redirectURI {
		s.audit(r.Context(), userID, "token.fail", map[string]any{"reason": "expired or mismatched request"})
		invalidGrant(w)
		return
	}
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if subtle.ConstantTimeCompare([]byte(b64(sum[:])), []byte(challenge)) != 1 {
		s.audit(r.Context(), userID, "token.fail", map[string]any{"reason": "pkce verifier mismatch"})
		invalidGrant(w)
		return
	}

	var email string
	if err := s.pool.QueryRow(r.Context(),
		`SELECT email FROM id.users WHERE id = $1`, userID).Scan(&email); err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	now := time.Now()
	access, err := s.signer.Sign(Claims{Iss: s.issuer, Sub: userID, Aud: "vault",
		Exp: now.Add(tokenTTL).Unix(), Iat: now.Unix(), Email: email})
	if err != nil {
		httpx.Error(w, 500, "sign")
		return
	}
	idToken, err := s.signer.Sign(Claims{Iss: s.issuer, Sub: userID, Aud: clientID,
		Exp: now.Add(tokenTTL).Unix(), Iat: now.Unix(), Email: email, Nonce: nonce})
	if err != nil {
		httpx.Error(w, 500, "sign")
		return
	}
	refresh, err := s.issueRefreshToken(r.Context(), userID, clientID, "")
	if err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	s.audit(r.Context(), userID, "token.issue", map[string]any{"client": clientID})
	httpx.JSON(w, 200, map[string]any{
		"access_token": access, "id_token": idToken, "refresh_token": refresh,
		"token_type": "Bearer", "expires_in": int(tokenTTL.Seconds()),
	})
}

const refreshTTL = 30 * 24 * time.Hour

// issueRefreshToken mints a rotating refresh token. familyID "" starts a new
// family (fresh auth-code grant); otherwise the new token joins the family of
// the token it replaces.
func (s *Server) issueRefreshToken(ctx context.Context, userID, clientID, familyID string) (string, error) {
	raw := randToken(32)
	var err error
	if familyID == "" {
		_, err = s.pool.Exec(ctx,
			`INSERT INTO id.refresh_tokens (family_id, user_id, client_id, token_hash, expires_at)
			 VALUES (gen_random_uuid(), $1, $2, $3, $4)`,
			userID, clientID, hashCode(raw), time.Now().Add(refreshTTL))
	} else {
		_, err = s.pool.Exec(ctx,
			`INSERT INTO id.refresh_tokens (family_id, user_id, client_id, token_hash, expires_at)
			 VALUES ($1, $2, $3, $4, $5)`,
			familyID, userID, clientID, hashCode(raw), time.Now().Add(refreshTTL))
	}
	if err != nil {
		return "", err
	}
	return raw, nil
}

// handleRefreshGrant rotates refresh tokens. Presenting an already-used token
// is treated as theft: the whole family is revoked (RFC 6819 refresh token
// reuse detection).
func (s *Server) handleRefreshGrant(w http.ResponseWriter, r *http.Request) {
	raw := r.PostForm.Get("refresh_token")
	clientID := r.PostForm.Get("client_id")
	if raw == "" || clientID == "" {
		invalidGrant(w)
		return
	}
	var id, familyID, userID, tokClient string
	var expiresAt time.Time
	var usedAt, revokedAt *time.Time
	err := s.pool.QueryRow(r.Context(),
		`SELECT id, family_id, user_id, client_id, expires_at, used_at, revoked_at
		 FROM id.refresh_tokens WHERE token_hash = $1`, hashCode(raw)).
		Scan(&id, &familyID, &userID, &tokClient, &expiresAt, &usedAt, &revokedAt)
	if err != nil || tokClient != clientID {
		invalidGrant(w)
		return
	}
	if revokedAt != nil {
		invalidGrant(w)
		return
	}
	if usedAt != nil {
		// reuse detected: burn the whole family
		s.pool.Exec(r.Context(),
			`UPDATE id.refresh_tokens SET revoked_at = now() WHERE family_id = $1 AND revoked_at IS NULL`,
			familyID)
		s.audit(r.Context(), userID, "refresh.reuse_detected",
			map[string]any{"family": familyID, "client": clientID})
		invalidGrant(w)
		return
	}
	if time.Now().After(expiresAt) {
		invalidGrant(w)
		return
	}
	// atomic rotation: only one concurrent request gets to mark it used
	tag, err := s.pool.Exec(r.Context(),
		`UPDATE id.refresh_tokens SET used_at = now() WHERE id = $1 AND used_at IS NULL`, id)
	if err != nil || tag.RowsAffected() != 1 {
		invalidGrant(w)
		return
	}
	var email string
	if err := s.pool.QueryRow(r.Context(),
		`SELECT email FROM id.users WHERE id = $1`, userID).Scan(&email); err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	now := time.Now()
	access, err := s.signer.Sign(Claims{Iss: s.issuer, Sub: userID, Aud: "vault",
		Exp: now.Add(tokenTTL).Unix(), Iat: now.Unix(), Email: email})
	if err != nil {
		httpx.Error(w, 500, "sign")
		return
	}
	next, err := s.issueRefreshToken(r.Context(), userID, clientID, familyID)
	if err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	s.audit(r.Context(), userID, "token.refresh", map[string]any{"client": clientID})
	httpx.JSON(w, 200, map[string]any{
		"access_token": access, "refresh_token": next,
		"token_type": "Bearer", "expires_in": int(tokenTTL.Seconds()),
	})
}

func (s *Server) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, 200, map[string]any{
		"issuer":                                s.issuer,
		"authorization_endpoint":                s.issuer + "/authorize",
		"token_endpoint":                        s.issuer + "/token",
		"jwks_uri":                              s.issuer + "/.well-known/jwks.json",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code"},
		"code_challenge_methods_supported":      []string{"S256"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"scopes_supported":                      []string{"openid", "profile"},
	})
}

func (s *Server) handleJWKS(w http.ResponseWriter, r *http.Request) {
	keys, err := PublicKeys(r.Context(), s.pool)
	if err != nil {
		httpx.Error(w, 500, "keys")
		return
	}
	doc, err := JWKS(keys)
	if err != nil {
		httpx.Error(w, 500, "keys")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(doc)
}
