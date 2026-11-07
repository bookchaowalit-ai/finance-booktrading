"""SPEC-003 — non-fundamental dip trigger (observe-only)."""

from __future__ import annotations

from datetime import UTC, date, datetime, timedelta

import pytest

from app.market_intel.dip_trigger import (
    DipInput,
    DipStatus,
    DipTriggerConfig,
    FundamentalEvent,
    evaluate_dip,
    format_alert_th,
    parse_yahoo_chart,
)

AS_OF = date(2026, 11, 6)


def _calm(n: int, start: float = 100.0) -> list[float]:
    """n closes that wiggle +/-0.5 % around a flat line."""
    closes = [start]
    for i in range(n - 1):
        closes.append(closes[-1] * (1.005 if i % 2 == 0 else 0.995))
    return closes


def _with_drop(total_drop: float, calm_days: int = 61, window: int = 3) -> list[float]:
    closes = _calm(calm_days)
    step = (1 - total_drop) ** (1 / window)
    for _ in range(window):
        closes.append(closes[-1] * step)
    return closes


def _volumes(n: int, spike: float = 3.0, window: int = 3) -> list[float]:
    base = [1_000_000.0] * (n - window)
    return base + [1_000_000.0 * spike] * window


def _input(**overrides) -> DipInput:
    closes = overrides.pop("closes", _with_drop(0.10))
    values = {
        "symbol": "SKH.KS",
        "as_of": AS_OF,
        "closes": closes,
        "volumes": _volumes(len(closes)),
        "benchmark_closes": _with_drop(0.05),
        "events": [],
        "weight_pct": 5.0,
    }
    values.update(overrides)
    return DipInput(**values)


def test_ac1_forced_selling_dip_without_news_triggers():
    result = evaluate_dip(_input())

    assert result.status is DipStatus.TRIGGER
    assert result.drop_pct == pytest.approx(-10.0, abs=0.01)
    assert result.worst_zscore is not None and result.worst_zscore <= -2.0
    assert set(result.forced_signals) == {"benchmark_down", "volume_spike"}


def test_ac2_fundamental_event_blocks_the_signal():
    event = FundamentalEvent(symbol="SKH.KS", kind="earnings", occurred_on=AS_OF - timedelta(days=2), title="Q3 miss")

    result = evaluate_dip(_input(events=[event]))

    assert result.status is DipStatus.FUNDAMENTAL
    assert any("earnings" in reason and "Q3 miss" in reason for reason in result.reasons)


def test_ac3_missing_event_data_needs_human_review():
    result = evaluate_dip(_input(events=None))

    assert result.status is DipStatus.NEEDS_REVIEW


def test_ac4_small_drop_is_not_a_dip():
    result = evaluate_dip(_input(closes=_with_drop(0.04)))

    assert result.status is DipStatus.NO_DIP


def test_ac5_drop_alone_without_forced_selling_evidence():
    closes = _with_drop(0.10)
    result = evaluate_dip(_input(closes=closes, volumes=[1_000_000.0] * len(closes), benchmark_closes=_calm(64)))

    assert result.status is DipStatus.NO_FORCED_SELLING
    assert result.forced_signals == ()


def test_ac6_margin_drop_counts_as_forced_selling_evidence():
    closes = _with_drop(0.10)
    result = evaluate_dip(
        _input(
            closes=closes,
            volumes=[1_000_000.0] * len(closes),
            margin_balance_before=1_000.0,
            margin_balance_now=850.0,
        )
    )

    assert result.status is DipStatus.TRIGGER
    assert set(result.forced_signals) == {"benchmark_down", "margin_unwind"}


def test_ac7_short_history_is_insufficient():
    closes = _with_drop(0.10, calm_days=20)

    result = evaluate_dip(_input(closes=closes, volumes=_volumes(len(closes))))

    assert result.status is DipStatus.INSUFFICIENT_DATA


def test_ac8_position_cap_headroom():
    over = evaluate_dip(_input(weight_pct=18.0))
    under = evaluate_dip(_input(weight_pct=5.0))
    unknown = evaluate_dip(_input(weight_pct=None))

    assert over.at_cap is True
    assert over.headroom_pct == 0.0
    assert over.status is DipStatus.TRIGGER
    assert under.at_cap is False
    assert under.headroom_pct == pytest.approx(10.0)
    assert unknown.headroom_pct is None and unknown.at_cap is False


def test_ac8_custom_cap():
    result = evaluate_dip(_input(weight_pct=5.0), DipTriggerConfig(max_position_pct=10.0))

    assert result.headroom_pct == pytest.approx(5.0)


def test_ac9_old_or_foreign_events_are_ignored():
    events = [
        FundamentalEvent(symbol="SKH.KS", kind="earnings", occurred_on=AS_OF - timedelta(days=30)),
        FundamentalEvent(symbol="NVDA", kind="guidance", occurred_on=AS_OF),
        FundamentalEvent(symbol="SKH.KS", kind="macro", occurred_on=AS_OF),
    ]

    result = evaluate_dip(_input(events=events))

    assert result.status is DipStatus.TRIGGER


def test_ac10_thai_alert_text():
    text = format_alert_th(evaluate_dip(_input(weight_pct=5.0)))

    assert "SKH.KS" in text
    assert "TRIGGER" in text
    assert "-10.0%" in text
    assert "10.0%" in text  # headroom
    assert "ไม่ใช่คำแนะนำการลงทุน" in text
    assert "ไม่ได้ส่งคำสั่งซื้อ" in text


