# Nova (v4): Vault's Marketplace Design System

Nova replaces "Slate Enterprise (v3)" (indigo, light and dark mode). It ships a
white canvas and one orange brand color, and it drops dark mode entirely. See
[`docs/superpowers/plans/2026-08-19-ui-redesign-phase1-foundation.md`](docs/superpowers/plans/2026-08-19-ui-redesign-phase1-foundation.md)
for the full rationale. Tokens live in [`web/app/globals.css`](web/app/globals.css)
as Tailwind v4 `@theme` variables.

## 1. Design philosophy

- **One accent, used sparingly.** Orange (`#fe5922`) marks the brand and
  primary actions. It does not fill the page.
- **Light-only by decision.** The team removed dark mode on purpose: the
  `ThemeToggle` component, the `data-theme` attribute, and the pre-paint theme
  script are all gone. This is not a dropped maintenance task, it's a choice.
- **Density matches the job.** Storefront pages stay airy. Admin, orders, and
  wallet tables read denser, closer to back-office software.
- **AI suggestions stay visible.** On `/sell`, a field the assistant filled
  keeps an orange left border until the user edits it. The "Suggest" button
  uses a separate violet `copilot` color, so an AI action never looks like a
  brand action.

## 2. Color tokens

All values below are current, read directly from `web/app/globals.css`.

| Token | Value | Role |
|---|---|---|
| `canvas` | `#FFFFFF` | Page background |
| `surface` | `#FFFFFF` | Cards, panels |
| `surface-2` | `#F7F7F5` | Soft section backgrounds |
| `surface-3` | `#EFEEEA` | Hover and raised rows |
| `ink` | `#171412` | Primary text |
| `ink-2` | `#46423D` | Secondary text |
| `muted-foreground` | `#78736C` | Captions, metadata |
| `faint` | `#A8A29A` | Placeholders, disabled text |
| `line` | `#E8E6E1` | Hairline borders |
| `line-strong` | `#D6D2CA` | Stronger dividers, focus outlines |
| `fill` | `#F4F3EF` | Chip and code backgrounds |
| `primary` | `#FE5922` | Brand color, primary buttons, links |
| `primary-strong` | `#D8430F` | Hover and pressed states |
| `primary-tint` | `#FEF3EC` | Tinted surfaces behind the brand color |
| `success` / `success-tint` | `#16A34A` / `#EFFBF3` | Paid, confirmed states |
| `warning` / `warning-tint` | `#CA8A04` / `#FFFBEB` | Pending states |
| `danger` / `danger-tint` | `#DC2626` / `#FEF2F2` | Errors, destructive actions |
| `copilot` / `copilot-tint` | `#7C3AED` / `#F5F3FF` | AI-assistant actions and fields |

**A naming note.** The brand color is `--color-primary`, not `--color-accent`.
shadcn/ui's own components hardcode `bg-accent` and `bg-muted` as neutral
hover and background colors, names that once meant something different in
this file. Renaming the brand token to `primary` and the secondary-text token
to `muted-foreground` freed `accent` and `muted` for shadcn's own use. A
hovered menu item now shows a neutral highlight, not a flash of orange.

## 3. Typography

- **Plus Jakarta Sans** carries every UI role: headings, body text, buttons.
- **Geist Mono**, with JetBrains Mono as a fallback, renders money and other
  tabular data. The `money` utility class sets `tabular-nums` and tight
  tracking so prices line up in columns.
- Large display type, such as the homepage hero and page titles, stays at a
  regular or medium weight. Avoid heavy bold at large sizes: that restraint is
  a large part of why the page reads as premium rather than templated.

## 4. Shape and elevation

- Radii: `10px` for controls (buttons, inputs), `18px` for cards, `24px` for
  large panels like the hero.
- Three shadow steps (`sm`, `md`, `lg`), tinted warm to match the ink color,
  plus a `shadow-glow` utility: a soft orange halo for active or
  trust-related elements.
- Depth comes from shadow and whitespace first. Reach for a hairline border
  (`border-line`) only where structure calls for one, such as a table row.
- `glass`: a 72% translucent surface with a 14px blur, for sticky headers and
  floating chrome.
- `wash-accent`: a soft orange-and-green radial gradient, for hero and
  feature backgrounds.

## 5. Component foundation

- The UI runs on shadcn/ui, built on `@base-ui/react` rather than Radix.
  `web/components.json` names the style `base-nova`, the base color
  `neutral`, and the icon set `lucide-react`.
- Six primitives ship today, in `web/components/ui/`: Button, Input, Textarea,
  Select, Card, and InputGroup. The rollout runs page by page. A few raw
  `<button>` elements remain (admin's approve and reject actions, the listing
  buy button, the login screen's recovery-code link). Check the actual page
  before claiming the shadcn migration is complete.
- `Button` carries a `copilot` variant (violet) for AI-assist actions, such as
  the `/sell` page's "Suggest" trigger. Keep it separate from the default
  brand-colored button.
- Framer Motion drives the motion layer: scroll-triggered reveals, staggered
  listing grids, a hover lift on cards, a sliding `layoutId` indicator on tabs
  and chips, a slight tap-scale on primary buttons, and a skeleton-to-content
  crossfade. All of it respects `prefers-reduced-motion`, through both the CSS
  block in `globals.css` and Framer Motion's `useReducedMotion()` hook.

## 6. Accessibility

- The focus ring is `2px solid var(--color-primary)`, marked `!important` in
  `globals.css`. Tailwind v4's own `outline-none` utility ties with it on
  specificity and would otherwise win on source order.
- Contrast was rechecked after the palette moved to orange. Solid buttons use
  `primary-strong` for their fill, so button text clears WCAG AA; `primary`
  itself stays on larger elements like icons, borders, and tints.
- `prefers-reduced-motion` is honored at both the CSS layer and the component
  layer.

## Lineage

Ishidatami (the original palette) gave way to Slate, then Slate Enterprise v2
and v3 (indigo, light and dark mode via a `ThemeToggle`). Nova v4 is current:
orange, light-only. The old Ishidatami alias block is gone from
`globals.css`; no page still resolves `torii`, `moss`, `kohaku`, or `sumi`
token names. [`web/UI_REDESIGN_PROMPT.md`](web/UI_REDESIGN_PROMPT.md) is the
brief that produced Nova. This file is the settled reference: when the two
disagree, trust this file and the code, and update the brief.
