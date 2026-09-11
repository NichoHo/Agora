package id

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"agora/internal/httpx"
	"agora/internal/ratelimit"
	"agora/internal/tracing"
)

const (
	sessionCookie = "vault_sid"
	sessionTTL    = 30 * 24 * time.Hour
	codeTTL       = 5 * time.Minute
	tokenTTL      = 15 * time.Minute
)

type Server struct {
	pool    *pgxpool.Pool
	signer  *Signer
	issuer  string
	webURL  string
	totpKey [32]byte
}

func NewServer(pool *pgxpool.Pool, signer *Signer, issuer, webURL, totpKeyPassphrase string) http.Handler {
	s := &Server{pool: pool, signer: signer, issuer: issuer, webURL: webURL,
		totpKey: sha256.Sum256([]byte(totpKeyPassphrase))}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, 200, map[string]bool{"ok": true})
	})
	// Tighter limit on the security-sensitive routes a credential-stuffing
	// or brute-force attempt would hit: login, registration, MFA verify
	// (wired in mfaRoutes), password change. 2 req/s per IP with a burst of
	// 10 covers a real user mistyping a code a few times in a row. Each route
	// gets its own limiter instance (its own budget): a burst spent retrying
	// a TOTP code must not also lock the user out of the unrelated /login
	// they'd need to try again.
	mux.HandleFunc("POST /register", ratelimit.New(2, 10).Wrap(s.handleRegister))
	mux.HandleFunc("POST /login", ratelimit.New(2, 10).Wrap(s.handleLogin))
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.HandleFunc("GET /me", s.handleMe)
	mux.HandleFunc("GET /authorize", s.handleAuthorize)
	mux.HandleFunc("POST /consent", s.handleConsent)
	mux.HandleFunc("POST /token", s.handleToken)
	mux.HandleFunc("GET /.well-known/openid-configuration", s.handleDiscovery)
	mux.HandleFunc("GET /.well-known/jwks.json", s.handleJWKS)
	mux.HandleFunc("POST /password", ratelimit.New(2, 10).Wrap(s.handleChangePassword))
	mux.HandleFunc("POST /deactivate", s.handleDeactivate)
	s.mfaRoutes(mux)
	return mux
}

func (s *Server) audit(ctx context.Context, actor, action string, meta map[string]any) {
	b, _ := json.Marshal(meta)
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO id.audit_events (actor, action, meta) VALUES ($1, $2, $3)`, actor, action, b); err != nil {
		slog.Error("audit write failed", "action", action, "err", err)
	}
}

// emit writes to id's outbox (relayed to the id.events stream the same way
// every other service's outbox is). Separate from audit: audit is id's own
// permanent record and fires on far more actions than risk needs to hear
// about; emit is only for the two signals AGORA_SPEC.md section 8.3 names.
func (s *Server) emit(ctx context.Context, topic string, payload map[string]any) {
	if tp := tracing.Traceparent(ctx); tp != "" {
		payload["_trace"] = tp
	}
	b, _ := json.Marshal(payload)
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO id.outbox (topic, payload) VALUES ($1, $2)`, topic, b); err != nil {
		slog.Error("outbox write failed", "topic", topic, "err", err)
	}
}

func randToken(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Server) newSession(w http.ResponseWriter, r *http.Request, userID string, pendingMFA bool) error {
	sid := randToken(32)
	if _, err := s.pool.Exec(r.Context(),
		`INSERT INTO id.sessions (id, user_id, expires_at, pending_mfa) VALUES ($1, $2, $3, $4)`,
		sid, userID, time.Now().Add(sessionTTL), pendingMFA); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: sid, Path: "/", HttpOnly: true,
		// Secure whenever the browser-facing origin is HTTPS (prod); stays off
		// for http://localhost dev so the cookie is still sent.
		Secure:   strings.HasPrefix(s.webURL, "https://"),
		SameSite: http.SameSiteLaxMode, MaxAge: int(sessionTTL.Seconds()),
	})
	return nil
}

type sessionUser struct {
	ID     string `json:"id"`
	Email  string `json:"email"`
	Handle string `json:"handle"`
}

