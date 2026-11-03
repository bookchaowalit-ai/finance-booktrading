# 10 — Sequence Diagrams

4 request path ที่สำคัญที่สุด: login, real grid order (เงินจริง), dashboard อ่าน command center ผ่าน proxy, และ admin kill switch

## 1. Login

```mermaid
sequenceDiagram
    actor U as User
    participant FE as Frontend LoginModal
    participant R as Router ServeHTTP
    participant A as AuthHandler
    participant DB as Postgres users
    participant RD as Redis
    U->>FE: email + password
    FE->>R: POST /api/auth/login
    R->>R: global rate limit 1000/min per client IP
    R->>A: public route
    A->>A: per IP and per email+IP lockout, progressive delay
    A->>DB: find user by normalized email
    A->>A: bcrypt compare only if user found
    alt bad credentials
        A-->>FE: 401 or 429 Retry-After
    else ok
        A->>A: token = sha256 hex of 32 random bytes
        A->>RD: SetSession(token, userID, 7 days), error is logged and login continues
        A-->>FE: token + user
        FE->>FE: localStorage auth_token, user
    end
```

Sources: `frontend/src/components/LoginModal.tsx`, `frontend/src/services/auth.ts`, `backend/internal/adapter/http/handler.go` (`globalRateLimiter`, `ServeHTTP`), `backend/internal/adapter/http/auth_handler.go` (`Login`, `sessionTTL`, lockout functions), `backend/internal/adapter/http/client_ip.go`, `backend/internal/adapter/redis/redis_adapter.go` (`SetSession`), `docs/UPGRADE-PLAN.md` pass 13.

## 2. Real grid order (real money path)

```mermaid
sequenceDiagram
    participant G as RealGridBot
    participant RM as RiskManager
    participant R as Backend Router
    participant T as TradeHandler
    participant X as ExchangeManager
    participant EX as Binance TH
    participant DB as Postgres
    G->>G: balance check _can_place_buy / _can_place_sell
    G->>RM: check_order_allowed(symbol, side, qty, price)
    alt halted or limit breached
        RM-->>G: False, reason
        G->>G: log "Order blocked by risk", no order
    else allowed
        RM-->>G: True
        G->>R: POST /api/trade/order Bearer AUTH_TOKEN
        R->>R: serviceRule and isServiceToken
        R->>T: PlaceOrder
        T->>X: PlaceOrder(symbol, side, qty, price)
        X->>EX: signed REST order
        alt exchange error
            EX-->>T: error
            T-->>G: 400 error
        else accepted
            EX-->>X: order
            T->>DB: INSERT real_trades
            T-->>G: 201 status placed + order
            G->>RM: record_order_placed
        end
    end
```

Sources: `strategy/app/real_grid_bot.py` (order placement block around `check_order_allowed` and `POST {BACKEND_API_BASE}/api/trade/order`), `strategy/app/backend_auth.py`, `strategy/app/risk_manager.py`, `backend/internal/adapter/http/route_access.go` (`/api/trade/order: serviceRule`), `backend/internal/adapter/http/handler.go`, `backend/internal/adapter/http/trade_handler.go` (`PlaceOrder`, `persistTrade`), `backend/internal/adapter/exchange/manager.go`.

## 3. Dashboard reads Command Center via strategy proxy

```mermaid
sequenceDiagram
    participant FE as Dashboard page
    participant R as Backend Router
    participant P as StrategyProxy
    participant S as Strategy /api/command-center
    participant RD as Redis
    FE->>R: GET /strategy-api/api/command-center Bearer session
    R->>R: userReadAdmin, read = any session
    R->>P: forward
    P->>P: section allow-list, strip Authorization Cookie X-Forwarded
    P->>S: GET /api/command-center Bearer AUTH_TOKEN, timeout 25s
    S->>RD: health_check
    S->>S: risk_manager.get_status, poly paper status, persisted paper state, real grid status
    S-->>P: decision JSON
    P-->>FE: JSON, upstream CORS and Set-Cookie stripped
```

Sources: `frontend/src/services/api.ts` (`getCommandCenter`), `frontend/src/app/[lang]/dashboard/page.tsx`, `backend/internal/adapter/http/strategy_proxy.go`, `backend/internal/adapter/http/route_access.go`, `strategy/infrastructure/api/app.py` (`command_center`), `docs/UPGRADE-PLAN.md` pass 8.

## 4. Admin presses kill on the real grid bot

```mermaid
sequenceDiagram
    actor A as Admin
    participant FE as Monitoring page
    participant R as Backend Router
    participant P as StrategyProxy
    participant S as Strategy API
    participant G as RealGridBot
    A->>FE: click Kill
    FE->>R: POST /strategy-api/api/real-grid/kill Bearer session
    R->>R: write on userReadAdmin needs IsAdmin
    alt not admin
        R-->>FE: 403 Admin role required
    else admin
        R->>P: forward
        P->>P: write method, check IsAdmin again
        P->>S: POST /api/real-grid/kill Bearer AUTH_TOKEN
        S->>S: auth_required compare_digest
        S->>G: disable
        S-->>FE: 200
    end
```

Sources: `frontend/src/app/[lang]/dashboard/monitoring/page.tsx`, `frontend/src/services/monitoring.ts` (`killBot`), `backend/internal/adapter/http/handler.go`, `backend/internal/adapter/http/strategy_proxy.go` (privileged routes), `strategy/infrastructure/api/app.py` (`/api/real-grid/kill`, `require_auth`).

## Reused

- Central flow pack layer 3 / 7: `bookchaowalit/architecture-docs` → `docs/architecture/flows/booktrading-ui/03-system-sequence.md`, `07-error-retry.md`. Login sequence ใน pack ยังถูกในส่วน token / TTL / localStorage แต่ lockout เปลี่ยนแล้ว (per-IP + per email+IP + progressive delay). Order sequence ใน pack ให้ Go `service/implementation.go` เรียก `risk_manager.py check_order_allowed()` — ไม่พบใน code; ภาพที่ 2 ข้างบนคือ path จริง.

## Open questions

- Login: code ปัจจุบัน `found == nil || bcrypt.Compare...` ข้าม bcrypt เมื่อไม่พบ email จึงยังตอบเร็วกว่า (UPGRADE-PLAN P1 ยังเปิด) แม้ pass 13 จะทำ status/body/delay/lockout ให้เท่ากัน — ยืนยันว่ายอมรับ timing leak นี้ชั่วคราวหรือไม่.
- ถ้า `SetSession` ล้มเหลว (Redis down) login ยังคืน token ที่ `ValidateToken` จะไม่รู้จัก — user จะได้ 401 ทันทีที่เรียก API ถัดไป.
