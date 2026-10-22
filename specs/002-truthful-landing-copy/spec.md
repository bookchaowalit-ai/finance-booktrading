---
title: "SPEC-002 — Landing copy says what the bots do"
status: implemented
tags: [spec, sdd, tdd, copy, truthfulness]
created: 2026-10-22
updated: 2026-10-22
description: "Replace the 'advanced AI algorithms' and 'machine learning' claims on the landing page with what the strategy code does."
---

# SPEC-002 — Landing copy says what the bots do

Standard: ADR-0004 (TDD + SDD). Plan: Big Loop Plan 2026-10-21, Tier B batch (b-tier-b).

## 1. Problem and intent

- **Problem:** the hero said "advanced AI algorithms", feature 1 said strategies are "powered by machine learning",
  the footer said "powered by AI" and the page description said "AI Financial Operating System". The strategies are
  rule-based: `strategy/app/grid_bot.py` (grid, ATR spacing off by default), `dca_bot.py`, `trend_bot.py` (EMA + ADX)
  and `predictor.py` (moving-average crossover + RSI). The bot defaults to observe-only paper mode with a kill switch.
- **Outcome:** the English and Thai copy describes rule-based bots on technical indicators, paper-first, with a kill switch.

## 2. Acceptance

| ID | Given / when / then | Test |
|---|---|---|
| AC-1 | For `en` and `th`, `hero_desc`, `feat1_title`, `feat1_desc` and `footer_desc` contain no "AI", "machine learning" or "ขั้นสูง" | `frontend/src/app/[lang]/landing-copy.test.ts` |
| AC-2 | `hero_desc` names rule-based bots and paper mode (en) and "ตามกฎ" (th) | same file |
| AC-3 | The layout metadata description no longer says "AI Financial Operating System" | review |

Red first: the test failed 3/3 against the old copy, then passed after the copy change.

## 3. UI review (375 / 768 / 1440, en + th)

Full-page screenshots from local `next dev` at all three widths: the hero wraps to 3 lines at 1440 and 5–6 lines at 375,
there is no horizontal overflow, and the footer copy fits. The console 500s are API calls with no local backend.
Sign-off: Claude Code (b-tier-b session), 2026-10-22.

## 4. Out of scope (follow-up)

Other unverified claims on the same page: "< 1ms" order speed and "Sub-millisecond order execution", "99.9% uptime",
"never stored on our servers", and the sample contact details. Each needs evidence or a rewrite.

## 5. Deploy

The Vercel project `booktrading-frontend` is CLI-deployed only (D9). The live site keeps the old copy until the owner
reconnects Vercel Git or approves a deploy.
