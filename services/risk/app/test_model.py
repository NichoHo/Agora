from . import model


def test_decide_thresholds():
    assert model.decide(0.2) == "allow"
    assert model.decide(0.5) == "review"
    assert model.decide(0.9) == "block"


def test_reservation_rules_flag_high_velocity():
    scorer = model.ReservationScorer(model_path=model.MODEL_PATH.with_name("does-not-exist.joblib"))
    quiet = model.ReservationFeatures(recent_count_this_drop=1, recent_count_all_drops=1, recent_expired_ratio=0.0)
    scalper = model.ReservationFeatures(recent_count_this_drop=5, recent_count_all_drops=12, recent_expired_ratio=0.8)
    quiet_score, _ = scorer.score(quiet)
    scalper_score, reasons = scorer.score(scalper)
    assert quiet_score < scalper_score
    assert model.decide(scalper_score) in ("review", "block")
    assert reasons


def test_order_score_flags_high_frequency_buyer():
    calm_score, calm_reasons = model.order_score(order_count_by_buyer_1h=2)
    hot_score, hot_reasons = model.order_score(order_count_by_buyer_1h=15)
    assert calm_score == 0.0 and calm_reasons == []
    assert hot_score > calm_score
    assert hot_reasons


def test_transfer_score_scales_with_amount():
    small_score, _ = model.transfer_score(amount_minor=5_000, kind="escrow_fund")
    large_score, large_reasons = model.transfer_score(amount_minor=600_000, kind="escrow_fund")
    assert small_score == 0.0
    assert large_score == 0.7  # both large and huge thresholds crossed
    assert len(large_reasons) == 2
