package sale

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/twmb/franz-go/pkg/kgo"
)

// StartMarketConsumer closes the loop the reserve/checkout hot path
// deliberately doesn't touch: it learns when a handed-off order actually
// gets paid (or dies) via market's own outbox stream, same transport every
// other consumer in this repo uses (spec 9.3: no new bus mechanism).
// Handlers are idempotent guarded UPDATEs, so redelivery is a safe no-op.
func StartMarketConsumer(ctx context.Context, brokers []string, s *Server) error {
	if len(brokers) == 0 {
		slog.Warn("REDPANDA_BROKERS unset; sale's market consumer disabled")
		return nil
	}
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup("sale"),
		kgo.ConsumeTopics("market.events"),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		return err
	}
	go func() {
		defer client.Close()
		for ctx.Err() == nil {
			fetches := client.PollFetches(ctx)
			if ctx.Err() != nil {
				return
			}
			fetches.EachError(func(_ string, _ int32, err error) {
				slog.Error("sale market consumer", "err", err)
			})
			fetches.EachRecord(func(rec *kgo.Record) {
				handleMarketEvent(ctx, s, rec)
			})
		}
	}()
	return nil
}

func handleMarketEvent(ctx context.Context, s *Server, rec *kgo.Record) {
	var domainTopic string
	for _, h := range rec.Headers {
		if h.Key == "domain-topic" {
			domainTopic = string(h.Value)
		}
	}
	var evt struct {
		OrderID string `json:"order_id"`
	}
	if err := json.Unmarshal(rec.Value, &evt); err != nil || evt.OrderID == "" {
		return
	}
	var err error
	switch domainTopic {
	case "order.funded":
		err = s.confirmReservation(ctx, evt.OrderID)
	case "order.cancelled", "order.refunded":
		err = s.releaseByOrder(ctx, evt.OrderID)
	default:
		return
	}
	if err != nil {
		slog.Error("sale market consumer handle", "topic", domainTopic, "order", evt.OrderID, "err", err)
	}
}
