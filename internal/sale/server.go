package sale

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/singleflight"

	"agora/internal/authn"
	"agora/internal/httpx"
	"agora/internal/ratelimit"
)

type Server struct {
	pool        *pgxpool.Pool
	auth        *authn.Verifier
	rdb         *Redis
	market      *MarketClient
	dropSF      singleflight.Group
	tokenSecret []byte
	mux         *http.ServeMux
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func NewServer(pool *pgxpool.Pool, auth *authn.Verifier, rdb *Redis, market *MarketClient, tokenSecret []byte) *Server {
	s := &Server{pool: pool, auth: auth, rdb: rdb, market: market, tokenSecret: tokenSecret}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /drops", s.auth.Require(s.handleCreateDrop))
	mux.HandleFunc("POST /drops/{id}/open", s.auth.Require(s.handleOpenDrop))
	mux.HandleFunc("GET /drops/{id}", s.handleGetDrop)
	mux.HandleFunc("POST /drops/{id}/queue/join", s.auth.Require(s.handleJoinQueue))
	mux.HandleFunc("GET /drops/{id}/queue/stream", s.auth.Require(s.handleQueueStream))
	// AGORA_SPEC.md section 12: the queue/admission system already bounds
	// aggregate drop concurrency; these are the backstop per-IP and per-user
	// limits against one client hammering retries, not a substitute for it.
	reserveByIP := ratelimit.New(2, 10)
	reserveByUser := ratelimit.New(2, 10)
	mux.HandleFunc("POST /drops/{id}/reserve", reserveByIP.Wrap(s.auth.Require(func(w http.ResponseWriter, r *http.Request) {
		if !reserveByUser.Allow(authn.UserID(r.Context())) {
			httpx.Error(w, 429, "too many requests")
			return
		}
		s.handleReserve(w, r)
	})))
	mux.HandleFunc("GET /reservations/{id}", s.auth.Require(s.handleGetReservation))
	mux.HandleFunc("POST /reservations/{id}/checkout", s.auth.Require(s.handleCheckout))
	s.mux = mux
	return s
}

func (s *Server) handleGetReservation(w http.ResponseWriter, r *http.Request) {
	res, err := s.getReservation(r.Context(), r.PathValue("id"))
	if err != nil || res.UserID != authn.UserID(r.Context()) {
		httpx.Error(w, 404, "not found")
		return
	}
	httpx.JSON(w, 200, res)
}
