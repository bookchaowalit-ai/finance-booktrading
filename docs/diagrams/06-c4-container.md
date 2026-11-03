# 06 — C4 Level 2: Containers

Container มาจาก `docker-compose.prod.yml` (6 service) บวก Vercel frontend ที่เป็น live site จริง.
พอร์ตตาม compose: backend HTTP 8080, WS 8081, gRPC 9000 (bind 127.0.0.1), strategy 8000 (host 127.0.0.1:8001), Caddy 80/443

```mermaid
C4Container
    title BookFinance - Containers (docker-compose.prod.yml + Vercel)
    Person(op, "Operator / Trader", "Browser")
    System_Ext(ex, "Exchanges", "Binance TH, Binance, Bitkub")
    System_Ext(ext, "Data and notify APIs", "Polymarket, CoinGecko, Telegram, Discord")
    Container(vfe, "Vercel frontend", "Next.js 15.5 on Vercel", "booktrading.bookchaowalit.com, no backend reachable")
    System_Boundary(host, "Compose host") {
        Container(caddy, "Caddy", "caddy:2-alpine", "TLS, routes /api, /ws, /strategy-api, security headers")
        Container(fe, "Frontend", "Next.js standalone :3000", "Dashboard UI, rewrites /api and /strategy-api to backend")
        Container(be, "Backend", "Go 1.26 :8080 :8081 :9000", "HTTP API, access matrix, WebSocket, gRPC, strategy proxy")
        Container(st, "Strategy", "Python 3.11 FastAPI :8000", "Real grid bot, risk manager, brain, market intel")
        ContainerDb(pg, "PostgreSQL", "timescale/timescaledb:latest-pg16", "Orders, trades, users, finance, journal")
        ContainerDb(rd, "Redis", "redis:7-alpine AOF", "Sessions, market data pub/sub, bot state")
    }
    Rel(op, caddy, "HTTPS")
    Rel(op, vfe, "HTTPS")
    Rel(caddy, fe, "/*", "HTTP")
    Rel(caddy, be, "/api/*, /ws, /strategy-api/*", "HTTP")
    Rel(fe, be, "rewrites BACKEND_URL", "HTTP")
    Rel(be, st, "/strategy-api proxy with AUTH_TOKEN", "HTTP")
    Rel(st, be, "/api/trade, /api/paper, /api/journal with AUTH_TOKEN", "HTTP")
    Rel(st, be, "ExecuteOrder, market data", "gRPC")
    Rel(be, pg, "pgx pool", "TCP 5432")
    Rel(be, rd, "sessions, publish", "TCP 6379")
    Rel(st, rd, "subscribe, state keys", "TCP 6379")
    Rel(be, ex, "REST, WSS")
    Rel(st, ext, "HTTPS")
```

Sources: `docker-compose.prod.yml`, `Caddyfile`, `frontend/next.config.js` (rewrites, `output: 'standalone'`), `frontend/package.json` (next 15.5.28), `backend/go.mod` (go 1.26.8), `backend/cmd/main.go` (HTTP/WS/gRPC servers, strategy proxy), `strategy/Dockerfile`, `strategy/infrastructure/api/app.py` (`lifespan`: gRPC client, Redis adapter, bots), `strategy/app/real_grid_bot.py` (`BACKEND_API_BASE`), `strategy/app/backend_auth.py`, `backend/internal/adapter/redis/redis_adapter.go`, `specs/001-shared-ci-sentry-deps/spec.md` §5.

ข้อสังเกต:
- Vercel frontend ใช้ `next.config.js` ชุดเดียวกัน แต่ไม่มี `BACKEND_URL` ใน Vercel env (`vercel env ls --project booktrading-frontend` เห็นแค่ `NEXT_PUBLIC_SENTRY_DSN`, 2026-11-03) ทำให้ rewrite ไปที่ `http://backend:8080` และตอบ 502 `DNS_HOSTNAME_NOT_FOUND`.
- `docker-compose.yml` (dev) มี container เดียวกันแต่ชื่อ `trading-bot-*` และ frontend publish ที่ `3001`; ไม่มี Caddy.
- Strategy ใช้ SQLite local (`DurablePaperJournal`) สำหรับ alpha review lane ผ่าน `strategy/scripts/world_alpha_*.py` — ไม่ใช่ container แยก.

## Open questions

- Compose host จริง (`/opt/bookfinance` ใน deploy job) คือเครื่องไหน และยังรันอยู่หรือไม่ — repo ไม่มีข้อมูล; `Caddyfile` ยังเป็น `your-domain.com`.
