# 13 — Threat Model (DFD + STRIDE)

DFD ตาม `docs/diagrams/06-c4-container.md` แบ่ง trust boundary 4 ชั้น: Internet, edge (Caddy / Vercel), compose network
(frontend, backend, strategy), data stores; และ third-party ภายนอก. Mitigation และ gap ทุกข้ออ้าง file จริง

```mermaid
flowchart LR
    subgraph Internet["Trust boundary: Internet (untrusted)"]
        B["Browser user"]
        TGU["Telegram user"]
    end
    subgraph Edge["Trust boundary: Edge"]
        CAD["Caddy TLS :443"]
        VER["Vercel frontend"]
    end
    subgraph Net["Trust boundary: compose network bookfinance-network"]
        FE["Frontend :3000"]
        BE["Backend :8080 / :8081 / :9000"]
        ST["Strategy :8000"]
    end
    subgraph Data["Trust boundary: data stores"]
        PG[("Postgres: users, exchange_api_keys, dex_wallets")]
        RD[("Redis: sessions, bot state")]
    end
    subgraph Third["Trust boundary: third parties"]
        EX["Exchanges with API keys"]
        TGA["Telegram API"]
        SEN["Sentry"]
    end
    B -->|"HTTPS"| CAD
    B -->|"HTTPS"| VER
    CAD -->|"/*"| FE
    CAD -->|"/api, /ws, /strategy-api"| BE
    FE -->|"rewrite"| BE
    BE -->|"proxy + AUTH_TOKEN"| ST
    ST -->|"service token"| BE
    ST -->|"gRPC"| BE
    BE --> PG
    BE --> RD
    ST --> RD
    BE -->|"signed REST"| EX
    TGU --> TGA
    BE -->|"long poll"| TGA
    VER -->|"errors only"| SEN
```

Sources: `docker-compose.prod.yml`, `Caddyfile`, `frontend/next.config.js`, `backend/cmd/main.go`, `backend/internal/adapter/http/*.go`, `strategy/infrastructure/api/app.py`, `frontend/src/lib/sentry.ts`, `backend/internal/domain/service/telegram_bot_service.go`.

## STRIDE per element

