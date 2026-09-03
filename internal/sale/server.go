package sale

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/singleflight"

	"agora/internal/authn"
	"agora/internal/httpx"
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
	mux.HandleFunc("POST /drops/{id}/reserve", s.auth.Require(s.handleReserve))
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
