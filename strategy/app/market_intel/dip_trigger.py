"""Non-fundamental dip trigger (SPEC-003). Observe-only: it never places orders.

Flags a stock that fell hard for reasons other than its own fundamentals, for
example forced selling, and reports how much room the position cap leaves.

All three gates must pass for ``TRIGGER``:

- G1 hard drop: the window return is at or below ``-min_drop_pct`` and the worst
  daily return in the window is at least ``min_zscore`` standard deviations
  below the prior ``lookback_days`` of daily returns.
- G2 not fundamental: no fundamental event for the symbol in the last
  ``event_lookback_days``. Without event data the result is ``NEEDS_REVIEW``.
- G3 forced-selling evidence: at least ``min_forced_signals`` of benchmark
  down, volume spike and margin unwind.

The position check is reported next to the status and never changes it.
"""

from __future__ import annotations

from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from datetime import UTC, date, datetime, timedelta
from enum import StrEnum
from math import isfinite
from statistics import fmean, pstdev
from typing import Any

DIP_TRIGGER_VERSION = "dip-trigger.v1"

FUNDAMENTAL_EVENT_KINDS = frozenset(
    {
        "earnings",
        "guidance",
        "rating_change",
        "company_news",
        "regulatory",
        "corporate_action",
    }
)

VOLUME_BASELINE_DAYS = 20

# Inclusive thresholds compare with this slack so exact boundaries (100 -> 92 is -8 %) survive float error.
EPS = 1e-9


def canonical_symbol(symbol: str) -> str:
    return symbol.strip().upper()


class DipStatus(StrEnum):
    TRIGGER = "TRIGGER"
    NEEDS_REVIEW = "NEEDS_REVIEW"
    FUNDAMENTAL = "FUNDAMENTAL"
    NO_FORCED_SELLING = "NO_FORCED_SELLING"
    NO_DIP = "NO_DIP"
    INSUFFICIENT_DATA = "INSUFFICIENT_DATA"


@dataclass(frozen=True)
class DipTriggerConfig:
    window_days: int = 3
    lookback_days: int = 60
    min_drop_pct: float = 8.0
    min_zscore: float = 2.0
    event_lookback_days: int = 5
    min_benchmark_drop_pct: float = 3.0
    min_volume_ratio: float = 2.0
    min_margin_drop_pct: float = 10.0
    min_forced_signals: int = 2
    max_position_pct: float = 15.0

    @property
    def min_closes(self) -> int:
        return self.lookback_days + self.window_days + 1


@dataclass(frozen=True)
class FundamentalEvent:
    symbol: str
    kind: str
    occurred_on: date
    title: str = ""

    def __post_init__(self) -> None:
        object.__setattr__(self, "symbol", canonical_symbol(self.symbol))
        object.__setattr__(self, "kind", self.kind.strip().lower())

    @classmethod
    def from_mapping(cls, raw: Mapping[str, Any]) -> FundamentalEvent:
        return cls(
            symbol=str(raw["symbol"]),
            kind=str(raw["kind"]),
            occurred_on=date.fromisoformat(str(raw["occurred_on"])),
            title=str(raw.get("title", "")).strip(),
        )


@dataclass(frozen=True)
class DipInput:
    """Closes and volumes run oldest to newest; the last bar is ``as_of``.

    When ``dates`` and ``benchmark_dates`` are both given, the benchmark return is
    taken between the stock's own window dates (exact date match), so a holiday or
    suspension cannot pair the stock with an unrelated benchmark window.
    """

    symbol: str
    as_of: date
    closes: Sequence[float]
    volumes: Sequence[float] | None = None
    benchmark_closes: Sequence[float] | None = None
    events: Sequence[FundamentalEvent] | None = None
    margin_balance_before: float | None = None
    margin_balance_now: float | None = None
    weight_pct: float | None = None
    dates: Sequence[date] | None = None
    benchmark_dates: Sequence[date] | None = None


@dataclass(frozen=True)
class DipTriggerResult:
    symbol: str
    as_of: date
    status: DipStatus
    drop_pct: float | None
    worst_zscore: float | None
    forced_signals: tuple[str, ...]
    reasons: tuple[str, ...]
    weight_pct: float | None
    max_position_pct: float
    headroom_pct: float | None
    at_cap: bool
    version: str = DIP_TRIGGER_VERSION


def _validate_prices(values: Sequence[float], name: str) -> list[float]:
    result = []
    for value in values:
        number = float(value)
        if not isfinite(number) or number <= 0:
            raise ValueError(f"{name} must be finite positive numbers")
        result.append(number)
    return result


