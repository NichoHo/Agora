// Package authn: Bearer-JWT middleware shared by resource services.
// Verifies RS256 tokens against the IdP's JWKS (cached, refetched on
// unknown kid so key rotation propagates).
package authn

import (
	"context"
	"crypto/rsa"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

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
	mu      sync.Mutex
	ttl     time.Time
	kk      map[string]*rsa.PublicKey
}

func New(jwksURL, issuer string) *Verifier {
	return &Verifier{jwksURL: jwksURL, issuer: issuer}
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
		next(w, r.WithContext(context.WithValue(r.Context(), userKey, claims.Sub)))
	}
}
