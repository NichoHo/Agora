// Package ratelimit: a per-client-IP token bucket for the small set of
// sensitive endpoints a security checklist names as needing tighter limits
// than the rest of the API (login, registration, TOTP/recovery verify,
// password change, drop reservation) — not a general-purpose gateway
// rate limiter.
package ratelimit

import (
	"net"
	"net/http"
	"strings"
	"sync"

	"golang.org/x/time/rate"

	"agora/internal/httpx"
)

type Limiter struct {
	mu       sync.Mutex
	visitors map[string]*rate.Limiter
	rps      rate.Limit
	burst    int
}

// New builds a limiter allowing rps requests/second per IP, with burst
// allowed immediately.
//
// ponytail: the visitor map never evicts entries — fine at this project's
// demo scale (bounded by distinct IPs that actually hit these routes, and
// the process restarts on every deploy); add a TTL sweep if this ever runs
// for weeks under real traffic from many distinct IPs.
func New(rps float64, burst int) *Limiter {
	return &Limiter{visitors: map[string]*rate.Limiter{}, rps: rate.Limit(rps), burst: burst}
}

func (l *Limiter) allow(key string) bool {
	l.mu.Lock()
	v, ok := l.visitors[key]
	if !ok {
		v = rate.NewLimiter(l.rps, l.burst)
		l.visitors[key] = v
	}
	l.mu.Unlock()
	return v.Allow()
}

func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		return strings.TrimSpace(strings.Split(fwd, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Wrap rejects with 429 once the caller's IP is over the limit, otherwise
// calls next.
func (l *Limiter) Wrap(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !l.allow(clientIP(r)) {
			httpx.Error(w, 429, "too many requests")
			return
		}
		next(w, r)
	}
}