func (s *Server) sessionUser(r *http.Request) (sessionUser, bool) {
	var u sessionUser
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return u, false
	}
	err = s.pool.QueryRow(r.Context(),
		`SELECT u.id, u.email, u.handle FROM id.sessions s JOIN id.users u ON u.id = s.user_id
		 WHERE s.id = $1 AND s.expires_at > now() AND NOT s.pending_mfa`, c.Value).
		Scan(&u.ID, &u.Email, &u.Handle)
	return u, err == nil
}

var handleRe = regexp.MustCompile(`^[a-z0-9_]{3,30}$`)

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var in struct{ Email, Password, Handle string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpx.Error(w, 400, "bad json")
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	in.Handle = strings.ToLower(strings.TrimSpace(in.Handle))
	switch {
	case !strings.Contains(in.Email, "@") || len(in.Email) > 254:
		httpx.Error(w, 400, "invalid email")
		return
	case len(in.Password) < 8:
		httpx.Error(w, 400, "password must be at least 8 characters")
		return
	case !handleRe.MatchString(in.Handle):
		httpx.Error(w, 400, "handle must be 3-30 chars of a-z, 0-9, _")
		return
	}
	hash, err := HashPassword(in.Password)
	if err != nil {
		httpx.Error(w, 500, "hash failed")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	defer tx.Rollback(r.Context())
	var userID string
	err = tx.QueryRow(r.Context(),
		`INSERT INTO id.users (email, handle) VALUES ($1, $2) RETURNING id`,
		in.Email, in.Handle).Scan(&userID)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		httpx.Error(w, 409, "email or handle already taken")
		return
	}
	if err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	if _, err := tx.Exec(r.Context(),
		`INSERT INTO id.credentials (user_id, password_hash) VALUES ($1, $2)`, userID, hash); err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	if err := s.newSession(w, r, userID, false); err != nil {
		httpx.Error(w, 500, "session")
		return
	}
	s.audit(r.Context(), userID, "user.register", map[string]any{"email": in.Email})
	httpx.JSON(w, 201, sessionUser{ID: userID, Email: in.Email, Handle: in.Handle})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct{ Email, Password string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpx.Error(w, 400, "bad json")
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	var u sessionUser
	var hash string
	var active bool
	err := s.pool.QueryRow(r.Context(),
		`SELECT u.id, u.email, u.handle, u.is_active, c.password_hash
		 FROM id.users u JOIN id.credentials c ON c.user_id = u.id
		 WHERE u.email = $1`, in.Email).Scan(&u.ID, &u.Email, &u.Handle, &active, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		// burn time so absent accounts cost the same as wrong passwords
		HashPassword(in.Password)
		s.audit(r.Context(), in.Email, "login.fail", map[string]any{"reason": "no user"})
		httpx.Error(w, 401, "invalid credentials")
		return
	}
	if err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	if ok, _ := VerifyPassword(in.Password, hash); !ok {
		s.audit(r.Context(), u.ID, "login.fail", map[string]any{"reason": "bad password"})
		httpx.Error(w, 401, "invalid credentials")
		return
	}
	if !active {
		// same generic message as any other failed login: a deactivated
		// account shouldn't be distinguishable from a wrong password.
		s.audit(r.Context(), u.ID, "login.fail", map[string]any{"reason": "deactivated"})
		httpx.Error(w, 401, "invalid credentials")
		return
	}
	if s.mfaEnabled(r, u.ID) {
		// password accepted, but the session stays pending until the TOTP step
		if err := s.newSession(w, r, u.ID, true); err != nil {
			httpx.Error(w, 500, "session")
			return
		}
		s.audit(r.Context(), u.ID, "login.password_ok_mfa_pending", nil)
		httpx.JSON(w, 200, map[string]any{"mfa_required": true})
		return
	}
	if err := s.newSession(w, r, u.ID, false); err != nil {
		httpx.Error(w, 500, "session")
		return
	}
	s.audit(r.Context(), u.ID, "login.success", nil)
	httpx.JSON(w, 200, u)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.pool.Exec(r.Context(), `DELETE FROM id.sessions WHERE id = $1`, c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	w.WriteHeader(204)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u, ok := s.sessionUser(r)
	if !ok {
		httpx.Error(w, 401, "not signed in")
		return
	}
	httpx.JSON(w, 200, u)
}
