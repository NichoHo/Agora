import logging
import os
from contextlib import asynccontextmanager

from fastapi import Depends, FastAPI, HTTPException
from pydantic import BaseModel

from . import consumer, db
from .auth import admin_user

logging.basicConfig(level=logging.INFO)


@asynccontextmanager
async def lifespan(app: FastAPI):
    db.pool.open()
    db.migrate()
    consumer.start_consumer(db.pool, os.environ.get("REDPANDA_BROKERS", ""))
    yield
    db.pool.close()


app = FastAPI(title="agora-risk", lifespan=lifespan)


@app.get("/healthz")
def healthz():
    return {"ok": True}


@app.get("/admin/metrics")
def admin_metrics(_admin=Depends(admin_user)):
    with db.pool.connection() as conn:
        by_decision = dict(
            conn.execute(
                "SELECT decision, count(*) FROM risk.scores GROUP BY decision"
            ).fetchall()
        )
        queued = conn.execute(
            "SELECT count(*) FROM risk.scores WHERE status = 'queued'"
        ).fetchone()[0]
    return {"scored_by_decision": by_decision, "review_queue_open": queued}


@app.get("/admin/scores")
def admin_scores(_admin=Depends(admin_user)):
    with db.pool.connection() as conn:
        rows = conn.execute(
            """SELECT id, subject_type, subject_id, score, decision, reasons, model_version, status, created_at
               FROM risk.scores ORDER BY status = 'queued' DESC, created_at DESC LIMIT 200"""
        ).fetchall()
    return [
        {
            "id": r[0], "subject_type": r[1], "subject_id": r[2], "score": r[3],
            "decision": r[4], "reasons": r[5], "model_version": r[6],
            "status": r[7], "created_at": r[8].isoformat(),
        }
        for r in rows
    ]


class ResolveIn(BaseModel):
    action: str


@app.post("/admin/scores/{score_id}/resolve")
def admin_resolve(score_id: int, body: ResolveIn, admin=Depends(admin_user)):
    if body.action not in ("approve", "reject"):
        raise HTTPException(400, "action must be approve or reject")
    status = "approved" if body.action == "approve" else "rejected"
    with db.pool.connection() as conn:
        row = conn.execute(
            """UPDATE risk.scores SET status = %s, resolved_by = %s, resolved_at = now()
               WHERE id = %s AND status = 'queued' RETURNING id""",
            (status, admin.get("email", ""), score_id),
        ).fetchone()
    if not row:
        raise HTTPException(409, "not queued")
    return {"ok": True}
