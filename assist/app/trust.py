"""Trust scoring: pure rules over market events, feeding the admin review queue.

ponytail: rules only. Upgrade path: IsolationForest over engineered features.
Delivery/dedup is handled by consume() (idempotent) fed from Redpanda by
app.consumer.
"""

import json
import logging
from datetime import datetime, timedelta, timezone

log = logging.getLogger("assist.trust")

HIGH_VALUE = 50_000
NEW_ACCOUNT = timedelta(hours=1)
RAPID_WINDOW = timedelta(minutes=10)
RAPID_COUNT = 5


def score_listing_created(payload: dict, seller_created_at: datetime | None,
                          recent_listing_count: int, now: datetime) -> tuple[float, list[str]]:
    """Pure rules for a listing.created event → (score, reasons)."""
    score, reasons = 0.0, []
    price = int(payload.get("price_minor", 0))
    if price >= HIGH_VALUE and seller_created_at is not None and now - seller_created_at < NEW_ACCOUNT:
        score += 0.8
        reasons.append(f"account younger than 1h listing ${price / 100:,.2f}")
    if recent_listing_count >= RAPID_COUNT:
        score += 0.6
        reasons.append(f"{recent_listing_count} listings in 10 minutes")
    return min(score, 1.0), reasons


def score_order_created(buyer_created_at: datetime | None, now: datetime) -> tuple[float, list[str]]:
    if buyer_created_at is not None and now - buyer_created_at < NEW_ACCOUNT:
        return 0.5, ["buyer account younger than 1h"]
    return 0.0, []


def _user_created_at(conn, user_id: str) -> datetime | None:
    row = conn.execute("SELECT created_at FROM id.users WHERE id = %s::uuid", (user_id,)).fetchone()
    return row[0] if row else None


def _score(conn, topic: str, payload: dict) -> None:
    now = datetime.now(timezone.utc)
    score, reasons, subject_type, subject_id = 0.0, [], "", ""
    if topic == "listing.created":
        seller = payload.get("seller_id", "")
        recent = conn.execute(
            """SELECT count(*) FROM market.listings
               WHERE seller_id = %s::uuid AND created_at > now() - interval '10 minutes'""",
            (seller,),
        ).fetchone()[0]
        score, reasons = score_listing_created(payload, _user_created_at(conn, seller), recent, now)
        subject_type, subject_id = "listing", payload.get("listing_id", "")
    elif topic == "order.created":
        buyer = payload.get("buyer_id", "")
        score, reasons = score_order_created(_user_created_at(conn, buyer), now)
        subject_type, subject_id = "order", payload.get("order_id", "")

    if score > 0:
        conn.execute(
            """INSERT INTO assist.risk_scores (subject_type, subject_id, score, reasons)
               VALUES (%s, %s, %s, %s::jsonb)""",
            (subject_type, subject_id, score, json.dumps(reasons)),
        )


def consume(conn, event_id: int, topic: str, payload: dict) -> bool:
    """Idempotent consumer entry (the Python analog of outboxkit.Idempotent.Guard):
    record the event id and score it in one transaction; a redelivered event whose
    marker already committed is a no-op. Returns True when scoring ran."""
    with conn.transaction():
        tag = conn.execute(
            "INSERT INTO assist.consumed_events (source, event_id) VALUES ('market', %s) "
            "ON CONFLICT DO NOTHING",
            (event_id,),
        )
        if tag.rowcount == 0:
            return False  # already consumed
        _score(conn, topic, payload)
    return True