def _returns(closes: Sequence[float]) -> list[float]:
    return [closes[i] / closes[i - 1] - 1 for i in range(1, len(closes))]


def _window_return_pct(closes: Sequence[float], window: int) -> float | None:
    if len(closes) < window + 1:
        return None
    return (closes[-1] / closes[-1 - window] - 1) * 100


def _benchmark_return_pct(inp: DipInput, window: int) -> float | None:
    bench = _validate_prices(inp.benchmark_closes or (), "benchmark_closes")
    if inp.dates is None or inp.benchmark_dates is None:
        return _window_return_pct(bench, window)
    if len(inp.dates) != len(inp.closes) or len(inp.benchmark_dates) != len(bench):
        raise ValueError("dates must line up with their closes")
    if len(inp.dates) < window + 1:
        return None
    by_date = dict(zip(inp.benchmark_dates, bench, strict=True))
    start, end = by_date.get(inp.dates[-1 - window]), by_date.get(inp.dates[-1])
    if start is None or end is None:
        return None
    return (end / start - 1) * 100


def _zscore(value: float, sample: Sequence[float]) -> float:
    mean = fmean(sample)
    spread = pstdev(sample)
    if spread == 0:
        return float("-inf") if value < mean else 0.0
    return (value - mean) / spread


def _position(weight_pct: float | None, cap: float) -> tuple[float | None, bool]:
    if weight_pct is None:
        return None, False
    headroom = max(cap - weight_pct, 0.0)
    return round(headroom, 4), weight_pct >= cap


def _fundamental_hits(inp: DipInput, config: DipTriggerConfig) -> list[FundamentalEvent]:
    earliest = inp.as_of - timedelta(days=config.event_lookback_days)
    return [
        event
        for event in inp.events or ()
        if event.symbol == canonical_symbol(inp.symbol)
        and event.kind in FUNDAMENTAL_EVENT_KINDS
        and earliest <= event.occurred_on <= inp.as_of
    ]


def _forced_signals(inp: DipInput, config: DipTriggerConfig) -> tuple[list[str], list[str]]:
    signals: list[str] = []
    reasons: list[str] = []
    window = config.window_days

    if inp.benchmark_closes:
        bench_pct = _benchmark_return_pct(inp, window)
        if bench_pct is None and inp.dates is not None and inp.benchmark_dates is not None:
            reasons.append("ไม่มีข้อมูลตลาดในวันที่ตรงกันกับช่วงของหุ้น ข้ามสัญญาณนี้")
        if bench_pct is not None and bench_pct <= -config.min_benchmark_drop_pct + EPS:
            signals.append("benchmark_down")
            reasons.append(f"ตลาด/กลุ่มลง {bench_pct:.1f}% ในช่วงเดียวกัน")

    volumes = list(inp.volumes or ())
    if len(volumes) >= window + VOLUME_BASELINE_DAYS:
        baseline = fmean(volumes[-window - VOLUME_BASELINE_DAYS : -window])
        peak = max(volumes[-window:])
        if baseline > 0 and peak / baseline >= config.min_volume_ratio - EPS:
            signals.append("volume_spike")
            reasons.append(f"วอลุ่มสูงสุด {peak / baseline:.1f}x ของค่าเฉลี่ย 20 วัน")

    before, now = inp.margin_balance_before, inp.margin_balance_now
    if before is not None and now is not None and before > 0:
        margin_pct = (now / before - 1) * 100
        if margin_pct <= -config.min_margin_drop_pct + EPS:
            signals.append("margin_unwind")
            reasons.append(f"ยอด margin ลด {margin_pct:.1f}%")

    return signals, reasons


