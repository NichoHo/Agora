// Package authn: Bearer-JWT middleware shared by resource services.
// Verifies RS256 tokens against the IdP's JWKS (cached, refetched on
// unknown kid so key rotation propagates).
package authn

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"agora/internal/httpx"
	"agora/internal/id"
)

type ctxKey string

const userKey ctxKey = "user"

// UserID returns the authenticated subject, set by Require.
func UserID(ctx context.Context) string {
	v, _ := ctx.Value(userKey).(string)
	return v
}

type Verifier struct {
	jwksURL string
	issuer  string
	pool    *pgxpool.Pool
	mu      sync.Mutex
	ttl     time.Time
	kk      map[string]*rsa.PublicKey
}

// New builds a Verifier. pool is every calling service's own pgxpool, which
// (per this repo's "one database, schema per service" layout) already points
// at the same physical Postgres instance id.users lives in — Require uses it
// to reject a deactivated account on its very next request, not just once
// its access token expires. Pass nil to skip that check (used by tests that
// don't migrate the id schema; production always passes the real pool).
func New(jwksURL, issuer string, pool *pgxpool.Pool) *Verifier {
	return &Verifier{jwksURL: jwksURL, issuer: issuer, pool: pool}
}

// active reports whether userID is still allowed to authenticate: true if
// the id schema isn't present at all (test databases that don't migrate it),
// false if the user is missing or deactivated, true otherwise.
func (v *Verifier) active(ctx context.Context, userID string) bool {
	if v.pool == nil {
		return true
	}
	var isActive bool
	err := v.pool.QueryRow(ctx, `SELECT is_active FROM id.users WHERE id = $1`, userID).Scan(&isActive)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42P01" { // undefined_table
		return true
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	return err == nil && isActive
}

func (v *Verifier) keys(force bool) (map[string]*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !force && v.kk != nil && time.Now().Before(v.ttl) {
		return v.kk, nil
	}
	resp, err := http.Get(v.jwksURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("jwks: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	kk, err := id.ParseJWKS(body)
	if err != nil {
		return nil, err
	}
	v.kk, v.ttl = kk, time.Now().Add(5*time.Minute)
	return kk, nil
}

func (v *Verifier) Require(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			httpx.Error(w, 401, "bearer token required")
			return
		}
		keys, err := v.keys(false)
		if err != nil {
			httpx.Error(w, 503, "idp unreachable")
			return
		}
		claims, err := id.VerifyJWT(token, keys, v.issuer, "vault", time.Now())
		if err != nil && strings.Contains(err.Error(), "unknown kid") {
			if keys, err2 := v.keys(true); err2 == nil {
				claims, err = id.VerifyJWT(token, keys, v.issuer, "vault", time.Now())
			}
		}
		if err != nil {
			httpx.Error(w, 401, "invalid token")
			return
		}
		if !v.active(r.Context(), claims.Sub) {
			httpx.Error(w, 401, "invalid token")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userKey, claims.Sub)))
	}
}
