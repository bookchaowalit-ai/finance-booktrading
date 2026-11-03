# ADR-0004 — Capital protection: observe-only UI, kill switch ใน strategy, safe defaults

- Status: accepted (retro-documented 2026-11-03; amended 2026-11-03 after the kill / risk-per-trade fixes)

## Context

README: "The trading bot is halted by design. No real money is at risk." RUNBOOK ให้ kill switch เป็น gate ที่ต้อง reset
ด้วยมือพร้อมหลักฐาน

## Decision

- ทุก real grid order ผ่าน `RiskManager.check_order_allowed()` (`strategy/app/risk_manager.py`) ก่อน `POST /api/trade/order`: halted → block; daily loss, consecutive losses, drawdown → `_halt()` ซึ่งปิด real grid bot ด้วย; order size cap (`max_order_size_thb`). Real grid **BUY** ส่ง `current_balance_thb` = free quote + free base × ราคา (`_account_value_quote`) จึงบังคับ `risk_per_trade_pct` ได้จริง; balance ไม่รู้ค่า → block BUY. SELL ไม่ถูก cap เพราะลด exposure (`test_real_grid_risk_per_trade.py`).
- Halt ไม่ถูกล้างโดย daily reset — ต้อง `reset_kill_switch()` (admin, `POST /api/risk/reset` ผ่าน proxy).
- Kill = containment: `RealGridBot.kill()` / `contain()` หยุด Go grid loop ก่อน (`POST /api/bot/stop`, service route; 409 = ไม่ได้รันอยู่ นับว่าหยุดแล้ว) แล้ว cancel open order **ทุกตัว** ของทุก symbol ที่ตั้งค่าไว้ ก่อน drain รอ order ที่กำลังวางอยู่ให้จบ (สูงสุด 30 s, `inflight_settled`) และทุก placement เช็ค `_enabled` ซ้ำก่อน POST. Go `Stop` รอ loop ของ run นั้นออกจริงก่อนตอบ (30 s; ถ้าไม่ออก → 500 และ retry ไม่ตอบ "not running" จนกว่า loop จะจบ; `Start` ถูกปฏิเสธระหว่างนั้น). ระหว่าง containment `enable()` ถูกปฏิเสธ. Order ที่ fill บางส่วนอยู่ใน `partial_fills`, journal ยังเปิด, BUY ส่วนที่ซื้อแล้วกลายเป็น lot ที่ stop-loss คุ้มครอง. kill หลายครั้งพร้อมกันใช้ containment task เดียว; drain ถือ lock เดียวกับ tick (ไม่ชนกับ sync/stale cancel) และอ่าน symbol ของ Go หลัง stop; order ที่ cancel แล้วแต่ยังไม่รู้ executed qty ถูกเช็คซ้ำทุก containment จนกว่าจะรู้; drain ทั้ง `REAL_SYMBOLS` และทุก symbol ที่ Go bot เทรดใน run ล่าสุด (`/api/bot/status` `symbols`, จำไว้จนกว่า containment สำเร็จ; อ่าน status ไม่ได้ → ไม่ contained); หลัง cancel อ่าน executed qty สุดท้ายจาก order-status (อ่านไม่ได้ → `unreconciled`, order ยังอยู่ใน state ให้ sync, ไม่ contained); ทุก SELL ที่ cancel แล้วอ่าน executed qty ก่อน re-arm stop หรือลดขนาด lot. ยังไม่ทำ: realized PnL ของ sell ที่ fill บางส่วนแล้ว cancel (journal ค้างเปิด, ต้อง review); lot ที่ fill บางส่วนเก็บขนาดจริงใน `lot_qty` และ stop-loss ขายตามขนาดนั้น. ตอบกลับ `{go_bot_stopped, inflight_settled, cancelled, failed, partial_fills, unreconciled, contained}`; HTTP 503 เมื่อ `contained` false. Stop-loss re-arm เฉพาะเมื่อยืนยันว่า sell ถูก cancel (containment, sync verdict `cancelled`, halted reconcile, stale/rebalance cancel) — order ที่หายไปโดยไม่รู้ผลไม่ re-arm เพราะอาจ fill แล้ว (`test_real_grid_stop_loss_rearm.py`). `_halt()` เรียก `disable()` ซึ่ง schedule `contain()` เช่นกัน.
- Reset เป็นสองขั้นแยกกัน: `POST /api/risk/reset` ล้าง halt อย่างเดียว (ไม่เปิด bot); `POST /api/real-grid/enable` เปิด bot และตอบ 409 ถ้า risk manager ยัง halted (`test_real_grid_kill.py`).
- Safe defaults ใน code: `SIGNAL_ORDER_EXECUTION_ENABLED=false`, `FUTURES_ENABLED=false` และ futures mainnet ถูก refuse, real/dca/trend bots เป็น safety mode เว้นแต่ `BINANCE_TH_USE_TESTNET=false` ชัดเจน (`test_mainnet_default.py`).
- UI: core 5 หน้าเป็น read-only; หน้าที่มี action ซ่อนหลัง `NEXT_PUBLIC_ADVANCED_UI=true`.

## Consequences

- `contained: false` (Go stop ไม่ยืนยัน หรือ cancel ไม่สำเร็จ) ต้องเช็ค exchange เอง; order ที่ cancel ไม่ได้ยังอยู่ใน local state ให้ reconcile เห็น.
- ที่ `risk_per_trade_pct=1.0` (default) กับ min notional 100 THB ของ Binance TH, BUY ต้องมีมูลค่าใน pair ≥ 10,000 THB — ทุนปัจจุบัน (~555 THB) จึงวาง real grid BUY ไม่ได้จนกว่า owner จะปรับ cap หรือทุน.
- Gate อยู่ฝั่ง Python เท่านั้น: `/api/trade/order` ฝั่ง Go รับ service token แล้วส่ง order ทันที (ไม่มี risk check ฝั่ง Go).
- `POLY_DRY_RUN` default ใน code คือ `false` (`paper_bot.py`) — README บอกว่า "enforced" แต่การบังคับขึ้นกับ env; paper bot ถูก comment out ใน `lifespan` อยู่แล้ว.
- มีสอง risk source (`paper_bot` และ `risk_manager`) ที่ RUNBOOK เตือนว่าห้ามปนกัน.

## Evidence

`README.md` (Current Operating Mode), `strategy/RUNBOOK.md`, `docs/READINESS_CHECKLIST.md`, `strategy/app/risk_manager.py`, `strategy/app/real_grid_bot.py`, `strategy/infrastructure/api/app.py` (`SIGNAL_ORDER_EXECUTION_ENABLED`, `lifespan`), `strategy/app/polymarket/paper_bot.py` (`DRY_RUN_MODE`), `strategy/tests/test_kill_switch.py`, `test_real_grid_halt.py`, `test_real_grid_kill.py`, `test_real_grid_risk_per_trade.py`, `backend/internal/adapter/http/bot_stop_test.go`, `test_mainnet_default.py`, `test_futures_safety.py`, `frontend/src/components/Sidebar.tsx`.
