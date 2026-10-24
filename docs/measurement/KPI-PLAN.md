---
title: "BookTrading — KPI and Instrumentation Plan"
status: approved
created: 2026-10-22
updated: 2026-10-22
description: "How BookTrading's paper and live trading results are measured, and which guardrails stop it."
template: architecture-docs templates/sdlc/kpi-and-instrumentation-plan.md
---

# BookTrading — KPI and Instrumentation Plan

## Document control

| Field | Value |
|---|---|
| Product | bookchaowalit-ai/finance-booktrading (https://booktrading.bookchaowalit.com) |
| Version / status | 0.1 / **approved** (2026-10-24) — definitions only; baselines not captured |
| Metric owner | Owner (bookchaowalit) |
| AI maker | Claude Code (Wave 3 session, 2026-10-22) |
| Analytics source of truth | backend PostgreSQL/TimescaleDB (orders, fills, positions) and the strategy paper journal (`app/alpha/durable_journal.py`); Prometheus metrics from the backend; Sentry for frontend errors |
| Next review | 2026-11-15 (Milestone M1) |

## KPI tree

```text
OBJ-01 Run strategies that make money after costs without unacceptable risk, proven on paper first
  └─ KPI-O-01 Net P&L after fees per strategy per month (paper, then live)
       ├─ KPI-L-01 Win rate and average win / average loss per strategy (rolling 30 days)
       ├─ KPI-L-02 Backtest-to-paper drift: paper return − backtest return for the same window
       ├─ KPI-G-01 Maximum drawdown per strategy (must stay under the configured limit)
       └─ KPI-G-02 Order error rate: rejected or failed orders / orders sent
```

## KPI definitions

| KPI ID | Type | Formula | Source | Baseline | Target | Alert |
|---|---|---|---|---|---|---|
| KPI-O-01 | outcome | Σ realised P&L − fees, per strategy, per calendar month | fills (live) / paper journal settlements | not yet measured | > 0 for 3 consecutive paper months before any live capital | negative month |
| KPI-L-01 | leading | wins / closed trades; mean(win) / mean(loss) | fills / paper journal | not yet measured | set per strategy from backtests | — |
| KPI-L-02 | leading | paper return − backtest return, same window | paper journal + `research/` backtests | not yet measured | within ±25% of backtest | outside ±50% |
| KPI-G-01 | guardrail | max peak-to-trough equity decline | equity curve from positions | not yet measured | below the strategy's configured limit | limit reached → strategy stops |
| KPI-G-02 | guardrail | failed orders / orders sent | backend order log, Prometheus | not yet measured | < 1% | > 5% in a day |

A rate is "not measurable" below 30 closed trades in the window.

## Event and data specification

All KPIs come from server-side records the system already writes (orders, fills, positions, the paper journal). No
client analytics events are needed, and no personal data is involved (single owner account).

## Instrumentation verification (before G10)

| # | Check | Result |
|---|---|---|
| 1–2 | Report queries reconcile with fills and the paper journal | not run: queries not written yet |
| 3 | No personal data | by design |
| 4–5 | Dashboard and drawdown alert | not set up as a KPI dashboard (backend exposes Prometheus metrics) |
| 6 | Baselines captured | waived — dated action: first monthly paper report by 2026-11-30 |
| 7 | Consent | not applicable (no third-party users) |

## Approval (2026-10-24)

| Field | Value |
|---|---|
| Checker | independent Claude reviewer subagent (`solo:solo-reviewer`, Sonnet, read-only, no maker context) |
| Approver | AI approval under the owner's process (approvals by the owner or AI with evidence; maker and checker are different agents). Recorded by Claude session oa-remainder on the owner's instruction "ทำที่เหลือให้หน่อย" (2026-10-24) |
| Verdict | **approved** |
| Evidence | strategy/app/alpha/durable_journal.py; backend metrics.go (trades_failed_total); migrations (orders, paper_*, real_trades); risk_config.go MaxDrawdownPercent; strategy/research/futures_backtest.py; frontend/src/lib/sentry.ts |
