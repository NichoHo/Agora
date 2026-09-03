package market

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"agora/internal/authn"
	"agora/internal/httpx"
)

const reservationTTL = 15 * time.Minute

type Order struct {
	ID          string     `json:"id"`
	ListingID   string     `json:"listing_id"`
	BuyerID     string     `json:"buyer_id"`
	SellerID    string     `json:"seller_id"`
	PriceMinor  int64      `json:"price_minor"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	FundedAt    *time.Time `json:"funded_at"`
	ShippedAt   *time.Time `json:"shipped_at"`
	CompletedAt *time.Time `json:"completed_at"`
	// joined for the UI
	ListingTitle string `json:"listing_title"`
	ListingImage string `json:"listing_image"`
}

const orderCols = `o.id, o.listing_id, o.buyer_id, o.seller_id, o.price_minor, o.status,
	o.created_at, o.funded_at, o.shipped_at, o.completed_at, l.title, l.image_url`

func scanOrder(row pgx.Row) (Order, error) {
	var o Order
	err := row.Scan(&o.ID, &o.ListingID, &o.BuyerID, &o.SellerID, &o.PriceMinor, &o.Status,
		&o.CreatedAt, &o.FundedAt, &o.ShippedAt, &o.CompletedAt, &o.ListingTitle, &o.ListingImage)
	return o, err
}

func (s *Server) orderRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /orders", s.auth.Require(s.handleCreateOrder))
	mux.HandleFunc("GET /orders", s.auth.Require(s.handleListOrders))
	mux.HandleFunc("GET /orders/{id}", s.auth.Require(s.handleGetOrder))
	mux.HandleFunc("POST /orders/{id}/pay", s.auth.Require(s.handlePayOrder))
	mux.HandleFunc("POST /orders/{id}/ship", s.auth.Require(s.handleShipOrder))
	mux.HandleFunc("POST /orders/{id}/confirm", s.auth.Require(s.handleConfirmOrder))
	mux.HandleFunc("POST /orders/{id}/cancel", s.auth.Require(s.handleCancelOrder))
}

func outboxTx(ctx context.Context, tx pgx.Tx, topic string, payload map[string]any) error {
	b, _ := json.Marshal(payload)
	_, err := tx.Exec(ctx, `INSERT INTO market.outbox (topic, payload) VALUES ($1, $2)`, topic, b)
	return err
}

func (s *Server) handleCreateOrder(w http.ResponseWriter, r *http.Request) {
	buyer := authn.UserID(r.Context())
	var in struct {
		ListingID string `json:"listing_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.ListingID == "" {
		httpx.Error(w, 400, "bad request")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	defer tx.Rollback(r.Context())

	var sellerID, status string
	var price int64
	err = tx.QueryRow(r.Context(),
		`SELECT seller_id, status, price_minor FROM market.listings WHERE id = $1 FOR UPDATE`,
		in.ListingID).Scan(&sellerID, &status, &price)
	if err != nil {
		httpx.Error(w, 404, "listing not found")
		return
	}
	if status != "active" {
		httpx.Error(w, 409, "listing is not available")
		return
	}
	if sellerID == buyer {
		httpx.Error(w, 400, "you cannot buy your own listing")
		return
	}
	var orderID string
	if err := tx.QueryRow(r.Context(),
		`INSERT INTO market.orders (listing_id, buyer_id, seller_id, price_minor)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		in.ListingID, buyer, sellerID, price).Scan(&orderID); err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	if _, err := tx.Exec(r.Context(),
		`UPDATE market.listings SET status = 'reserved', updated_at = now() WHERE id = $1`,
		in.ListingID); err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	if _, err := tx.Exec(r.Context(),
		`INSERT INTO market.reservations (order_id, listing_id, expires_at) VALUES ($1, $2, $3)`,
		orderID, in.ListingID, time.Now().Add(reservationTTL)); err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	if err := outboxTx(r.Context(), tx, "order.created",
		map[string]any{"order_id": orderID, "listing_id": in.ListingID, "buyer_id": buyer}); err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	o, err := s.getOrder(r.Context(), orderID)
	if err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	httpx.JSON(w, 201, o)
}

func (s *Server) getOrder(ctx context.Context, id string) (Order, error) {
	return scanOrder(s.pool.QueryRow(ctx,
		`SELECT `+orderCols+` FROM market.orders o JOIN market.listings l ON l.id = o.listing_id
		 WHERE o.id = $1`, id))
}

func (s *Server) handleGetOrder(w http.ResponseWriter, r *http.Request) {
	o, err := s.getOrder(r.Context(), r.PathValue("id"))
	if err != nil {
		httpx.Error(w, 404, "not found")
		return
	}
	user := authn.UserID(r.Context())
	if user != o.BuyerID && user != o.SellerID {
		httpx.Error(w, 404, "not found") // don't leak order existence
		return
	}
	httpx.JSON(w, 200, o)
}

func (s *Server) handleListOrders(w http.ResponseWriter, r *http.Request) {
	user := authn.UserID(r.Context())
	col := "buyer_id"
	if r.URL.Query().Get("role") == "seller" {
		col = "seller_id"
	}
	rows, err := s.pool.Query(r.Context(),
		`SELECT `+orderCols+` FROM market.orders o JOIN market.listings l ON l.id = o.listing_id
		 WHERE o.`+col+` = $1 ORDER BY o.created_at DESC LIMIT 100`, user)
	if err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	defer rows.Close()
	orders := []Order{}
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			httpx.Error(w, 500, "db")
			return
		}
		orders = append(orders, o)
	}
	httpx.JSON(w, 200, orders)
}

// lockOrder loads an order FOR UPDATE inside tx.
func lockOrder(ctx context.Context, tx pgx.Tx, id string) (Order, error) {
	var o Order
	err := tx.QueryRow(ctx,
		`SELECT id, listing_id, buyer_id, seller_id, price_minor, status
		 FROM market.orders WHERE id = $1 FOR UPDATE`, id).
		Scan(&o.ID, &o.ListingID, &o.BuyerID, &o.SellerID, &o.PriceMinor, &o.Status)
	return o, err
}

func (s *Server) handlePayOrder(w http.ResponseWriter, r *http.Request) {
	user := authn.UserID(r.Context())
	orderID := r.PathValue("id")

	// check state + authorization before touching money
	o, err := s.getOrder(r.Context(), orderID)
	if err != nil || o.BuyerID != user {
		httpx.Error(w, 404, "not found")
		return
	}
	if o.Status != "pending_payment" {
		httpx.Error(w, 409, "order is not awaiting payment")
		return
	}
	// fund escrow first (idempotent at pay); then flip state
	if err := s.pay.Fund(r.Context(), o.ID, o.BuyerID, o.PriceMinor); err != nil {
		if errors.Is(err, ErrInsufficientFunds) {
			httpx.JSON(w, 402, map[string]string{"error": "insufficient_funds"})
			return
		}
		slog.Error("pay fund", "order", o.ID, "err", err)
		httpx.Error(w, 502, "payment service error")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	defer tx.Rollback(r.Context())
	tag, err := tx.Exec(r.Context(),
		`UPDATE market.orders SET status = 'funded', funded_at = now(), updated_at = now()
		 WHERE id = $1 AND status = 'pending_payment'`, o.ID)
	if err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	if tag.RowsAffected() == 1 {
		if _, err := tx.Exec(r.Context(),
			`UPDATE market.listings SET status = 'sold', updated_at = now() WHERE id = $1`, o.ListingID); err != nil {
			httpx.Error(w, 500, "db")
			return
		}
		if _, err := tx.Exec(r.Context(),
			`DELETE FROM market.reservations WHERE order_id = $1`, o.ID); err != nil {
			httpx.Error(w, 500, "db")
			return
		}
		if err := outboxTx(r.Context(), tx, "order.funded",
			map[string]any{"order_id": o.ID, "amount_minor": o.PriceMinor}); err != nil {
			httpx.Error(w, 500, "db")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	out, _ := s.getOrder(r.Context(), o.ID)
	httpx.JSON(w, 200, out)
}

func (s *Server) handleShipOrder(w http.ResponseWriter, r *http.Request) {
	user := authn.UserID(r.Context())
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	defer tx.Rollback(r.Context())
	o, err := lockOrder(r.Context(), tx, r.PathValue("id"))
	if err != nil || (user != o.BuyerID && user != o.SellerID) {
		httpx.Error(w, 404, "not found")
		return
	}
	if user != o.SellerID {
		httpx.Error(w, 403, "only the seller can ship")
		return
	}
	if o.Status != "funded" {
		httpx.Error(w, 409, "order is not funded")
		return
	}
	if _, err := tx.Exec(r.Context(),
		`UPDATE market.orders SET status = 'shipped', shipped_at = now(), updated_at = now()
		 WHERE id = $1`, o.ID); err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	if err := outboxTx(r.Context(), tx, "order.shipped", map[string]any{"order_id": o.ID}); err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	out, _ := s.getOrder(r.Context(), o.ID)
	httpx.JSON(w, 200, out)
}

// completeOrder releases escrow then marks the order completed. Called by the
// buyer's confirm AND the auto-release sweeper, possibly concurrently.
// Exactly-once holds because (a) pay's release is idempotent on key
// "release:<order>", and (b) the status flip is guarded by WHERE status='shipped'.
func (s *Server) completeOrder(ctx context.Context, o Order) error {
	if err := s.pay.Release(ctx, o.ID, o.SellerID); err != nil && !errors.Is(err, ErrAlreadySettled) {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx,
		`UPDATE market.orders SET status = 'completed', completed_at = now(), updated_at = now()
		 WHERE id = $1 AND status = 'shipped'`, o.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		if err := outboxTx(ctx, tx, "order.completed", map[string]any{"order_id": o.ID}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Server) handleConfirmOrder(w http.ResponseWriter, r *http.Request) {
	user := authn.UserID(r.Context())
	o, err := s.getOrder(r.Context(), r.PathValue("id"))
	if err != nil || (user != o.BuyerID && user != o.SellerID) {
		httpx.Error(w, 404, "not found")
		return
	}
	if user != o.BuyerID {
		httpx.Error(w, 403, "only the buyer can confirm receipt")
		return
	}
	if o.Status != "shipped" {
		httpx.Error(w, 409, "order is not shipped")
		return
	}
	if err := s.completeOrder(r.Context(), o); err != nil {
		slog.Error("complete order", "order", o.ID, "err", err)
		httpx.Error(w, 502, "payment service error")
		return
	}
	out, _ := s.getOrder(r.Context(), o.ID)
	httpx.JSON(w, 200, out)
}

func (s *Server) handleCancelOrder(w http.ResponseWriter, r *http.Request) {
	user := authn.UserID(r.Context())
	orderID := r.PathValue("id")
	o, err := s.getOrder(r.Context(), orderID)
	if err != nil || (user != o.BuyerID && user != o.SellerID) {
		httpx.Error(w, 404, "not found")
		return
	}
	switch o.Status {
	case "pending_payment":
		if err := s.cancelPending(r.Context(), orderID, "cancelled"); err != nil {
			httpx.Error(w, 409, "could not cancel")
			return
		}
	case "funded":
		if user != o.SellerID {
			httpx.Error(w, 403, "only the seller can cancel a paid order")
			return
		}
		if err := s.pay.Refund(r.Context(), o.ID, o.BuyerID); err != nil && !errors.Is(err, ErrAlreadySettled) {
			slog.Error("pay refund", "order", o.ID, "err", err)
			httpx.Error(w, 502, "payment service error")
			return
		}
		tx, err := s.pool.Begin(r.Context())
		if err != nil {
			httpx.Error(w, 500, "db")
			return
		}
		defer tx.Rollback(r.Context())
		tag, err := tx.Exec(r.Context(),
			`UPDATE market.orders SET status = 'refunded', updated_at = now()
			 WHERE id = $1 AND status = 'funded'`, o.ID)
		if err != nil || tag.RowsAffected() != 1 {
			httpx.Error(w, 409, "could not cancel")
			return
		}
		if _, err := tx.Exec(r.Context(),
			`UPDATE market.listings SET status = 'active', updated_at = now() WHERE id = $1`, o.ListingID); err != nil {
			httpx.Error(w, 500, "db")
			return
		}
		if err := outboxTx(r.Context(), tx, "order.refunded", map[string]any{"order_id": o.ID}); err != nil {
			httpx.Error(w, 500, "db")
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			httpx.Error(w, 500, "db")
			return
		}
	default:
		httpx.Error(w, 409, "order cannot be cancelled in state "+o.Status)
		return
	}
	out, _ := s.getOrder(r.Context(), orderID)
	httpx.JSON(w, 200, out)
}

// cancelPending cancels a pending_payment order and re-activates its listing.
func (s *Server) cancelPending(ctx context.Context, orderID, newStatus string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	o, err := lockOrder(ctx, tx, orderID)
	if err != nil {
		return err
	}
	if o.Status != "pending_payment" {
		return errors.New("not pending")
	}
	if _, err := tx.Exec(ctx,
		`UPDATE market.orders SET status = $2, updated_at = now() WHERE id = $1`, orderID, newStatus); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE market.listings SET status = 'active', updated_at = now()
		 WHERE id = $1 AND status = 'reserved'`, o.ListingID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM market.reservations WHERE order_id = $1`, orderID); err != nil {
		return err
	}
	if err := outboxTx(ctx, tx, "order.cancelled", map[string]any{"order_id": orderID}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SweepReservations cancels expired pending_payment orders and re-activates
// their listings.
func (s *Server) SweepReservations(ctx context.Context) (int, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT r.order_id FROM market.reservations r
		 JOIN market.orders o ON o.id = r.order_id
		 WHERE r.expires_at < now() AND o.status = 'pending_payment'`)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	n := 0
	for _, id := range ids {
		if err := s.cancelPending(ctx, id, "cancelled"); err == nil {
			n++
		}
	}
	return n, nil
}

