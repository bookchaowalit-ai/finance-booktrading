---
name: BookTrading Compact
description: Touch-first, no-scroll compact design system for the BookTrading mobile trading interface.
colors:
  foreground: "#000000"
  background-start: "#d6dbdc"
  background-end: "#ffffff"
  dark-foreground: "#ffffff"
  dark-background: "#000000"
  scrollbar-thumb: "#6b7280"
  scrollbar-hover: "#9ca3af"
typography:
  body:
    fontFamily: "inherit (Tailwind default)"
    fontWeight: 400
    lineHeight: 1.5
  compact:
    fontSize: "0.75rem"
    lineHeight: 1.25
rounded:
  sm: "2px"
  md: "4px"
spacing:
  compact: "8px"
  sm: "12px"
  md: "16px"
components:
  button-touch:
    minHeight: "44px"
    minWidth: "44px"
  input-touch:
    minHeight: "44px"
    fontSize: "16px"
  scrollbar:
    width: "4px"
    thumbColor: "{colors.scrollbar-thumb}"
---

# Design System: BookTrading Compact

## 1. Overview

**Creative North Star: "The Pocket App"**

A compact, no-scroll design system built for a mobile trading interface where every pixel of viewport matters. The system's defining constraint is absolute: `overflow: hidden` + `100vh` — the entire app lives within a single screen with zero page scrolling. Content is managed through internal scrolling regions with 4px compact scrollbars, not page-level overflow.

The palette is minimal: black text on a light gray-to-white gradient background, with automatic dark mode via `prefers-color-scheme`. No custom fonts are declared — the system inherits Tailwind's defaults. No accent color exists in the token set. The interface is a transparent container for trading data.

Touch ergonomics are non-negotiable. Every interactive element meets a 44px minimum target size on coarse-pointer devices. Safe-area insets protect content from notched device cutouts. Grid layouts collapse from multi-column to single-column on small screens. The system is designed thumb-first.

**Key Characteristics:**
- No page scroll. `overflow: hidden` on html/body. All content fits in one viewport.
- Touch-first. 44px minimum touch targets via `@media (pointer: coarse)`.
- Safe-area aware. `env(safe-area-inset-*)` padding for notched devices.
- Compact utilities. `.text-compact`, `.p-compact`, `.gap-compact` for dense layouts.
- Auto dark mode. `prefers-color-scheme` — no class toggle.
- System fonts. No custom typeface declared.

## 2. Colors

The palette is basic RGB custom properties — three tokens for light mode, three for dark.

### Primary
- **Foreground** (rgb(0, 0, 0)): Light mode text. Flips to white in dark mode.

### Neutral
- **Background Start** (rgb(214, 219, 220)): Light mode gradient origin — cool-tinted light gray.
- **Background End** (rgb(255, 255, 255)): Light mode gradient destination — pure white.
- **Dark Background** (rgb(0, 0, 0)): Dark mode background — pure black, no gradient.

