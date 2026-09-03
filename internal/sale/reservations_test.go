package sale

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/redis/go-redis/v9"

	"agora/internal/pg"
	"agora/migrations"
)

// testServer wires a Server against a real throwaway Postgres schema and a
// real Redis logical DB (matching this repo's existing style of testing
// invariants against real infra, not mocks; see pay/ledger_test.go). Skips
// if the test infra isn't configured, same as every other suite here.
func testServer(t *testing.T) *Server {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	redisAddr := os.Getenv("TEST_REDIS_ADDR")
	if dbURL == "" || redisAddr == "" {
		t.Skip("TEST_DATABASE_URL and TEST_REDIS_ADDR not set")
	}
	ctx := context.Background()
	pool, err := pg.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS sale CASCADE`); err != nil {
		t.Fatal(err)
	}
	sub, _ := fs.Sub(migrations.FS, "sale")
	if err := pg.Migrate(ctx, pool, "sale", sub); err != nil {
		t.Fatal(err)
	}

	rdb := &Redis{rdb: redis.NewClient(&redis.Options{Addr: redisAddr, DB: 1})}
	if err := rdb.rdb.FlushDB(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rdb.Close() })

	return &Server{
		pool:        pool,
		rdb:         rdb,
		market:      &MarketClient{BaseURL: "http://unused.invalid"},
		tokenSecret: []byte("test-secret"),
	}
}

// seedDrop creates an open drop with totalUnits fake units, bypassing
// handleOpenDrop's market minting (out of scope for these Redis/Postgres
// invariant tests; the market handoff is exercised separately in
// checkout_test.go-style HTTP/e2e tests, not here).
func seedDrop(t *testing.T, s *Server, totalUnits, unitsPerUser, shardCount int) Drop {
	t.Helper()
	ctx := context.Background()
	d, err := scanDrop(s.pool.QueryRow(ctx,
		`INSERT INTO sale.drops (seller_id, title, starts_at, total_units, price_minor, units_per_user, shard_count, status)
		 VALUES ($1,'seed',now(),$2,1000,$3,$4,'open') RETURNING `+dropCols,
		newUUID(), totalUnits, unitsPerUser, shardCount))
	if err != nil {
		t.Fatal(err)
	}
	unitsByShard := make(map[int][]string, shardCount)
	for i := 0; i < totalUnits; i++ {
		listingID := newUUID()
		shard := i % shardCount
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO sale.drop_units (drop_id, shard, listing_id) VALUES ($1,$2,$3)`, d.ID, shard, listingID); err != nil {
			t.Fatal(err)
		}
		unitsByShard[shard] = append(unitsByShard[shard], listingID)
	}
	if err := s.rdb.Seed(ctx, d.ID, unitsByShard); err != nil {
		t.Fatal(err)
	}
	return d
}

func admitAll(t *testing.T, rdb *Redis, dropID string, users []string) {
	t.Helper()
	ids := make([]any, len(users))
	for i, u := range users {
		ids[i] = u
	}
	if err := rdb.rdb.SAdd(context.Background(), admittedKey(dropID), ids...).Err(); err != nil {
		t.Fatal(err)
	}
}

