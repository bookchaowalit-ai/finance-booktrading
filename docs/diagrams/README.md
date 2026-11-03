# Core 22 SDLC Diagram Pack — finance-booktrading (BookFinance)

ชุด 22 visual ตาม SDLC Diagram Catalog v2 (knowledge-base `knowledge/tech/core-cs/sdlc-diagram-catalog.md`) วาดจาก code และ config
ที่ `origin/main` **425b989** (2026-10-24), ตรวจ live 2026-11-03. Live: https://booktrading.bookchaowalit.com (Tier A, Vercel frontend เท่านั้น).
ทุกภาพมี `Sources:` เป็น path ใน repo; สิ่งที่ไม่รู้อยู่ใน "Open questions" ของแต่ละไฟล์

Central flow pack (read-only, pinned 8de42ee, 2026-08-19): `bookchaowalit/architecture-docs` → `docs/architecture/flows/booktrading-ui/`.
ถูก reuse ใน 01, 02, 03, 04, 10, 11, 13, 16 — และจุดที่ **ล้าสมัยแล้ว** ถูกระบุในไฟล์นั้นๆ (AuthZ layer 6, order sequence layer 3, paper-bot reset bug layer 4/7, test counts layer 8)

| # | Item | Status | File | Sources read |
|---|---|---|---|---|
| 1 | Business process (As-Is / To-Be) | done (reuses flow pack L1) | [01-business-process.md](01-business-process.md) | `strategy/infrastructure/api/app.py`, `strategy/app/real_grid_bot.py`, `strategy/app/risk_manager.py`, `backend/internal/adapter/http/trade_handler.go`, `strategy/RUNBOOK.md`, `docs/READINESS_CHECKLIST.md`, `docs/UPGRADE-PLAN.md` |
| 2 | Use cases | done (flow pack L2/L6 stale) | [02-use-cases.md](02-use-cases.md) | `route_access.go`, `handler.go`, `auth_handler.go`, `registration.go`, `telegram_bot_service.go`, `Sidebar.tsx` |
| 3 | User flows | done (extends flow pack L2) | [03-user-flows.md](03-user-flows.md) | `frontend/src/app/**`, `LoginModal.tsx`, `dashboard/layout.tsx`, `Sidebar.tsx`, `services/auth.ts`, `services/monitoring.ts` |
| 4 | Traceability (RTM) | done (flow pack L8 stale) | [04-traceability.md](04-traceability.md) | `specs/*/spec.md`, `docs/UPGRADE-PLAN.md`, `.github/workflows/ci-cd.yml`, test files |
| 5 | C4 Context | done | [05-c4-context.md](05-c4-context.md) | `backend/cmd/main.go`, exchange adapters, `config.go`, `strategy/app/**` clients, `sentry.ts` |
| 6 | C4 Container | done | [06-c4-container.md](06-c4-container.md) | `docker-compose.prod.yml`, `Caddyfile`, `next.config.js`, `go.mod`, `app.py` lifespan |
| 7 | C4 Component | done | [07-c4-component.md](07-c4-component.md) | `backend/cmd/main.go`, `backend/internal/**`, `strategy/infrastructure/api/app.py`, `strategy/app/**` |
| 8 | ADRs | done (6 ADRs) | [08-adr/](08-adr/) | see each ADR "Evidence" |
| 9 | ERD | done | [09-erd.md](09-erd.md) | `backend/migrations/*.up.sql` (22), `durable_journal.py`, `redis_adapter.go`, `wallet_service.go` |
| 10 | Sequences | done (corrects flow pack L3) | [10-sequences.md](10-sequences.md) | `auth_handler.go`, `real_grid_bot.py`, `trade_handler.go`, `strategy_proxy.go`, `monitoring/page.tsx` |
| 11 | State machines | done (flow pack L4 bug now fixed) | [11-state-machines.md](11-state-machines.md) | `model/*.go`, `implementation.go`, `grid_orders.go`, `dca_service.go`, `risk_manager.py`, `paper_bot.py` |
| 12 | API and integrations | done (reuses `API.md`) | [12-api-integration.md](12-api-integration.md) | `route_access.go`, handlers, `strategy_proxy.go`, `app.py`, `proto/trading.proto`, external clients |
| 13 | Threat model (DFD + STRIDE) | done | [13-threat-model.md](13-threat-model.md) | `Caddyfile`, `route_access.go`, `auth_handler.go`, `broadcaster.go`, `grpc_server.go`, `api_keys.go`, `wallet_service.go`, `sentry.ts` |
| 14 | Backup / DR | done (RTO/RPO not defined) | [14-backup-dr.md](14-backup-dr.md) | `docker-compose.prod.yml`, `PRODUCTION.md`, `ci-cd.yml`, `docs/EVIDENCE_LOG.md`, `RUNBOOK.md` |
| 15 | CI/CD pipeline | done | [15-cicd-pipeline.md](15-cicd-pipeline.md) | `.github/workflows/ci-cd.yml`, `gh run list` |
| 16 | Deployment | done (reuses `PRODUCTION.md`) | [16-deployment.md](16-deployment.md) | `docker-compose*.yml`, `Caddyfile`, `next.config.js`, `vercel ls/env ls`, `curl` live |
| 17 | Test coverage matrix | done | [17-test-coverage-matrix.md](17-test-coverage-matrix.md) | all test files, `ci-cd.yml`, `pyproject.toml`, `playwright.config.ts` |
| 18 | Git / release workflow | done | [18-git-release-workflow.md](18-git-release-workflow.md) | `git log`, `gh pr list`, branch protection API, `ci-cd.yml` |
| 19 | Service dependency map | done | [19-service-dependency-map.md](19-service-dependency-map.md) | `docker-compose.prod.yml`, `backend/cmd/main.go`, `config.go`, `app.py` lifespan |
| 20 | Observability | done | [20-observability.md](20-observability.md) | `sentry.ts`, `logger/`, `metrics.go`, `feature_handler.go`, `alert_service.go`, `webhook_notifier.py`, `prometheus.yml`, `monitor.sh` |
| 21 | Incident response | done | [21-incident-response.md](21-incident-response.md) | `webhook_notifier.py`, `monitor.py`, `strategy/RUNBOOK.md`, `docs/EVIDENCE_LOG.md`, `RUNBOOK.md` |
| 22 | DORA | done (2 of 5 computed, 3 proxies) | [22-dora.md](22-dora.md) | `git log`, `gh run list`, `vercel ls/inspect` |

