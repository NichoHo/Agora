# Vault UI Redesign — Enterprise Marketplace

A ready-to-execute brief for restyling the Vault web app (`web/`) into a professional, modern, clean enterprise-marketplace look. Hand this whole file to an implementation session as the starting prompt.

## Context

Vault is a Next.js 16 (App Router) + React 19 + Tailwind v4 C2C marketplace. It already has a token-based design system in [globals.css](app/globals.css) called "Slate Enterprise (v3)": indigo accent, slate neutrals, light **and** dark mode toggled via `data-theme` and a `ThemeToggle` component. Pages: home, search, listing detail, sell, checkout, orders (+detail), wallet, admin, and the auth flow (login/register/consent/MFA). Shared components live in `web/components/` (`ListingCard`, `StatusBadge`, `ServiceUnavailable`, `OrderTimeline`, `CategoryChips`, `BackLink`, `ThemeToggle`). No UI library is installed yet — everything is hand-rolled Tailwind.

This redesign replaces the visual system end to end. It is not a rebrand of the product concept — copy, IA, and data stay the same; only the look, the component foundation, and the motion layer change.

## Goals

- Enterprise-marketplace polish: think Amazon/Shopee-grade information density and trust signals, with the clean, high-end feel of a premium SaaS/template product (visual reference below).
- Professional, modern, clean. Confident whitespace, not cluttered.
- Tasteful motion — present and noticeable, never gratuitous.
- One coherent design system, applied to every page, not just the homepage.

## Non-Goals

