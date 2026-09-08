"""Redpanda consumer: reads market.events and feeds the trust rules.

The market service's outboxkit relay publishes market.outbox rows to the
`market.events` topic (row id as key, domain topic in a `domain-topic` header,
payload as the value). We dedupe via assist.consumed_events, so redelivery after
a relay crash is a safe no-op.
"""

import json
import logging
import threading
import time

from kafka import KafkaConsumer
from kafka.errors import KafkaError
from opentelemetry import trace

from . import trust, tracing

log = logging.getLogger("assist.consumer")
tracer = trace.get_tracer("assist.consumer")


def _run(pool, brokers: str) -> None:
    consumer = None
    while consumer is None:
        try:
            consumer = KafkaConsumer(
                "market.events",
                bootstrap_servers=brokers.split(","),
                group_id="assist-trust",
                enable_auto_commit=True,
                auto_offset_reset="earliest",
                value_deserializer=lambda b: json.loads(b.decode()),
            )
        except KafkaError:
            log.warning("redpanda not reachable yet, retrying")
            time.sleep(3)
    log.info("consuming market.events from %s", brokers)
    for record in consumer:
        try:
            topic = ""
            for key, val in record.headers or []:
                if key == "domain-topic":
                    topic = val.decode()
            event_id = int(record.key.decode()) if record.key else record.offset
            with tracer.start_as_current_span(f"assist.trust {topic}", context=tracing.context_from_payload(record.value)):
                with pool.connection() as conn:
                    trust.consume(conn, event_id, topic, record.value)
        except Exception:
            log.exception("failed to process event at offset %s", record.offset)


def start_consumer(pool, brokers: str) -> None:
    if not brokers:
        log.warning("REDPANDA_BROKERS unset; trust consumer disabled")
        return
    threading.Thread(
        target=_run, args=(pool, brokers), daemon=True, name="assist-consumer"
    ).start()
