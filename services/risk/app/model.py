"""Scoring logic for the three signals risk consumes today (see consumer.py's
module docstring for which topics and why). Pure: no Kafka, no database.

ponytail: a real IsolationForest is trained only for reservation velocity,
agora's clearest scalper signal and the one closest to tally's original
"transfer velocity" model this was adapted from. order/transfer scoring is
rules-only. Add a model for those if a real anomaly pattern shows up in the
data; guessing feature spaces for two more signal types ahead of any real
data is exactly the over-fitting tally's own model.py already warns against.
"""

from __future__ import annotations

import os
from dataclasses import dataclass
from pathlib import Path

MODEL_PATH = Path(__file__).parent / "reservation_model.joblib"

REVIEW_THRESHOLD = float(os.getenv("RISK_REVIEW_THRESHOLD", "0.5"))
BLOCK_THRESHOLD = float(os.getenv("RISK_BLOCK_THRESHOLD", "0.8"))


def decide(score: float) -> str:
    if score > BLOCK_THRESHOLD:
        return "block"
    if score >= REVIEW_THRESHOLD:
        return "review"
    return "allow"


@dataclass(frozen=True)
class ReservationFeatures:
    """Scalper signal: how fast is this user reserving across this drop and
    across all recent drops, and how quickly do they abandon (never check out)?"""

    recent_count_this_drop: int   # this user's reservations on this drop, last 10 min
    recent_count_all_drops: int   # this user's reservations on any drop, last 10 min
    recent_expired_ratio: float   # of this user's recent reservations, fraction that expired unpaid


def reservation_vector(f: ReservationFeatures) -> list[float]:
    return [float(f.recent_count_this_drop), float(f.recent_count_all_drops), f.recent_expired_ratio]


def _reservation_rule_score(f: ReservationFeatures) -> float:
    score = 0.0
    if f.recent_count_this_drop > 3:
        score += 0.4
    if f.recent_count_all_drops > 8:
        score += 0.3
    if f.recent_expired_ratio > 0.5:
        score += 0.3
    return min(score, 1.0)


class ReservationScorer:
    """Rules alone, or blended with the IsolationForest when reservation_model.joblib exists."""

    version = "reservation-rules-v1"

    def __init__(self, model_path: Path = MODEL_PATH) -> None:
        self.model = None
        if model_path.exists():
            import joblib  # lazy: rule-only tests need no sklearn

            self.model = joblib.load(model_path)
            self.version = "reservation-iforest-v1"

    def score(self, f: ReservationFeatures) -> tuple[float, list[str]]:
        rules = _reservation_rule_score(f)
        reasons = []
        if f.recent_count_this_drop > 3:
            reasons.append(f"{f.recent_count_this_drop} reservations on this drop in 10 minutes")
        if f.recent_count_all_drops > 8:
            reasons.append(f"{f.recent_count_all_drops} reservations across all drops in 10 minutes")
        if f.recent_expired_ratio > 0.5:
            reasons.append(f"{f.recent_expired_ratio:.0%} of recent reservations expired unpaid")
        if self.model is None:
            return rules, reasons
        # decision_function: positive = normal, negative = anomalous, roughly
        # in [-0.2, 0.2]. ponytail: crude linear calibration, same as tally's
        # original — replace with a calibrated mapping if score quality matters.
        df = float(self.model.decision_function([reservation_vector(f)])[0])
        ml = min(1.0, max(0.0, 0.5 - df * 2.5))
        if ml > rules:
            reasons.append("flagged as an outlier by the anomaly model")
        return max(rules, ml), reasons


def order_score(order_count_by_buyer_1h: int) -> tuple[float, list[str]]:
    """Purchase-behavior rule: a buyer completing many orders in an hour is
    unusual for a C2C marketplace (contrast a drop, where it's expected)."""
    reasons = []
    score = 0.0
    if order_count_by_buyer_1h > 10:
        score = 0.6
        reasons.append(f"{order_count_by_buyer_1h} completed orders by this buyer in the last hour")
    return score, reasons


def transfer_score(amount_minor: int, kind: str) -> tuple[float, list[str]]:
    """Amount-anomaly rule, adapted directly from tally's original thresholds."""
    reasons = []
    score = 0.0
    if amount_minor > 100_000:  # $1,000
        score += 0.4
        reasons.append(f"{kind} of {amount_minor} minor units exceeds $1,000")
    if amount_minor > 500_000:  # $5,000
        score += 0.3
        reasons.append(f"{kind} of {amount_minor} minor units exceeds $5,000")
    return min(score, 1.0), reasons
