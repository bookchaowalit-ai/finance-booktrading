# ADR-0005 — Hosting: self-hosted Docker Compose + Caddy (manual SSH deploy) และ frontend บน Vercel (CLI)

- Status: accepted (retro-documented 2026-11-03)

## Context

ระบบต้องการ Postgres, Redis, long-running bot loops และ exchange credentials — ไม่เหมาะกับ serverless

## Decision

- Full stack: `docker-compose.prod.yml` (postgres, redis, backend, strategy, frontend, caddy) บน host เดียว, path `/opt/bookfinance`; Caddy ทำ TLS อัตโนมัติ.
- Deploy: job `deploy` ใน `ci-cd.yml` รันเฉพาะ `workflow_dispatch` + `deploy_production=true` บน main: SSH (`appleboy/ssh-action`) → `git pull`, `docker compose pull`, `up -d --build`, `docker system prune -af --volumes`. ไม่มี image registry (`push: false`).
- Public site `booktrading.bookchaowalit.com` = Vercel project `booktrading-frontend`, deploy ด้วย CLI (ไม่ผูก Git) ตาม SPEC-001 §5 / SPEC-002 §5.

## Consequences

- Live Vercel site ไม่มี backend: `/api/health` ตอบ 502 `DNS_HOSTNAME_NOT_FOUND` (curl 2026-11-03) เพราะ rewrite ไปหา `http://backend:8080`.
- ไม่มี run ของ `workflow_dispatch` deploy ใน 90 วันล่าสุด (`gh run list`).
- `docker system prune -af --volumes` ลบ volume ที่ไม่ถูกใช้ทุกครั้งที่ deploy.

## Evidence

`docker-compose.prod.yml`, `Caddyfile`, `deploy.sh`, `PRODUCTION.md`, `.github/workflows/ci-cd.yml` (`docker-build`, `deploy`), `specs/001-shared-ci-sentry-deps/spec.md` §5, `specs/002-truthful-landing-copy/spec.md` §5, `vercel ls booktrading-frontend --prod` (2026-11-03).