// TestNoOversell is spec 10.2's headline invariant and Phase 2's exit
// criterion: 50,000 concurrent attempts against 1,000 units must yield
// exactly 1,000 successes. Run with -race.
func TestNoOversell(t *testing.T) {
	s := testServer(t)
	d := seedDrop(t, s, 1000, 1, 8)

	const attempts = 50_000
	users := make([]string, attempts)
	for i := range users {
		users[i] = newUUID()
	}
	admitAll(t, s.rdb, d.ID, users)

	var successes int64
	var wg sync.WaitGroup
	for _, u := range users {
		wg.Add(1)
		go func(user string) {
			defer wg.Done()
			_, err := s.Reserve(context.Background(), d.ID, user, "k")
			switch {
			case err == nil:
				atomic.AddInt64(&successes, 1)
			case errors.Is(err, ErrSoldOut):
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(u)
	}
	wg.Wait()

	if successes != 1000 {
		t.Fatalf("got %d successful reservations, want exactly 1000", successes)
	}
	remaining, err := s.rdb.Remaining(context.Background(), d.ID, d.ShardCount)
	if err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("redis remaining = %d, want 0", remaining)
	}
}

// TestConservation checks spec 10.2: confirmed + released + expired +
// still_reserved must equal the total decremented from Redis, across a mix
// of terminal states.
func TestConservation(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()
	d := seedDrop(t, s, 20, 20, 4) // one user can hold up to all 20, isolates this test from the per-user-cap invariant

	user := newUUID()
	admitAll(t, s.rdb, d.ID, []string{user})

	var reservations []Reservation
	for i := 0; i < 12; i++ {
		res, err := s.Reserve(ctx, d.ID, user, newUUID())
		if err != nil {
			t.Fatal(err)
		}
		reservations = append(reservations, res)
	}

	// 4 confirmed (simulate market reporting the order funded)
	for i := 0; i < 4; i++ {
		if _, err := s.pool.Exec(ctx, `UPDATE sale.reservations SET state='payment_pending', order_id=$2 WHERE id=$1`,
			reservations[i].ID, newUUID()); err != nil {
			t.Fatal(err)
		}
		var orderID string
		if err := s.pool.QueryRow(ctx, `SELECT order_id FROM sale.reservations WHERE id=$1`, reservations[i].ID).Scan(&orderID); err != nil {
			t.Fatal(err)
		}
		if err := s.confirmReservation(ctx, orderID); err != nil {
			t.Fatal(err)
		}
	}
	// 3 released
	for i := 4; i < 7; i++ {
		if err := s.releaseReservation(ctx, reservations[i].ID, "reserved", "released", "test.released"); err != nil {
			t.Fatal(err)
		}
	}
	// 2 expired
	for i := 7; i < 9; i++ {
		if err := s.releaseReservation(ctx, reservations[i].ID, "reserved", "expired", "test.expired"); err != nil {
			t.Fatal(err)
		}
	}
	// remaining 3 stay 'reserved'

	var confirmed, released, expired, stillReserved int
	rows := []struct {
		state string
		dest  *int
	}{{"confirmed", &confirmed}, {"released", &released}, {"expired", &expired}, {"reserved", &stillReserved}}
	for _, r := range rows {
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM sale.reservations WHERE drop_id=$1 AND state=$2`, d.ID, r.state).Scan(r.dest); err != nil {
			t.Fatal(err)
		}
	}
	// "Total decremented from Redis" (spec 10.2) is cumulative: a released or
	// expired reservation gave its unit back, but the SPOP that claimed it in
	// the first place still happened. That cumulative count is exactly the
	// row count (INSERT is the only thing that ever creates a reservation).
	var totalClaims int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM sale.reservations WHERE drop_id=$1`, d.ID).Scan(&totalClaims); err != nil {
		t.Fatal(err)
	}
	sum := confirmed + released + expired + stillReserved
	if sum != totalClaims {
		t.Fatalf("confirmed(%d)+released(%d)+expired(%d)+reserved(%d) = %d, want %d (total reservation rows = total redis claims)",
			confirmed, released, expired, stillReserved, sum, totalClaims)
	}
	// And released/expired units must actually be back in the pool.
	remaining, err := s.rdb.Remaining(ctx, d.ID, d.ShardCount)
	if err != nil {
		t.Fatal(err)
	}
	wantRemaining := d.TotalUnits - confirmed - stillReserved
	if remaining != wantRemaining {
		t.Fatalf("redis remaining = %d, want %d (total(%d) - confirmed(%d) - reserved(%d): released/expired units must be back in the pool)",
			remaining, wantRemaining, d.TotalUnits, confirmed, stillReserved)
	}
}

// TestIdempotency: one reservation key replayed 20 times concurrently must
// produce exactly one reservation row and consume exactly one unit.
func TestIdempotency(t *testing.T) {
	s := testServer(t)
	d := seedDrop(t, s, 5, 20, 2)
	user := newUUID()
	admitAll(t, s.rdb, d.ID, []string{user})

	const replays = 20
	ids := make([]string, replays)
	var wg sync.WaitGroup
	for i := 0; i < replays; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := s.Reserve(context.Background(), d.ID, user, "same-key")
			if err != nil {
				t.Errorf("replay %d: %v", i, err)
				return
			}
			ids[i] = res.ID
		}(i)
	}
	wg.Wait()

	first := ids[0]
	for i, id := range ids {
		if id != first {
			t.Fatalf("replay %d got reservation id %q, want %q (all replays must return the same row)", i, id, first)
		}
	}
	var count int
	if err := s.pool.QueryRow(context.Background(), `SELECT count(*) FROM sale.reservations WHERE drop_id=$1`, d.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("got %d reservation rows, want exactly 1", count)
	}
	remaining, err := s.rdb.Remaining(context.Background(), d.ID, d.ShardCount)
	if err != nil {
		t.Fatal(err)
	}
	if remaining != 4 {
		t.Fatalf("redis remaining = %d, want 4 (5 units - 1 consumed)", remaining)
	}
}