| Element | S | T | R | I | D | E | Existing mitigations (code) | Gaps |
|---|---|---|---|---|---|---|---|---|
| Browser → Caddy | ✓ | ✓ |  | ✓ |  |  | TLS 1.2/1.3, HSTS, `X-Frame-Options DENY`, `nosniff`, CSP (`Caddyfile`); Caddy overwrites `X-Real-IP` | CSP allows `'unsafe-inline' 'unsafe-eval'`; `Caddyfile` still `your-domain.com` |
| Frontend (token storage) | ✓ |  |  | ✓ |  | ✓ | Bearer token only from header for HTTP (`extractBearerToken`); 2-min `/api/auth/me` revalidation | Token in `localStorage` (no httpOnly) → XSS can steal it; no `middleware.ts`, dashboard guard is client-side |
| Backend router / auth | ✓ |  | ✓ |  | ✓ | ✓ | Access matrix fail-closed + CI test (`route_access.go`, `routes_access_test.go`); bcrypt; lockout per IP and per email+IP; `TRUSTED_PROXIES`; global 1000 req/min/IP; `BodyLimitMiddleware` 1 MB; audit log middleware → `audit_logs` | Lockout state in process memory (UPGRADE-PLAN P1); unknown-email login skips bcrypt (timing); trader can read operator balances/orders (`userReadAdmin`) |
| WebSocket :8081 | ✓ |  |  | ✓ | ✓ |  | Token required, never in the URL: `?token=` → 400; `Authorization: Bearer` on upgrade or `{"type":"auth"}` first message within 10 s, nothing broadcast before; 4 KiB read limit; nil validator fails closed; `CheckOrigin` rejects empty Origin (`broadcaster.go`, `ws_auth_test.go`); `WS_MAX_CLIENTS` default 100 | Unauthenticated sockets are not counted against a cap while they wait (10 s max) |
| Strategy proxy | ✓ | ✓ |  | ✓ |  | ✓ | Section allow-list, dot/encoded segment reject, strips caller `Authorization`/`Cookie`/`X-Forwarded-*`, writes admin-only, 25 s timeout (`strategy_proxy.go`, `strategy_proxy_test.go`) | New upstream section must be added by hand (UPGRADE-PLAN P2) |
| Strategy API :8000 | ✓ | ✓ |  |  | ✓ | ✓ | `@auth_required` with `hmac.compare_digest`; fail closed in production without `AUTH_TOKEN` (`require_auth`); port bound to `127.0.0.1` | Single shared `AUTH_TOKEN` for both directions (backend→strategy and strategy→backend service routes) |
| Service token on backend | ✓ |  | ✓ |  |  | ✓ | Accepted only on `Service: true` routes, constant-time compare (`isServiceToken`) | Service calls carry no user ID → audit rows have no actor |
| gRPC :9000 | ✓ | ✓ |  |  |  | ✓ | Bound to `127.0.0.1` in compose; both interceptors require `authorization: Bearer $AUTH_TOKEN` (constant-time), server refuses to start without it (`grpc_server.go`, `grpc_auth_test.go`); strategy `GRPCClient` sends it (`test_grpc_client_auth.py`) | No TLS (plaintext on the compose network); same shared `AUTH_TOKEN` as the HTTP service routes |
| Telegram bot | ✓ |  |  |  |  | ✓ | `telegram_users.is_authorized` check per message | `/startbot` `/stopbot` bypass HTTP admin role; no `AuthorizeUser` call site → process to grant access is undocumented |
| Postgres | | ✓ |  | ✓ |  |  | Exchange keys AES-256-GCM with key from `ENCRYPTION_KEY` (`api_keys.go`); port bound to 127.0.0.1 in dev; prod not published | DEX wallet keys: AES-256-GCM, key from `DEX_WALLET_ENCRYPTION_KEY` (HKDF), fail closed, backend refuses to start with DEX on and no key (`wallet_service.go`, `wallet_key_test.go`). **Rows sealed before 2026-11-03 used a constant now public in git history**: re-key with `cmd/rekey-dex-wallets` (dry run default) and move funds out of any listed wallet; `DATABASE_SSLMODE=disable` (same host only) |
| Redis | | ✓ |  | ✓ | ✓ |  | `--requirepass`; not published in prod | `allkeys-lru` may evict sessions / bot state |
| Exchanges | ✓ | ✓ | ✓ |  |  |  | Client order IDs + reconcile (`order_reconcile.go`), mainnet default off for strategy bots (`test_mainnet_default.py`), futures mainnet refused | Go `/api/trade/order` has no risk check of its own; relies on the Python `check_order_allowed` (real grid BUYs now pass `current_balance_thb`); kill / halt stops the Go grid loop and cancels open orders, but only for symbols in `REAL_SYMBOLS` |
| Sentry | |  |  | ✓ |  |  | `scrubEvent` drops request, user, breadcrumbs, extra, frame vars, URL queries (`sentry.ts`, `sentry.test.ts`) | Backend and strategy have no Sentry |
| CI / supply chain | | ✓ |  |  |  | ✓ | Shared `security.yml` (gitleaks, Semgrep, OSV, Trivy); actions SHA-pinned; `uv.lock` + `--require-hashes` | No branch protection on `main` (`gh api .../branches/main/protection` → 404) |

Sources: ไฟล์ในคอลัมน์ mitigations / gaps, `docs/UPGRADE-PLAN.md` (P1/P2), `backend/internal/adapter/grpcserver/grpc_server.go`.

## Reused

- Central flow pack layer 6: `bookchaowalit/architecture-docs` → `docs/architecture/flows/booktrading-ui/06-authz-decision.md` และ `docs/architecture/ATTACK-PATH-DIAGRAMS.md` (ไม่ได้อ่านไฟล์หลังนี้). ข้อสรุปของ layer 6 ที่ว่า "most trading routes never check the token" **ไม่จริงแล้ว** ที่ 425b989.

## Open questions

- มี DEX wallet ที่มีเงินจริงใน `dex_wallets` บน compose host ไหนหรือไม่ (`DEX_ENABLED` default `false`)? 2026-11-03 ไม่พบ DB ของ app นี้ในเครื่อง local หรือ Infisical. ถ้ามี: ย้ายเงินออกก่อน แล้วรัน `cmd/rekey-dex-wallets -apply` (owner approve).
- Caddy JSON log เก่าอาจมี WS token จาก `?token=` ก่อน 2026-11-03 — ลบ log เก่าหรือรอ session หมดอายุ.
- gRPC ยังเป็น plaintext — ต้องการ TLS / แยก token ต่อทิศทางหรือไม่.
