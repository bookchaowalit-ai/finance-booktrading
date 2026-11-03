# 09 — ERD

Schema มาจาก 22 migration ใน `backend/migrations/*.up.sql` (golang-migrate). มี 48 ตาราง จึงแบ่งเป็น 4 ภาพตาม domain.
เส้นทึบ (`--`) = foreign key ที่ประกาศจริงใน migration; เส้นประ (`..`) = ความสัมพันธ์เชิง logic ผ่าน `user_id` / `exchange_order_id`
ที่ **ไม่มี** FK constraint. แสดงเฉพาะ column สำคัญ (PK, FK, status, เงิน) — column ทั้งหมดดูใน migration

## 1. Users, trading core, system

```mermaid
erDiagram
    users {
        varchar id PK
        varchar email
        varchar role
        varchar password_hash
    }
    orders {
        varchar id PK
        varchar user_id FK
        varchar symbol
        varchar side
        double quantity
        double price
        varchar status
    }
    trade_history {
        varchar id PK
        varchar symbol
        double total
        double fee
    }
    portfolio {
        varchar symbol PK
        double balance
        double locked
    }
    bot_status {
        serial id PK
        boolean is_active
        integer total_trades
    }
    market_data {
        timestamptz time
        varchar symbol
        double price
    }
    klines {
        timestamptz time PK
        varchar symbol PK
        varchar interval_type PK
        numeric close
    }
    exchange_api_keys {
        varchar provider PK
        text api_key
        text api_secret
        boolean use_testnet
    }
    user_preferences {
        serial id PK
        varchar user_id
        varchar language
    }
    audit_logs {
        uuid id PK
        varchar user_id
        varchar action
        integer status_code
    }
    notifications {
        varchar id PK
        varchar user_id
        boolean read
    }
    telegram_users {
        serial id PK
        bigint telegram_id
        boolean is_authorized
    }
    alert_history {
        uuid id PK
        varchar channel
        boolean sent
    }
    users ||--o{ orders : "fk_orders_user_id ON DELETE SET NULL"
    users ||..o{ audit_logs : "user_id, no FK"
    users ||..o{ user_preferences : "user_id, no FK"
    users ||..o{ notifications : "user_id, no FK"
```

Sources: `backend/migrations/001_init_schema.up.sql`, `003_add_user_support.up.sql`, `004_add_user_preferences.up.sql`, `005_add_journal_notifications_sltp.up.sql`, `006_add_exchange_api_keys.up.sql`, `008_add_users_table.up.sql`, `009_add_audit_table.up.sql`, `011_add_klines.up.sql`, `012_add_telegram_users.up.sql`, `014_add_alert_history.up.sql`, `022_fix_orders_user_id_default.up.sql`.

## 2. Paper, real trades, journal, risk

```mermaid
erDiagram
    paper_orders {
        uuid id PK
        varchar user_id
        varchar symbol
        varchar status
        numeric fee
    }
    paper_positions {
        uuid id PK
        varchar user_id
        varchar symbol
        numeric realized_pnl
    }
    paper_portfolios {
        uuid id PK
        varchar user_id
        numeric current_balance
        numeric max_drawdown
    }
    paper_trades {
        uuid id PK
        varchar symbol
        varchar status
    }
    portfolio_snapshots {
        uuid id PK
        numeric total_value
        numeric total_pnl
    }
    real_trades {
        varchar id PK
        varchar exchange_order_id
        varchar symbol
        varchar status
        varchar exchange
        boolean testnet
        varchar strategy
    }
    real_trade_summaries {
        uuid id PK
        date trade_date
        numeric total_pnl
    }
    trade_journal {
        bigserial id PK
        varchar exchange_order_id
        varchar strategy
        numeric actual_pnl
        numeric fee
        varchar status
    }
    journal_entries {
        varchar id PK
        varchar user_id
        double pnl
        integer rating
    }
    stop_loss_take_profit {
        serial id PK
        varchar user_id
        varchar symbol
        boolean enabled
    }
    price_alerts {
        uuid id PK
        varchar symbol
        numeric target_price
        boolean triggered
    }
    real_trades ||..o| trade_journal : "exchange_order_id, no FK"
```

Sources: `backend/migrations/005_add_journal_notifications_sltp.up.sql`, `013_add_paper_trading_tables.up.sql`, `018_add_real_trading_tables.up.sql`, `019_add_trade_journal.up.sql`, `020_add_portfolio_snapshots_and_price_alerts.up.sql`, `021_add_paper_trades_compatibility.up.sql`; join `real_trades` ↔ `trade_journal` ผ่าน `exchange_order_id` ตามที่ `docs/EVIDENCE_LOG.md` 2026-07-23 ใช้ตรวจ.

## 3. Personal finance (per user)