// TestPerUserCap: a user racing themselves across many connections must
// never hold more than units_per_user reservations.
func TestPerUserCap(t *testing.T) {
	s := testServer(t)
	d := seedDrop(t, s, 100, 3, 4) // plenty of stock; the cap, not sold-out, must be what blocks
	user := newUUID()
	admitAll(t, s.rdb, d.ID, []string{user})

	const attempts = 30
	var successes int64
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.Reserve(context.Background(), d.ID, user, newUUID())
			switch {
			case err == nil:
				atomic.AddInt64(&successes, 1)
			case errors.Is(err, ErrPerUserCap):
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if successes != 3 {
		t.Fatalf("got %d successful reservations for a units_per_user=3 drop, want exactly 3", successes)
	}
}

// TestTTLExpiryRace: an expired reservation must be released exactly once,
// even when the sweeper and a manual confirm race for the same row.
func TestTTLExpiryRace(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()
	d := seedDrop(t, s, 1, 1, 1)
	user := newUUID()
	admitAll(t, s.rdb, d.ID, []string{user})

	res, err := s.Reserve(ctx, d.ID, user, "k")
	if err != nil {
		t.Fatal(err)
	}
	// Move it to payment_pending the way checkout really does (order_id set,
	// same expires_at carried over), then force the TTL into the past. This
	// is the actual race in production: the sweeper and the Kafka consumer's
	// confirmReservation both act on a payment_pending row, guarded by the
	// same `WHERE state = 'payment_pending'`. TestConservation already
	// covers plain 'reserved' expiry; this test is specifically about that
	// guard, so it must exercise the real confirmReservation path, not
	// releaseReservation (which is for expire/release, not confirm, and
	// unconditionally frees the unit — using it here would race-condition
	// the test itself, not the code under test).
	orderID := newUUID()
	if _, err := s.pool.Exec(ctx,
		`UPDATE sale.reservations SET state='payment_pending', order_id=$2, expires_at = now() - interval '1 minute' WHERE id=$1`,
		res.ID, orderID); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		s.SweepExpired(ctx)
	}()
	go func() {
		defer wg.Done()
		s.confirmReservation(ctx, orderID)
	}()
	wg.Wait()

	var state string
	if err := s.pool.QueryRow(ctx, `SELECT state FROM sale.reservations WHERE id=$1`, res.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "confirmed" && state != "expired" {
		t.Fatalf("state = %q, want exactly one of confirmed/expired to have won", state)
	}
	remaining, err := s.rdb.Remaining(ctx, d.ID, d.ShardCount)
	if err != nil {
		t.Fatal(err)
	}
	wantRemaining := 0
	if state == "expired" {
		wantRemaining = 1 // released back to the shard
	}
	if remaining != wantRemaining {
		t.Fatalf("redis remaining = %d for final state %q, want %d (unit released exactly once, not zero or twice)",
			remaining, state, wantRemaining)
	}
}
