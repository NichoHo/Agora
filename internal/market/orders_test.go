package market

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"agora/internal/authn"
	"agora/internal/pay"
	"agora/internal/pg"
	"agora/migrations"
)

func migratePay(ctx context.Context, pool *pgxpool.Pool) error {
	sub, _ := fs.Sub(migrations.FS, "pay")
	return pg.Migrate(ctx, pool, "pay", sub)
}

// orderEnv extends the Phase 1 test env with a real pay service (same test DB,
// pay schema) wired behind the market server.
type orderEnv struct {
	*env
	ledger *pay.Ledger
	srv    *Server
}

func orderTestEnv(t *testing.T) *orderEnv {
	t.Helper()
	e := testEnv(t) // market schema + jwks + market server (no pay yet)

	// pay schema on the same pool
	pool := e.srv.pool
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS pay CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err := migratePay(ctx, pool); err != nil {
		t.Fatal(err)
	}
	ledger := &pay.Ledger{Pool: pool}

	payAuth := authn.New(e.jwksURL, testIssuer)
	paySrv := httptest.NewServer(pay.NewServer(pool, payAuth, "test-internal", nil))
	t.Cleanup(paySrv.Close)
	e.srv.pay = &PayClient{BaseURL: paySrv.URL, Token: "test-internal"}

	return &orderEnv{env: e, ledger: ledger, srv: e.srv}
}

func createListing(t *testing.T, e *env, sellerToken string, price int64) string {
	t.Helper()
	resp := e.do(t, "POST", "/listings", sellerToken, map[string]any{
		"title": "test item", "description": "d", "price_minor": price})
	if resp.StatusCode != 201 {
		t.Fatalf("create listing: %d", resp.StatusCode)
	}
	var l Listing
	json.NewDecoder(resp.Body).Decode(&l)
	return l.ID
}

func decodeOrder(t *testing.T, resp *http.Response) Order {
	t.Helper()
	var o Order
	if err := json.NewDecoder(resp.Body).Decode(&o); err != nil {
		t.Fatal(err)
	}
	return o
}

