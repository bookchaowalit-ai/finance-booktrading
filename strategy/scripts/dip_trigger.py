#!/usr/bin/env python3
"""Non-fundamental dip trigger (SPEC-003). Observe-only: prints or sends an alert, never orders.

Run from strategy/:

    uv run python scripts/dip_trigger.py NVDA 000660.KS --events events.json --weights weights.json
    uv run python scripts/dip_trigger.py PTT.BK --send     # also post to Telegram (TELEGRAM_BOT_TOKEN/CHAT_ID)

events.json: [{"symbol": "NVDA", "kind": "earnings", "occurred_on": "2026-11-05", "title": "Q3"}]
             Pass [] after checking the news and finding nothing. Without --events every hit is NEEDS_REVIEW.
weights.json: {"NVDA": 12.5}  (percent of portfolio)
margin.json: {"NVDA": {"before": 1000, "now": 850}}
"""

from __future__ import annotations

import argparse
import asyncio
import json
import sys
from collections.abc import Awaitable, Callable
from pathlib import Path

import httpx

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from app.market_intel.dip_trigger import (
    DipInput,
    DipStatus,
    DipTriggerConfig,
    DipTriggerResult,
    FundamentalEvent,
    canonical_symbol,
    evaluate_dip,
    format_alert_th,
    parse_yahoo_chart,
)

YF_CHART_URL = "https://query1.finance.yahoo.com/v8/finance/chart/{symbol}"
ALERT_STATUSES = {DipStatus.TRIGGER, DipStatus.NEEDS_REVIEW}


def default_benchmark(symbol: str) -> str:
    symbol = canonical_symbol(symbol)
    if symbol.endswith(".BK"):
        return "^SET.BK"
    if symbol.endswith((".KS", ".KQ")):
        return "^KS11"
    return "SPY"


async def fetch_chart(client: httpx.AsyncClient, symbol: str):
    resp = await client.get(
        YF_CHART_URL.format(symbol=symbol),
        params={"interval": "1d", "range": "6mo"},
        headers={"User-Agent": "bookfinance-dip-trigger"},
    )
    resp.raise_for_status()
    return parse_yahoo_chart(resp.json())


def _load(path: str | None, default):
    return json.loads(Path(path).read_text(encoding="utf-8")) if path else default


def canonical_keys(mapping: dict) -> dict:
    return {canonical_symbol(str(key)): value for key, value in mapping.items()}


async def evaluate_symbol(
    raw_symbol: str,
    *,
    fetch: Callable[[str], Awaitable[tuple[list, list, list]]],
    benchmark: str | None,
    events: list[FundamentalEvent] | None,
    weights: dict,
    margins: dict,
    config: DipTriggerConfig,
) -> DipTriggerResult | None:
    """Evaluate one symbol; None when the stock's own bars cannot be fetched.

    A benchmark failure only drops the benchmark signal; the stock is still evaluated.
    ``weights`` and ``margins`` must already be canonicalised (see ``canonical_keys``).
    """
    symbol = canonical_symbol(raw_symbol)
    try:
        dates, closes, volumes = await fetch(symbol)
    except (httpx.HTTPError, ValueError) as exc:
        print(f"{symbol}: fetch failed ({exc})", file=sys.stderr)
        return None

    bench_symbol = canonical_symbol(benchmark) if benchmark else default_benchmark(symbol)
    bench_dates = bench_closes = None
    try:
        bench_dates, bench_closes, _ = await fetch(bench_symbol)
    except (httpx.HTTPError, ValueError) as exc:
        print(f"{symbol}: benchmark {bench_symbol} fetch failed ({exc}); skipping that signal", file=sys.stderr)

    margin = margins.get(symbol) or {}
    return evaluate_dip(
        DipInput(
            symbol=symbol,
            as_of=dates[-1],
            closes=closes,
            volumes=volumes,
            benchmark_closes=bench_closes,
            dates=dates,
            benchmark_dates=bench_dates,
            events=events,
            margin_balance_before=margin.get("before"),
            margin_balance_now=margin.get("now"),
            weight_pct=weights.get(symbol),
        ),
        config,
    )


async def run(args: argparse.Namespace) -> int:
    events = None
    if args.events:
        events = [FundamentalEvent.from_mapping(raw) for raw in _load(args.events, [])]
    weights = canonical_keys(_load(args.weights, {}))
    margins = canonical_keys(_load(args.margin, {}))
    config = DipTriggerConfig(max_position_pct=args.cap)

    alerts: list[str] = []
    async with httpx.AsyncClient(timeout=15.0) as client:

        async def fetch(symbol: str):
            return await fetch_chart(client, symbol)

        for raw_symbol in args.symbols:
            result = await evaluate_symbol(
                raw_symbol,
                fetch=fetch,
                benchmark=args.benchmark,
                events=events,
                weights=weights,
                margins=margins,
                config=config,
            )
            if result is None:
                continue
            text = format_alert_th(result)
            print(text, end="\n\n")
            if result.status in ALERT_STATUSES:
                alerts.append(text)

    if args.send and alerts:
        from app.telegram_bot import TelegramBot

        bot = TelegramBot()
        for text in alerts:
            if not await bot.send_message(text, parse_mode=""):
                print("Telegram send failed or not configured", file=sys.stderr)
                return 1
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("symbols", nargs="+", help="Yahoo symbols, e.g. NVDA 000660.KS PTT.BK")
    parser.add_argument("--benchmark", help="override the benchmark (default by suffix: SPY, ^SET.BK, ^KS11)")
    parser.add_argument("--events", help="JSON list of fundamental events; [] means 'checked, none'")
    parser.add_argument("--weights", help="JSON map symbol -> portfolio weight in percent")
    parser.add_argument("--margin", help="JSON map symbol -> {before, now} margin balance")
    parser.add_argument("--cap", type=float, default=15.0, help="max position percent (default 15)")
    parser.add_argument("--send", action="store_true", help="send TRIGGER / NEEDS_REVIEW alerts to Telegram")
    return asyncio.run(run(parser.parse_args()))


if __name__ == "__main__":
    raise SystemExit(main())
