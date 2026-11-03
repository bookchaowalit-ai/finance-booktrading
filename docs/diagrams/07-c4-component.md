# 07 — C4 Level 3: Components

แยก 2 container ที่มี logic มากที่สุด: Go backend (hexagonal: `adapter/`, `domain/service`, `port/`) และ Python strategy

## Backend (Go)

```mermaid
C4Component
    title Backend container - components
    Container_Ext(st, "Strategy service", "FastAPI")
    ContainerDb_Ext(pg, "PostgreSQL", "TimescaleDB")
    ContainerDb_Ext(rd, "Redis", "")
    System_Ext(ex, "Exchanges", "Binance TH, Binance, Bitkub")
    Container_Boundary(be, "Backend") {
        Component(mw, "Middleware chain", "BodyLimit 1MB, SecurityHeaders, AuditMiddleware", "Wraps router")
        Component(router, "Router + access matrix", "handler.go, route_access.go", "Rate limit 1000/min/IP, CORS, public/user/admin/service")
        Component(auth, "AuthHandler", "auth_handler.go, registration.go", "bcrypt, opaque token, Redis session 7d, lockout")
        Component(proxy, "StrategyProxy", "strategy_proxy.go", "/strategy-api allow-list, injects AUTH_TOKEN")
        Component(trade, "TradeHandler", "trade_handler.go", "Real orders, real_trades, trade_journal")
        Component(feat, "FeatureHandler", "feature_handler.go", "Paper engine, risk, alerts, backtest, metrics, audit")
        Component(fin, "FinanceHandler", "finance_handler.go", "Per-user personal finance")
        Component(bot, "BotService", "implementation.go, grid_orders.go", "Go grid/auto bot with gridBook")
        Component(exm, "ExchangeManager", "adapter/exchange/manager.go", "Multi-exchange, reconcile by client order ID")
        Component(ws, "WebSocket broadcaster", "adapter/websocket", ":8081 /ws, token required")
        Component(grpc, "gRPC server", "adapter/grpcserver", ":9000 TradingService, DEX")
        Component(tgb, "TelegramBotService", "telegram_bot_service.go", "Optional, TELEGRAM_BOT_TOKEN")
    }
    Rel(mw, router, "")
    Rel(router, auth, "ValidateToken, IsAdmin")
    Rel(router, proxy, "/strategy-api/*")
    Rel(router, trade, "/api/trade/*, /api/journal/*")
    Rel(router, feat, "/api/paper, /api/risk ...")
    Rel(router, fin, "/api/finance/*")
    Rel(proxy, st, "HTTP + Bearer AUTH_TOKEN")
    Rel(trade, exm, "PlaceOrder")
    Rel(bot, exm, "grid orders")
    Rel(exm, ex, "REST")
    Rel(auth, rd, "sessions")
    Rel(trade, pg, "INSERT real_trades")
    Rel(fin, pg, "repositories")
    Rel(grpc, bot, "Start/Stop")
```

Sources: `backend/cmd/main.go` (wiring order, middleware chain), `backend/internal/adapter/http/handler.go`, `route_access.go`, `auth_handler.go`, `registration.go`, `strategy_proxy.go`, `trade_handler.go`, `feature_handler.go`, `finance_handler.go`, `audit_middleware.go`, `security_middleware.go`, `backend/internal/domain/service/implementation.go`, `grid_orders.go`, `telegram_bot_service.go`, `backend/internal/adapter/exchange/manager.go`, `backend/internal/adapter/websocket/broadcaster.go`, `backend/internal/adapter/grpcserver/grpc_server.go`, `proto/trading.proto`.

## Strategy (Python)

```mermaid
C4Component
    title Strategy container - components
    Container_Ext(be, "Backend", "Go")
    ContainerDb_Ext(rd, "Redis", "")
    System_Ext(poly, "Polymarket and market data", "")
    System_Ext(notify, "Telegram / Discord", "")
    Container_Boundary(st, "Strategy") {
        Component(api, "FastAPI app", "infrastructure/api/app.py", "81 routes, @auth_required on writes")
        Component(rgb, "RealGridBot", "app/real_grid_bot.py", "Always started in lifespan")
        Component(risk, "RiskManager", "app/risk_manager.py", "check_order_allowed, _halt, reset_kill_switch")
        Component(brain, "Brain", "app/brain/", "Technical, funding, sentiment signals")
        Component(intel, "Market intel scanner", "app/market_intel/", "Background scan every 5 min")
        Component(fut, "FuturesBot", "app/futures_bot.py", "Only if FUTURES_ENABLED, paper/testnet")
        Component(wh, "WebhookNotifier", "app/webhook_notifier.py", "Fill, kill switch, error alerts")
        Component(cc, "Command center", "app.py /api/command-center", "Decision, kill switch, readiness")
    }
    Rel(api, rgb, "kill, enable, restart, config")
    Rel(rgb, risk, "check_order_allowed")
    Rel(risk, rgb, "_halt disables bot")
    Rel(rgb, brain, "directive")
    Rel(rgb, be, "POST /api/trade/order with AUTH_TOKEN")
    Rel(rgb, rd, "real_grid:SYMBOL:state")
    Rel(rgb, wh, "alerts")
    Rel(wh, notify, "HTTPS")
    Rel(intel, poly, "HTTPS")
    Rel(api, cc, "")
```

Sources: `strategy/infrastructure/api/app.py` (`lifespan`, `require_auth`, routes), `strategy/tests/test_route_access_matrix.py` (81 method/path pairs ตาม `docs/UPGRADE-PLAN.md` pass 11), `strategy/app/real_grid_bot.py`, `strategy/app/risk_manager.py`, `strategy/app/brain/brain.py`, `strategy/app/market_intel/scanner.py`, `strategy/app/futures_bot.py`, `strategy/app/webhook_notifier.py`, `strategy/app/backend_auth.py`.

## Open questions

- Go `RiskManager` (`backend/internal/domain/service/risk_manager.go`) ใช้แค่ใน `/api/risk/check` และ backtest; ไม่ได้ gate `OrderService.CreateOrder` หรือ `/api/trade/order` — ตั้งใจให้ gate อยู่ฝั่ง Python อย่างเดียวหรือไม่?
