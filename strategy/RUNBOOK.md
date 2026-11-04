# Polymarket Paper Bot — Operations Runbook

> **Current mode: CAPITAL PROTECTION / EVIDENCE COLLECTION**
>
> Last updated: 2026-06-20

---

## Dashboard UI (Observe-Only)

The frontend has **5 main pages** as the single source of truth for observing the system. All are observe-only — no Start/Trade/Configure/Reset buttons in the main UX.

| Page | Answers | Key Data |
|------|---------|----------|
| **Today** | What to do now? | Decision, kill switch, capital, next triggers |
| **Evidence** | What happened? | Timeline, gates, paper positions, activity feed |
| **Research** | What's worth watching? | Crypto candidates, Polymarket reviews, intelligence |
| **System** | Is everything healthy? | Component health, risk sources, grid/bot status |
| **Daily Report** | Operational snapshot? | Trade journal, daily PnL, system overview |

**Frontend route consolidation** (Batch 1+2 extraction) is complete. 38 routes have been categorised: 5 keep-main, 14 extracted-to-component, 8 advanced-only, 11 hidden/deferred. The 5 main pages above will NOT gain new action buttons — all future feature extractions go into components or Advanced UI only.

---

## System State Snapshot

| Metric                | Value         |
|-----------------------|---------------|
| Kill switch           | **ACTIVE**    |
| Kill reason           | Paper-bot max drawdown 15.84% (limit 5%) |
| Bankroll              | $84.54 paper bankroll |
| Peak bankroll         | $100.45       |
| Active positions      | 14 (legacy)   |
| Resolved positions    | 6             |
| Max positions allowed | 8             |
| Daily loss limit      | $5            |
| Consecutive loss limit| 3             |

---

## Risk Sources

There are two separate risk systems. Do not mix them when making readiness decisions.

| Source | Scope | Used For |
|--------|-------|----------|
| `paper_bot` | Polymarket / paper-capital state | Kill switch, paper drawdown, active positions, readiness gates |
| `risk_manager` / grid bot | BTCTHB grid state | Grid health, grid drawdown, grid daily limits |

`/api/command-center` is the dashboard source of truth and should use `paper_bot` as the effective kill-switch source while the system is in capital-protection mode.

---

## Daily Monitor Command

```bash
cd domains/product/engineering/book-dev/github/bookchaowalit-ai/book-products/booktrading
docker compose exec strategy python scripts/monitor.py
```

Expected output includes:
- Active / resolved position counts
- Bankroll, PnL, drawdown %
- Kill switch status + reason
- Consecutive losses, daily PnL
- Per-signal PnL breakdown
- Decision recommendation

## Financial Engineering Evidence Run

The Spot/Futures comparison is read-only and does not use trading credentials:

```bash
cd domains/product/engineering/book-dev/github/bookchaowalit-ai/book-products/booktrading
docker compose run --rm strategy python /app/scripts/futures_backtest.py --symbol BTCUSDT --interval 1h --days 90
```

Review the **out-of-sample** segment first. Treat `futures_1x` as the short-access
baseline, then compare 2x/3x/5x for incremental return versus drawdown, fees,
funding, and liquidation count. A positive backtest is evidence for further
paper testing only; it is not approval for mainnet execution.

---

## Decision Tree

```
START
  │
  ├─ Kill switch ACTIVE? ──────────► WAIT
  │                                   └─ Do nothing. Log the state.
  │
  ├─ Active positions > 8? ────────► WAIT
  │                                   └─ Legacy positions need to resolve.
  │                                      Check back daily.
  │
  ├─ Resolved trades < 50%? ───────► WAIT
  │                                   └─ Not enough data to evaluate signals.
  │
  ├─ Per-signal PnL available? ────► REVIEW_SIGNALS
  │                                   └─ Identify worst performers.
  │                                      Disable signals with consistent losses.
  │
  ├─ Signal PnL reviewed,          ► ENABLE_DRY_RUN
  │  positions ≤ 8?
  │                                   └─ Set POLY_DRY_RUN=true
  │                                      Run for 2-4 weeks. Log would-trade entries.
  │
  ├─ Dry-run stable 2-4 weeks? ───► RESET_PAPER
  │                                   └─ Reset paper state. Start fresh paper cycle.
  │
  └─ Paper stable 2-4 weeks        ► MICRO-LIVE (see prerequisites below)
     + all prerequisites met?
```

---

## When NOT to Reset Kill Switch

**Do NOT reset the kill switch if ANY of these are true:**

1. Active positions still > 8 (legacy positions unresolved)
2. You haven't reviewed per-signal PnL for all signal types
3. Dry-run mode hasn't been run for at least 2 weeks
4. Bankroll drawdown cause hasn't been identified
5. You're doing it "just to see what happens"

**The kill switch is working correctly.** It stopped trading at 15.84% paper-bot drawdown — that's the system doing its job. Resetting without evidence is how money is lost.

---

## When to Enable POLY_DRY_RUN=true

Only after ALL of these are met:

- [ ] Kill switch has been reset
- [ ] Active positions ≤ 8
- [ ] Per-signal PnL reviewed — worst signals disabled
- [ ] Market blocklist confirmed: `politics,sports,entertainment,celebrities`
- [ ] You're ready to log would-trade entries daily for 2-4 weeks

Enable with:
```bash
# In .env
POLY_DRY_RUN=true
```

---

## When to Disable Losing Signals

When resolved trades have PnL data:

