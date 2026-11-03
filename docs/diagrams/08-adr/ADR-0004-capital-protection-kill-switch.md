# ADR-0004 — Capital protection: observe-only UI, kill switch ใน strategy, safe defaults

- Status: accepted (retro-documented 2026-11-03)

## Context

README: "The trading bot is halted by design. No real money is at risk." RUNBOOK ให้ kill switch เป็น gate ที่ต้อง reset
ด้วยมือพร้อมหลักฐาน

## Decision

- ทุก real grid order ผ่าน `RiskManager.check_order_allowed()` (`strategy/app/risk_manager.py`) ก่อน `POST /api/trade/order`: halted → block; daily loss, consecutive losses, drawdown → `_halt()` ซึ่งปิด real grid bot ด้วย; order size cap (`max_order_size_thb`). Check risk per trade (`risk_per_trade_pct`) ทำงานเฉพาะเมื่อส่ง `current_balance_thb > 0` — `RealGridBot` เรียก `check_order_allowed()` โดยไม่ส่งค่านี้ (default 0) จึง **ไม่ได้บังคับ risk per trade กับ real grid order**.
- Halt ไม่ถูกล้างโดย daily reset — ต้อง `reset_kill_switch()` (admin, `POST /api/risk/reset` ผ่าน proxy).
- Safe defaults ใน code: `SIGNAL_ORDER_EXECUTION_ENABLED=false`, `FUTURES_ENABLED=false` และ futures mainnet ถูก refuse, real/dca/trend bots เป็น safety mode เว้นแต่ `BINANCE_TH_USE_TESTNET=false` ชัดเจน (`test_mainnet_default.py`).
- UI: core 5 หน้าเป็น read-only; หน้าที่มี action ซ่อนหลัง `NEXT_PUBLIC_ADVANCED_UI=true`.

## Consequences

- Kill / halt แค่ตั้ง `_enabled = False` (`RealGridBot.disable()`); ไม่ cancel open order ที่ exchange — limit order ที่ค้างอยู่ยัง fill ได้หลัง kill.
- `RealGridBot.enable()` เรียก `reset_kill_switch()` ด้วย จึงการกด enable = ล้าง halt ของ risk manager ไปในตัว.
- Gate อยู่ฝั่ง Python เท่านั้น: `/api/trade/order` ฝั่ง Go รับ service token แล้วส่ง order ทันที (ไม่มี risk check ฝั่ง Go).
- `POLY_DRY_RUN` default ใน code คือ `false` (`paper_bot.py`) — README บอกว่า "enforced" แต่การบังคับขึ้นกับ env; paper bot ถูก comment out ใน `lifespan` อยู่แล้ว.
- มีสอง risk source (`paper_bot` และ `risk_manager`) ที่ RUNBOOK เตือนว่าห้ามปนกัน.

## Evidence

`README.md` (Current Operating Mode), `strategy/RUNBOOK.md`, `docs/READINESS_CHECKLIST.md`, `strategy/app/risk_manager.py`, `strategy/app/real_grid_bot.py`, `strategy/infrastructure/api/app.py` (`SIGNAL_ORDER_EXECUTION_ENABLED`, `lifespan`), `strategy/app/polymarket/paper_bot.py` (`DRY_RUN_MODE`), `strategy/tests/test_kill_switch.py`, `test_real_grid_halt.py`, `test_mainnet_default.py`, `test_futures_safety.py`, `frontend/src/components/Sidebar.tsx`.
