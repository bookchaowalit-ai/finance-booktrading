# Release / Runbook (BD-039) — booktrading

**Date:** 2026-08-11  
**Tier:** A flagship  
**Status:** runbook stub — fill env values only in private secrets store, never commit secrets.

## Build

```bash
# Inspect README first for the authoritative flow.
# Common patterns:
#   npm ci && npm run build
#   docker compose build
#   flutter build apk   # mobile only
```

Detected: Docker marker=yes; npm scripts sample=['dev', 'build', 'start', 'lint', 'lint:fix', 'format', 'format:check', 'test', 'test:watch']

## Configure (names only)

| Variable class | Notes |
|---|---|
| Database URL | If backend present — never commit |
| JWT / SECRET_KEY | Backend auth — rotate if leaked |
| Public API base URL | Frontend `NEXT_PUBLIC_*` style |
| OAuth / third-party | Optional; leave disabled for demos |
| `DEX_WALLET_ENCRYPTION_KEY` | 32+ chars, Infisical. Seals `dex_wallets` private keys; backend refuses to start with `DEX_ENABLED=true` and no key. Separate from `ENCRYPTION_KEY` |

Use `.env.example` when present. **Do not** commit `.env`.

## Re-key DEX wallets (one time, owner-approved)

Before 2026-11-03 wallet keys were sealed with a constant in `wallet_service.go`,
which is public in git history. Rows sealed that way no longer open. To move
them to `DEX_WALLET_ENCRYPTION_KEY`:

1. Back up the database.
2. Dry run (changes nothing; lists legacy rows by address):
   `DATABASE_URL=... DEX_WALLET_ENCRYPTION_KEY=... go run ./cmd/rekey-dex-wallets` (from `backend/`)
3. Any listed address that holds funds was readable by anyone with the repo:
   move the funds to a fresh wallet first, then retire the old one.
4. With owner approval: run again with `-apply` (one transaction).

## Run (local)

Prefer README. Typical:

```bash
# install deps → migrate DB if any → start API → start web
```

## Health check

see README /docs

## Rollback

1. Redeploy previous container image / previous git tag  
2. Keep DB migrations backward-compatible or document one-way migrations  
3. Feature flags: prefer observe-only / disabled bots (booktrading)

## Owner

Solo operator (bookchaowalit). No multi-person on-call.

## Non-claims

Not proof of production uptime. Nested work may still be dirty or unpushed.
