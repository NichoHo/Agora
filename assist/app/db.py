"""Postgres pool + a tiny migration runner mirroring internal/pg (Go)."""

import os
from pathlib import Path

from psycopg_pool import ConnectionPool

DATABASE_URL = os.environ.get("DATABASE_URL", "postgres://vault:vault@localhost:5432/vault")

pool = ConnectionPool(DATABASE_URL, min_size=1, max_size=5, open=False)

MIGRATIONS_DIR = Path(__file__).resolve().parent.parent / "migrations"


def migrate() -> None:
    with pool.connection() as conn:
        conn.execute("CREATE SCHEMA IF NOT EXISTS assist")
        conn.execute(
            """CREATE TABLE IF NOT EXISTS assist.schema_migrations
               (name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())"""
        )
        for path in sorted(MIGRATIONS_DIR.glob("*.sql")):
            done = conn.execute(
                "SELECT 1 FROM assist.schema_migrations WHERE name = %s", (path.name,)
            ).fetchone()
            if done:
                continue
            with conn.transaction():
                conn.execute("SET LOCAL search_path TO assist")
                conn.execute(path.read_text())
                conn.execute(
                    "INSERT INTO assist.schema_migrations (name) VALUES (%s)", (path.name,)
                )
        _seed_comparables(conn)


# synthetic sold-history: assist owns its own demo data, seeded once on boot.
# price_minor is US cents.
COMPARABLES = [
    ("Vintage 35mm film SLR camera body", "electronics", 45000),
    ("35mm film SLR with 50mm lens", "electronics", 38000),
    ("Rangefinder film camera", "electronics", 32000),
    ("Wireless noise cancelling headphones", "electronics", 21000),
    ("Over-ear wireless headphones bluetooth", "electronics", 32000),
    ("Apple iPad 9th gen 64GB wifi", "electronics", 29000),
    ("Apple iPad air 5 256GB", "electronics", 62000),
    ("Kindle Paperwhite 11th generation", "electronics", 11000),
    ("Kindle Paperwhite 10th gen 8GB", "electronics", 8000),
    ("Steam Deck 256GB handheld", "electronics", 35000),
    ("Navy crew neck tee cotton", "fashion", 1000),
    ("Crew neck long sleeve tee", "fashion", 900),
    ("Levis 501 jeans vintage", "fashion", 6500),
    ("Levis 511 slim jeans", "fashion", 4200),
    ("North face mountain parka", "fashion", 18000),
    ("Dune Frank Herbert paperback", "books", 700),
    ("Dune Messiah paperback", "books", 900),
    ("Design of Everyday Things Norman", "books", 1500),
    ("Clean Code Robert Martin", "books", 2400),
    ("Gooseneck electric kettle white", "home", 9500),
    ("Electric kettle stainless 1.7L", "home", 4500),
    ("Oak desk lamp LED", "home", 3500),
    ("Ceramic aroma diffuser", "home", 2800),
    ("Sci-fi robot model kit master grade", "hobby", 4800),
    ("Robot model kit snap-fit unbuilt", "hobby", 2500),
    ("11 speed rear derailleur road", "hobby", 7000),
    ("Road bike crankset 50/34", "hobby", 14000),
    ("Vintage automatic watch mechanical", "hobby", 17000),
    ("Automatic dress watch sapphire", "hobby", 42000),
    ("Dreadnought acoustic guitar", "hobby", 21000),
]


def _seed_comparables(conn) -> None:
    if conn.execute("SELECT count(*) FROM assist.comparables").fetchone()[0] > 0:
        return
    for title, cat, price in COMPARABLES:
        conn.execute(
            "INSERT INTO assist.comparables (title, category_slug, price_minor) VALUES (%s, %s, %s)",
            (title, cat, price),
        )
