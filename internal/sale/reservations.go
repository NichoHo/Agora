package sale

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"agora/internal/authn"
	"agora/internal/httpx"
	"agora/internal/tracing"
)

const defaultReservationTTL = 5 * time.Minute

var (
	ErrDropNotOpen = errors.New("drop is not open")
	ErrNotAdmitted = errors.New("not admitted; join the queue first")
	ErrPerUserCap  = errors.New("per_user_cap_reached")
	ErrSoldOut     = errors.New("sold_out")
)

type Reservation struct {
	ID             string    `json:"id"`
	DropID         string    `json:"drop_id"`
	UserID         string    `json:"user_id"`
	ListingID      *string   `json:"listing_id"`
	Shard          *int      `json:"-"`
	Units          int       `json:"units"`
	State          string    `json:"state"`
	IdempotencyKey string    `json:"-"`
	ReservedAt     time.Time `json:"reserved_at"`
	ExpiresAt      time.Time `json:"expires_at"`
	OrderID        *string   `json:"order_id"`
}

const reservationCols = `id, drop_id, user_id, listing_id, shard, units, state, idempotency_key, reserved_at, expires_at, order_id`

func scanReservation(row pgx.Row) (Reservation, error) {
	var res Reservation
	err := row.Scan(&res.ID, &res.DropID, &res.UserID, &res.ListingID, &res.Shard, &res.Units,
		&res.State, &res.IdempotencyKey, &res.ReservedAt, &res.ExpiresAt, &res.OrderID)
	return res, err
}

func outboxTx(ctx context.Context, tx pgx.Tx, topic string, payload map[string]any) error {
	if tp := tracing.Traceparent(ctx); tp != "" {
		payload["_trace"] = tp
	}
	b, _ := json.Marshal(payload)
	_, err := tx.Exec(ctx, `INSERT INTO sale.outbox (topic, payload) VALUES ($1, $2)`, topic, b)
	return err
}

func bearerToken(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

// Reserve holds one unit for userID: Redis claim + durable Postgres row.
// Deliberately does not call market here — that HTTP hop belongs on the
// checkout step, not this one, so the admission-gated hot path stays fast
// (spec 7.1's bounded-latency goal). See docs/adr/0002. This is the layer
// the invariant tests exercise directly, the same way pay's Ledger is tested
// beneath its HTTP handlers.
func (s *Server) Reserve(ctx context.Context, dropID, userID, idempotencyKey string) (Reservation, error) {
	key := userID + ":" + idempotencyKey

	d, err := s.getDrop(ctx, dropID)
	if err != nil {
		return Reservation{}, err
	}
	if d.Status != "open" {
		return Reservation{}, ErrDropNotOpen
	}
	admitted, err := s.rdb.rdb.SIsMember(ctx, admittedKey(dropID), userID).Result()
	if err != nil {
		return Reservation{}, err
	}
	if !admitted {
		return Reservation{}, ErrNotAdmitted
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Reservation{}, err
	}
	defer tx.Rollback(ctx)

	// Serializes only this user's own concurrent attempts on this drop, so
	// the per-user cap check below is race-free without blocking other
	// buyers (they hash to different lock ids and proceed fully in parallel).
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, dropID+":"+userID); err != nil {
		return Reservation{}, err
	}

	if existing, err := scanReservation(tx.QueryRow(ctx,
		`SELECT `+reservationCols+` FROM sale.reservations WHERE drop_id = $1 AND idempotency_key = $2`,
		dropID, key)); err == nil {
		return existing, nil // idempotent replay: no new Redis claim
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Reservation{}, err
	}

	var held int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM sale.reservations WHERE drop_id = $1 AND user_id = $2 AND state IN ('reserved','payment_pending','confirmed')`,
		dropID, userID).Scan(&held); err != nil {
		return Reservation{}, err
	}
	if held+1 > d.UnitsPerUser {
		return Reservation{}, ErrPerUserCap
	}

	reservationID := newUUID()
	expiresAt := time.Now().Add(defaultReservationTTL)
	listingID, err := s.rdb.Claim(ctx, dropID, reservationID, d.ShardCount, int64(defaultReservationTTL.Seconds()))
	if err != nil {
		return Reservation{}, err
	}
	if listingID == "" {
		return Reservation{}, ErrSoldOut
	}
	shard, err := s.unitShard(ctx, tx, dropID, listingID)
	if err != nil {
		return Reservation{}, err
	}

	res, err := scanReservation(tx.QueryRow(ctx,
		`INSERT INTO sale.reservations (id, drop_id, user_id, listing_id, shard, idempotency_key, expires_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING `+reservationCols,
		reservationID, dropID, userID, listingID, shard, key, expiresAt))
	if err != nil {
		return Reservation{}, err
	}
	if err := outboxTx(ctx, tx, "sale.reservation_created",
		map[string]any{"reservation_id": res.ID, "drop_id": dropID, "user_id": userID}); err != nil {
		return Reservation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Reservation{}, err
	}
	return res, nil
}

func (s *Server) handleReserve(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IdempotencyKey string `json:"idempotency_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.IdempotencyKey == "" {
		httpx.Error(w, 400, "idempotency_key is required")
		return
	}
	res, err := s.Reserve(r.Context(), r.PathValue("id"), authn.UserID(r.Context()), in.IdempotencyKey)
	switch {
	case err == nil:
		httpx.JSON(w, 201, res)
	case errors.Is(err, ErrDropNotOpen):
		httpx.Error(w, 409, err.Error())
	case errors.Is(err, ErrNotAdmitted):
		httpx.Error(w, 403, err.Error())
	case errors.Is(err, ErrPerUserCap), errors.Is(err, ErrSoldOut):
		httpx.JSON(w, 409, map[string]string{"error": err.Error()})
	case errors.Is(err, pgx.ErrNoRows):
		httpx.Error(w, 404, "drop not found")
	default:
		httpx.Error(w, 500, "reserve failed")
	}
}

