package pay

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agora/internal/authn"
	"agora/internal/id"
)

const internalToken = "test-internal-token"

type httpEnv struct {
	ts     *httptest.Server
	signer *id.Signer
	ledger *Ledger
}

func testHTTP(t *testing.T, sw *SwitchClient) *httpEnv {
	t.Helper()
	l := testLedger(t)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer := &id.Signer{Kid: "pay-test", Key: key}
	jwksDoc, _ := id.JWKS(map[string]*rsa.PublicKey{signer.Kid: &key.PublicKey})
	jwksSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(jwksDoc)
	}))
	t.Cleanup(jwksSrv.Close)

	auth := authn.New(jwksSrv.URL, "http://issuer.test")
	ts := httptest.NewServer(NewServer(l.Pool, auth, internalToken, sw))
	t.Cleanup(ts.Close)
	return &httpEnv{ts: ts, signer: signer, ledger: l}
}

func (e *httpEnv) token(t *testing.T, sub string) string {
	t.Helper()
	now := time.Now()
	tok, err := e.signer.Sign(id.Claims{Iss: "http://issuer.test", Sub: sub, Aud: "vault",
		Exp: now.Add(time.Hour).Unix(), Iat: now.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func (e *httpEnv) post(t *testing.T, path, bearer, internal string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", e.ts.URL+path, strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if internal != "" {
		req.Header.Set("X-Internal-Token", internal)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestInternalEndpointsRequireToken(t *testing.T) {
	e := testHTTP(t, nil)
	body := map[string]any{"order_id": "o1", "buyer_id": buyer, "amount_minor": 100}

	if resp := e.post(t, "/internal/escrow/fund", "", "", body); resp.StatusCode != 403 {
		t.Fatalf("no token: want 403, got %d", resp.StatusCode)
	}
	if resp := e.post(t, "/internal/escrow/fund", "", "wrong-token", body); resp.StatusCode != 403 {
		t.Fatalf("wrong token: want 403, got %d", resp.StatusCode)
	}
	// a valid USER JWT must not open internal endpoints either
	if resp := e.post(t, "/internal/escrow/fund", e.token(t, buyer), "", body); resp.StatusCode != 403 {
		t.Fatalf("user jwt on internal: want 403, got %d", resp.StatusCode)
	}
}

func TestDepositAndWalletOverHTTP(t *testing.T) {
	e := testHTTP(t, nil)
	tok := e.token(t, buyer)

	if resp := e.post(t, "/deposits", tok, "", map[string]any{
		"idempotency_key": "k1", "amount_minor": 200_000}); resp.StatusCode != 400 {
		t.Fatalf("over cap: want 400, got %d", resp.StatusCode)
	}
	if resp := e.post(t, "/deposits", tok, "", map[string]any{
		"idempotency_key": "k1", "amount_minor": 50_000}); resp.StatusCode != 200 {
		t.Fatalf("deposit: want 200, got %d", resp.StatusCode)
	}

	req, _ := http.NewRequest("GET", e.ts.URL+"/wallet", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var w Wallet
	json.NewDecoder(resp.Body).Decode(&w)
	if w.BalanceMinor != 50_000 || len(w.Entries) != 1 || w.Entries[0].Kind != "deposit" {
		t.Fatalf("wallet: %+v", w)
	}

	// no token → 401
	if resp := e.post(t, "/deposits", "", "", map[string]any{
		"idempotency_key": "k2", "amount_minor": 100}); resp.StatusCode != 401 {
		t.Fatalf("unauthenticated deposit: want 401, got %d", resp.StatusCode)
	}
}

func TestEscrowFlowOverHTTP(t *testing.T) {
	e := testHTTP(t, nil)
	ctx := context.Background()
	if _, err := e.ledger.Deposit(ctx, "d", buyer, 10_000); err != nil {
		t.Fatal(err)
	}

	if resp := e.post(t, "/internal/escrow/fund", "", internalToken, map[string]any{
		"order_id": "o1", "buyer_id": buyer, "amount_minor": 10_000}); resp.StatusCode != 200 {
		t.Fatalf("fund: want 200, got %d", resp.StatusCode)
	}
	// second fund attempt for more than remains → 402
	if resp := e.post(t, "/internal/escrow/fund", "", internalToken, map[string]any{
		"order_id": "o2", "buyer_id": buyer, "amount_minor": 1}); resp.StatusCode != 402 {
		t.Fatalf("broke fund: want 402, got %d", resp.StatusCode)
	}
	if resp := e.post(t, "/internal/escrow/release", "", internalToken, map[string]any{
		"order_id": "o1", "seller_id": seller}); resp.StatusCode != 200 {
		t.Fatalf("release: want 200, got %d", resp.StatusCode)
	}
	if resp := e.post(t, "/internal/escrow/refund", "", internalToken, map[string]any{
		"order_id": "o1", "buyer_id": buyer}); resp.StatusCode != 409 {
		t.Fatalf("refund after release: want 409, got %d", resp.StatusCode)
	}
	checkBooks(t, e.ledger)
}

// TestEscrowFundsFromCardWithZeroWalletBalance covers the gap ADR 0003's
// addendum fixes: a buyer who never deposited anything must still be able
// to fund escrow once switch reports the card charge captured.
func TestEscrowFundsFromCardWithZeroWalletBalance(t *testing.T) {
	sw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201)
		switch r.URL.Path {
		case "/v1/tokens":
			json.NewEncoder(w).Encode(map[string]string{"token": "tok_test"})
		case "/v1/payments":
			json.NewEncoder(w).Encode(map[string]string{"id": "pay_test", "state": "CAPTURED"})
		default:
			t.Fatalf("unexpected switch call: %s", r.URL.Path)
		}
	}))
	t.Cleanup(sw.Close)

	e := testHTTP(t, &SwitchClient{BaseURL: sw.URL, APIKey: "k"})
	if resp := e.post(t, "/internal/escrow/fund", "", internalToken, map[string]any{
		"order_id": "o1", "buyer_id": buyer, "amount_minor": 1_500}); resp.StatusCode != 200 {
		t.Fatalf("fund with zero wallet balance but captured card: want 200, got %d", resp.StatusCode)
	}
	checkBooks(t, e.ledger)
}
