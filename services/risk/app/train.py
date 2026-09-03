"""Train the reservation IsolationForest on synthetic drop activity.

Run: python -m app.train (from services/risk/). Writes reservation_model.joblib
next to model.py. Synthetic data, illustrative only, same stance as the
tally model.py/train.py this was adapted from.
"""

from __future__ import annotations

import joblib
import numpy as np
from sklearn.ensemble import IsolationForest

from .model import MODEL_PATH

RNG = np.random.default_rng(42)
N = 20_000


def synthetic_normal_activity(n: int) -> np.ndarray:
    """Plausible everyday drop participation, in reservation_vector() order:
    recent_count_this_drop, recent_count_all_drops, recent_expired_ratio.
    Most buyers reserve once or twice and follow through."""
    this_drop = RNG.poisson(lam=1.0, size=n)
    all_drops = this_drop + RNG.poisson(lam=0.5, size=n)
    expired_ratio = np.clip(RNG.beta(a=1.0, b=6.0, size=n), 0, 1)
    return np.column_stack([this_drop, all_drops, expired_ratio])


def main() -> None:
    X = synthetic_normal_activity(N)
    model = IsolationForest(n_estimators=100, contamination=0.02, random_state=42)
    model.fit(X)
    joblib.dump(model, MODEL_PATH)
    print(f"trained IsolationForest on {N} synthetic rows -> {MODEL_PATH}")


if __name__ == "__main__":
    main()
