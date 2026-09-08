"""Redpanda consumer: risk's actual event inputs (see AGORA_SPEC.md section
8.3). All five rows of that section's table are wired here:

| Topic (stream)  | domain-topic header       | Signal                            |
|-----------------|----------------------------|------------------------------------|
| id.events       | auth.login                | login velocity per user            |
| id.events       | auth.refresh_reuse_detected | token theft (always top-of-scale) |
| sale.events     | sale.reservation_created   | scalper velocity                   |
| market.events   | order.completed            | purchase behaviour                 |
| pay.events      | transfer.created           | amount anomalies (escrow/deposit)  |

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
from opentelemetry import trace

from . import model, tracing

tracer = trace.get_tracer("risk.consumer")

log = logging.getLogger("risk.consumer")

TOPICS = ["id.events", "sale.events", "market.events", "pay.events"]


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
    # allow needs no human attention and is auto-approved; review/block always
    # (re-)join the queue, even overwriting a past approval, because a new
    # anomalous event is new signal, not something a prior approval waives.
    status = "approved" if decision == "allow" else "queued"
    conn.execute(
        """INSERT INTO risk.scores (subject_type, subject_id, score, decision, reasons, model_version, status)
           VALUES (%s, %s, %s, %s, %s, %s, %s)
           ON CONFLICT (subject_type, subject_id) DO UPDATE SET
             score = EXCLUDED.score, decision = EXCLUDED.decision,
             reasons = EXCLUDED.reasons, model_version = EXCLUDED.model_version, status = EXCLUDED.status""",
        (subject_type, subject_id, round(score, 3), decision, json.dumps(reasons), version, status),
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


def _score_login(conn, payload: dict) -> None:
    user_id = payload["user_id"]
    (count_10m,) = conn.execute(
        """SELECT count(*) FROM id.audit_events
           WHERE actor = %s AND action = 'token.issue' AND at > now() - interval '10 minutes'""",
        (user_id,),
    ).fetchone()
    score, reasons = model.login_score(count_10m)
    _write_score(conn, "login", user_id, score, model.decide(score), reasons, "login-rules-v1")


def _score_refresh_reuse(conn, payload: dict) -> None:
    score, reasons = model.refresh_reuse_score()
    _write_score(conn, "refresh_reuse", payload["family"], score, model.decide(score), reasons, "refresh-reuse-v1")


def process(conn, scorer: model.ReservationScorer, source: str, event_id: int, domain_topic: str, payload: dict) -> None:
    if _already_consumed(conn, source, event_id):
        return
    handlers = {
        "auth.login": lambda: _score_login(conn, payload),
        "auth.refresh_reuse_detected": lambda: _score_refresh_reuse(conn, payload),
        "sale.reservation_created": lambda: _score_reservation(conn, scorer, payload),
        "order.completed": lambda: _score_order(conn, payload),
        "transfer.created": lambda: _score_transfer(conn, payload),
    }
    handler = handlers.get(domain_topic)
    if handler:
        # Continues the trace the producing service started (see
        # tracing.context_from_payload); a span still opens with no parent
        # when "_trace" is missing, so untraced producers don't break this.
        with tracer.start_as_current_span(f"risk.score {domain_topic}", context=tracing.context_from_payload(payload)):
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