## Headline findings

1. Live site เป็น frontend อย่างเดียว: `/api/*` บน `booktrading.bookchaowalit.com` ตอบ 502 `DNS_HOSTNAME_NOT_FOUND` (Vercel ไม่มี `BACKEND_URL`).
2. AuthZ ปัจจุบันเป็น access matrix แบบ fail-closed — ข้อสรุป "most trading routes never check the token" ใน flow pack layer 6 ไม่จริงแล้ว.
3. Kill switch gate อยู่ฝั่ง Python (`RealGridBot` → `check_order_allowed`) เท่านั้น; Go `/api/trade/order` ไม่มี risk check ของตัวเอง; risk-per-trade ไม่ถูกบังคับกับ real grid order และ kill/halt ไม่ cancel open order ที่ exchange.
4. DEX wallet private keys เข้ารหัสด้วย key คงที่ใน source (`wallet_service.go getEncryptionKey`).
5. gRPC :9000 ไม่มี auth; WebSocket token ส่งทาง query string.
6. ไม่มี backup ที่ implement จริง, ไม่มี uptime monitor, Prometheus metric ส่วนใหญ่ไม่ถูกบันทึก (`trades_failed_total` ที่ KPI plan อ้างไม่เคยเพิ่มค่า).

Validate: `node ~/work/core22/tools/validate.mjs docs/diagrams/*.md docs/diagrams/08-adr/*.md`