func (s *Server) unitShard(ctx context.Context, tx pgx.Tx, dropID, listingID string) (int, error) {
	var shard int
	err := tx.QueryRow(ctx, `SELECT shard FROM sale.drop_units WHERE drop_id = $1 AND listing_id = $2`,
		dropID, listingID).Scan(&shard)
	return shard, err
}

func (s *Server) getReservation(ctx context.Context, id string) (Reservation, error) {
	return scanReservation(s.pool.QueryRow(ctx, `SELECT `+reservationCols+` FROM sale.reservations WHERE id = $1`, id))
}

// handleCheckout turns a held reservation into a real market order, using the
// caller's own bearer token (market has no service-to-service order API).
// Retries once with a fresh unit if market reports the first one already
// taken (spec 7.3/7.4 boundary; see the ADR for why that can happen).
func (s *Server) handleCheckout(w http.ResponseWriter, r *http.Request) {
	user := authn.UserID(r.Context())
	res, err := s.getReservation(r.Context(), r.PathValue("id"))
	if err != nil || res.UserID != user {
		httpx.Error(w, 404, "not found")
		return
	}
	if res.State == "payment_pending" && res.OrderID != nil {
		httpx.JSON(w, 200, res) // idempotent replay
		return
	}
	if res.State != "reserved" {
		httpx.Error(w, 409, "reservation is not in a checkout-able state")
		return
	}
	if time.Now().After(res.ExpiresAt) {
		httpx.Error(w, 409, "reservation expired")
		return
	}
	bearer := bearerToken(r)
	listingID := *res.ListingID
	orderID, err := s.market.CreateOrder(r.Context(), bearer, listingID)
	if errors.Is(err, ErrListingUnavailable) {
		// market hasn't caught up with a prior TTL release yet; hand this
		// reservation a fresh unit and try once more.
		d, derr := s.getDrop(r.Context(), res.DropID)
		if derr != nil {
			httpx.Error(w, 500, "db")
			return
		}
		fresh, cerr := s.rdb.Claim(r.Context(), res.DropID, res.ID+":retry", d.ShardCount, int64(time.Until(res.ExpiresAt).Seconds()))
		if cerr != nil || fresh == "" {
			httpx.Error(w, 503, "try again shortly")
			return
		}
		orderID, err = s.market.CreateOrder(r.Context(), bearer, fresh)
		if err == nil {
			listingID = fresh
		}
	}
	if err != nil {
		httpx.Error(w, 502, "market unavailable")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	defer tx.Rollback(r.Context())
	shard, err := s.unitShard(r.Context(), tx, res.DropID, listingID)
	if err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	updated, err := scanReservation(tx.QueryRow(r.Context(),
		`UPDATE sale.reservations SET state = 'payment_pending', listing_id = $2, shard = $3, order_id = $4
		 WHERE id = $1 AND state = 'reserved' RETURNING `+reservationCols,
		res.ID, listingID, shard, orderID))
	if err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	// Best-effort: a drop is time-pressured, so pay immediately instead of
	// making the buyer click again. Failure (insufficient funds, an
	// AUTH_UNKNOWN card still resolving, pay unreachable) leaves the
	// reservation exactly where it already is, payment_pending, which is
	// the correct state for "not yet settled" either way; the sweeper and
	// the market-events consumer (confirmReservation/releaseByOrder) are
	// what actually resolve it, not this response.
	if err := s.market.PayOrder(r.Context(), bearer, orderID); err != nil {
		slog.Warn("immediate pay failed, reservation stays payment_pending", "reservation", updated.ID, "err", err)
	}
	httpx.JSON(w, 200, updated)
}

// releaseReservation transitions a reserved/payment_pending row to newState
// and returns its unit to Redis, guarded so a race with confirmReservation
// resolves exactly once (mirrors market's completeOrder/sweeper pattern).
func (s *Server) releaseReservation(ctx context.Context, id, fromState, newState, topic string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	res, err := scanReservation(tx.QueryRow(ctx,
		`UPDATE sale.reservations SET state = $3 WHERE id = $1 AND state = $2 RETURNING `+reservationCols,
		id, fromState, newState))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // already resolved by a confirm/expire race; not an error
	}
	if err != nil {
		return err
	}
	if err := outboxTx(ctx, tx, topic, map[string]any{"reservation_id": res.ID, "drop_id": res.DropID}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if res.ListingID != nil && res.Shard != nil {
		return s.rdb.Release(ctx, res.DropID, *res.Shard, *res.ListingID)
	}
	return nil
}

// confirmReservation marks a reservation confirmed once market reports the
// order funded (via the Kafka consumer). The unit stays sold: no Redis release.
func (s *Server) confirmReservation(ctx context.Context, orderID string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE sale.reservations SET state = 'confirmed' WHERE order_id = $1 AND state = 'payment_pending'`, orderID)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	return nil
}

// releaseByOrder handles market reporting an order cancelled/refunded before
// sale's own TTL fired (e.g. the buyer backed out mid-checkout).
func (s *Server) releaseByOrder(ctx context.Context, orderID string) error {
	var id string
	err := s.pool.QueryRow(ctx, `SELECT id FROM sale.reservations WHERE order_id = $1 AND state = 'payment_pending'`, orderID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.releaseReservation(ctx, id, "payment_pending", "released", "sale.reservation_released")
}
