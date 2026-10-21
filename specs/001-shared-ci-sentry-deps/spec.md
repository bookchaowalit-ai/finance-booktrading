---
title: "SPEC-001 — Shared CI, Sentry, strategy lockfile and dependency security"
status: implemented
tags: [spec, sdd, tdd, ci, sentry, security]
created: 2026-10-22
updated: 2026-10-22
description: "Move the frontend and strategy jobs to the shared CI, add the security scan, lock the strategy service with uv, report frontend errors to Sentry, and clear the dependency findings."
---

# SPEC-001 — Shared CI, Sentry, strategy lockfile and dependency security

Standard: ADR-0004 (TDD + SDD). Plan: Big Loop Plan 2026-10-21, Wave 3 items 4 and 5.

## Document control

| Field | Value |
|---|---|
| Spec ID / version | SPEC-001 / 1.0 |
| Status | implemented |
| Repository | bookchaowalit-ai/finance-booktrading |
| Owner (maker) | Claude Code (Wave 3 session) |
| Approver | Owner (bookchaowalit) |

## 1. Problem and intent

- **Problem:** `ci-cd.yml` ran its own frontend and strategy jobs and no security scan; `strategy/` had no lockfile
  (`*.lock` was git-ignored); the frontend reported no errors; the Go backend sat on go-ethereum 1.13, pgx 5.5, x/crypto
  0.17 and grpc 1.60 with known advisories; all three images ran as root.
- **Outcome:** frontend and strategy run on the shared workflows, the security scan passes, strategy installs from
  `uv.lock`, frontend errors reach Sentry without personal data, and the trading safety suites still pass.

## 2. Workflow mapping (`.github/workflows/ci-cd.yml`)

| Job | Before | After |
|---|---|---|
| backend-test | Go 1.21, TimescaleDB + Redis services, tidy/gofmt/vet/race tests, Codecov | unchanged except Go from `go.mod` (1.26) and no Codecov upload |
| frontend-test | hand-written lint, tsc, vitest, build (Node 24) | shared `ci-node.yml`, Node 24 |
| strategy-test | pip install, offline pytest (sockets disabled, paper mode), research tests, ruff | shared `ci-python.yml` with the same commands, installed from `uv.lock`, plus a `requirements.txt` ↔ `uv.lock` sync check |
| security | — | shared `security.yml` |
| docker-build | after the three test jobs | also waits for `security` |
| deploy | manual `workflow_dispatch` with `deploy_production`, SSH to the production host | unchanged |

Shared workflows are pinned to v1.1.1 (`87dc5e1`).

## 3. Behaviour — scenarios

| AC | Given / When / Then | Test |
|---|---|---|
| AC-01 | Sentry: off without `NEXT_PUBLIC_SENTRY_DSN`; errors only; request, user, breadcrumbs, extra and frame locals stripped | `frontend/src/lib/sentry.test.ts` |
| AC-02 | Strategy behaviour unchanged on fastapi 0.157 / pydantic 2.14 / uvicorn 0.54 | 659 offline strategy tests + 4 research tests |
| AC-03 | Backend behaviour unchanged on go-ethereum 1.17, pgx 5.11, go-redis 9.23, grpc 1.83 | Go test suite (race in CI) |
| AC-04 | Security scan finds no unreviewed vulnerability or HIGH misconfiguration | `security` job |

## 4. Interfaces and data

| Area | Change |
|---|---|
| frontend | `@sentry/nextjs` ^11 (`src/lib/sentry.ts`, `src/instrumentation*.ts`); postcss forced to ≥ 8.5.25 (next 15.5 pins 8.4.31); vitest 4, vite 7.3.7 |
| strategy | `pyproject.toml` `[project]` + `uv.lock` (now tracked); `requirements.txt` is `uv export` output with hashes, installed with `--require-hashes`; container runs as uid 1000 |
| backend | `go 1.26.8`; go-ethereum 1.17.0, pgx 5.11, go-redis 9.23, gorilla/websocket, x/*, grpc 1.83.2, protobuf; Docker builder golang:1.26, runtime runs as user `app` |

Reviewed exceptions (reason + expiry): `frontend/osv-scanner.toml` (braces, postcss-selector-parser — dev-only, no fix in
the required major) and `.trivyignore.yaml` (DS-0002 for `backend/Dockerfile.dev`, a dev-only image that writes to its bind mount).

## 5. Deployment and known gaps

- The SSH deploy job is manual and unchanged; merging this spec does not deploy. The next manual deploy ships the new
  non-root images. Check after that deploy that the strategy service can write its journals (uid 1000).
- `https://booktrading.bookchaowalit.com` is the Vercel project `booktrading-frontend`, last deployed by CLI on 2026-08-12,
  not from this repository's CI. Set `NEXT_PUBLIC_SENTRY_DSN` there when the Sentry project exists.