```mermaid
erDiagram
    finance_accounts {
        varchar id PK
        varchar user_id
        varchar type
        double balance
    }
    finance_categories {
        varchar id PK
        varchar user_id
        varchar parent_id FK
        varchar type
    }
    finance_transactions {
        varchar id PK
        varchar user_id
        varchar account_id FK
        varchar category_id FK
        double amount
    }
    finance_budgets {
        varchar id PK
        varchar category_id FK
        double amount
        varchar period
    }
    finance_goals {
        varchar id PK
        varchar user_id
        double target_amount
        varchar status
    }
    finance_assets {
        varchar id PK
        varchar user_id
        double current_value
    }
    finance_liabilities {
        varchar id PK
        varchar user_id
        double current_balance
        varchar status
    }
    finance_subscriptions {
        varchar id PK
        varchar account_id FK
        varchar category_id FK
        double amount
    }
    finance_recurring_transactions {
        varchar id PK
        varchar account_id FK
        varchar category_id FK
        varchar frequency
    }
    finance_diary_entries {
        varchar id PK
        varchar user_id
        timestamptz date
    }
    finance_net_worth_history {
        serial id PK
        varchar user_id
        double net_worth
        jsonb breakdown
    }
    finance_accounts ||--o{ finance_transactions : "ON DELETE CASCADE"
    finance_categories ||--o{ finance_transactions : "ON DELETE SET NULL"
    finance_categories ||--o{ finance_categories : "parent_id"
    finance_categories ||--o{ finance_budgets : "ON DELETE SET NULL"
    finance_accounts ||--o{ finance_subscriptions : "ON DELETE SET NULL"
    finance_categories ||--o{ finance_subscriptions : "ON DELETE SET NULL"
    finance_accounts ||--o{ finance_recurring_transactions : "ON DELETE SET NULL"
    finance_categories ||--o{ finance_recurring_transactions : "ON DELETE SET NULL"
```

Sources: `backend/migrations/007_add_finance_module.up.sql`. ทุกตาราง finance มี `user_id` แต่ไม่มี FK ไป `users`.

## 4. Bots, copy trading, rebalancing, DEX

```mermaid
erDiagram
    users {
        varchar id PK
    }
    dca_bots {
        uuid id PK
        varchar user_id
        varchar symbol
        varchar status
        numeric total_invested
    }
    dca_orders {
        uuid id PK
        uuid bot_id FK
        varchar side
        varchar status
    }
    strategy_profiles {
        uuid id PK
        varchar owner_id
        boolean is_public
    }
    copy_relationships {
        uuid id PK
        varchar copier_id
        uuid strategy_id FK
        boolean is_active
    }
    copy_trades {
        uuid id PK
        uuid copy_relationship_id FK
        numeric pnl
        varchar status
    }
    rebalance_targets {
        uuid id PK
        varchar user_id
        varchar symbol
        numeric target_percent
    }
    rebalance_history {
        uuid id PK
        varchar user_id
        varchar status
    }
    dex_wallets {
        uuid id PK
        varchar user_id FK
        varchar address
        text private_key_encrypted
    }
    dex_tokens {
        uuid id PK
        bigint chain_id
        varchar address
    }
    dex_liquidity_positions {
        uuid id PK
        varchar user_id FK
        uuid wallet_id FK
        boolean is_active
    }
    dex_swap_history {
        uuid id PK
        varchar user_id FK
        uuid wallet_id FK
        varchar tx_hash
        varchar status
    }
    dex_liquidity_history {
        uuid id PK
        varchar user_id FK
        uuid wallet_id FK
        varchar action
        varchar status
    }
    dex_user_settings {
        uuid id PK
        varchar user_id FK
        numeric slippage_tolerance
    }
    dca_bots ||--o{ dca_orders : "bot_id"
    strategy_profiles ||--o{ copy_relationships : "strategy_id"
    copy_relationships ||--o{ copy_trades : "copy_relationship_id"
    users ||--o{ dex_wallets : "user_id"
    users ||--o{ dex_liquidity_positions : "user_id"
    users ||--o{ dex_swap_history : "user_id"
    users ||--o{ dex_liquidity_history : "user_id"
    users ||--o| dex_user_settings : "user_id"
    dex_wallets ||--o{ dex_liquidity_positions : "wallet_id"
    dex_wallets ||--o{ dex_swap_history : "wallet_id"
    dex_wallets ||--o{ dex_liquidity_history : "wallet_id"
    users ||..o{ dca_bots : "user_id, no FK"
```

Sources: `backend/migrations/010_add_dca_bots.up.sql`, `015_add_copy_trading.up.sql`, `016_add_portfolio_rebalancing.up.sql`, `017_add_dex_support.up.sql`.

## Persisted data นอก Postgres

| Store | ข้อมูล | Source |
|---|---|---|
| Redis | session token → userID (TTL 7 วัน), market data channel, `real_grid:{symbol}:state` | `backend/internal/adapter/redis/redis_adapter.go`, `strategy/app/real_grid_bot.py` |
| SQLite local | `alpha_policy`, `alpha_reviews` … (paper review lane) | `strategy/app/alpha/durable_journal.py` |

## Open questions

- `exchange_api_keys.api_key/api_secret` เข้ารหัส AES-256-GCM ด้วย key ที่ derive จาก `ENCRYPTION_KEY` (`backend/internal/adapter/database/api_keys.go`) — มี rotation plan หรือไม่?
- `dex_wallets.private_key_encrypted`: AES-256-GCM, key จาก `DEX_WALLET_ENCRYPTION_KEY` (HKDF) ตั้งแต่ 2026-11-03. Row ที่สร้างก่อนนั้นใช้ key คงที่ซึ่งอยู่ใน git history — ต้อง re-key ด้วย `backend/cmd/rekey-dex-wallets` (ดู [13-threat-model](13-threat-model.md)).
- `finance_recurring_transactions` สร้าง repository แต่ "Initialized but not yet wired" (`cmd/main.go`) — ยังต้องการหรือไม่?
