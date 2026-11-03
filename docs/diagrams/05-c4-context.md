# 05 — C4 Level 1: System Context

BookFinance อยู่ตรงกลาง; คนที่ใช้มี 3 แบบ (operator/admin, trader, Telegram user) และพึ่งพา exchange,
market data, notification และ hosting ภายนอก. ทุก external ข้างล่างมี URL หรือ SDK อยู่ใน code

```mermaid
C4Context
    title BookFinance - System Context
    Person(op, "Operator (admin)", "Single owner, FIRST_ADMIN account")
    Person(tr, "Trader", "Self-registered account when registration is open")
    Person(tg, "Telegram user", "Row in telegram_users with is_authorized")
    System(bf, "BookFinance", "Go backend, Python strategy, Next.js dashboard")
    System_Ext(bth, "Binance TH", "api.binance.th spot orders and balances")
    System_Ext(bgl, "Binance Global", "api.binance.com spot, fapi and demo-fapi futures")
    System_Ext(btk, "Bitkub", "api.bitkub.com v3 orders")
    System_Ext(poly, "Polymarket", "gamma-api, clob, data-api")
    System_Ext(mkt, "Market data sources", "CoinGecko, Yahoo Finance, alternative.me, Birdeye, open.er-api, metals.live, RSS")
    System_Ext(evm, "EVM RPC nodes", "Ethereum, BSC, Base, Arbitrum public RPC for DEX")
    System_Ext(notify, "Telegram and Discord", "Bot API and webhooks")
    System_Ext(sentry, "Sentry", "Frontend error events")
    System_Ext(vercel, "Vercel", "Hosts booktrading.bookchaowalit.com frontend")
    Rel(op, bf, "Observes, admin writes", "HTTPS")
    Rel(tr, bf, "Reads, own finance data", "HTTPS")
    Rel(tg, bf, "Commands", "Telegram Bot API")
    Rel(bf, bth, "Orders, balances, tickers", "HTTPS REST")
    Rel(bf, bgl, "Klines, WS stream, futures paper/testnet", "HTTPS, WSS")
    Rel(bf, btk, "Orders, reconcile", "HTTPS REST")
    Rel(bf, poly, "Markets, order books", "HTTPS")
    Rel(bf, mkt, "Prices, sentiment, macro", "HTTPS")
    Rel(bf, evm, "Quotes, swaps, balances", "JSON-RPC")
    Rel(bf, notify, "Fills, kill switch, errors", "HTTPS")
    Rel(bf, sentry, "Errors only, scrubbed", "HTTPS")
    Rel(vercel, bf, "Serves frontend build", "CLI deploy")
```

Sources: `backend/cmd/main.go`, `backend/internal/adapter/exchange/binance_adapter.go`, `backend/internal/adapter/exchange/binance_th_adapter.go`, `backend/internal/adapter/exchange/bitkub/`, `backend/internal/config/config.go` (EVM RPC URLs), `backend/internal/adapter/http/news_handler.go` (CoinGecko, alternative.me), `backend/internal/domain/service/alert_service.go`, `backend/internal/domain/service/telegram_bot_service.go`, `strategy/app/polymarket/client.py`, `strategy/app/market_intel/sources/` (`macro.py`, `stocks.py`, `degen.py`, `binance_alpha.py`), `strategy/app/futures_bot.py` (`fapi`, `demo-fapi`), `strategy/app/webhook_notifier.py`, `frontend/src/lib/sentry.ts`, `specs/001-shared-ci-sentry-deps/spec.md` (Vercel project `booktrading-frontend`).

หมายเหตุ: `strategy/app/world/client.py` เรียก `markets-api-proxy.world-xyz.workers.dev` (world markets) — รวมอยู่ใน "Market data sources" เพื่อไม่ให้ภาพแน่นเกิน.

## Open questions

- External ไหน "เปิดใช้จริง" บน production ขึ้นกับ env (`DEX_ENABLED`, `FUTURES_ENABLED`, `TELEGRAM_BOT_TOKEN`, `DISCORD_WEBHOOK_URL`, `MARKET_INTEL_ONCHAIN_STREAM_ENABLED`) — ไม่ทราบค่าบน host จริง.
