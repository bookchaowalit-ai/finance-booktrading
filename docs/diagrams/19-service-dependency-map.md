# 19 — Service Dependency Map (runtime)

เส้นทึบ = จำเป็นตอน start (compose `depends_on: condition: service_healthy` หรือ code fatal), เส้นประ = optional / degrade ได้

```mermaid
flowchart TD
    CAD["caddy"] --> FE["frontend"]
    CAD --> BE["backend"]
    FE -->|"depends_on healthy"| BE
    ST["strategy"] -->|"depends_on healthy"| BE
    ST -->|"depends_on healthy"| RD[("redis")]
    BE -->|"depends_on healthy, Validate requires REDIS_PASSWORD"| RD
    BE -->|"depends_on healthy, Fatal if connect fails"| PG[("postgres timescale")]
    BE -->|"/strategy-api upstream, proxy disabled if URL invalid"| ST
    ST -.->|"gRPC connect_with_retry"| BE
    ST -.->|"Redis optional at startup, continues without"| RD
    BE -.->|"WS stream, retry on demand"| BINWS["Binance WS"]
    BE -.->|"REST every 10s price poller"| EXREST["Binance / Binance TH / Bitkub REST"]
    BE -.->|"klines sync 1h, 24h"| BINREST["api.binance.com"]
    BE -.->|"if TELEGRAM_BOT_TOKEN"| TG["Telegram API"]
    BE -.->|"if DEX_ENABLED"| RPC["EVM RPC"]
    BE -.->|"news / sentiment"| CG["CoinGecko, alternative.me"]
    ST -.->|"market intel every 5 min"| MD["Polymarket, Yahoo, Birdeye, FX, metals"]
    ST -.->|"if FUTURES_ENABLED"| FAPI["Binance futures paper / testnet"]
    ST -.->|"if token / webhook set"| NOTI["Telegram, Discord"]
    FE -.->|"if NEXT_PUBLIC_SENTRY_DSN"| SEN["Sentry"]
```

Sources: `docker-compose.prod.yml` (`depends_on`, healthchecks), `backend/cmd/main.go` (`cfg.Validate`, `logger.Fatal` on DB, strategy proxy creation, Binance stream connect, price poller, `historicalService.StartSync`, Telegram, DEX init), `backend/internal/config/config.go` (`Validate`), `strategy/infrastructure/api/app.py` (`lifespan`: `connect_with_retry`, Redis non-fatal, market intel scan, futures opt-in), `strategy/app/webhook_notifier.py`, `frontend/src/lib/sentry.ts`.

## Healthchecks ที่ compose ใช้

| Service | Check | Source |
|---|---|---|
| postgres | `pg_isready` | `docker-compose.prod.yml` |
| redis | `redis-cli -a $REDIS_PASSWORD ping` | `docker-compose.prod.yml` |
| backend | `wget --spider http://localhost:8080/api/health` (handler คืน `{"status":"healthy"}` คงที่ ไม่เช็ค DB/Redis) | `docker-compose.prod.yml`, `backend/internal/adapter/http/handler.go` (`HealthCheck`) |
| strategy | `curl -f http://localhost:8000/api/health` | `docker-compose.prod.yml`, `strategy/infrastructure/api/app.py` |
| frontend, caddy | none | `docker-compose.prod.yml` |

## Open questions

- Backend `/api/health` ไม่ตรวจ dependency — ต้องการ readiness check ที่เช็ค Postgres/Redis หรือไม่?
