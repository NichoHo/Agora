import logging
import os
from contextlib import asynccontextmanager

from fastapi import Depends, FastAPI, HTTPException
from opentelemetry.instrumentation.fastapi import FastAPIInstrumentor
from pydantic import BaseModel, Field

from . import consumer, db, suggest, tracing
from .auth import admin_user, current_user

logging.basicConfig(level=logging.INFO)
tracing.init("assist")

ACCEPTABLE_FIELDS = {"title", "description", "category", "price"}


@asynccontextmanager
async def lifespan(app: FastAPI):
    db.pool.open()
    db.migrate()
    consumer.start_consumer(db.pool, os.environ.get("REDPANDA_BROKERS", ""))
    yield
    db.pool.close()


app = FastAPI(title="vault-assist", lifespan=lifespan)
FastAPIInstrumentor.instrument_app(app)


@app.get("/healthz")
def healthz():
    return {"ok": True}


class SuggestIn(BaseModel):
    image_url: str = ""
    title_hint: str = Field("", max_length=120)


@app.post("/suggest")
def post_suggest(body: SuggestIn, user=Depends(current_user)):
    if not body.image_url and not body.title_hint.strip():
        raise HTTPException(400, "provide an image_url or a title_hint")
    with db.pool.connection() as conn:
        return suggest.build_suggestion(conn, user["sub"], body.image_url, body.title_hint)


class OutcomeIn(BaseModel):
    accepted_fields: list[str]
    listing_id: str | None = None


@app.post("/suggestions/{suggestion_id}/outcome")
def post_outcome(suggestion_id: str, body: OutcomeIn, user=Depends(current_user)):
    fields = [f for f in body.accepted_fields if f in ACCEPTABLE_FIELDS]
    with db.pool.connection() as conn:
        row = conn.execute(
            """UPDATE assist.suggestions
               SET accepted_fields = %s, listing_id = %s::uuid
               WHERE id = %s::uuid AND user_id = %s::uuid
               RETURNING id""",
            (fields, body.listing_id, suggestion_id, user["sub"]),
        ).fetchone()
    if not row:
        raise HTTPException(404, "suggestion not found")
    return {"ok": True}


@app.get("/admin/metrics")
def admin_metrics(_admin=Depends(admin_user)):
    with db.pool.connection() as conn:
        total, resolved = conn.execute(
            """SELECT count(*), count(accepted_fields) FROM assist.suggestions"""
        ).fetchone()
        per_field = {}
        for field in sorted(ACCEPTABLE_FIELDS):
            accepted = conn.execute(
                "SELECT count(*) FROM assist.suggestions WHERE %s = ANY(accepted_fields)",
                (field,),
            ).fetchone()[0]
            per_field[field] = round(accepted / resolved, 3) if resolved else None
        queued = conn.execute(
            "SELECT count(*) FROM assist.risk_scores WHERE status = 'queued'"
        ).fetchone()[0]
    return {
        "suggestions_total": total,
        "suggestions_with_outcome": resolved,
        "acceptance_rate_by_field": per_field,
        "trust_queue_open": queued,
    }


@app.get("/admin/trust")
def admin_trust(_admin=Depends(admin_user)):
    with db.pool.connection() as conn:
        rows = conn.execute(
            """SELECT id, subject_type, subject_id, score, reasons, status, created_at
               FROM assist.risk_scores ORDER BY status = 'queued' DESC, created_at DESC LIMIT 100"""
        ).fetchall()
    return [
        {
            "id": r[0], "subject_type": r[1], "subject_id": r[2], "score": r[3],
            "reasons": r[4], "status": r[5], "created_at": r[6].isoformat(),
        }
        for r in rows
    ]


class ResolveIn(BaseModel):
    action: str


@app.post("/admin/trust/{risk_id}/resolve")
def admin_resolve(risk_id: int, body: ResolveIn, admin=Depends(admin_user)):
    if body.action not in ("approve", "reject"):
        raise HTTPException(400, "action must be approve or reject")
    status = "approved" if body.action == "approve" else "rejected"
    with db.pool.connection() as conn:
        row = conn.execute(
            """UPDATE assist.risk_scores
               SET status = %s, resolved_by = %s, resolved_at = now()
               WHERE id = %s AND status = 'queued' RETURNING id""",
            (status, admin.get("email", ""), risk_id),
        ).fetchone()
    if not row:
        raise HTTPException(409, "not queued")
    return {"ok": True}