### Functional
- **Scrollbar Thumb** (#6b7280): 4px scrollbar thumb, gray-500.
- **Scrollbar Hover** (#9ca3af): Scrollbar thumb on hover, gray-400.

### Named Rules
**The Three-Token Rule.** The entire color system is three RGB triplets: foreground, background-start, background-end. No accent, no semantic colors, no chart palette. Trading data visualization is handled by inline component styles, not system tokens.

## 3. Typography

**Display/Body Font:** Tailwind defaults (no custom font declared)

**Character:** The system has no typographic personality — it inherits whatever Tailwind provides. This is intentional: a trading app's data is the voice, not the interface chrome.

### Hierarchy
- **Compact** (0.75rem, 1.25 line-height): Dense data labels, trading values, timestamps. The system's signature text style.
- **Body** (400, 1rem, 1.5 line-height): Standard reading text.
- **Responsive SM** (0.75rem on mobile): Auto-shrunk text for screens < 640px.
- **Responsive Base** (0.875rem on mobile): Slightly compressed body for screens < 640px.

### Named Rules
**The Compact Text Rule.** Default body text is too spacious for a trading interface. The `.text-compact` utility (0.75rem, tight leading) is the system's default for data-dense areas. Use it on any element displaying trading values, prices, or tabular data.

## 4. Elevation

This system has no custom elevation tokens. No box-shadows, no ambient glows, no layered surfaces. Depth is conveyed through the background gradient (light gray → white) and internal scroll regions.

### Named Rules
**The No-Elevation Rule.** No shadows. No card lift. The interface is a single flat surface with internal scrolling pockets. Adding shadows would create visual noise in a space-constrained trading view.

## 5. Components

### Buttons
- **Touch Target:** Minimum 44px height and width on coarse-pointer devices.
- **Default:** Tailwind default styling. No custom button classes.
- **Focus:** Standard browser focus ring.

### Inputs / Fields
- **Touch Target:** Minimum 44px height on coarse-pointer devices.
- **Font Size:** 16px minimum on touch devices (prevents iOS auto-zoom).
- **Types Covered:** text, number, email, password, select, textarea.

### Scrollbar
- **Width:** 4px (compact, unobtrusive).
- **Track:** Transparent.
- **Thumb:** Gray (#6b7280), 2px border-radius. Darker on hover (#9ca3af).

### Grid Layouts
- **Desktop:** 2–5 column grids for trading data panels.
- **Mobile (< 640px):** Collapses to 2 columns.
- **Small mobile (< 480px):** Single column.

### Signature Components
- **Safe-Area Wrappers:** `.safe-top`, `.safe-bottom`, `.safe-left`, `.safe-right` classes apply `env(safe-area-inset-*)` padding. Used on edge-to-edge layouts to avoid notched device cutouts.
- **Compact Utilities:** `.text-compact` (xs + tight leading), `.p-compact` (p-2), `.gap-compact` (gap-2) — the system's density toolkit for trading data views.

## 6. Do's and Don'ts

### Do:
- **Do** keep everything in one viewport. No page scrolling. Use internal scroll regions with 4px scrollbars.
- **Do** enforce 44px minimum touch targets on all interactive elements. Use `@media (pointer: coarse)`.
- **Do** apply safe-area insets on edge-to-edge layouts. Notched devices are a primary target.
- **Do** use `.text-compact` for data-dense areas. Trading values need tight leading.
- **Do** set 16px minimum font size on form inputs to prevent iOS zoom.
- **Do** let grids collapse responsively. 2-col at 640px, 1-col at 480px.

### Don't:
- **Don't** add page-level scrolling. `overflow: hidden` is the system's foundation.
- **Don't** add custom fonts. The system inherits Tailwind defaults intentionally.
- **Don't** add accent colors or semantic color tokens. The palette is foreground/background only.
- **Don't** add box-shadows or elevation effects. The system is flat and viewport-constrained.
- **Don't** make touch targets smaller than 44px. Thumb ergonomics are non-negotiable.
- **Don't** use `border-left` or `border-right` greater than 1px as colored stripes.
- **Don't** use gradient text (`background-clip: text` with gradient).
- **Don't** add uppercase tracked eyebrows above every section.

## Tokens

ADR-0007 design floor 1.0.0. Vendored and unchanged: `frontend/src/app/design-floor/tokens.css` (`css-vars` 1.0.0) and `frontend/src/app/design-floor/tailwind/v3.config.snippet.js` (`tailwind-v3`). This app's values are in `frontend/src/app/app-theme.css`; Tailwind maps them in `frontend/tailwind.config.js`.

| Token | Value | Note |
|---|---|---|
| `--color-bg` | `#020617` | slate-950, page background |
| `--color-surface` | `#0f172a` | slate-900, panels |
| `--color-fg` | `#ffffff` | primary text |
| `--color-fg-muted` | `#9ca3af` | secondary text, >= 4.5:1 on bg and surface |
| `--color-accent` / `--color-accent-fg` | `#9333ea` / `#ffffff` | primary buttons, white text 5.4:1 |
| `--color-border` | `#334155` | slate-700 |
| `--color-focus` | `#a855f7` | focus ring colour, >= 3:1 on bg |
| `--color-danger` / `--color-success` | `#f87171` / `#4ade80` | red-400 / green-400 |
| `--font-sans` | Inter (Latin), then Noto Sans Thai (Thai) | `next/font` in `frontend/src/lib/fonts.ts`; Latin-first stack |
| `--leading-thai` / `--leading-thai-heading` | 1.7 / 1.35 | every Tailwind text size uses these (body >= 1.6, headings >= 1.3) |
| `--radius-sm` / `-md` / `-lg` | 0.125rem / 0.375rem / 0.5rem | Tailwind v3 defaults, so existing `rounded-*` are unchanged |
| `--shadow-sm` / `-md` / `-lg` | Tailwind v3 defaults | existing `shadow-*` are unchanged |

Dark mode: **yes**, class-based. The `dark` class on `<html>` (ThemeProvider and LanguageSelector) drives the existing `dark:` utilities. The vendored `:root[data-theme="dark"]` set is not applied: the snippet's `darkMode: ["selector", '[data-theme="dark"]']` would switch off those utilities, so `tailwind.config.js` keeps `darkMode: 'class'`.

Waivers: none. Public routes checked: `/en`, `/th`, and the not-found page `/no-such-page/` (`frontend/design-floor.json`).

Fonts: this section supersedes "No custom fonts" in section 3 and the line-height of 1.5 in section 3, which describe the app before the design floor.