def evaluate_dip(inp: DipInput, config: DipTriggerConfig | None = None) -> DipTriggerResult:
    config = config or DipTriggerConfig()
    closes = _validate_prices(inp.closes, "closes")
    headroom, at_cap = _position(inp.weight_pct, config.max_position_pct)

    def result(status, *, drop=None, z=None, signals=(), reasons=()):
        return DipTriggerResult(
            symbol=inp.symbol,
            as_of=inp.as_of,
            status=status,
            drop_pct=None if drop is None else round(drop, 4),
            worst_zscore=None if z is None else round(z, 4),
            forced_signals=tuple(signals),
            reasons=tuple(reasons),
            weight_pct=inp.weight_pct,
            max_position_pct=config.max_position_pct,
            headroom_pct=headroom,
            at_cap=at_cap,
        )

    if len(closes) < config.min_closes:
        return result(
            DipStatus.INSUFFICIENT_DATA,
            reasons=[f"ต้องมีราคาปิดอย่างน้อย {config.min_closes} วัน มี {len(closes)}"],
        )

    window = config.window_days
    drop = (closes[-1] / closes[-1 - window] - 1) * 100
    daily = _returns(closes)
    worst = min(daily[-window:])
    z = _zscore(worst, daily[-window - config.lookback_days : -window])
    reasons = [f"ลง {drop:.1f}% ใน {window} วัน (z-score วันแย่สุด {z:.1f})"]

    if drop > -config.min_drop_pct + EPS or z > -config.min_zscore + EPS:
        return result(DipStatus.NO_DIP, drop=drop, z=z, reasons=reasons)

    signals, signal_reasons = _forced_signals(inp, config)
    reasons += signal_reasons

    if inp.events is not None:
        hits = _fundamental_hits(inp, config)
        if hits:
            reasons += [f"มีข่าวพื้นฐาน: {e.kind} {e.occurred_on.isoformat()} {e.title}".rstrip() for e in hits]
            return result(DipStatus.FUNDAMENTAL, drop=drop, z=z, signals=signals, reasons=reasons)

    if len(signals) < config.min_forced_signals:
        reasons.append(f"หลักฐานการถูกบังคับขาย {len(signals)}/{config.min_forced_signals}")
        return result(DipStatus.NO_FORCED_SELLING, drop=drop, z=z, signals=signals, reasons=reasons)

    if inp.events is None:
        reasons.append("ยังไม่ได้ตรวจข่าวพื้นฐาน ต้องเช็กเองก่อน")
        return result(DipStatus.NEEDS_REVIEW, drop=drop, z=z, signals=signals, reasons=reasons)

    reasons.append(f"ไม่พบข่าวพื้นฐานใน {config.event_lookback_days} วันล่าสุด")
    return result(DipStatus.TRIGGER, drop=drop, z=z, signals=signals, reasons=reasons)


def format_alert_th(result: DipTriggerResult) -> str:
    lines = [f"[Dip trigger] {result.symbol} — {result.status.value} ({result.as_of.isoformat()})"]
    if result.drop_pct is not None:
        lines.append(f"ราคา: {result.drop_pct:.1f}%")
    lines += [f"- {reason}" for reason in result.reasons]
    if result.weight_pct is None:
        lines.append(f"สัดส่วนในพอร์ต: ไม่ทราบ (เพดาน {result.max_position_pct:.1f}%)")
    elif result.at_cap:
        lines.append(f"สัดส่วนในพอร์ต: {result.weight_pct:.1f}% เต็มเพดาน {result.max_position_pct:.1f}% แล้ว ไม่มีที่ให้เพิ่ม")
    else:
        lines.append(
            f"สัดส่วนในพอร์ต: {result.weight_pct:.1f}% เพิ่มได้อีก {result.headroom_pct:.1f}% "
            f"(เพดาน {result.max_position_pct:.1f}%)"
        )
    lines.append("แจ้งเตือนเพื่อการศึกษา ไม่ใช่คำแนะนำการลงทุน ระบบไม่ได้ส่งคำสั่งซื้อ")
    return "\n".join(lines)


def parse_yahoo_chart(
    payload: Mapping[str, Any], *, now: datetime | None = None
) -> tuple[list[date], list[float], list[float]]:
    """Daily bars from a Yahoo v8 chart response.

    Skips bars with no close, and drops the last bar while its regular session
    (``meta.currentTradingPeriod.regular``) is still open: that bar's close is
    only the latest trade and its volume is partial.
    """
    results = (payload.get("chart") or {}).get("result") or []
    if not results:
        raise ValueError("chart response has no result")
    bar = results[0]
    stamps = bar.get("timestamp") or []
    quote = ((bar.get("indicators") or {}).get("quote") or [{}])[0]
    raw_closes = quote.get("close") or []
    raw_volumes = quote.get("volume") or []
    regular = ((bar.get("meta") or {}).get("currentTradingPeriod") or {}).get("regular") or {}
    session_start, session_end = regular.get("start"), regular.get("end")
    now_ts = (now or datetime.now(UTC)).timestamp()
    session_known = session_start is not None and session_end is not None
    if stamps and session_known and stamps[-1] >= session_start and now_ts < session_end:
        stamps = stamps[:-1]

    dates: list[date] = []
    closes: list[float] = []
    volumes: list[float] = []
    for i, stamp in enumerate(stamps):
        close = raw_closes[i] if i < len(raw_closes) else None
        if close is None:
            continue
        volume = raw_volumes[i] if i < len(raw_volumes) else None
        dates.append(datetime.fromtimestamp(stamp, tz=UTC).date())
        closes.append(float(close))
        volumes.append(float(volume or 0))
    if not closes:
        raise ValueError("chart response has no closes")
    return dates, closes, volumes
