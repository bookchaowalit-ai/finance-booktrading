---
title: "SPEC-003 — Non-fundamental dip trigger (observe-only)"
status: implemented
tags: [spec, sdd, tdd, market-intel, stocks, alert]
created: 2026-11-07
updated: 2026-11-07
description: "Flag a stock that fell hard for reasons other than its fundamentals (forced selling), and show how much room the portfolio cap leaves. Alerts only, never orders."
---

# SPEC-003 — Non-fundamental dip trigger

Standard: ADR-0004 (TDD + SDD). Source idea: ALPHAMIND Podcast EP.11 (published 2026-08-14), the guest's rule
"the best time to buy is when a stock falls for reasons unrelated to its fundamentals", e.g. forced selling,
with each position capped at 10–20 % of the portfolio. The code is a general rule. It is not advice about any market move.

## 1. Problem and intent

- **Problem:** the stock scanner (`app/market_intel/sources/stocks.py`) only flags "moved more than 3 % today". It cannot
  tell a forced-selling dip from a drop caused by bad news, and it knows nothing about position size.
- **Outcome:** a pure, deterministic detector `app/market_intel/dip_trigger.py` plus an observe-only script
  `scripts/dip_trigger.py`. They never place orders, which keeps the Capital Protection mode intact.

## 2. Rule

All three gates must pass for a `TRIGGER`:

| Gate | Pass when (defaults in `DipTriggerConfig`) |
|---|---|
| G1 Hard drop | close-to-close return over `window_days` (3) ≤ −`min_drop_pct` (8 %) **and** the worst daily return in the window has a z-score ≤ −`min_zscore` (2.0) against the prior `lookback_days` (60) daily returns |
| G2 Not fundamental | no event of a fundamental type (earnings, guidance, rating change, company news, regulatory, corporate action) for the symbol in the last `event_lookback_days` (5) |
| G3 Forced-selling evidence | at least `min_forced_signals` (2) of: benchmark (market or sector) fell ≥ `min_benchmark_drop_pct` (3 %) in the same window; peak volume in the window ≥ `min_volume_ratio` (2×) the prior 20-day average; margin balance fell ≥ `min_margin_drop_pct` (10 %) |

Fail-closed rules:
- Too little history (fewer than `lookback_days + window_days + 1` closes) → `INSUFFICIENT_DATA`.
- G1 fails → `NO_DIP`.
- A fundamental event in range → `FUNDAMENTAL` (no buy signal; the alert names the event).
- Event data not supplied (`events=None`) while G1 and G3 pass → `NEEDS_REVIEW`: a human must check the news first.
- G3 fails → `NO_FORCED_SELLING`.

Position check (does not change the status): `weight_pct` vs `max_position_pct` (15 %). The result reports the
headroom; at or over the cap it says "no room" (`at_cap=True`).

## 3. Acceptance

| ID | Given / when / then | Test |
|---|---|---|
| AC-1 | 60 calm days then −10 % over 3 days with SPY −5 % and 3× volume, events `[]` → `TRIGGER` | `tests/test_dip_trigger.py` |
| AC-2 | Same drop with an earnings event 2 days ago → `FUNDAMENTAL`, reason names the event | same |
| AC-3 | Same drop with `events=None` → `NEEDS_REVIEW` | same |
| AC-4 | −4 % drop → `NO_DIP` | same |
| AC-5 | Drop with flat benchmark and normal volume → `NO_FORCED_SELLING` | same |
| AC-6 | Only 1 forced signal, but margin balance −15 % adds the second → `TRIGGER` | same |
| AC-7 | Fewer closes than needed → `INSUFFICIENT_DATA` | same |
| AC-8 | Weight 18 % with cap 15 % → `at_cap=True`, headroom 0; weight 5 % → headroom 10 | same |
| AC-9 | Event older than the lookback, or for another symbol → ignored | same |
| AC-10 | Thai alert text has the symbol, status, drop, reasons, headroom and the "not advice, no order placed" line | same |
| AC-11 | The script parses Yahoo chart JSON into closes and volumes and skips null bars (no network in tests) | same |
| AC-12 | With dates given, the benchmark return uses the stock's own window dates; no exact match → signal skipped with a reason | same |
| AC-13 | The parser drops the last bar while its regular session is still open (`meta.currentTradingPeriod`) | same |
| AC-14 | Symbols are matched case- and space-insensitively (`nvda` == `NVDA`) | same |
| AC-15 | Exact boundaries count: 100 → 92 is a −8 % drop; margin 1000 → 900 is −10 % (tolerance `EPS`) | same |

| AC-16 | A missing volume stays `None`; any gap in the 20-day baseline or the window skips the volume signal | same |
| AC-17 | The script still evaluates the stock when only the benchmark fetch fails (benchmark signal dropped) | same |
| AC-18 | CLI symbols and `--weights` / `--margin` keys are canonicalised before benchmark choice and lookups | same |

| AC-19 | A bar skipped for a null close leaves the next bar's volume `None`, so the gap still blocks the volume signal | same |
| AC-20 | Bar dates are calendar days in `meta.exchangeTimezoneName` (UTC if unknown) | same |

AC-12 to AC-20 came from three Codex review rounds on 2026-11-07. Red first each round (8, 6, then 2 new tests failed), then passed.

## 4. Out of scope (follow-up)

- No news feed is wired: callers pass events as JSON (`--events`). Without it the result is `NEEDS_REVIEW`.
- No margin-balance feed. Pass it with `--margin` when known.
- No schedule or deploy. Run it by hand or from a cron later.
