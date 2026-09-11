"""Pure-logic checks: price band math, trust rules, heuristic fallback.
DB/API paths are exercised by the compose e2e run."""

import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from app.suggest import heuristic_suggestion, price_band, validate_suggestion  # noqa: E402
from app.trust import score_listing_created, score_order_created  # noqa: E402

NOW = datetime(2026, 7, 19, 12, 0, tzinfo=timezone.utc)


def test_price_band_quartiles():
    prices = [1000, 2000, 3000, 4000, 5000, 6000, 7000, 8000]
    low, high = price_band(prices)
    assert low is not None and high is not None
    assert low < high
    assert 2000 <= low <= 3500
    assert 5500 <= high <= 7500
    assert low % 100 == 0 and high % 100 == 0


def test_price_band_edges():
    assert price_band([]) == (None, None)
    low, high = price_band([10_000])
    assert (low, high) == (8000, 12000)


def test_heuristic_suggestion():
    s = heuristic_suggestion("  Film SLR camera  ")
    assert s["title"] == "Film SLR camera"
    assert s["category_slug"] == "other"
    assert s["model"] == "heuristic"
    assert heuristic_suggestion("")["title"] == "Untitled item"
    assert len(heuristic_suggestion("x" * 300)["title"]) == 80


def test_validate_suggestion_accepts_well_formed_output():
    assert validate_suggestion(
        {
            "title": "Vintage 35mm camera",
            "description": "A well-loved film SLR. Light scratches on the body.",
            "category_slug": "electronics",
            "search_terms": "35mm film camera",
        }
    )


def test_validate_suggestion_rejects_unknown_category():
    assert not validate_suggestion(
        {
            "title": "x",
            "description": "y",
            "category_slug": "not-a-real-category",  # not in CATEGORY_SLUGS
            "search_terms": "z",
        }
    )


def test_validate_suggestion_rejects_bad_shapes():
    assert not validate_suggestion({})
    assert not validate_suggestion(
        {"title": "", "description": "y", "category_slug": "other", "search_terms": "z"}
    )
    assert not validate_suggestion(
        {"title": "x" * 500, "description": "y", "category_slug": "other", "search_terms": "z"}
    )
    assert not validate_suggestion(
        {"title": 123, "description": "y", "category_slug": "other", "search_terms": "z"}
    )
    assert not validate_suggestion("not a dict")


def test_new_seller_high_value_flagged():
    score, reasons = score_listing_created(
        {"price_minor": 80_000}, NOW - timedelta(minutes=10), 1, NOW
    )
    assert score == 0.8
    assert reasons


def test_old_seller_high_value_not_flagged():
    score, _ = score_listing_created({"price_minor": 80_000}, NOW - timedelta(days=30), 1, NOW)
    assert score == 0.0


def test_rapid_listing_flagged_and_capped():
    score, reasons = score_listing_created(
        {"price_minor": 90_000}, NOW - timedelta(minutes=5), 7, NOW
    )
    assert score == 1.0  # 0.8 + 0.6 capped
    assert len(reasons) == 2


def test_cheap_listing_new_seller_not_flagged():
    score, _ = score_listing_created({"price_minor": 500}, NOW - timedelta(minutes=5), 1, NOW)
    assert score == 0.0


def test_order_rules():
    assert score_order_created(NOW - timedelta(minutes=30), NOW)[0] == 0.5
    assert score_order_created(NOW - timedelta(days=2), NOW)[0] == 0.0
    assert score_order_created(None, NOW)[0] == 0.0
