"""Postgres pool + a tiny migration runner, mirroring assist/app/db.py."""

import os
from pathlib import Path

from psycopg_pool import ConnectionPool

DATABASE_URL = os.environ.get("DATABASE_URL", "postgres://vault:vault@localhost:5432/vault")

pool = ConnectionPool(DATABASE_URL, min_size=1, max_size=5, open=False)

MIGRATIONS_DIR = Path(__file__).resolve().parent.parent / "migrations"


def migrate() -> None:
    with pool.connection() as conn:
        conn.execute("CREATE SCHEMA IF NOT EXISTS risk")
        conn.execute(
            """CREATE TABLE IF NOT EXISTS risk.schema_migrations
               (name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())"""
        )
        for path in sorted(MIGRATIONS_DIR.glob("*.sql")):
            done = conn.execute(
                "SELECT 1 FROM risk.schema_migrations WHERE name = %s", (path.name,)
            ).fetchone()
            if done:
                continue
            with conn.transaction():
                conn.execute("SET LOCAL search_path TO risk")
                conn.execute(path.read_text())
                conn.execute(
                    "INSERT INTO risk.schema_migrations (name) VALUES (%s)", (path.name,)
                )
