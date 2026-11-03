# 15 — CI/CD Pipeline

Workflow เดียว: `.github/workflows/ci-cd.yml` ("CI/CD Pipeline"), `permissions: contents: read`

```mermaid
flowchart TD
    T1["push main / master"] --> J
    T2["pull_request to main / master"] --> J
    T3["workflow_dispatch deploy_production boolean"] --> J
    subgraph J["Parallel test jobs"]
        BT["backend-test: Timescale pg16 + Redis services, go mod tidy diff, gofmt, go vet, go test -race"]
        FT["frontend-test: shared ci-node.yml v1.1.1, Node 24, lint, typecheck, vitest, next build"]
        STT["strategy-test: shared ci-python.yml v1.1.1, Python 3.11, uv.lock, ruff subset, offline pytest, research tests, requirements diff"]
        SEC["security: shared security.yml v1.1.1, gitleaks, Semgrep, OSV-Scanner, Trivy"]
    end
    BT --> DB
    FT --> DB
    STT --> DB
    SEC --> DB
    DB{"docker-build if: push to main/master, or dispatch with deploy_production on main/master"}
    DB -->|"yes"| IMG["Build backend, frontend, strategy images, push: false, GHA cache"]
    DB -->|"no, e.g. PR"| END1["stop"]
    IMG --> DP{"deploy if: workflow_dispatch and deploy_production and main/master"}
    DP -->|"yes"| SSH["appleboy/ssh-action: cd /opt/bookfinance, git pull, compose pull, up -d --build, system prune -af --volumes"]
    DP -->|"no"| END2["stop"]
```

Sources: `.github/workflows/ci-cd.yml`, `specs/001-shared-ci-sentry-deps/spec.md` §2.

ข้อสังเกต:
- `docker-build` สร้าง image แล้วทิ้ง (`push: false`) — deploy บน host build ใหม่ด้วย `--build` จาก `git pull` อีกรอบ.
- Secrets ที่ deploy ใช้: `PRODUCTION_HOST`, `PRODUCTION_USER`, `SSH_PRIVATE_KEY` (ชื่อจาก workflow เท่านั้น).
- Frontend live (Vercel) **ไม่อยู่ใน pipeline นี้** — deploy ด้วย `vercel` CLI จากเครื่อง owner (ดู [16-deployment](16-deployment.md), [18-git-release-workflow](18-git-release-workflow.md)).
- Strategy test exclude `tests/test_command_center_contract.py` (live API) และ Playwright e2e ไม่ถูกเรียก.
- ผล 90 วัน (`gh run list`, ตั้งแต่ 2026-08-05): 22 run ทั้งหมดเป็น `push` บน main — 17 success, 5 failure; ไม่มี `workflow_dispatch` run.

## Open questions

- Secrets `PRODUCTION_HOST` ฯลฯ ยังตั้งอยู่ใน repo settings หรือไม่ (ไม่ได้ตรวจ — `gh secret list` ต้องใช้สิทธิ์ admin).
