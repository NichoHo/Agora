package pay

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"agora/internal/authn"
	"agora/internal/httpx"
)

type Server struct {
	ledger        *Ledger
	auth          *authn.Verifier
	internalToken string
	sw            *SwitchClient // nil disables card auth: escrow funds from wallet balance only, pre-Phase-3 behavior
}

// depositCap: demo money, so self-service top-ups are capped per call.
const depositCap = 100_000

// authUnknownWait bounds how long a fund request waits for switch's own
// StatusProbeJob (runs every 30s, ignores payments under 10s old) to resolve
// an AUTH_UNKNOWN response. See docs/adr/0003. A var, not a const, so
// switchclient_test.go can shorten it instead of a test waiting 90s.
var authUnknownWait = 90 * time.Second

func NewServer(pool *pgxpool.Pool, auth *authn.Verifier, internalToken string, sw *SwitchClient) http.Handler {
	s := &Server{ledger: &Ledger{Pool: pool}, auth: auth, internalToken: internalToken, sw: sw}
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
//
// handleFund's amount_minor is trusted from the request body once past this
// gate, not independently re-verified against any order record pay owns
// (pay has no orders/listings table of its own). Signing (order_id,
// amount_minor) with a second shared secret was considered and rejected: it
// would use the same market-only secret this gate already checks, so it
// raises the bar against nothing a leaked PAY_INTERNAL_TOKEN doesn't already
// defeat. The actual boundary is that only market holds this token, and
// market itself always computes amount server-side from listings.price_minor
// / sale_drops.price_minor (see internal/market/orders.go). Revisit if a
// third caller ever needs this token, or if pay grows its own copy of order
// data to check against.

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

// declinedStates: switch outcomes that mean "the card said no", mapped onto
// FundEscrow's own ErrInsufficientFunds so market's existing PayClient (which
// only knows 200/402/409) needs no change. See docs/adr/0003.
var declinedStates = map[string]bool{
	"AUTH_DECLINED": true, "RISK_DECLINED": true, "AUTHENTICATION_FAILED": true,
}

var errCardAuthIndeterminate = errors.New("card authorization still unresolved")

// authorizeCard runs before FundEscrow's ledger transfer, gating it on a
// successful card charge when switch is configured. FundEscrow itself is
// untouched: this is the only new step in the fund path (Section 6 of
// AGORA_SPEC.md: extend pay, don't rewrite its ledger).
func (s *Server) authorizeCard(ctx context.Context, orderID string, amountMinor int64) error {
	if s.sw == nil {
		return nil
	}
	state, err := s.sw.AuthorizeAndResolve(ctx, orderID, amountMinor, "USD", authUnknownWait)
	if err != nil {
		return err
	}
	if declinedStates[state] {
		return ErrInsufficientFunds
	}
	if state == "AUTH_UNKNOWN" {
		return errCardAuthIndeterminate
	}
	return nil // AUTHORIZED or CAPTURED
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
	if err := s.authorizeCard(r.Context(), in.OrderID, in.AmountMinor); err != nil {
		if errors.Is(err, ErrInsufficientFunds) {
			writeLedgerErr(w, err)
			return
		}
		httpx.Error(w, 502, "card authorization unresolved; retry")
		return
	}
	if s.sw != nil {
		// A successful card charge is the funding source, not a pre-existing
		// wallet balance: credit it as a deposit before FundEscrow's own
		// (untouched) buyer-must-have-balance transfer runs. See ADR 0003.
		if _, err := s.ledger.Deposit(r.Context(), "card:"+in.OrderID, in.BuyerID, in.AmountMinor); err != nil {
			httpx.Error(w, 502, "card deposit failed")
			return
		}
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
