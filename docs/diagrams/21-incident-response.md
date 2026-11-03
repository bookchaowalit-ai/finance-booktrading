# 21 — Incident Response

ไม่มี incident runbook เฉพาะใน repo; flow ข้างล่างประกอบจาก alert channel และเครื่องมือที่มีจริง
และจาก incident ที่เคยบันทึกใน `docs/EVIDENCE_LOG.md` (2026-07-23: `real_trades` บันทึกผิด + Redis ไม่ restart หลัง host reboot)

```mermaid
flowchart TD
    subgraph Detect["Detect"]
        A1["Strategy WebhookNotifier: kill switch, fill, error -> Telegram / Discord"]
        A2["Sentry frontend error"]
        A3["Operator runs scripts/monitor.py or opens System page"]
        A4["Backend logs: docker compose logs -f backend"]
    end
    subgraph Contain["Contain"]
        C1{"Real money at risk?"}
        C2["Admin POST /strategy-api/api/real-grid/kill"]
        C3["RiskManager already halted: leave halted"]
        C4["List open orders: GET /api/trade/open-orders per symbol"]
        C5["Cancel each: POST /api/trade/cancel-order, or cancel on exchange UI"]
        C6{"Open orders = 0?"}
    end
    subgraph Triage["Triage"]
        T1["Read docker logs, infra/scripts/monitor.sh"]
        T2["Compare real_trades vs trade_journal vs exchange order API"]
        T3["Check /api/command-center decision and risk sources"]
    end
    subgraph Fix["Fix and deploy"]
        F1["Test-first fix on main, CI green"]
        F2["Full stack: workflow_dispatch deploy_production=true"]
        F3["Frontend only: vercel deploy --prod"]
    end
    subgraph Learn["Record"]
        P1["Entry in docs/EVIDENCE_LOG.md: context, bugs, verification"]
        P2["Gate status in docs/READINESS_CHECKLIST.md"]
        P3["Kill switch reset only per strategy/RUNBOOK.md rules"]
    end
    A1 --> C1
    A2 --> T1
    A3 --> C1
    A4 --> T1
    C1 -->|"yes"| C2
    C1 -->|"already halted"| C3
    C1 -->|"no"| T1
    C2 --> C4
    C3 --> C4
    C4 --> C5 --> C6
    C6 -->|"no"| C4
    C6 -->|"yes, contained"| T1
    T1 --> T2 --> T3 --> F1
    F1 --> F2
    F1 --> F3
    F2 --> P1
    F3 --> P1
    P1 --> P2 --> P3
```

Sources: `strategy/app/webhook_notifier.py` (`send_kill_switch_alert`, `send_fill_alert`, `send_error_alert`), `strategy/app/real_grid_bot.py`, `frontend/src/lib/sentry.ts`, `strategy/scripts/monitor.py`, `infra/scripts/monitor.sh`, `PRODUCTION.md` (Troubleshooting), `strategy/RUNBOOK.md` ("When NOT to Reset Kill Switch"), `docs/EVIDENCE_LOG.md` (2026-07-23 entry: cross-check กับ Binance TH `GET /api/v1/order`, LEFT JOIN `real_trades` ↔ `trade_journal`), `docs/READINESS_CHECKLIST.md`, `.github/workflows/ci-cd.yml`, `RUNBOOK.md` (Owner: "Solo operator, no multi-person on-call").

Kill / halt ไม่ cancel order: `RealGridBot.disable()` แค่ตั้ง `_enabled = False` และ `RiskManager._halt()` เรียก `disable()` (`strategy/app/real_grid_bot.py`, `strategy/app/risk_manager.py`, `/api/real-grid/kill` ใน `app.py`) — limit order ที่ค้างที่ exchange ยัง fill ต่อได้ จึงต้องตรวจและ cancel open orders ก่อนถือว่า contained. Endpoint ที่ใช้ได้: `/api/trade/open-orders`, `/api/trade/cancel-order` (service route; admin session หรือ service token, `route_access.go`) หรือ `infra/scripts/monitor.sh` ที่นับ open orders ของ 5 symbol.

## Roles

| Role | Who | Source |
|---|---|---|
| On-call, incident commander, fixer | owner คนเดียว | `RUNBOOK.md` |
| Approver for kill-switch reset | owner, ตาม gate checklist ("Do not self-approve") | `docs/READINESS_CHECKLIST.md` |

## Open questions

- Severity levels, response-time target และ postmortem template ยังไม่กำหนด — ใช้ EVIDENCE_LOG แทนอยู่ ต้องการแยก postmortem หรือไม่?
- Alert ไม่มีจาก backend (AlertService ปิด default) และไม่มี uptime monitor — host down จะไม่มีใครรู้จนกว่า operator เปิดดูเอง.
- Rollback: ไม่มี image tag; full stack rollback ต้อง `git checkout` commit เก่าบน host แล้ว `up -d --build` (ไม่มีขั้นตอนเขียนไว้).