// SweepAutoRelease completes shipped orders older than the cutoff. The
// "buyer never pressed confirm" timer.
func (s *Server) SweepAutoRelease(ctx context.Context, after time.Duration) (int, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+orderCols+` FROM market.orders o JOIN market.listings l ON l.id = o.listing_id
		 WHERE o.status = 'shipped' AND o.shipped_at < now() - $1::interval`,
		after.String())
	if err != nil {
		return 0, err
	}
	var orders []Order
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			rows.Close()
			return 0, err
		}
		orders = append(orders, o)
	}
	rows.Close()
	n := 0
	for _, o := range orders {
		if err := s.completeOrder(ctx, o); err == nil {
			n++
		} else {
			slog.Error("auto-release", "order", o.ID, "err", err)
		}
	}
	return n, nil
}

// StartSweeper runs both sweeps on a ticker. ponytail: single-instance ticker;
// move to a leader-elected job if market ever runs more than one replica.
func (s *Server) StartSweeper(ctx context.Context, every, autoReleaseAfter time.Duration) {
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if n, err := s.SweepReservations(ctx); err != nil {
					slog.Error("sweep reservations", "err", err)
				} else if n > 0 {
					slog.Info("sweep reservations", "cancelled", n)
				}
				if n, err := s.SweepAutoRelease(ctx, autoReleaseAfter); err != nil {
					slog.Error("sweep auto-release", "err", err)
				} else if n > 0 {
					slog.Info("sweep auto-release", "completed", n)
				}
			}
		}
	}()
}
