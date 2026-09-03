package pay

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"testing"

	"agora/internal/pg"
	"agora/migrations"
)

const (
	buyer  = "aaaaaaaa-1111-1111-1111-111111111111"
	seller = "bbbbbbbb-2222-2222-2222-222222222222"
)

func testLedger(t *testing.T) *Ledger {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pg.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS pay CASCADE`); err != nil {
		t.Fatal(err)
	}
	sub, _ := fs.Sub(migrations.FS, "pay")
	if err := pg.Migrate(ctx, pool, "pay", sub); err != nil {
		t.Fatal(err)
	}
	return &Ledger{Pool: pool}
}

// checkBooks asserts the core double-entry invariants:
// per-transfer entries sum to zero, global entries sum to zero (value is
// conserved), and every account balance equals the sum of its entries.
func checkBooks(t *testing.T, l *Ledger) {
	t.Helper()
	ctx := context.Background()
	var badTransfers int
	if err := l.Pool.QueryRow(ctx,
		`SELECT count(*) FROM (SELECT transfer_id FROM pay.entries GROUP BY transfer_id
		  HAVING sum(amount_minor) <> 0) x`).Scan(&badTransfers); err != nil {
		t.Fatal(err)
	}
	if badTransfers != 0 {
		t.Fatalf("%d transfers with non-zero entry sums", badTransfers)
	}
	var global int64
	if err := l.Pool.QueryRow(ctx,
		`SELECT coalesce(sum(amount_minor), 0) FROM pay.entries`).Scan(&global); err != nil {
		t.Fatal(err)
	}
	if global != 0 {
		t.Fatalf("value not conserved: global entry sum = %d", global)
	}
	var drift int
	if err := l.Pool.QueryRow(ctx,
		`SELECT count(*) FROM pay.accounts a
		 WHERE a.balance <> coalesce((SELECT sum(e.amount_minor) FROM pay.entries e WHERE e.account_id = a.id), 0)`).
		Scan(&drift); err != nil {
		t.Fatal(err)
	}
	if drift != 0 {
		t.Fatalf("%d accounts where balance <> sum(entries)", drift)
	}
}

func balance(t *testing.T, l *Ledger, ownerType string, ownerID *string) int64 {
	t.Helper()
	acct, err := l.AccountFor(context.Background(), ownerType, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	var b int64
	if err := l.Pool.QueryRow(context.Background(),
		`SELECT balance FROM pay.accounts WHERE id = $1`, acct).Scan(&b); err != nil {
		t.Fatal(err)
	}
	return b
}

func str(s string) *string { return &s }

func TestDoubleEntryAndConservation(t *testing.T) {
	l := testLedger(t)
	ctx := context.Background()

	if _, err := l.Deposit(ctx, "dep1", buyer, 50_000); err != nil {
		t.Fatal(err)
	}
	if _, err := l.FundEscrow(ctx, "order-1", buyer, 30_000); err != nil {
		t.Fatal(err)
	}
	if _, err := l.ReleaseEscrow(ctx, "order-1", seller); err != nil {
		t.Fatal(err)
	}
	checkBooks(t, l)

	if got := balance(t, l, "user", str(buyer)); got != 20_000 {
		t.Fatalf("buyer balance: want 20000, got %d", got)
	}
	if got := balance(t, l, "user", str(seller)); got != 27_000 {
		t.Fatalf("seller balance: want 27000 (90%%), got %d", got)
	}
	if got := balance(t, l, "platform", nil); got != 3_000 {
		t.Fatalf("platform balance: want 3000 (10%% fee), got %d", got)
	}
	if got := balance(t, l, "escrow", nil); got != 0 {
		t.Fatalf("escrow should zero out, got %d", got)
	}
}

func TestIdempotency(t *testing.T) {
	l := testLedger(t)
	ctx := context.Background()
	t1, err := l.Deposit(ctx, "dup-key", buyer, 1_000)
	if err != nil {
		t.Fatal(err)
	}
	t2, err := l.Deposit(ctx, "dup-key", buyer, 1_000)
	if err != nil {
		t.Fatal(err)
	}
	if t1.ID != t2.ID {
		t.Fatalf("replay created a second transfer: %s vs %s", t1.ID, t2.ID)
	}
	if got := balance(t, l, "user", str(buyer)); got != 1_000 {
		t.Fatalf("balance after replay: want 1000, got %d", got)
	}
}

func TestInsufficientFunds(t *testing.T) {
	l := testLedger(t)
	ctx := context.Background()
	if _, err := l.Deposit(ctx, "d", buyer, 100); err != nil {
		t.Fatal(err)
	}
	_, err := l.FundEscrow(ctx, "order-x", buyer, 200)
	if !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("want ErrInsufficientFunds, got %v", err)
	}
	var transfers int
	l.Pool.QueryRow(ctx, `SELECT count(*) FROM pay.transfers WHERE kind = 'escrow_fund'`).Scan(&transfers)
	if transfers != 0 {
		t.Fatalf("failed fund left %d transfer rows", transfers)
	}
	checkBooks(t, l)
}

func TestConcurrentDoubleSpend(t *testing.T) {
	l := testLedger(t)
	ctx := context.Background()
	if _, err := l.Deposit(ctx, "d", buyer, 10_000); err != nil {
		t.Fatal(err)
	}
	const n = 20
	var wg sync.WaitGroup
	results := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, results[i] = l.FundEscrow(ctx, fmt.Sprintf("order-%d", i), buyer, 1_000)
		}(i)
	}
	wg.Wait()
	succeeded := 0
	for _, err := range results {
		if err == nil {
			succeeded++
		} else if !errors.Is(err, ErrInsufficientFunds) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if succeeded != 10 {
		t.Fatalf("want exactly 10 successful funds, got %d", succeeded)
	}
	if got := balance(t, l, "user", str(buyer)); got != 0 {
		t.Fatalf("buyer should end at exactly 0, got %d", got)
	}
	checkBooks(t, l)
}

func TestEscrowZeroesOutOnRefund(t *testing.T) {
	l := testLedger(t)
	ctx := context.Background()
	l.Deposit(ctx, "d", buyer, 5_000)
	if _, err := l.FundEscrow(ctx, "order-r", buyer, 5_000); err != nil {
		t.Fatal(err)
	}
	if _, err := l.RefundEscrow(ctx, "order-r", buyer); err != nil {
		t.Fatal(err)
	}
	if got := balance(t, l, "user", str(buyer)); got != 5_000 {
		t.Fatalf("buyer should be whole after refund, got %d", got)
	}
	if got := balance(t, l, "escrow", nil); got != 0 {
		t.Fatalf("escrow should zero out, got %d", got)
	}
	checkBooks(t, l)
}

func TestReleaseRefundExclusive(t *testing.T) {
	l := testLedger(t)
	ctx := context.Background()
	l.Deposit(ctx, "d", buyer, 5_000)
	l.FundEscrow(ctx, "order-e", buyer, 5_000)
	if _, err := l.ReleaseEscrow(ctx, "order-e", seller); err != nil {
		t.Fatal(err)
	}
	if _, err := l.RefundEscrow(ctx, "order-e", buyer); !errors.Is(err, ErrAlreadySettled) {
		t.Fatalf("refund after release: want ErrAlreadySettled, got %v", err)
	}

	// and concurrently on a fresh order: exactly one of release/refund wins
	l.Deposit(ctx, "d2", buyer, 5_000)
	l.FundEscrow(ctx, "order-c", buyer, 5_000)
	var wg sync.WaitGroup
	var relErr, refErr error
	wg.Add(2)
	go func() { defer wg.Done(); _, relErr = l.ReleaseEscrow(ctx, "order-c", seller) }()
	go func() { defer wg.Done(); _, refErr = l.RefundEscrow(ctx, "order-c", buyer) }()
	wg.Wait()
	if (relErr == nil) == (refErr == nil) {
		t.Fatalf("exactly one of release/refund must win: release=%v refund=%v", relErr, refErr)
	}
	if got := balance(t, l, "escrow", nil); got != 0 {
		t.Fatalf("escrow should zero out either way, got %d", got)
	}
	checkBooks(t, l)
}

func TestReleaseIdempotentReplay(t *testing.T) {
	l := testLedger(t)
	ctx := context.Background()
	l.Deposit(ctx, "d", buyer, 5_000)
	l.FundEscrow(ctx, "order-i", buyer, 5_000)
	t1, err := l.ReleaseEscrow(ctx, "order-i", seller)
	if err != nil {
		t.Fatal(err)
	}
	t2, err := l.ReleaseEscrow(ctx, "order-i", seller)
	if err != nil {
		t.Fatal(err)
	}
	if t1.ID != t2.ID {
		t.Fatal("second release created a new transfer")
	}
	if got := balance(t, l, "user", str(seller)); got != 4_500 {
		t.Fatalf("seller paid twice? want 4500, got %d", got)
	}
	checkBooks(t, l)
}