def test_ac10_alert_says_no_room_at_cap():
    text = format_alert_th(evaluate_dip(_input(weight_pct=20.0)))

    assert "เต็มเพดาน" in text


def test_ac11_parse_yahoo_chart_skips_null_bars():
    payload = {
        "chart": {
            "result": [
                {
                    "timestamp": [1762300800, 1762387200, 1762473600],
                    "indicators": {"quote": [{"close": [100.0, None, 95.0], "volume": [10, None, 30]}]},
                }
            ]
        }
    }

    dates, closes, volumes = parse_yahoo_chart(payload)

    assert closes == [100.0, 95.0]
    assert volumes == [10.0, 30.0]
    assert len(dates) == 2 and dates[0] < dates[1]


def test_ac11_parse_yahoo_chart_rejects_empty():
    with pytest.raises(ValueError):
        parse_yahoo_chart({"chart": {"result": []}})


def test_rejects_bad_prices():
    with pytest.raises(ValueError):
        evaluate_dip(_input(closes=[*_with_drop(0.10)[:-1], 0.0]))


# --- Codex review follow-ups (2026-11-07) ---


def _bdays(n: int, end: date = AS_OF) -> list[date]:
    days = [end]
    while len(days) < n:
        prev = days[-1] - timedelta(days=1)
        while prev.weekday() >= 5:
            prev -= timedelta(days=1)
        days.append(prev)
    return days[::-1]


def test_benchmark_uses_the_stocks_window_dates():
    # Stock's last bar is one day earlier than the benchmark's; the benchmark only fell on its extra day.
    stock = _with_drop(0.10)
    stock_dates = _bdays(len(stock), end=AS_OF - timedelta(days=1))
    bench = [*_calm(len(stock)), _calm(len(stock))[-1] * 0.90]
    bench_dates = _bdays(len(bench), end=AS_OF)

    result = evaluate_dip(
        _input(
            closes=stock,
            dates=stock_dates,
            benchmark_closes=bench,
            benchmark_dates=bench_dates,
            as_of=stock_dates[-1],
        )
    )

    assert "benchmark_down" not in result.forced_signals


def test_benchmark_missing_matching_dates_omits_signal():
    stock = _with_drop(0.10)
    stock_dates = _bdays(len(stock))
    bench = _with_drop(0.05)
    bench_dates = [d - timedelta(days=400) for d in _bdays(len(bench))]

    result = evaluate_dip(_input(closes=stock, dates=stock_dates, benchmark_closes=bench, benchmark_dates=bench_dates))

    assert "benchmark_down" not in result.forced_signals
    assert any("วันที่ตรงกัน" in r for r in result.reasons)


def test_benchmark_aligned_dates_still_count():
    stock = _with_drop(0.10)
    dates = _bdays(len(stock))

    result = evaluate_dip(_input(closes=stock, dates=dates, benchmark_closes=_with_drop(0.05), benchmark_dates=dates))

    assert result.status is DipStatus.TRIGGER
    assert "benchmark_down" in result.forced_signals


def test_parse_yahoo_chart_drops_the_unfinished_session():
    payload = {
        "chart": {
            "result": [
                {
                    "meta": {"currentTradingPeriod": {"regular": {"start": 1762473600, "end": 1762497000}}},
                    "timestamp": [1762300800, 1762387200, 1762473600],
                    "indicators": {"quote": [{"close": [100.0, 99.0, 90.0], "volume": [10, 20, 5]}]},
                }
            ]
        }
    }

    _, during, _ = parse_yahoo_chart(payload, now=datetime.fromtimestamp(1762480000, tz=UTC))
    _, after, _ = parse_yahoo_chart(payload, now=datetime.fromtimestamp(1762500000, tz=UTC))

    assert during == [100.0, 99.0]
    assert after == [100.0, 99.0, 90.0]


def test_event_symbol_matching_ignores_case_and_spaces():
    event = FundamentalEvent(symbol=" skh.ks ", kind="Earnings", occurred_on=AS_OF)

    result = evaluate_dip(_input(symbol="SKH.ks", events=[event]))

    assert result.status is DipStatus.FUNDAMENTAL


def test_from_mapping_event_is_normalised():
    event = FundamentalEvent.from_mapping({"symbol": "nvda", "kind": "EARNINGS", "occurred_on": "2026-11-05"})

    assert event.symbol == "NVDA" and event.kind == "earnings"


def test_exact_threshold_drop_counts():
    closes = [*_calm(61)]
    base = closes[-1]
    closes += [base * 0.97, base * 0.94, base * 0.92]  # exactly -8 %
    closes = [c * 100 / base for c in closes]  # end at 92 from 100

    result = evaluate_dip(_input(closes=closes, volumes=_volumes(len(closes))))

    assert result.status is not DipStatus.NO_DIP


def test_exact_threshold_margin_counts():
    closes = _with_drop(0.10)
    result = evaluate_dip(
        _input(
            closes=closes,
            volumes=[1_000_000.0] * len(closes),
            margin_balance_before=1_000.0,
            margin_balance_now=900.0,
        )
    )

    assert "margin_unwind" in result.forced_signals
