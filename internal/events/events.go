// Package events wires the outboxkit relay for Agora's services. Each service's
// <schema>.outbox is drained to a Redpanda stream topic named "<schema>.events".
package events

import (
	"context"
	"log/slog"
	"strings"

	"github.com/NichoHo/outboxkit"
	"github.com/NichoHo/outboxkit/kafkapub"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StartRelay runs an outboxkit relay for one schema in the background. When
// brokers is empty the relay is disabled so the stack still boots without
// Redpanda (dev convenience); outbox rows simply accumulate unpublished.
func StartRelay(ctx context.Context, pool *pgxpool.Pool, schema, brokers string) error {
	if brokers == "" {
		slog.Warn("REDPANDA_BROKERS unset; outbox relay disabled", "schema", schema)
		return nil
	}
	pub, err := kafkapub.New(strings.Split(brokers, ","), schema+".events")
	if err != nil {
		return err
	}
	relay := &outboxkit.Relay{Pool: pool, Schema: schema, Pub: pub}
	go relay.Run(ctx)
	slog.Info("outbox relay started", "schema", schema, "topic", schema+".events")
	return nil
}