1. Run monitor — check per-signal PnL breakdown
2. Any signal type with **net PnL < -$5** → disable before any reset/dry-run
3. Add it to `POLY_DISABLED_SIGNALS` or the default disabled signal list
4. Re-run dry-run mode to validate improvement

Current evidence: `momentum` is disabled by default after -$10.93 PnL over 4 resolved trades with 0% win rate.

---

## Micro-Live Prerequisites

**Real money trading is FORBIDDEN until ALL are satisfied:**

- [ ] Dry-run mode stable for **2-4 weeks** with positive expected value
- [ ] Per-signal PnL shows at least 3 signal types profitable
- [ ] Max drawdown in dry-run < 5%
- [ ] Win rate > 52% over 50+ would-trade entries
- [ ] Market blocklist filtering confirmed working
- [ ] Kill switch tested and confirmed responsive
- [ ] Starting capital is money you can 100% afford to lose

Even then: start with **minimum position sizes** ($1-2 per trade).

---

## Emergency Stop & Reset Procedure

### Emergency Stop (halt everything NOW)

```bash
# Stop the bot container
docker compose stop strategy

# Or set kill switch manually via Redis
docker compose exec redis redis-cli SET poly_paper:kill_override 1
```

### Real grid kill (mainnet)

`POST /strategy-api/api/real-grid/kill` (admin) disables the real grid bot,
stops the Go grid loop (`/api/bot/stop`), then cancels every open order on each
configured symbol. A risk-manager halt does the same automatically. Read the
response: `contained: false` means the Go loop was not confirmed stopped or an
order could not be cancelled (`failed` lists them) — check the exchange by hand.

It is also false while an order's outcome is unknown:
- `failed` entries with `reason: go_unresolved` are Go submissions the
  exchange has not confirmed yet (Go lists them in `/api/bot/status`
  `unresolved_orders` and re-checks them on every stop).
- `uncertain` lists Python order POSTs that timed out or got a 5xx. Retry the
  kill after 60 s: an open-orders listing from then on would show the order
  if it went live, and the kill cancels it.

The Go bot stores every traded symbol in `bot_traded_symbols` (migration 023),
so a kill after a backend restart still drains them. If that table cannot be
written, the Go bot refuses to trade.

Two more `failed` reasons:
- `go_running`: the Go bot was started again between the stop and the drain.
  Stop it and retry the kill.
- `other_exchange`: a symbol was traded on another exchange than the current
  one (migration 024 records the exchange), and the drain only reaches the
  current exchange. Cancel the orders on that exchange by hand, then delete its
  rows: `DELETE FROM bot_traded_symbols WHERE exchange = '<exchange>';`

Partly filled SELLs that a kill or stale-order cancel stops are booked like
fills: their realized PnL reaches the daily-loss and drawdown guards and the
journal (`PARTIAL_SELL_CANCELLED`).

Resuming is two explicit steps; neither implies the other:

1. `POST /strategy-api/api/risk/reset` clears the risk halt only.
2. `POST /strategy-api/api/real-grid/enable` resumes trading; it returns 409
   while the risk manager is still halted.

### Reset Kill Switch (ONLY when decision tree says GO)

```bash
# 1. Verify conditions are met (run monitor first)
docker compose exec strategy python scripts/monitor.py

# 2. If monitor says REVIEW_SIGNALS or better, reset via Python
docker compose exec strategy python -c "
from app.polymarket.paper_bot import PolymarketPaperBot
bot = PolymarketPaperBot()
bot.reset_kill_switch()
print('Kill switch reset. Monitor closely.')
"
```

### Full Paper Reset (nuclear option — clears all positions)

```bash
# ONLY after dry-run validation is complete
docker compose exec redis redis-cli DEL poly_paper:state
```

**WARNING:** This deletes all position history. Only do this when you're starting a completely fresh paper cycle after dry-run validation.

---

## Safety Commits

| Commit    | Description                          |
|-----------|--------------------------------------|
| `1aa12d8` | Readiness safeguards: kill switch, dry-run, blocklist/allowlist |
| `b3e0907` | Monitor tooling: daily monitoring script |
| `fcd4cf5` | Safety filter tests: 20 tests + dry-run AlphaSignal bug fix |

Full test suite covers kill-switch, safety filters, monitor decision logic, grid safety, and `/api/command-center` contract checks.

---

## Key Files

| File | Purpose |
|------|---------|
| `strategy/app/polymarket/paper_bot.py` | Core bot logic |
| `strategy/scripts/monitor.py` | Daily monitoring script |
| `strategy/tests/test_kill_switch.py` | 13 kill switch tests |
| `strategy/tests/test_monitor_decision.py` | 27 monitor decision tree tests |
| `strategy/tests/test_safety_filters.py` | 20 safety filter tests |
| `strategy/tests/test_command_center_contract.py` | Command Center API safety contract tests (`pytest -m integration`) |
| `strategy/RUNBOOK.md` | This file |
| `docs/READINESS_CHECKLIST.md` | Versioned gate criteria for each stage |
| `docs/EVIDENCE_LOG.md` | State-change evidence log |

---

## Rules

1. **No real money** until micro-live prerequisites are all checked
2. **No kill switch reset** without evidence from per-signal PnL
3. **No architecture changes** while in capital protection mode
4. **Monitor daily** — even if nothing changed, log the state
5. **Small changes only** — tests, docs, config. No big refactors.
6. **Never set `POLY_DRY_RUN=false`** for real money until RUNBOOK micro-live prerequisites are met. See `.env.example` for safe-by-default config.