func listingStatus(t *testing.T, e *orderEnv, id string) string {
	t.Helper()
	var s string
	if err := e.srv.pool.QueryRow(context.Background(),
		`SELECT status FROM market.listings WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func payBalance(t *testing.T, e *orderEnv, ownerType string, ownerID *string) int64 {
	t.Helper()
	acct, err := e.ledger.AccountFor(context.Background(), ownerType, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	var b int64
	if err := e.ledger.Pool.QueryRow(context.Background(),
		`SELECT balance FROM pay.accounts WHERE id = $1`, acct).Scan(&b); err != nil {
		t.Fatal(err)
	}
	return b
}

func str(s string) *string { return &s }

func TestOrderHappyPath(t *testing.T) {
	e := orderTestEnv(t)
	ctx := context.Background()
	sellerTok, buyerTok := e.token(t, alice), e.token(t, bob)
	listingID := createListing(t, e.env, sellerTok, 10_000)
	if _, err := e.ledger.Deposit(ctx, "d", bob, 30_000); err != nil {
		t.Fatal(err)
	}

	// create order → listing reserved
	resp := e.do(t, "POST", "/orders", buyerTok, map[string]any{"listing_id": listingID})
	if resp.StatusCode != 201 {
		t.Fatalf("create order: %d", resp.StatusCode)
	}
	o := decodeOrder(t, resp)
	if o.Status != "pending_payment" || listingStatus(t, e, listingID) != "reserved" {
		t.Fatalf("after create: order=%s listing=%s", o.Status, listingStatus(t, e, listingID))
	}

	// pay → funded, listing sold, buyer debited
	resp = e.do(t, "POST", "/orders/"+o.ID+"/pay", buyerTok, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("pay: %d", resp.StatusCode)
	}
	if o = decodeOrder(t, resp); o.Status != "funded" {
		t.Fatalf("after pay: %s", o.Status)
	}
	if got := listingStatus(t, e, listingID); got != "sold" {
		t.Fatalf("listing after pay: %s", got)
	}
	if got := payBalance(t, e, "user", str(bob)); got != 20_000 {
		t.Fatalf("buyer balance: want 20000, got %d", got)
	}

	// buyer cannot ship; seller ships
	if resp := e.do(t, "POST", "/orders/"+o.ID+"/ship", buyerTok, nil); resp.StatusCode != 403 {
		t.Fatalf("buyer ship: want 403, got %d", resp.StatusCode)
	}
	if resp := e.do(t, "POST", "/orders/"+o.ID+"/ship", sellerTok, nil); resp.StatusCode != 200 {
		t.Fatalf("ship: %d", resp.StatusCode)
	}

	// seller cannot confirm; buyer confirms → completed, money settled
	if resp := e.do(t, "POST", "/orders/"+o.ID+"/confirm", sellerTok, nil); resp.StatusCode != 403 {
		t.Fatalf("seller confirm: want 403, got %d", resp.StatusCode)
	}
	resp = e.do(t, "POST", "/orders/"+o.ID+"/confirm", buyerTok, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("confirm: %d", resp.StatusCode)
	}
	if o = decodeOrder(t, resp); o.Status != "completed" {
		t.Fatalf("after confirm: %s", o.Status)
	}
	if got := payBalance(t, e, "user", str(alice)); got != 9_000 {
		t.Fatalf("seller: want 9000 (90%%), got %d", got)
	}
	if got := payBalance(t, e, "platform", nil); got != 1_000 {
		t.Fatalf("platform: want 1000, got %d", got)
	}
	if got := payBalance(t, e, "escrow", nil); got != 0 {
		t.Fatalf("escrow should zero out, got %d", got)
	}
}

func TestConcurrentBuy(t *testing.T) {
	e := orderTestEnv(t)
	sellerTok := e.token(t, alice)
	listingID := createListing(t, e.env, sellerTok, 5_000)
	buyer2 := "cccccccc-3333-3333-3333-333333333333"

	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i, tok := range []string{e.token(t, bob), e.token(t, buyer2)} {
		wg.Add(1)
		go func(i int, tok string) {
			defer wg.Done()
			resp := e.do(t, "POST", "/orders", tok, map[string]any{"listing_id": listingID})
			codes[i] = resp.StatusCode
		}(i, tok)
	}
	wg.Wait()
	if !(codes[0] == 201 && codes[1] == 409 || codes[0] == 409 && codes[1] == 201) {
		t.Fatalf("want exactly one 201 and one 409, got %v", codes)
	}
}

func TestPayInsufficientFunds(t *testing.T) {
	e := orderTestEnv(t)
	listingID := createListing(t, e.env, e.token(t, alice), 99_999)
	buyerTok := e.token(t, bob) // bob has no deposit here

	resp := e.do(t, "POST", "/orders", buyerTok, map[string]any{"listing_id": listingID})
	o := decodeOrder(t, resp)
	if resp := e.do(t, "POST", "/orders/"+o.ID+"/pay", buyerTok, nil); resp.StatusCode != 402 {
		t.Fatalf("broke buyer: want 402, got %d", resp.StatusCode)
	}
	var status string
	e.srv.pool.QueryRow(context.Background(),
		`SELECT status FROM market.orders WHERE id = $1`, o.ID).Scan(&status)
	if status != "pending_payment" {
		t.Fatalf("order should stay pending, got %s", status)
	}
}

func TestTimerVsManualConfirm(t *testing.T) {
	e := orderTestEnv(t)
	ctx := context.Background()
	sellerTok, buyerTok := e.token(t, alice), e.token(t, bob)
	listingID := createListing(t, e.env, sellerTok, 8_000)
	e.ledger.Deposit(ctx, "d", bob, 8_000)

	resp := e.do(t, "POST", "/orders", buyerTok, map[string]any{"listing_id": listingID})
	o := decodeOrder(t, resp)
	e.do(t, "POST", "/orders/"+o.ID+"/pay", buyerTok, nil)
	e.do(t, "POST", "/orders/"+o.ID+"/ship", sellerTok, nil)

	// backdate shipped_at so the sweeper considers it due
	if _, err := e.srv.pool.Exec(ctx,
		`UPDATE market.orders SET shipped_at = now() - interval '100 hours' WHERE id = $1`, o.ID); err != nil {
		t.Fatal(err)
	}

	// timer and manual confirm race
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); e.srv.SweepAutoRelease(ctx, 72*time.Hour) }()
	go func() { defer wg.Done(); e.do(t, "POST", "/orders/"+o.ID+"/confirm", buyerTok, nil) }()
	wg.Wait()

	// exactly one release transfer
	var releases int
	e.ledger.Pool.QueryRow(ctx,
		`SELECT count(*) FROM pay.transfers WHERE kind = 'escrow_release' AND reference = $1`,
		"order:"+o.ID).Scan(&releases)
	if releases != 1 {
		t.Fatalf("timer-vs-manual: want exactly 1 release transfer, got %d", releases)
	}
	var status string
	e.srv.pool.QueryRow(ctx, `SELECT status FROM market.orders WHERE id = $1`, o.ID).Scan(&status)
	if status != "completed" {
		t.Fatalf("order should be completed, got %s", status)
	}
	if got := payBalance(t, e, "user", str(alice)); got != 7_200 {
		t.Fatalf("seller paid exactly once: want 7200, got %d", got)
	}
}

func TestReservationExpiry(t *testing.T) {
	e := orderTestEnv(t)
	ctx := context.Background()
	listingID := createListing(t, e.env, e.token(t, alice), 3_000)
	resp := e.do(t, "POST", "/orders", e.token(t, bob), map[string]any{"listing_id": listingID})
	o := decodeOrder(t, resp)

	if _, err := e.srv.pool.Exec(ctx,
		`UPDATE market.reservations SET expires_at = now() - interval '1 minute' WHERE order_id = $1`, o.ID); err != nil {
		t.Fatal(err)
	}
	n, err := e.srv.SweepReservations(ctx)
	if err != nil || n != 1 {
		t.Fatalf("sweep: n=%d err=%v", n, err)
	}
	var status string
	e.srv.pool.QueryRow(ctx, `SELECT status FROM market.orders WHERE id = $1`, o.ID).Scan(&status)
	if status != "cancelled" {
		t.Fatalf("order: want cancelled, got %s", status)
	}
	if got := listingStatus(t, e, listingID); got != "active" {
		t.Fatalf("listing should be active again, got %s", got)
	}
}

func TestCancelFundedRefunds(t *testing.T) {
	e := orderTestEnv(t)
	ctx := context.Background()
	sellerTok, buyerTok := e.token(t, alice), e.token(t, bob)
	listingID := createListing(t, e.env, sellerTok, 6_000)
	e.ledger.Deposit(ctx, "d", bob, 6_000)

	resp := e.do(t, "POST", "/orders", buyerTok, map[string]any{"listing_id": listingID})
	o := decodeOrder(t, resp)
	e.do(t, "POST", "/orders/"+o.ID+"/pay", buyerTok, nil)

	// buyer may not cancel a funded order; seller may
	if resp := e.do(t, "POST", "/orders/"+o.ID+"/cancel", buyerTok, nil); resp.StatusCode != 403 {
		t.Fatalf("buyer cancel funded: want 403, got %d", resp.StatusCode)
	}
	resp = e.do(t, "POST", "/orders/"+o.ID+"/cancel", sellerTok, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("seller cancel: %d", resp.StatusCode)
	}
	if o = decodeOrder(t, resp); o.Status != "refunded" {
		t.Fatalf("want refunded, got %s", o.Status)
	}
	if got := payBalance(t, e, "user", str(bob)); got != 6_000 {
		t.Fatalf("buyer should be whole, got %d", got)
	}
	if got := payBalance(t, e, "escrow", nil); got != 0 {
		t.Fatalf("escrow should zero out, got %d", got)
	}
	if got := listingStatus(t, e, listingID); got != "active" {
		t.Fatalf("listing should be active again, got %s", got)
	}
}

func TestStrangerCannotActOnOrder(t *testing.T) {
	e := orderTestEnv(t)
	listingID := createListing(t, e.env, e.token(t, alice), 1_000)
	resp := e.do(t, "POST", "/orders", e.token(t, bob), map[string]any{"listing_id": listingID})
	o := decodeOrder(t, resp)

	stranger := e.token(t, "dddddddd-4444-4444-4444-444444444444")
	for _, action := range []string{"", "/pay", "/ship", "/confirm", "/cancel"} {
		method := "POST"
		if action == "" {
			method = "GET"
		}
		resp := e.do(t, method, "/orders/"+o.ID+action, stranger, nil)
		if resp.StatusCode != 404 {
			t.Fatalf("stranger %s %s: want 404, got %d", method, action, resp.StatusCode)
		}
	}
}
