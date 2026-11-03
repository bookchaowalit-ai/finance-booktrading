# ADR-0002 — PostgreSQL/TimescaleDB + golang-migrate, Redis สำหรับ session และ pub/sub

- Status: accepted (retro-documented 2026-11-03)

## Context

ต้องเก็บ order, trade, ราคา time-series (`market_data`, `klines`), ข้อมูล personal finance และ user; และต้องมี
session store กับ channel ส่งราคาให้ strategy

## Decision

- Database: image `timescale/timescaledb:latest-pg16`; `market_data` และ `klines` เป็น hypertable (`create_hypertable` ใน migration 001, 011).
- Schema: 22 migration ใน `backend/migrations/` ใช้ `github.com/golang-migrate/migrate/v4`; backend รัน `database.RunMigrations` ตอน start และ **ทำงานต่อแม้ migration error** (`logger.Info("Migration warning"...)`). Compose prod ยัง mount `./backend/migrations` เป็น `/docker-entrypoint-initdb.d` ด้วย.
- Redis 7 (`--appendonly yes`, `maxmemory 256mb`, `allkeys-lru`): session token (TTL 7 วัน), market data publish, real grid state key `real_grid:{symbol}:state`.

## Consequences

- `allkeys-lru` อาจ evict session หรือ bot state เมื่อ memory เต็ม (ไม่มี code แยก keyspace).
- `latest-pg16` ไม่ pin version — rebuild อาจได้ Timescale คนละ minor.
- Migration ที่ 013, 014, 018–020 ไม่มีไฟล์ `.down.sql`.

## Evidence

`docker-compose.prod.yml`, `backend/migrations/*.sql`, `backend/MIGRATIONS.md`, `backend/internal/adapter/database/migrate.go`, `backend/cmd/main.go`, `backend/internal/adapter/redis/redis_adapter.go`, `backend/internal/adapter/http/auth_handler.go` (`sessionTTL`), `strategy/app/real_grid_bot.py`.
