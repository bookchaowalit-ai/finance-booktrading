# 03 — User Flows

Frontend มี 42 ไฟล์ `page.tsx` (นับด้วย `find frontend/src/app -name page.tsx`): root redirect, `[lang]` landing,
`[lang]/dashboard` (Command Center) และหน้า dashboard ย่อย. Sidebar แบ่งเป็น **core 5 หน้า (observe-only)** และ
**advanced groups** ที่โชว์เฉพาะเมื่อ build ด้วย `NEXT_PUBLIC_ADVANCED_UI=true`

## Flow 1 — Visitor เข้าเว็บและ login

```mermaid
flowchart TD
    A["GET / (root page.tsx)"] --> B{"Accept-Language starts with en?"}
    B -->|yes| C["redirect /en"]
    B -->|no| D["redirect /th"]
    C --> E["Landing page LandingPageClient"]
    D --> E
    E --> F["Open LoginModal"]
    F --> G["GET /api/auth/config"]
    G --> H{"registrationOpen?"}
    H -->|no| I["Sign-in form only"]
    H -->|yes| J["Sign-in or sign-up, invite field if inviteRequired"]
    I --> K["POST /api/auth/login"]
    J --> K
    K -->|"200 token"| L["localStorage auth_token + user"]
    K -->|"401 / 429 lockout"| F
    L --> M["window.location = /lang/dashboard"]
```

Sources: `frontend/src/app/page.tsx`, `frontend/src/app/[lang]/LandingPageClient.tsx`, `frontend/src/components/LoginModal.tsx`, `frontend/src/services/auth.ts` (`authenticate`, `getAuthConfig`, `register`), `backend/internal/adapter/http/auth_handler.go`, `backend/internal/adapter/http/registration.go`.

## Flow 2 — Operator ใช้ dashboard (observe-only default)

```mermaid
flowchart TD
    A["/lang/dashboard layout mounts"] --> B{"localStorage auth_token present?"}
    B -->|no| X["router.push /lang (landing)"]
    B -->|yes| C["Command Center: api.getCommandCenter()"]
    C --> D["/strategy-api/api/command-center via backend proxy"]
    A --> E["Every 2 min GET /api/auth/me"]
    E -->|401| X
    A --> F["Every 15 s GET /api/notifications"]
    C --> P1["Daily Report"]
    C --> P2["Evidence"]
    C --> P3["Research"]
    C --> P4["System"]
    P1 --> R["Read-only views; no trade buttons in core 5"]
    P2 --> R
    P3 --> R
    P4 --> R
    A --> G{"NEXT_PUBLIC_ADVANCED_UI == true?"}
    G -->|yes| H["Advanced groups: trading, grid, dca, dex, copy, rebalancing, polymarket, arbitrage, money, wallet, portfolio, finance/*, analytics, backtest, monitoring, risk, alerts, news, strategy, settings"]
    G -->|no| I["Advanced menu hidden"]
```

Sources: `frontend/src/app/[lang]/dashboard/layout.tsx` (auth check, `validateSession`, `fetchUnreadCount`), `frontend/src/components/Sidebar.tsx` (core items, `advancedGroups`, `isAdvancedUI`), `frontend/src/app/[lang]/dashboard/page.tsx`, `frontend/src/app/[lang]/dashboard/{daily-report,evidence,research,system}/page.tsx`, `frontend/src/services/api.ts` (`getCommandCenter`), `frontend/src/services/auth-headers.ts` (`STRATEGY_URL = /strategy-api`).

## Flow 3 — Admin หยุด / เปิด real grid bot

```mermaid
flowchart TD
    A["Monitoring page (advanced)"] --> B{"isAdmin() from localStorage user.role"}
    B -->|no| C["Kill / enable buttons disabled"]
    B -->|yes| D["POST /strategy-api/api/real-grid/kill or enable"]
    D --> E["Backend router: userReadAdmin, write needs admin"]
    E -->|"not admin"| F["403, ForbiddenNotice Thai toast"]
    E -->|admin| G["StrategyProxy adds Bearer AUTH_TOKEN"]
    G --> H["Strategy @auth_required handler"]
    H --> I["RealGridBot disabled / enabled"]
```

Sources: `frontend/src/app/[lang]/dashboard/monitoring/page.tsx` (`isAdmin`, `killBot`, `enableBot`), `frontend/src/services/monitoring.ts`, `frontend/src/services/forbidden.ts`, `frontend/src/components/ForbiddenNotice.tsx`, `backend/internal/adapter/http/route_access.go` (`StrategyProxyPrefix + "/": userReadAdmin`), `backend/internal/adapter/http/strategy_proxy.go`, `strategy/infrastructure/api/app.py` (`/api/real-grid/kill`, `/api/real-grid/enable`).

ข้อสังเกต:
- Frontend ไม่มี `middleware.ts` — การกันหน้า dashboard เป็น client-side `useEffect` เท่านั้น; ข้อมูลจริงถูกกันที่ backend (access matrix).
- บน live Vercel site ทุก `/api/*` ตอบ 502 (`DNS_HOSTNAME_NOT_FOUND`, ตรวจ 2026-11-03) ดังนั้น flow 1 หยุดที่ login ไม่ผ่าน และ flow 2–3 ใช้ได้เฉพาะ local / self-hosted compose.

## Reused

- Central flow pack layer 2: `bookchaowalit/architecture-docs` → `docs/architecture/flows/booktrading-ui/02-operator-storyboard.md` (5 screen clusters, นับ 37 dashboard page ที่ 8de42ee). ไฟล์นี้เพิ่ม split core/advanced ตาม `Sidebar.tsx` ซึ่ง pack ไม่ได้แยก.

## Open questions

- README บอก "38 routes: 5 keep-main, 14 extracted-to-component, 8 advanced-only, 11 hidden/deferred" — ไม่พบ list ใน code ว่า route ไหน hidden/deferred (บาง page ยังมีไฟล์อยู่แต่ไม่อยู่ใน Sidebar เช่น `history` อยู่ใน advanced). ต้องการ list ทางการจากเจ้าของ.
- Live Vercel build ตั้ง `NEXT_PUBLIC_ADVANCED_UI` หรือไม่ — `vercel env ls` (2026-11-03) เห็นแค่ `NEXT_PUBLIC_SENTRY_DSN`.
