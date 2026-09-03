"""Redpanda consumer: risk's actual event inputs (see AGORA_SPEC.md section
8.3). Three of that section's five rows are wired here; auth.login and
auth.refresh_reuse_detected are not, because `id` publishes no Kafka events
today (only its own append-only Postgres audit log) and adding one is out of
scope for this pass — see the migration report for the full reasoning.

| Topic (stream)  | domain-topic header    | Signal                        |
|------------------|------------------------|--------------------------------|
| sale.events      | sale.reservation_created | scalper velocity              |
| market.events    | order.completed         | purchase behaviour             |
| pay.events       | transfer.created        | amount anomalies (escrow/deposit) |

Dedupes via risk.consumed_events, mirroring assist's consumer: redelivery
after a relay crash is a safe no-op, same guarantee outboxkit gives every
other consumer in this repo.
"""

import json
import logging
import threading
import time

from kafka import KafkaConsumer
from kafka.errors import KafkaError

from . import model

log = logging.getLogger("risk.consumer")

TOPICS = ["sale.events", "market.events", "pay.events"]


def _already_consumed(conn, source: str, event_id: int) -> bool:
    row = conn.execute(
        "SELECT 1 FROM risk.consumed_events WHERE source = %s AND event_id = %s",
        (source, event_id),
    ).fetchone()
    return row is not None


def _mark_consumed(conn, source: str, event_id: int) -> None:
    conn.execute(
        "INSERT INTO risk.consumed_events (source, event_id) VALUES (%s, %s) ON CONFLICT DO NOTHING",
        (source, event_id),
    )


def _write_score(conn, subject_type: str, subject_id: str, score: float, decision: str, reasons: list, version: str) -> None:
    conn.execute(
        """INSERT INTO risk.scores (subject_type, subject_id, score, decision, reasons, model_version)
           VALUES (%s, %s, %s, %s, %s, %s)
           ON CONFLICT (subject_type, subject_id) DO UPDATE SET
             score = EXCLUDED.score, decision = EXCLUDED.decision,
             reasons = EXCLUDED.reasons, model_version = EXCLUDED.model_version""",
        (subject_type, subject_id, round(score, 3), decision, json.dumps(reasons), version),
    )


def _score_reservation(conn, scorer: model.ReservationScorer, payload: dict) -> None:
    drop_id, user_id = payload["drop_id"], payload["user_id"]
    this_drop, all_drops, expired, total = conn.execute(
        """SELECT
             count(*) FILTER (WHERE drop_id = %(drop)s),
             count(*),
             count(*) FILTER (WHERE state = 'expired'),
             count(*) FILTER (WHERE state IN ('expired','released','confirmed'))
           FROM sale.reservations
           WHERE user_id = %(user)s AND reserved_at > now() - interval '10 minutes'""",
        {"drop": drop_id, "user": user_id},
    ).fetchone()
    ratio = (expired / total) if total else 0.0
    f = model.ReservationFeatures(this_drop, all_drops, ratio)
    score, reasons = scorer.score(f)
    _write_score(conn, "reservation", payload["reservation_id"], score, model.decide(score), reasons, scorer.version)


def _score_order(conn, payload: dict) -> None:
    order_id = payload["order_id"]
    row = conn.execute("SELECT buyer_id FROM market.orders WHERE id = %s", (order_id,)).fetchone()
    if not row:
        return
    (buyer_id,) = row
    (count_1h,) = conn.execute(
        """SELECT count(*) FROM market.orders
           WHERE buyer_id = %s AND status = 'completed' AND completed_at > now() - interval '1 hour'""",
        (buyer_id,),
    ).fetchone()
    score, reasons = model.order_score(count_1h)
    _write_score(conn, "order", order_id, score, model.decide(score), reasons, "order-rules-v1")


def _score_transfer(conn, payload: dict) -> None:
    score, reasons = model.transfer_score(payload["amount_minor"], payload["kind"])
    _write_score(conn, "transfer", payload["transfer_id"], score, model.decide(score), reasons, "transfer-rules-v1")


def process(conn, scorer: model.ReservationScorer, source: str, event_id: int, domain_topic: str, payload: dict) -> None:
    if _already_consumed(conn, source, event_id):
        return
    handlers = {
        "sale.reservation_created": lambda: _score_reservation(conn, scorer, payload),
        "order.completed": lambda: _score_order(conn, payload),
        "transfer.created": lambda: _score_transfer(conn, payload),
    }
    handler = handlers.get(domain_topic)
    if handler:
        handler()
    _mark_consumed(conn, source, event_id)


def _run(pool, brokers: str) -> None:
    scorer = model.ReservationScorer()
    log.info("reservation scoring with %s", scorer.version)
    consumer = None
    while consumer is None:
        try:
            consumer = KafkaConsumer(
                *TOPICS,
                bootstrap_servers=brokers.split(","),
                group_id="risk",
                enable_auto_commit=True,
                auto_offset_reset="earliest",
                value_deserializer=lambda b: json.loads(b.decode()),
            )
        except KafkaError:
            log.warning("redpanda not reachable yet, retrying")
            time.sleep(3)
    log.info("consuming %s from %s", TOPICS, brokers)
    for record in consumer:
        try:
            domain_topic = ""
            for key, val in record.headers or []:
                if key == "domain-topic":
                    domain_topic = val.decode()
            event_id = int(record.key.decode()) if record.key else record.offset
            with pool.connection() as conn:
                process(conn, scorer, record.topic, event_id, domain_topic, record.value)
        except Exception:
            log.exception("failed to process event at offset %s", record.offset)


def start_consumer(pool, brokers: str) -> None:
    if not brokers:
        log.warning("REDPANDA_BROKERS unset; risk consumer disabled")
        return
    threading.Thread(target=_run, args=(pool, brokers), daemon=True, name="risk-consumer").start()
