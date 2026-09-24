# Mecatl Studio appearance foundation — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — changes browser presentation and local interaction inside the existing Studio web app. It adds no daemon, BFF, deployment, or trust boundary.
**Decision record:** None — [ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md) already fixes Studio's browser/BFF boundary and local workspace; this plan defines a bounded visual and component contract within it.
**Phase:** Studio design alignment, foundation for #1844 and #1846
**Status:** proposed, 2026-09-24. The frozen design and material interface decisions below are ready for plan review.
**Delivery:** Split. The token and control contract is reviewed before the independent implementation PR and the dependent shell and settings work.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#1843](https://github.com/stacklok/mecatl/issues/1843).
**Plan PR:** added when opened
**Approved baseline:** absent until this Plan / Interface PR merges

Studio presents one local appearance vocabulary across its shell and feature pages. Light, dark, and system theme are independent of the Default, Aztec, Mono, and Solar palettes. The named palettes change brand and shell accents while semantic surfaces and status roles retain their light or dark meaning. The 2026-09-24 [design review](https://github.com/stacklok/mecatl/issues/1779) is the frozen visual baseline; implementation records approved contrast corrections there.

## Human decisions

- [x] Palette set and default. — Decision: keep the current Stacklok-green Default palette and add Aztec, Mono, and Solar as browser choices. No deployment palette override is added in #1843.
- [x] Baseline contrast. — Decision: retain the frozen palette's hues and make the exact corrections below to meet 4.5:1 for normal text and 3:1 for interactive icons, focus indicators, and required control boundaries. Record the changed values and before/after images in #1779.
- [x] Shared-control scope. — Decision: normalize controls already used in Studio and extract the appearance choice control for reuse. Search-specific command controls belong to #1844; settings-specific tables, tabs, and selectors belong to #1846.
- [x] Palette-picker ownership. — Decision: #1843 adds a usable palette picker to the existing Appearance section; #1846 reuses that control during its settings redesign.

## Interface contract

- **gRPC / protobuf:** None — appearance reads no daemon data and changes no protocol.
- **Exported Go APIs / interfaces:** None — no Go package changes.
- **Tool schemas:** None — no model-facing tool changes.
- **CLI / config:** None — no flag, environment variable, or deployment default is added. Default is always Stacklok green when the browser has no valid palette choice.
- **Events / persistence:** Browser-only `localStorage` keys are `mecatl-studio-theme` (existing; `light`, `dark`, or `system`, with missing/invalid meaning `system`) and `mecatl-studio.palette` (`default`, `aztec`, `mono`, or `solar`, with missing/invalid meaning `default`). Selecting System or Default removes its respective key. Both are device preferences outside the `studio.` account-scoped prefix, survive sign-out, and synchronize across tabs on a matching `storage` event or a storage clear (`key === null`). A denied or failing storage operation uses the same defaults on load and keeps a new choice in memory for the current page, including after a failed write or an unrelated notification; reload falls back safely. No BFF event, cookie, route, or server persistence is added.
- **Security / authority:** `apps/web/index.html` loads `/appearance-boot.js` from `apps/web/public/appearance-boot.js` as a same-origin, parser-blocking classic script in the document head, before styles and the app module. It reads only the two appearance keys and existing `studio.profile.ui-scale`; it validates known values, sets the root appearance and scale, and never sends them to a server. The existing static handler serves that asset to signed-out browsers with `no-cache`. It runs under the existing `script-src 'self'`, `style-src 'self' 'unsafe-inline'`, and `font-src 'self'` CSP in [security headers](../../apps/server/src/http/security.ts). Fonts load from Studio's own origin. No inline script, unsafe eval, SDK import, or new external design-system package is introduced.
- **Compatibility / migration:** Preserve `useTheme(): { theme, effectiveTheme, setTheme }` and its `Theme = "light" | "dark" | "system"` type in `apps/web/src/lib/theme.ts`. Add `Palette = "default" | "aztec" | "mono" | "solar"`, `BUILT_IN_PALETTES: readonly PaletteDef[]`, and `usePalette(): { palette: Palette; setPalette(next: Palette): void }` in `apps/web/src/lib/palettes.ts` for #1846. `PaletteDef` has `id: Palette`, `label: string`, `description: string`, and `swatch: string`; its four fixed entries appear below. Root `.dark` reflects the resolved light/dark theme; `data-palette` is absent for Default and equals a named palette id otherwise. Existing `mecatl-studio-theme` values continue to work without migration. Preserve the public props and variants of the current local UI components; move `OptionField` to `apps/web/src/components/ui/option-field.tsx` with its current `label`, `options`, `value`, and `onChange` contract, and update its existing settings import. Preserve `useIsMobile(): boolean` while making its first snapshot and resize updates agree with the 500px CSS pivot.

### Token contract for #1844 and #1846

The existing [base token set](../../apps/web/src/styles.css) remains the source for light and dark surfaces: `--background`/`--foreground`, `--card`, `--popover`, `--primary`, `--secondary`, `--muted`, `--accent`, `--destructive`, `--border`, `--input`, `--ring`, and their foreground partners. Status colors `--success`, `--warning`, and `--info` retain their semantic meaning in every palette. Add the light/dark roles below. `--control-border` marks the boundary of an input or outline action; `--border` continues to serve separators and cards. `--brand` is a decorative fill, `--btn-primary` is an action fill paired with white text, `--brand-ink` and `--brand-label` color text or icons on content surfaces, and shell controls use the nav roles. Register color roles through Tailwind's `@theme inline` mapping; components consume semantic roles rather than literal palette colors.

| Added base role | Light | Dark |
|---|---|---|
| `--destructive-strong` | hsl(0 74% 40%) | hsl(358.7 78% 55%) |
| `--sidebar` | hsl(240 4.8% 98.5%) | hsl(240 6.3% 12.5%) |
| `--avatar-background` | oklch(0.696 0 0 / 89.8%) | oklch(0.696 0 0 / 89.8%) |
| `--logo` | hsl(0 0% 28%) | hsl(0 0% 58%) |
| `--brand-label` | hsl(161 94% 21%) | hsl(158 64% 52%) |
| `--link` | hsl(211 92% 43%) | hsl(211 92% 66%) |
| `--brand-ink` | hsl(161 94% 21%) | hsl(142 50% 60%) |
| `--input-icon` | hsl(240 3.8% 46.1%) | hsl(0 0% 100%) |
| `--control-border` | #83838a | #8c8c8c |
| `--warning-foreground` | #ffffff | #18181b |

The contrast correction also changes three existing base roles: `--ring` is #6b6b75 in light and #a0a0a0 in dark; light `--warning` is hsl(35 92% 34%); dark `--destructive-foreground` is #18181b. Other base values remain as in the linked styles. Focus rings use solid `--ring` against content surfaces and solid `--nav-search-text` on the shell, without opacity that would drop them below 3:1. Warning banners use `--warning-foreground`; required control boundaries use `--control-border` and reach 3:1 against their adjacent surface. Status colors keep their role when their contrast value changes.

| `PaletteDef.id` / label | Description | Swatch |
|---|---|---|
| `default` / Default | Stacklok green. | hsl(161 94% 21%) |
| `aztec` / Aztec | Jade, turquoise and gold on obsidian. | #0f7f6c |
| `mono` / Mono | Neutral greys with a blue accent. | #2563eb |
| `solar` / Solar | Warm and light-leaning. | #9a7300 |

Every named palette supplies both `[data-palette="<id>"]` and `.dark[data-palette="<id>"]` blocks. Each pair overrides the same 16 roles: `--brand`, `--brand-ink`, `--brand-label`, `--btn-primary`, `--btn-primary-hover`, `--link`, `--info`, `--shell-gradient-start`, `--shell-gradient-mid`, `--shell-gradient-end`, `--nav-pill-bg`, `--nav-pill-text`, `--nav-icon`, `--nav-search-border`, `--nav-search-text`, and `--nav-kbd-bg`. The six frozen baseline combinations are pinned below. The correction table takes precedence where a baseline value fails the contrast floor; it also pins corrections to the existing Default palette. All other values stay fixed.

| Palette | Mode | Brand / ink / label | Button / hover | Link / info |
|---|---|---|---|---|
| Aztec | light | #0f7f6c / #0f7f6c / #0f7f6c | #0f7f6c / #0b6656 | #157a83 / #157a83 |
| Aztec | dark | #1fb39a / #4fd1b8 / #4fd1b8 | #12866f / #0e6b59 | #5cc8d0 / #5cc8d0 |
| Mono | light | #2563eb / #2563eb / #2563eb | #2563eb / #1d4ed8 | #2563eb / #2563eb |
| Mono | dark | #3b82f6 / #60a5fa / #60a5fa | #1d4ed8 / #1e40af | #60a5fa / #60a5fa |
| Solar | light | #9a7300 / #8a6800 / #8a6800 | #9a7300 / #7f5e00 | #cb4b16 / #1f6fa8 |
| Solar | dark | #b58900 / #e0b52e / #e0b52e | #8a6800 / #705400 | #f0884d / #5fb0ee |

| Palette | Mode | Shell start / mid / end | Nav pill background / text | Nav icon / search border / search text / keycap background |
|---|---|---|---|---|
| Aztec | light | #1f6b5c / #15302a / #0e1311 | #bfe6dc / #0e2a24 | #8a998f / #4b6a60 / #b7c4bd / #2e4039 |
| Aztec | dark | #143d33 / #0f1f1a / #0a0f0d | #a8ddd0 / #0e2a24 | #8a998f / #3d5a50 / #a9b8b1 / #26362f |
| Mono | light | #3a3a3a / #232323 / #141414 | #e4e4e7 / #18181b | #a1a1aa / #71717a / #c4c4c8 / #3f3f46 |
| Mono | dark | #262626 / #161616 / #0b0b0b | #d4d4d8 / #0b0b0b | #8a8a8a / #4a4a4a / #b5b5b5 / #2a2a2a |
| Solar | light | #a77b0a / #6b4f0a / #3f3008 | #f3e3b5 / #3f3008 | #d9c48a / #a8925a / #e6d8b4 / #7a5c0c |
| Solar | dark | #4a3708 / #2c2206 / #191404 | #e8d59a / #2c2206 | #b8a878 / #6b5a2a / #d6c89a / #3a2c08 |

| Palette / mode | Role | Frozen baseline → final contract |
|---|---|---|
| Default / light | `--nav-search-border`, `--nav-search-text` | #6e807d → #a5b0ae; #b4c0c1 → #cbd4d4 |
| Default / dark | `--nav-search-border` | #6e807d → #728481 |
| Aztec / light | `--nav-icon`, `--nav-search-border`, `--nav-search-text` | #8a998f → #aeb9b2; #4b6a60 → #aab9b4; #b7c4bd → #d7deda |
| Aztec / dark | `--btn-primary`, `--nav-search-border` | #12866f → #12846d; #3d5a50 → #71877f |
| Mono / light | `--nav-search-border` | #71717a → #85858d |
| Mono / dark | `--nav-search-border` | #4a4a4a → #727272 |
| Solar / light | `--btn-primary`, `--shell-gradient-start`, `--nav-search-border` | #9a7300 → #946e00; #a77b0a → #775707; #a8925a → #c0b188 |
| Solar / dark | `--nav-search-border` | #6b5a2a → #918561 |

These final values clear the declared floors against every gradient stop with a small margin. The Solar light start darkens enough for its existing search text and nav icon to remain legible on the whole band. The implementation records the correction table and rendered before/after evidence in #1779.

Typography uses self-hosted `@fontsource-variable/inter` for body and controls and `@fontsource/merriweather` at weights 300, 400, and 700 for display text, with local fallback stacks and `font-display: swap`. Map Tailwind `--font-sans` to `"Inter Variable"` and `--font-serif` to `"Merriweather"`; `pageTitleClass(...extra: Parameters<typeof cn>): string` in `apps/web/src/lib/typography.ts` supplies the single Merriweather 300 page-title treatment. The existing `--ui-scale` multiplies the root size: 16px at widths of at least 500px and 18px below 500px. Existing Tailwind 4px spacing steps and the `--radius` scale remain the shared control spacing and border grammar: 1px semantic borders, pill buttons, rounded inputs, and 12px cards. Button small/default/large heights are 32/36/44px at the 16px root and scale with `rem` on mobile. At widths below 500px, 16px Lucide glyphs render at 20px and 14px glyphs at 18px; deliberately larger icons retain their size.

### Local control contract

| Disposition | Controls and path | Contract |
|---|---|---|
| Reuse and normalize | `apps/web/src/components/ui/`: Button, Input, Textarea, Badge, Card, Dialog, AlertDialog, DropdownMenu, Sheet, Switch, Tooltip, Kbd | Keep existing component exports and Button `variant`/`size` names. Use semantic color, border, radius, and font roles, including solid focus and required control-boundary colors above. Each interactive state has visible keyboard focus in light and dark; disabled states do not activate. Dialogs and sheets trap and restore focus through their existing Radix primitives. |
| Move and repair | `OptionField` from `features/settings/option-field.tsx` to `components/ui/option-field.tsx` | Keep `OptionItem { value: string; label: string; description?: string; icon?: ComponentType }` and `{ label, options, value, onChange }`. The trigger announces its label and selected text. At widths ≥500px it opens a radio-style dropdown with Arrow, Home/End, Enter/Space, and Escape behavior. Below 500px it opens a bottom sheet with native radio options; Escape closes, the selected option is announced, and closing restores trigger focus. Resizing across the pivot closes the prior surface safely. |
| Add | `paletteSwatch(color: string)` in `apps/web/src/components/palette-swatch.tsx`, local to the appearance picker | Returns a stable decorative icon component for `OptionItem.icon`. The circle is `aria-hidden`; its adjacent palette label and description supply the accessible name. It is not a separate action target. |
| Owned by dependent issues | #1844's search command control; #1846's settings table, tabs, and other section-specific controls | Those components consume the tokens and focus rules above and remain local to Studio. |

## In scope — 4 scenarios, in implementation order

### Scenario 1 — tokens and fonts render a coherent vocabulary

The shell, form, dialog, and list use the semantic roles in [Studio styles](../../apps/web/src/styles.css) and retain the browser/BFF boundary of [ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md).

**Acceptance:**
- AC1.1: Default, Aztec, Mono, and Solar render in both light and dark. Every named palette has a complete light/dark override pair for the 16 listed roles; it changes accents and shell colors without changing surface/status semantics or Shiki's light/dark code-highlight roles.
  - verify: vitest:apps/web/src/lib/palettes.test.ts#a2VlcHMgZWFjaCBwYWxldHRlJ3MgbGlnaHQgYW5kIGRhcmsgdG9rZW4gc2V0cyBwYWlyZWQ — `keeps each palette's light and dark token sets paired`; inspect computed styles for all eight combinations.
- AC1.2: Inter and Merriweather load as same-origin font assets under the shipped CSP; no request goes to Google Fonts. Body and controls use Inter, and shared page titles use Merriweather 300.
  - verify: vitest:apps/web/src/lib/fonts.test.ts#bG9hZHMgSW50ZXIgYW5kIE1lcnJpd2VhdGhlciBmcm9tIHNhbWUtb3JpZ2luIGFzc2V0cw — `loads Inter and Merriweather from same-origin assets`; inspect the built CSS and network log because font loading depends on the bundle.
- AC1.3: Text, interactive icons, focus indicators, and control boundaries meet the declared contrast floors across the shell gradient, in all palette/theme combinations. Only failing baseline values receive corrections, recorded in #1779.
  - verify: inspection — computed-color contrast report plus maintainer-reviewed desktop/mobile screenshots in #1779; a visual judgment needs those rendered surfaces.

### Scenario 2 — appearance is applied before first paint and survives failure

The current [entry point](../../apps/web/src/main.tsx) applies theme after module loading, while [Studio's CSP](../../apps/server/src/http/security.ts) allows same-origin scripts. The new bootstrap resolves theme, palette, and UI scale before the first stylesheet and app module, keeping the one-origin boundary of [ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md).

**Acceptance:**
- AC2.1: On a cold load and reload, stored light/dark/system theme, named palette, and UI scale set the root appearance before the first styled paint; the React hooks read the same initial choice, so no default-colored frame appears.
  - verify: vitest:apps/web/src/lib/appearance-boot.test.ts#YXBwbGllcyBzdG9yZWQgYXBwZWFyYW5jZSBiZWZvcmUgdGhlIGZpcnN0IHN0eWxlc2hlZXQ — `applies stored appearance before the first stylesheet`; browser inspection records first paint on a throttled reload.
- AC2.2: A selected theme and palette survive reload and synchronize across mounted controls and tabs. A system theme follows the device change without changing the selected palette. Invalid values fall back to system/Default; a missing or failing media query resolves to light. When storage throws on read or write, switching still works for the current page, and reload safely uses defaults.
  - verify: vitest:apps/web/src/lib/appearance.test.ts#a2VlcHMgYW4gaW4tbWVtb3J5IGNob2ljZSB3aGVuIHN0b3JhZ2UgaXMgdW5hdmFpbGFibGU — `keeps an in-memory choice when storage is unavailable`; vitest:apps/web/src/lib/appearance.test.ts#cGVyc2lzdHMgdGhlbWUgYW5kIHBhbGV0dGUgYWNyb3NzIHJlbG9hZA — `persists theme and palette across reload`.
- AC2.3: Theme, palette, and system-mode changes suppress color transitions during the root change, then restore ordinary hover and focus transitions. Repeated rapid changes leave no suppression state behind.
  - verify: vitest:apps/web/src/lib/appearance.test.ts#c3VwcHJlc3NlcyBjb2xvciB0cmFuc2l0aW9ucyB3aGlsZSBhcHBlYXJhbmNlIGNoYW5nZXM — `suppresses color transitions while appearance changes`; inspect a recorded browser change.

### Scenario 3 — appearance controls work at the 500px pivot

The existing [settings choice control](../../apps/web/src/features/settings/option-field.tsx), [mobile hook](../../apps/web/src/lib/use-mobile.ts), and [Radix wrappers](../../apps/web/src/components/ui/dialog.tsx) provide the local starting point inside [ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md)'s web package.

**Acceptance:**
- AC3.1: The shared OptionField and the reused Button, Input, Dialog, Sheet, DropdownMenu, and Switch expose visible focus and keyboard operation in light and dark; the selected option is announced and focus returns to the trigger after close.
  - verify: vitest:apps/web/src/components/ui/option-field.test.tsx#c3VwcG9ydHMga2V5Ym9hcmQgc2VsZWN0aW9uIGFuZCBmb2N1cyByZXN0b3JhdGlvbg — `supports keyboard selection and focus restoration`; inspect focus in both modes because CSS focus visibility requires rendered styles.
- AC3.2: At 499px the choice uses the bottom sheet; at 500px it uses the dropdown. Neither clips options, traps focus behind an overlay, or presents two surfaces after a resize.
  - verify: vitest:apps/web/src/components/ui/option-field.test.tsx#c3dpdGNoZXMgdGhlIG9wdGlvbiBzdXJmYWNlIGF0IHRoZSA1MDBweCBwaXZvdA — `switches the option surface at the 500px pivot`; browser inspection at 499px and 500px proves layout.
- AC3.3: The Appearance section offers keyboard-reachable Light, Dark, System, Default, Aztec, Mono, and Solar choices with descriptive labels and swatches; changing either axis leaves the other selected.
  - verify: inspection — the browser Appearance flow exercises both selectors and the six named palette/theme combinations; component tests cover selection semantics.

### Scenario 4 — dependent surfaces use the same foundation

The current [shell](../../apps/web/src/components/shell/workspace-shell.tsx) and [settings workspace](../../apps/web/src/features/settings/settings-workspace.tsx) are the integration samples. The plan fixes their shared contract while [ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md) keeps the BFF and SDK ownership unchanged.

**Acceptance:**
- AC4.1: Desktop and mobile screenshots show the same token vocabulary across a shell, form, dialog, and list in Default and each named palette, in light and dark. The 500px pivot preserves readable text, visible focus, and unclipped controls. Maintainers record the component inventory and review the visual and keyboard evidence through #1779.
  - verify: inspection — a 4-palette × 2-theme × 2-width screenshot matrix and keyboard walkthrough in #1779; rendered design review is the proof.
- AC4.2: #1844 can style navigation and search through the shell/nav tokens and reused local controls; #1846 can render the appearance choices through `useTheme`, `usePalette`, and shared `OptionField` without inventing another palette or control API.
  - verify: inspection — import and token-use audit against the contract in this plan, plus `task studio:check` for the integrated Studio candidate.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Shell navigation, search behavior, error routes, and installable assets | #1844 | #1843 supplies their token and reused-control contract. |
| Settings section routing, provider detail, and About layout | #1846 | #1843 supplies the appearance state and choice control. |
| New BFF routes, daemon contracts, custom JSON palettes, and deployment palette configuration | Later approved work if requested | The frozen issue asks for local built-in appearance only; ADR 0351 remains in force. |
| Search command and settings-specific table, tabs, and selector primitives | #1844 and #1846 | Each dependent issue owns the controls its behavior needs. |

## Definition of done

1. Focused appearance and component tests and `task studio:check` pass on the implementation candidate; `task studio:build` verifies the fonts and boot asset in the production bundle.
2. `task docs` and `task ac-trace-strict` pass when this plan becomes `landed`. The public owning page, `user-docs/building/deployment/studio.md`, describes the shipped appearance choices; `task site:build` passes.
3. #1779 records the reused, moved, and added control inventory, contrast corrections, desktop/mobile screenshots, and keyboard review. The implementation PR cites this approved plan commit and reports interface conformance. Human review and merge remain separate.

## Deferred decisions and known risks

- The dedicated first-paint script and the React appearance store must use identical key validation and fallbacks; otherwise reload can flash a different palette. The first-paint proof checks the built document, not only hook state.
- Browser storage can fail after a successful read. The in-memory choice remains authoritative until reload, even if a same-tab notification or media event arrives.
- #1777 may mechanically move Studio files before implementation; that changes import paths and proof citations, while the token names, storage keys, and control signatures stay fixed. #1776 owns browser-test infrastructure. If its component harness has not landed, #1843 adds only the local test environment its focused proofs need and leaves Playwright and CI ownership with #1776.
