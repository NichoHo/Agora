package pay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSwitch serves the three switch endpoints AuthorizeAndResolve needs:
// tokenize, authorize, and get-payment. authorizeState is what the first
// authorize call returns; pollStates is what each subsequent
// GET /v1/payments/{id} returns in order, holding on the last entry once
// exhausted (mirrors a real acquirer that stays AUTH_UNKNOWN until it settles).
func fakeSwitch(t *testing.T, authorizeState string, pollStates []string) (*httptest.Server, *int32) {
	t.Helper()
	var polls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/v1/tokens":
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(map[string]string{"token": "tok_1"})
		case r.Method == "POST" && r.URL.Path == "/v1/payments":
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(authResult{ID: "pay_1", State: authorizeState})
		case r.Method == "GET" && r.URL.Path == "/v1/payments/pay_1":
			i := atomic.AddInt32(&polls, 1) - 1
			state := pollStates[len(pollStates)-1]
			if int(i) < len(pollStates) {
				state = pollStates[i]
			}
			json.NewEncoder(w).Encode(authResult{ID: "pay_1", State: state})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &polls
}

// TestAuthorizeAndResolve_ResolvesFromAuthUnknown covers AGORA_SPEC.md
// section 10.3's "force AUTH_UNKNOWN from switch" chaos case for the half
// that resolves: switch's real StatusProbeJob (simulated here by the second
// poll response) eventually settles the payment, and AuthorizeAndResolve
// must surface that final state rather than staying stuck on AUTH_UNKNOWN.
func TestAuthorizeAndResolve_ResolvesFromAuthUnknown(t *testing.T) {
	srv, polls := fakeSwitch(t, "AUTH_UNKNOWN", []string{"APPROVED"})
	c := &SwitchClient{BaseURL: srv.URL, APIKey: "k"}
	state, err := c.AuthorizeAndResolve(t.Context(), "order-1", 5000, "USD", 10*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != "APPROVED" {
		t.Fatalf("want APPROVED, got %s", state)
	}
	if got := atomic.LoadInt32(polls); got != 1 {
		t.Fatalf("want exactly 1 status poll, got %d", got)
	}
}

// TestAuthorizeAndResolve_GivesUpAfterMaxWait covers the other half: an
// acquirer response that never arrives. The reservation must end up neither
// silently approved nor silently declined — AuthorizeAndResolve reports
// AUTH_UNKNOWN with no error, its caller's signal to leave the order/
// reservation in payment_indeterminate rather than fund escrow or release
// the held unit.
func TestAuthorizeAndResolve_GivesUpAfterMaxWait(t *testing.T) {
	srv, polls := fakeSwitch(t, "AUTH_UNKNOWN", []string{"AUTH_UNKNOWN"})
	c := &SwitchClient{BaseURL: srv.URL, APIKey: "k"}
	state, err := c.AuthorizeAndResolve(t.Context(), "order-2", 5000, "USD", 2*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != "AUTH_UNKNOWN" {
		t.Fatalf("want AUTH_UNKNOWN (still unresolved), got %s", state)
	}
	if got := atomic.LoadInt32(polls); got != 1 {
		t.Fatalf("want exactly 1 status poll before giving up, got %d", got)
	}
}

// TestAuthorizeCard_IndeterminateBlocksFunding proves the actual integration
// point pay uses (authorizeCard, gating handleFund's ledger call): an
// unresolved AUTH_UNKNOWN must produce an error distinct from a decline, so
// handleFund's caller (market) neither funds escrow nor treats the order as
// declined — no double charge, no leaked inventory, per section 10.3.
func TestAuthorizeCard_IndeterminateBlocksFunding(t *testing.T) {
	saved := authUnknownWait
	authUnknownWait = 2 * time.Second
	t.Cleanup(func() { authUnknownWait = saved })

	srv, _ := fakeSwitch(t, "AUTH_UNKNOWN", []string{"AUTH_UNKNOWN"})
	s := &Server{sw: &SwitchClient{BaseURL: srv.URL, APIKey: "k"}}
	err := s.authorizeCard(t.Context(), "order-3", 5000)
	if err != errCardAuthIndeterminate {
		t.Fatalf("want errCardAuthIndeterminate, got %v", err)
	}
}