- No dark mode. Light only — see [Dropping dark mode](#dropping-dark-mode).
- No backend, API, or data-model changes.
- No new pages or features. This is a visual/structural-polish pass on existing screens.
- No full-page route-transition choreography (Next.js App Router makes this expensive to do well) — motion lives at the component/section level.

## Visual Reference

Requested reference: [Framer marketplace template gallery](https://www.framer.com/community/marketplace/templates/plumbzo/) plus general marketplace sites (Shopee, Amazon).

**Note on that link:** "Plumbzo" itself is a landing-page template for plumbing/home-service businesses, not an e-commerce marketplace layout — "marketplace" in the URL refers to Framer's own template marketplace. It's still a legitimate style reference (that's what was asked for), just not a source for grid/filter/cart UX patterns — pull those from Amazon/Shopee instead. Inspecting its live preview gave concrete, real values worth anchoring to:

- White canvas, near-black ink text, one vivid orange accent (`rgb(254, 89, 34)`) used sparingly on fills and highlights.
- A warm cream tint for soft section backgrounds, not pure gray.
- `Plus Jakarta Sans` throughout — a modern geometric sans, regular/medium weight even at large display sizes (not heavy bold).
- Generous, consistent corner radius (~18px) on cards, with soft shadows instead of hard borders.

That maps well onto the white + orange direction below.

## Visual Identity

### Color tokens

Replace the `@theme` block in [globals.css](app/globals.css) with a light-only palette. Keep the existing **token names** (`--color-canvas`, `--color-accent`, etc.) so pages already written against semantic tokens don't need class-level renames — only the values change.

```css
@theme {
  /* ---- Surface scale ---- */
  --color-canvas: #ffffff;
  --color-surface: #ffffff;
  --color-surface-2: #f7f7f5; /* soft warm-gray section backgrounds */
  --color-surface-3: #efeeea; /* hover / raised rows */

  /* ---- Ink & text ramp ---- */
  --color-ink: #171412; /* near-black, warm */
  --color-ink-2: #46423d;
  --color-muted: #78736c;
  --color-faint: #a8a29a;

  /* ---- Lines & fills ---- */
  --color-line: #e8e6e1;
  --color-line-strong: #d6d2ca;
  --color-fill: #f4f3ef;

  /* ---- Accent: vivid orange ---- */
  --color-accent: #fe5922;
  --color-accent-strong: #d8430f; /* hover / pressed */
  --color-accent-tint: #fef3ec;
  --color-on-solid: #ffffff;

  /* ---- Semantics ---- */
  --color-success: #16a34a;
  --color-success-tint: #effbf3;
  --color-warning: #ca8a04; /* shifted more gold than the old amber, so it reads distinct from the new orange accent */
  --color-warning-tint: #fffbeb;
  --color-danger: #dc2626;
  --color-danger-tint: #fef2f2;
  --color-copilot: #7c3aed; /* AI listing-assistant hue, kept distinct from accent */
  --color-copilot-tint: #f5f3ff;
}
```

Delete the entire `:root[data-theme="dark"]` override block and the `@custom-variant dark (...)` line — there's nothing left to switch to.

**Amendment (from Phase 1 planning):** shadcn/ui's component source hardcodes `bg-muted`/`text-muted-foreground` (a subtle background) and `bg-accent`/`text-accent-foreground` (a neutral hover surface, used by dropdown/select items) — both names collide with what those words already meant in this file (`muted` = secondary text, `accent` = the brand color). Since shadcn's registry files can't be safely hand-patched on every update, Vault's own usage is renamed instead: `--color-muted` → `--color-muted-foreground`, and `--color-accent`/`--color-accent-strong`/`--color-accent-tint` → `--color-primary`/`--color-primary-strong`/`--color-primary-tint` (a direct semantic match to shadcn's own "primary buttons and actions" token). `muted` and `accent` become shadcn's neutral background/hover-surface values. See the Phase 1 plan (`docs/superpowers/plans/2026-08-19-ui-redesign-phase1-foundation.md`) for the full reasoning and exact values.

The legacy "Ishidatami" alias block (`--color-paper`, `--color-torii`, `--color-sumi-*`, etc.) can stay temporarily if it's still load-bearing for unmigrated pages, but since this redesign covers every page in one pass, delete it once all pages are converted to the semantic names directly.

**Contrast checkpoint:** `#fe5922` on white is a strong CTA color but check white-text-on-solid-orange buttons against WCAG AA — if small text fails, use `--color-accent-strong` for solid-fill text buttons and reserve `--color-accent` for larger UI (icons, borders, tints, headline underlines).

### Typography

Switch the Google Font from Inter to **Plus Jakarta Sans** (variable), matching the reference. Keep the existing mono stack (Geist Mono / JetBrains Mono) for the `money` utility — tabular figures for prices is a good detail already in place, don't touch it.

Large display type (hero headline, page titles) should sit at regular/medium weight, not heavy bold — that restraint is a big part of why the reference reads as premium rather than default-template.

### Shape & elevation

- `--radius-control`: 10px (buttons, inputs)
- `--radius-card`: 18px (listing cards, panels)
- `--radius-panel`: 24px (hero, large section containers)
- Keep the existing soft-shadow scale (`--shadow-sm/md/lg/glow`) — it's already light-mode-tuned. Optionally warm the shadow tint from `rgb(15 23 42 / …)` to a near-black warm tone (`rgb(23 20 17 / …)`) to match the new ink color.
- Depth comes from shadow + whitespace, not borders. Prefer `border-line` only where a hairline is needed for structure (tables, dividers), not as the default card treatment.

## Dropping Dark Mode

1. Delete `web/components/ThemeToggle.tsx` and its usage in [layout.tsx](app/layout.tsx) header nav.
2. Remove the pre-hydration theme script (`dangerouslySetInnerHTML` block in `<head>`) and the now-unnecessary `suppressHydrationWarning` on `<html>`.
3. Remove `color-scheme: light` toggling logic — just hardcode `color-scheme: light` on `html` in `@layer base`.
4. Grep for any stray `dark:` Tailwind variants outside `globals.css` — per the existing code comment, there shouldn't be any (components all reference semantic tokens), but verify.

## Component Foundation: shadcn/ui

Adopt shadcn/ui as the primitive layer instead of continuing to hand-roll every control.

1. `npx shadcn init` in `web/`. Base color: neutral. Style: **New York** (more structured/enterprise than the default style, pairs well with the denser marketplace layouts below).
2. Reconcile shadcn's generated CSS variables with the token system above instead of running two parallel palettes — point shadcn's `--primary` / `--destructive` / `--border` / `--ring` etc. at the Vault semantic tokens (`var(--color-accent)`, `var(--color-danger)`, `var(--color-line)`, …) so every shadcn component inherits the orange/white identity automatically.
3. Icons: adopt `lucide-react` (shadcn's default) for header, category chips, status badges, and empty states — replace any ad hoc inline SVGs with it.
4. Pull in components as needed, prioritized by where they replace the most hand-rolled code:
   - `button`, `input`, `textarea`, `select`, `checkbox`, `radio-group`, `label`, `form` — forms across sell/checkout/auth.
   - `card` — base for `ListingCard` and dashboard panels.
   - `dialog`, `dropdown-menu`, `tooltip`, `tabs`, `sheet` (mobile filter drawer) — interactive chrome.
   - `table`, `pagination` — orders, wallet ledger, admin.
   - `badge` — rebuild `StatusBadge` on top of it.
   - `avatar`, `separator`, `skeleton`, `sonner` (toast) — polish details.
   - `input-otp` — MFA screen (`app/auth/mfa/MfaManager.tsx`).
   - `navigation-menu` or `menubar` — header category/account nav.
5. Don't fight Radix/shadcn's built-in open/close animations for dialogs, dropdowns, and tooltips — leave those as-is. Framer Motion (below) is for content choreography shadcn doesn't cover: page sections, grids, hover states.

## Motion: Framer Motion

Add `framer-motion` as a dependency. Guidance, deliberately restrained per "don't overdo it":

- **Scroll reveal:** hero and each major section fades + rises slightly into view once (`whileInView`, `viewport={{ once: true }}`). No repeat-on-scroll-up tricks.
- **Grid stagger:** listing grids (home, search results) stagger their cards in on first render/scroll-into-view — a small, fast stagger (~30–50ms per item), not a slow reveal.
- **Card hover:** `ListingCard` lifts slightly with a shadow grow on hover (`whileHover`), tap scales down slightly on mobile (`whileTap`).
- **Active-state indicators:** `CategoryChips` and any tab-like nav use a shared `layoutId` sliding pill/underline instead of an instant class swap.
- **Buttons:** subtle `whileTap={{ scale: 0.97 }}` on primary CTAs only, not every clickable element.
- **Loading states:** skeleton → content crossfade rather than a hard swap.
- **Respect reduced motion:** wrap Framer Motion usage with the `useReducedMotion()` hook where an animation is more than a fade, in addition to the existing `prefers-reduced-motion` CSS block in `globals.css` (keep that block as-is — it already covers CSS transitions).
- Skip: full route/page transitions, parallax, anything that delays perceived load time, animated backgrounds/particles.

## Layout & Page-by-Page Direction

- **Header** (`app/layout.tsx`): sticky, white/glass, bigger centered-ish search bar (Amazon-style prominence), category nav, account dropdown (shadcn `dropdown-menu`) replacing the current inline text links, "Sell" CTA in solid accent orange. Drop `ThemeToggle`.
- **Home** (`app/page.tsx`): hero on a warm cream wash (not the current indigo `wash-accent`) with a bold restrained headline, trust-badge row (escrow, OIDC/MFA, AI-assist — remap the "AI-assisted selling" chip from `warning` to `copilot` tint so it doesn't visually compete with the new orange accent), category grid (icon + label, Shopee-style), then the fresh-listings grid with scroll-reveal + stagger.
- **Search** (`app/search/page.tsx`): left filter sidebar (category, price, condition) collapsing into a `sheet` on mobile, sort dropdown, responsive card grid, pagination.
- **Listing detail** (`app/listing/[id]/page.tsx`): image gallery, price block in ink/accent, seller trust card (verified badge, rating), prominent escrow/buy CTA, related listings row.
- **Sell** (`app/sell/page.tsx`, `SellForm.tsx`): shadcn form primitives; keep the AI-assist entry point visually distinct (copilot tint).
- **Checkout** (`app/checkout/[orderId]/page.tsx`): clear order summary card, explicit escrow/trust messaging, shadcn form inputs.
- **Orders** (`app/orders/page.tsx`, `[id]/page.tsx`): shadcn `table` for the list; restyle `OrderTimeline` and `StatusBadge` on the new semantic tokens.
- **Wallet** (`app/wallet/page.tsx`): balance as the visual anchor of the page, transaction/ledger as a `table`.
- **Admin** (`app/admin/page.tsx`): dense data table with filters/pagination — same system, deliberately more "back office" in density than the storefront pages.
- **Auth** (`app/auth/**`): centered card layout, minimal chrome, `IdCard` restyled, MFA via `input-otp`, consent screen keeps a clear, trust-building permissions list.

## Accessibility

- Keep the `:focus-visible` ring, recolored to the new accent; keep the skip-to-content link and the reduced-motion CSS block as-is.
- Re-verify AA contrast for every text-on-tint and text-on-solid pairing after the palette swap, especially orange-on-white and white-on-orange (see contrast checkpoint above).
- shadcn/Radix primitives bring correct focus trapping and ARIA out of the box for dialogs/dropdowns/tabs — don't hand-roll around them.

## Technical Notes

- New dependencies: `framer-motion`, `shadcn` CLI output (Radix primitives, `class-variance-authority`, `tailwind-merge`, `lucide-react`).
- Tailwind v4, Next.js 16, React 19 stay as-is — this is styling/markup only.
- Existing Playwright e2e specs may break on markup/selector changes introduced by the shadcn swap — plan to update selectors alongside each page's migration, not as an afterthought.

## Suggested Execution Order

The prompt covers the whole app, but land it in reviewable chunks rather than one giant diff:

1. Tokens + dark-mode removal + shadcn scaffolding + header/footer chrome.
2. Buyer path: home, search, listing detail.
3. Sell + checkout.
4. Orders, wallet, admin.
5. Auth flow.
6. Motion pass across all of the above (can overlap with 2–5 per page instead of being a separate step, if that's a smaller diff per PR).

## Acceptance Criteria

- No dark-mode code paths remain (`data-theme`, `ThemeToggle`, the pre-paint script, `dark:` variants).
- Every page listed above uses the new tokens — no lingering "Slate Enterprise" indigo/slate values.
- shadcn primitives are in place for the components listed in [Component Foundation](#component-foundation-shadcnui); hand-rolled equivalents are removed, not left dead in the tree.
- Framer Motion is used per the Motion section — present, subtle, `prefers-reduced-motion`-safe.
- Contrast-checked orange usage (see checkpoint above).
- Responsive behavior at existing breakpoints is preserved or improved, not regressed.
- Playwright e2e suite passes (selectors updated where needed).
