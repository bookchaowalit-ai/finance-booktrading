# ADR-0006 — ใช้ shared CI (platform-ci-workflows v1.1.1, SHA-pinned) สำหรับ frontend, strategy และ security

- Status: accepted (retro-documented 2026-11-03); บันทึกครั้งแรกใน SPEC-001 (2026-10-22)

## Decision

- `frontend-test` → `bookchaowalit/platform-ci-workflows/.github/workflows/ci-node.yml@87dc5e1` (Node 24, `require-tests: true`, `npm run build`).
- `strategy-test` → `ci-python.yml@87dc5e1` (Python 3.11, ติดตั้งจาก `uv.lock`, ruff subset, offline pytest `--disable-socket`, research tests, `requirements.txt` ↔ `uv.lock` diff).
- `security` → `security.yml@87dc5e1` (gitleaks, Semgrep, OSV-Scanner, Trivy).
- `backend-test` ยังเป็น job ใน repo (Timescale + Redis service containers, tidy, gofmt, vet, `go test -race`).
- Third-party actions pin เป็น commit SHA (`docker/setup-buildx-action`, `docker/build-push-action`, `appleboy/ssh-action`).

## Consequences

- อัปเกรด shared workflow ต้องเปลี่ยน SHA ใน repo นี้.
- Playwright e2e (`frontend/e2e/landing.spec.ts`) ไม่ได้รันใน CI; `test_command_center_contract.py` ถูก `--ignore`.

## Evidence

`.github/workflows/ci-cd.yml`, `specs/001-shared-ci-sentry-deps/spec.md` §2, `frontend/playwright.config.ts`.
