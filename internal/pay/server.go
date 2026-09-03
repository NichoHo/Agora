package pay

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"agora/internal/authn"
	"agora/internal/httpx"
)

type Server struct {
	ledger        *Ledger
	auth          *authn.Verifier
	internalToken string
}

// depositCap: demo money, so self-service top-ups are capped per call.
const depositCap = 100_000

func NewServer(pool *pgxpool.Pool, auth *authn.Verifier, internalToken string) http.Handler {
	s := &Server{ledger: &Ledger{Pool: pool}, auth: auth, internalToken: internalToken}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /internal/escrow/fund", s.internal(s.handleFund))
	mux.HandleFunc("POST /internal/escrow/release", s.internal(s.handleRelease))
	mux.HandleFunc("POST /internal/escrow/refund", s.internal(s.handleRefund))
	mux.HandleFunc("GET /wallet", s.auth.Require(s.handleWallet))
	mux.HandleFunc("POST /deposits", s.auth.Require(s.handleDeposit))
	return mux
}

// internal guards service-to-service money endpoints: only holders of the
// shared PAY_INTERNAL_TOKEN (i.e. market) may move escrow.
func (s *Server) internal(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-Internal-Token")
		if s.internalToken == "" ||
			subtle.ConstantTimeCompare([]byte(got), []byte(s.internalToken)) != 1 {
			httpx.Error(w, 403, "internal endpoint")
			return
		}
		next(w, r)
	}
}

func writeLedgerErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInsufficientFunds):
		httpx.JSON(w, 402, map[string]string{"error": "insufficient_funds"})
	case errors.Is(err, ErrAlreadySettled):
		httpx.JSON(w, 409, map[string]string{"error": "already_settled"})
	default:
		httpx.Error(w, 500, "ledger error")
	}
}

func (s *Server) handleFund(w http.ResponseWriter, r *http.Request) {
	var in struct {
		OrderID     string `json:"order_id"`
		BuyerID     string `json:"buyer_id"`
		AmountMinor int64  `json:"amount_minor"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.OrderID == "" || in.BuyerID == "" {
		httpx.Error(w, 400, "bad request")
		return
	}
	t, err := s.ledger.FundEscrow(r.Context(), in.OrderID, in.BuyerID, in.AmountMinor)
	if err != nil {
		writeLedgerErr(w, err)
		return
	}
	httpx.JSON(w, 200, t)
}

func (s *Server) handleRelease(w http.ResponseWriter, r *http.Request) {
	var in struct {
		OrderID  string `json:"order_id"`
		SellerID string `json:"seller_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.OrderID == "" || in.SellerID == "" {
		httpx.Error(w, 400, "bad request")
		return
	}
	t, err := s.ledger.ReleaseEscrow(r.Context(), in.OrderID, in.SellerID)
	if err != nil {
		writeLedgerErr(w, err)
		return
	}
	httpx.JSON(w, 200, t)
}

func (s *Server) handleRefund(w http.ResponseWriter, r *http.Request) {
	var in struct {
		OrderID string `json:"order_id"`
		BuyerID string `json:"buyer_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.OrderID == "" || in.BuyerID == "" {
		httpx.Error(w, 400, "bad request")
		return
	}
	t, err := s.ledger.RefundEscrow(r.Context(), in.OrderID, in.BuyerID)
	if err != nil {
		writeLedgerErr(w, err)
		return
	}
	httpx.JSON(w, 200, t)
}

func (s *Server) handleWallet(w http.ResponseWriter, r *http.Request) {
	wallet, err := s.ledger.Wallet(r.Context(), authn.UserID(r.Context()))
	if err != nil {
		httpx.Error(w, 500, "wallet")
		return
	}
	httpx.JSON(w, 200, wallet)
}

func (s *Server) handleDeposit(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IdempotencyKey string `json:"idempotency_key"`
		AmountMinor    int64  `json:"amount_minor"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.IdempotencyKey == "" {
		httpx.Error(w, 400, "bad request")
		return
	}
	if in.AmountMinor <= 0 || in.AmountMinor > depositCap {
		httpx.Error(w, 400, "amount must be 1..100000")
		return
	}
	user := authn.UserID(r.Context())
	// namespace the key by user so one user cannot replay another's deposit
	t, err := s.ledger.Deposit(r.Context(), "deposit:"+user+":"+in.IdempotencyKey, user, in.AmountMinor)
	if err != nil {
		writeLedgerErr(w, err)
		return
	}
	httpx.JSON(w, 200, t)
}
