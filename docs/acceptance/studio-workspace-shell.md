# Mecatl Studio workspace shell — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — aligns browser navigation, search interaction, error states, and installation assets inside Studio's existing browser/BFF boundary; it adds no durable architecture or authority decision.
**Decision record:** None — [ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md) already owns the one-origin browser/BFF and published-SDK boundary, and this plan preserves the existing search authority contract.
**Phase:** Studio design alignment
**Status:** proposed, 2026-09-24. Contract drafted from [#1844](https://github.com/stacklok/mecatl/issues/1844), the [design review](https://github.com/stacklok/mecatl/issues/1779), current Studio, and the issue author's supplied visual reference; all material decisions below are resolved for Plan / Interface review.
**Delivery:** Split. An independent Plan / Interface PR targets `main` before an implementation PR.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#1844](https://github.com/stacklok/mecatl/issues/1844).
**Plan PR:** pending
**Approved baseline:** absent until the plan PR merges

Studio presents one workspace frame at desktop and mobile widths: a brand/home link, four primary destinations, a global search trigger, a reserved status band, and a route outlet. Search stays within the inventories already authorized for the signed-in browser. Unknown routes and unexpected rendering failures give a way back to the workspace. The installable manifest and icons identify the app; an already-loaded shell describes a lost connection without suggesting that agent actions run offline.

The [Studio architecture topic](../architecture.md#mecatl-studio) and [ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md) remain the system boundary. [#1843](https://github.com/stacklok/mecatl/issues/1843) owns semantic tokens, fonts, palettes, and shared controls. This plan consumes that contract for final styling and focus treatment. [#1845](https://github.com/stacklok/mecatl/issues/1845) owns public status, authentication recovery, banner state/copy/actions, and their security tests; this plan owns the shell positions in which those banners render. [#1777](https://github.com/stacklok/mecatl/issues/1777) owns any mechanical `apps/` move. Its possible `apps/` to `studio/` move overlaps every shell/search/route/asset path named here; a web feature-layout move overlaps `apps/web/src/components/shell/`, `features/search/`, and `routes/`. If #1777 lands first, implementation rebases onto its paths and relocates proof paths without doing a second structure move. `user-docs/building/deployment/studio.md` is the sole public documentation owner when the behavior ships.

## Human decisions

- [x] Visual baseline. — Decision: use the issue author's supplied shell, navigation, error-page, and asset reference together with #1844 and #1779. Keep this plan self-contained; the reference repository need not become a public dependency. Preserve the repository's stronger search and accessibility behavior where the reference differs.
- [x] Offline installation boundary. — Decision: installable manifest and icons, with a clear unavailable state when a loaded shell loses the connection. There is no service worker, offline cold-start guarantee, cached agent data, or locally executable agent action in this issue.
- [x] Ownership across the design batch. — Decision: #1844 owns composition and interaction; #1843 owns the tokens and reusable controls; #1845 owns status/auth facts and recovery actions. Final shell integration waits for their approved contracts, while independent assets, error routes, and interaction work can proceed.
- [x] File moves. — Decision: #1777 alone owns the mechanical app-layout move. #1844 does not move feature or shell directories; it rebases and updates citations if #1777 lands first.
- [x] Browser share target. — Decision: omit `share_target` from the manifest because Studio has no approved handler for shared text, titles, or URLs; installation does not imply an import workflow.

## Interface contract

- **gRPC / protobuf:** None — no daemon protocol or generated transport change.
- **Exported Go APIs / interfaces:** None — no Go source or public Go symbol change.
- **Tool schemas:** None — no model-facing tool change.
- **CLI / config:** None — no flag, environment key, server setting, or task change. The web manifest is static browser metadata specified below.
- **Events / persistence:** None — no BFF endpoint, event, schema, generated client, server persistence, service worker, or new browser data store. Browser installation may retain the public app identity and icons; existing account separation and theme storage remain owned by their current contracts.
- **Security / authority:** The browser continues to call only Studio's same-origin BFF, which alone uses the published SDK ([ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md)). Search indexes only static help pages and title/name/description/identifier metadata from this browser's already-authorized sessions, schedules, configured/learned skills, and memory inventories. Inventory queries begin only while the palette is open and the browser has an authorized session; the query string never goes to the BFF. No transcript, schedule prompt, memory value, or previous account's cached result appears in search. Losing or changing the account closes and clears the palette before another account can search. The manifest and icons carry no user data. Error pages show generic text, never a raw exception or credential.
- **Compatibility / migration:** Browser-only addition to the existing `/workspace/*` app. The brand link and recovery links lead to `/workspace/chat`; `Chats`, `Scheduled`, `Skills`, and `Settings` keep their current paths and route semantics. Unknown extensionless paths still receive the SPA document from the BFF and render a client-side not-found page; missing asset URLs still receive the existing HTTP `404`. Existing search index ranking, result destinations, and documented shortcuts stay intact. No server API migration or install migration is required.

The browser installation contract is `apps/web/public/manifest.webmanifest` with `name: "Mecatl Studio"`, `short_name: "Studio"`, `description: "The web workspace for the Mecatl agent harness"`, `id: "/workspace/"`, `start_url: "/workspace/chat"`, `scope: "/"`, `display: "standalone"`, and default `background_color`/`theme_color: "#03433e"`. It references `/icon-192.png` (192×192, PNG), `/icon-512.png` (512×512, PNG), and `/icon-maskable-512.png` (512×512, PNG, `purpose: "maskable"`). `index.html` links that manifest, `/apple-touch-icon.png` (180×180), and `/stacklok-favicon.png`; existing notification links use that same resolvable favicon. Its light/dark theme-color metadata uses the default shell colors (`#03433e`/`#02141b`); the viewport keeps browser zoom available. #1843 supplies the final token and shared-control implementations, without transferring ownership of them to this plan.

## In scope — 4 scenarios, in implementation order

### Scenario 1 — workspace navigation and status bands work at desktop and mobile widths

The shell follows the current route layout and the [Studio architecture topic](../architecture.md#mecatl-studio). The 500 px pivot retains direct access to all four destinations; the desktop active item has a visible label and the mobile row uses accessible icon buttons.

**Acceptance:**
- AC1.1: the brand link opens the chat draft; the four named destinations navigate by mouse, keyboard, and touch; within the primary navigation landmark, the active destination alone has `aria-current="page"`; inactive icon links have accessible names without relying on hover tooltips. The search trigger stays reachable at 320 px, 500 px, and desktop widths, with touch targets at least 44×44 CSS px.
  - verify: vitest:apps/web/src/components/shell/top-nav.test.tsx#a2VlcHMgZm91ciBkZXN0aW5hdGlvbnMgYWNjZXNzaWJsZSBhdCBkZXNrdG9wIGFuZCBtb2JpbGUgd2lkdGhz — `apps/web/src/components/shell/top-nav.test.tsx :: "keeps four destinations accessible at desktop and mobile widths"`
  - verify: inspection — focused `TopNav` interaction tests check route, accessible name, active state, and activation; maintainer-reviewed desktop/mobile screenshots show the 500 px pivot and 320 px minimum without clipped controls or page-level horizontal scrolling.
- AC1.2: the shell reserves a full-width band above the gradient/nav for unusable-connection and sign-in recovery states, including signed-out views. A distinct in-shell band immediately before the top nav holds transient reconnecting, storage, or trust notices. One connection cause never appears in both bands; independent storage/trust notices may coexist. Wrapped mobile banners never cover navigation or content; #1845 supplies their state, wording, and actions.
  - verify: inspection — focused shell rendering tests exercise empty, one-banner, and wrapped-banner states; desktop/mobile screenshots and keyboard traversal show band order and no obscured control, with #1845's banner tests supplying state coverage.
- AC1.3: the workspace frame accounts for display safe areas and changing banner height, keeps the route outlet independently usable, and gives all shell controls a visible focus indicator in light and dark themes using #1843's controls/tokens.
  - verify: inspection — desktop/mobile light/dark screenshots and focus traversal at the shell, banner, and content boundary; `task studio:check` validates the integrated controls.

### Scenario 2 — authorized global search is usable by pointer, keyboard, touch, and IME

The existing [search and shortcut contract](studio-search-shortcuts.md) remains authoritative for what may be indexed and which bindings work. This scenario aligns the palette's interaction and responsive presentation without widening that contract ([ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md)).

**Acceptance:**
- AC2.1: the search button and `Ctrl/Meta+K` open the same palette; at widths below 500 px it fills the available viewport without hiding the input or close control behind safe areas or the touch keyboard. At desktop widths it is a bounded dialog. Existing settings/shortcuts bindings still navigate once and close the palette; ordinary typing does not trigger plain-key shortcuts.
  - verify: inspection — focused shortcut/dialog interaction tests and desktop/mobile keyboard, pointer, and touch recordings show open, close, and navigation at both widths.
- AC2.2: opening search focuses its combobox; `Tab` stays within the dialog; Arrow Up/Down changes only the highlighted result; `Enter` activates that result once; `Escape`, the close control, or outside pointer activation closes without acting on the page behind it. A non-navigating close restores the invoking element when it still exists; result navigation moves focus to the destination's main heading or landmark.
  - verify: vitest:apps/web/src/features/search/global-search.test.tsx#dHJhcHMgZm9jdXMgYW5kIHN1cHByZXNzZXMgYmFja2dyb3VuZCBzaG9ydGN1dHMgd2hpbGUgb3Blbg — `apps/web/src/features/search/global-search.test.tsx :: "traps focus and suppresses background shortcuts while open"`
  - verify: inspection — focused dialog interaction tests assert focus trap/return, keyboard selection, background-shortcut suppression, and destination focus; a maintainer walks the flow with keyboard and screen reader.
- AC2.3: pointer hover changes the highlight without activation; a click or touch tap chooses one result; touch scrolling does not choose one. The input announces its listbox and active option, and loading, empty, and partial-inventory states remain distinguishable.
  - verify: inspection — focused pointer/touch interaction tests and mobile recordings show hover, tap, scroll, and result feedback; accessibility inspection confirms combobox/listbox relationships and status text.
- AC2.4: the index preserves existing result groups, ranking, and destinations while loading only authorized inventories on open. A failed inventory is reported as partial search, with available results retained. Sign-out, session expiry, or account switch removes the previous account's searchable results before the palette can reopen.
  - verify: inspection — existing `search-index` tests and new auth-transition component tests assert metadata-only indexing, on-open requests, partial failure, and account isolation; review of BFF request logs shows no submitted search query.
- AC2.5: composition start through the key event that commits an IME candidate leaves Enter and arrow keys to the input method, including events with `isComposing` or key code 229. Only a subsequent distinct Enter may choose a result.
  - verify: vitest:apps/web/src/features/search/search-keyboard.test.ts#bmV2ZXIgYWN0aXZhdGVzIHdoaWxlIGFuIElNRSBjb21wb3NpdGlvbiBpcyBjb21taXR0aW5n — `apps/web/src/features/search/search-keyboard.test.ts :: "never activates while an IME composition is committing"`
  - verify: inspection — focused `search-keyboard` tests cover native composition flag, key code 229, and post-commit Enter; desktop/mobile IME interaction evidence shows no navigation while composing.

### Scenario 3 — unknown routes and unexpected failures have accessible recovery

Client routing uses the existing SPA fallback, and the [Studio browser boundary](../adr/0351-mecatl-studio-in-repo-web-ui.md) remains unchanged.

**Acceptance:**
- AC3.1: an unknown application path shows a branded not-found page with one level-one heading and a keyboard/touch-operable link to `/workspace/chat`; direct loading a known route still works. A signed-out visitor first follows #1845's sign-in policy, then sees the requested route or not-found state after recovery.
  - verify: inspection — router tests cover direct known/unknown URLs and the home link; desktop/mobile screenshots show the branded state and focus order.
- AC3.2: a route render or loader failure shows generic error copy inside the workspace frame with `Try again` and a route home; retry resets the failed route without discarding the rest of the shell. A root render failure offers a full-page reload/home fallback. Neither state renders the raw error, stack, URL credential, or deployment detail.
  - verify: vitest:apps/web/src/components/error-page/error-routes.test.tsx#cmVjb3ZlcnMgZnJvbSBhIHJvdXRlIGVycm9yIHdpdGhvdXQgZXhwb3NpbmcgaXRzIG1lc3NhZ2U — `apps/web/src/components/error-page/error-routes.test.tsx :: "recovers from a route error without exposing its message"`
  - verify: inspection — focused route and root-boundary tests inject a sentinel error, assert it is absent from rendered text, then activate recovery by keyboard and touch; desktop/mobile screenshots cover both failure levels.
- AC3.3: the error pages have a programmatic main/heading focus target, visible focus on recovery controls, readable light/dark contrast, and no trap after recovery.
  - verify: inspection — keyboard and screen-reader walkthrough plus light/dark desktop/mobile screenshots; focused component tests assert heading, landmark, and recovery control names.

### Scenario 4 — installation identifies Studio and offline status remains honest

The image and one-origin serving rules remain those of [ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md); the BFF's [static handler](../../apps/server/src/http/static.ts) continues to keep API responses outside the SPA fallback.

**Acceptance:**
- AC4.1: a supporting browser on a secure origin can offer Studio for installation with the exact manifest identity, start URL, display mode, theme metadata, and resolvable 192/512/maskable/touch/favicon assets in the interface contract. The maskable mark stays inside its safe area, and standalone launch opens the chat route.
  - verify: vitest:apps/web/src/install-manifest.test.ts#ZGVjbGFyZXMgaW5zdGFsbCBtZXRhZGF0YSBhbmQgc2VydmVzIGVhY2ggaWNvbg — `apps/web/src/install-manifest.test.ts :: "declares install metadata and serves each icon"`
  - verify: inspection — manifest/schema and image-dimension checks; browser installability inspection and desktop/mobile installed-window screenshots confirm name, icon, scope, and launch route.
- AC4.2: when a loaded shell loses the BFF or daemon connection, #1845's banner occupies the reserved slot, names the connection or sign-in recovery need, and offers its applicable recovery action. An attempted agent action gives a visible connection-required outcome rather than appearing to execute locally; search reports unavailable inventories without inventing results. Restoring the connection clears the status through #1845's state contract.
  - verify: inspection — offline/restore interaction recording at desktop and mobile widths includes an attempted agent action, with focused #1845 banner-state tests and search partial-failure tests; inspect the rendered copy for unsupported offline-action claims.
- AC4.3: installation adds no service worker or cached private response; an offline cold launch has no promised Studio fallback, and the manifest advertises no share target.
  - verify: inspection — built web assets contain no service-worker registration or `share_target`; network inspection shows no API or user-data cache added by this issue.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Semantic tokens, fonts, palettes, shared controls | #1843 | Consume its approved contract and test final shell styling after it lands. |
| Public status, OIDC recovery, banner facts/actions | #1845 | #1844 supplies placement and integrated interaction evidence only. |
| Mechanical `apps/` layout move and browser-test infrastructure | #1777 and #1776 | Rebase on their paths/tools; do not duplicate either change. |
| Server-side search, search over transcript/prompt/memory values, share ingestion | later approved work | Preserve the existing authorized metadata index. |
| Cold offline launch, agent execution without the BFF, private-response caching | later product decision | Installation here supplies app identity and loaded-shell status only. |

## Definition of done

1. The implementation candidate passes focused shell/search/route/asset tests, `task studio:check`, `task docs`, `task site:build`, and `task ac-trace-strict`; applicable repository `task lint`, `task test:race`, and offline demo gates remain green.
2. The implementation PR updates the owning `user-docs/building/deployment/studio.md` page in the same PR, links this Plan / Interface PR and its approved commit, and reports conformance to all seven interface categories.
3. Maintainers review before/after desktop and mobile screenshots plus pointer, keyboard, touch, focus, and IME interaction evidence through #1779. The implemented app passes `task studio:check` after #1843 styling and #1845 banners integrate.
4. `/panel-review` reports no ship blockers or unwaived reviewer failures. Humans alone merge the implementation PR.

## Deferred decisions and known risks

- #1777 can change proof paths before implementation. Relocate test locators with the mechanical move; the observable AC text stays fixed.
- Browser install prompts vary by platform. Verify installability on a supporting secure-context browser rather than treating a missing prompt on every browser as a product failure.
