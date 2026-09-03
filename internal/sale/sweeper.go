package sale

import (
	"context"
	"log/slog"
	"time"
)

// SweepExpired releases reservations whose TTL passed with no completed
// payment (spec 7.4 step 5 / 10.2 TTL correctness). Sweeping both states
// independently means a stalled checkout (payment_pending) frees its unit on
// sale's own clock instead of waiting on market's slower 15-minute sweep.
func (s *Server) SweepExpired(ctx context.Context) (int, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, state FROM sale.reservations WHERE state IN ('reserved','payment_pending') AND expires_at < now()`)
	if err != nil {
		return 0, err
	}
	type target struct{ id, state string }
	var targets []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.id, &t.state); err != nil {
			rows.Close()
			return 0, err
		}
		targets = append(targets, t)
	}
	rows.Close()
	n := 0
	for _, t := range targets {
		if err := s.releaseReservation(ctx, t.id, t.state, "expired", "sale.reservation_expired"); err != nil {
			slog.Error("sweep expire", "reservation", t.id, "err", err)
			continue
		}
		n++
	}
	return n, nil
}

// ReconcileStock records Redis-vs-Postgres drift for observability (spec
// 7.8's stock_reconciliation table). Redis stays authoritative regardless;
// this is a smoke alarm, not a corrector.
func (s *Server) ReconcileStock(ctx context.Context, dropID string) error {
	d, err := s.getDrop(ctx, dropID)
	if err != nil {
		return err
	}
	remaining, err := s.rdb.Remaining(ctx, dropID, d.ShardCount)
	if err != nil {
		return err
	}
	var reserved int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM sale.reservations WHERE drop_id = $1 AND state IN ('reserved','payment_pending','confirmed')`,
		dropID).Scan(&reserved); err != nil {
		return err
	}
	drift := d.TotalUnits - remaining - reserved
	_, err = s.pool.Exec(ctx,
		`INSERT INTO sale.stock_reconciliation (drop_id, redis_remaining, postgres_reserved, drift) VALUES ($1,$2,$3,$4)`,
		dropID, remaining, reserved, drift)
	if drift != 0 {
		slog.Warn("sale stock drift", "drop", dropID, "drift", drift, "redis_remaining", remaining, "postgres_reserved", reserved)
	}
	return err
}

// StartSweeper runs the TTL sweep on a ticker, mirroring market's own
// sweeper pattern. ponytail: single-instance ticker, same known ceiling as
// market's — move to a leader-elected job if sale ever runs multiple replicas.
func (s *Server) StartSweeper(ctx context.Context, every time.Duration) {
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if n, err := s.SweepExpired(ctx); err != nil {
					slog.Error("sweep expired", "err", err)
				} else if n > 0 {
					slog.Info("sweep expired", "released", n)
				}
			}
		}
	}()
}
