# Mecatl Studio global search and keyboard shortcuts — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — adds two browser-only surfaces (the global search palette and the keyboard-shortcuts reference page) to the Studio web app over inventories the BFF already serves; no BFF route, schema, or daemon change, and no new durable architecture decision.
**Decision record:** None — the boundary is ADR 0351's; these surfaces add no new ingress, they read what earlier layers already authorised.
**Phase:** capability — fifth and last feature layer of the Studio stack
**Status:** landed, 2026-09-24. Candidate transition after `task studio:check`, `task docs`, and `task ac-trace` passed on the implementation layer; authoritative only when that PR merges. Proposed 2026-09-21. Eleventh layer of the Studio `gh stack`; scope decisions were taken by the assistant under the directing human's go-ahead and are recorded below for review.
**Delivery:** Split, under the same stacking exception as [the bootstrap plan](studio-bootstrap.md): this plan is a stack layer, the implementation is the next layer, and nothing merges until the whole series is reviewed.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#1736](https://github.com/stacklok/mecatl/issues/1736).
**Plan PR:** [stacklok/mecatl#1763](https://github.com/stacklok/mecatl/pull/1763)
**Approved baseline:** absent until the stack root merges

Port the prototype's two remaining browser features onto Studio: a global search palette
(opened from the top nav or by keyboard) that indexes the static pages and every inventory the
BFF already serves — chats, schedules, configured and learned skills, user memory — and a
keyboard-shortcuts reference page that lists the registry's bindings and the deployment's
enabled features. Neither talks to a new route; both are pure functions plus rendering.

## Human decisions

- [x] One plan for two features. — Decision: search and the shortcuts page ship in one Bounded plan because they are coupled (the palette registers the shortcut that opens the reference page, and the reference page documents the palette's binding) and neither has a BFF surface; the bootstrap plan's one-plan-per-feature rule was written for features that add `/api/v1` routes.
- [x] Scope of the port. — Decision: the whole prototype search index, palette, shortcut registry, reference page, and help-feature derivation; the top nav regains its search button where the chat layer removed it.
- [x] What search indexes. — Decision: only browser-owned inventories the BFF already returned to this user (sessions, schedules, configured and learned skills, memory keys) plus the static page list; inventories are fetched only while the palette is open, and results never include bodies, transcripts, or memory values — titles, names, and descriptions only.
- [x] Help features are honest. — Decision: the reference page lists only capabilities with a reachable spot in Studio's UI, each read off the same snapshot gate the owning component uses; a capability with no UI surface is not listed.

## Interface contract

- **gRPC / protobuf:** None — no daemon interaction beyond inventories the earlier layers already fetch.
- **Exported Go APIs / interfaces:** None — no Go source changes.
- **Tool schemas:** None — Studio adds no model-facing tool.
- **CLI / config:** None — no new variable, flag, task, or compose setting.
- **Events / persistence:** No BFF change: no route, schema, generated-client, or OpenAPI change, and no browser persistence. The search index is an in-memory list rebuilt from the open palette's queries; the shortcut registry is a static table (`shortcut-registry.ts`) keyed by identifier with a closed group set.
- **Security / authority:** No new ingress. Search results are derived from responses the BFF already authorised for this session (the `401` gate applies to every inventory query) and expose only titles, names, descriptions, and identifiers; a query is matched client-side with accent folding and never sent to the BFF. Keyboard bindings are pinned to browser combinations the app may prevent-default (primary-modifier commands and Escape) plus a few plain keys such as `?`, and only primary-modifier commands and Escape fire inside a text field, so a shortcut cannot swallow typed input. A schedule is indexed by its name, model, status, and owner, never its prompt. The palette is a modal dialog: it traps and restores focus, Escape closes only the palette, the chat's global shortcuts do not fire behind it, a palette shortcut that navigates also closes it, and Enter during an IME composition never activates a result.
- **Compatibility / migration:** Additive in the web app only: the top nav gains the search button, the about section's "Keyboard shortcuts" link (left out by the settings layer) returns, and `/workspace/shortcuts` becomes a route. No route or schema changes elsewhere.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — the search index ranks and groups browser-owned inventories

The index is a pure function over inventories the BFF already served; it adds no data the user
could not already see ([ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md), decision 3).

**Acceptance:**
- AC1.1: the index always contains the static pages, even when every inventory is empty, and indexes every browser-owned inventory when present.
  - verify: vitest:apps/web/src/features/search/search-index.test.ts#YWx3YXlzIGluY2x1ZGVzIHRoZSBzdGF0aWMgcGFnZXMsIGV2ZW4gd2l0aCBldmVyeSBpbnZlbnRvcnkgZW1wdHk — `apps/web/src/features/search/search-index.test.ts :: "always includes the static pages, even with every inventory empty"`
  - verify: vitest:apps/web/src/features/search/search-index.test.ts#aW5kZXhlcyBldmVyeSBicm93c2VyLW93bmVkIGludmVudG9yeQ — `apps/web/src/features/search/search-index.test.ts :: "indexes every browser-owned inventory"`
- AC1.2: exact and title-prefix matches rank ahead of description matches; every query term must match; accents are ignored; the result limit is observed; no results yields an empty array.
  - verify: vitest:apps/web/src/features/search/search-index.test.ts#cmFua3MgZXhhY3QgYW5kIHRpdGxlLXByZWZpeCBtYXRjaGVzIGFoZWFkIG9mIGRlc2NyaXB0aW9uIG1hdGNoZXM — `apps/web/src/features/search/search-index.test.ts :: "ranks exact and title-prefix matches ahead of description matches"`
  - verify: vitest:apps/web/src/features/search/search-index.test.ts#bWF0Y2hlcyBhbGwgcXVlcnkgdGVybXMsIGlnbm9yZXMgYWNjZW50cywgYW5kIG9ic2VydmVzIHRoZSByZXN1bHQgbGltaXQ — `apps/web/src/features/search/search-index.test.ts :: "matches all query terms, ignores accents, and observes the result limit"`
  - verify: vitest:apps/web/src/features/search/search-index.test.ts#cmV0dXJucyBhbiBlbXB0eSBhcnJheSBmb3Igbm8gcmVzdWx0cw — `apps/web/src/features/search/search-index.test.ts :: "returns an empty array for no results"`
- AC1.3: results group by section in the fixed order Chats, Schedules, Skills, Memory, Pages with empty sections dropped, and each result navigates to its owning route (a chat by session id, a schedule by name, a skill by view and item, a memory key into Settings → Memory, a page by path).
  - verify: vitest:apps/web/src/features/search/search-index.test.ts#YnVja2V0cyBieSBzZWN0aW9uIGluIHRoZSBmaXhlZCBzZWN0aW9uIG9yZGVyLCBkcm9wcGluZyBlbXB0eSBzZWN0aW9ucw — `apps/web/src/features/search/search-index.test.ts :: "buckets by section in the fixed section order, dropping empty sections"`

### Scenario 2 — the shortcut registry is closed, unique, and browser-safe

Bindings are a static registry with pinned platform rendering; matching is exact on modifiers
([ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md)).

**Acceptance:**
- AC2.1: every binding has a unique identifier in a known group; app-wide and chat bindings use combinations the browser lets the app prevent.
  - verify: vitest:apps/web/src/features/shortcuts/shortcut-registry.test.ts#aGFzIHVuaXF1ZSBpZGVudGlmaWVycyBpbiBrbm93biBncm91cHM — `apps/web/src/features/shortcuts/shortcut-registry.test.ts :: "has unique identifiers in known groups"`
  - verify: vitest:apps/web/src/features/shortcuts/shortcut-registry.test.ts#cGlucyBhcHAtd2lkZSBhbmQgY2hhdCBiaW5kaW5ncyB0byBwcmV2ZW50YWJsZSBicm93c2VyIGNvbWJvcw — `apps/web/src/features/shortcuts/shortcut-registry.test.ts :: "pins app-wide and chat bindings to preventable browser combos"`
- AC2.2: keycaps render platform modifiers and named keys; matching accepts primary modifiers, plain keys, aliases, and punctuation, rejects missing, extra, or incomplete modifiers (Ctrl and Meta held together never match, and Shift must match exactly except on punctuation, whose produced character already encodes it), and allows only primary-modifier commands and Escape to fire inside inputs.
  - verify: vitest:apps/web/src/features/shortcuts/shortcut-registry.test.ts#cmVuZGVycyBwbGF0Zm9ybSBtb2RpZmllcnMgYW5kIG5hbWVkIGtleXM — `apps/web/src/features/shortcuts/shortcut-registry.test.ts :: "renders platform modifiers and named keys"`
  - verify: vitest:apps/web/src/features/shortcuts/shortcut-registry.test.ts#bWF0Y2hlcyBwcmltYXJ5IG1vZGlmaWVycywgcGxhaW4ga2V5cywgYWxpYXNlcywgYW5kIHB1bmN0dWF0aW9u — `apps/web/src/features/shortcuts/shortcut-registry.test.ts :: "matches primary modifiers, plain keys, aliases, and punctuation"`
  - verify: vitest:apps/web/src/features/shortcuts/shortcut-registry.test.ts#cmVqZWN0cyBtaXNzaW5nLCBleHRyYSwgYW5kIGluY29tcGxldGUgbW9kaWZpZXJz — `apps/web/src/features/shortcuts/shortcut-registry.test.ts :: "rejects missing, extra, and incomplete modifiers"`
  - verify: vitest:apps/web/src/features/shortcuts/shortcut-registry.test.ts#YWxsb3dzIHByaW1hcnktbW9kaWZpZXIgY29tbWFuZHMgYW5kIGVzY2FwZQ — `apps/web/src/features/shortcuts/shortcut-registry.test.ts :: "allows primary-modifier commands and escape"`
  - verify: vitest:apps/web/src/features/shortcuts/shortcut-registry.test.ts#cmVqZWN0cyBhbiBleHRyYSBTaGlmdCBvbiBhIGxldHRlciBiaW5kaW5n — `apps/web/src/features/shortcuts/shortcut-registry.test.ts :: "rejects an extra Shift on a letter binding"`
  - verify: vitest:apps/web/src/features/shortcuts/shortcut-registry.test.ts#cmVqZWN0cyBDdHJsIGFuZCBNZXRhIHByZXNzZWQgdG9nZXRoZXI — `apps/web/src/features/shortcuts/shortcut-registry.test.ts :: "rejects Ctrl and Meta pressed together"`
  - verify: vitest:apps/web/src/features/shortcuts/shortcut-registry.test.ts#bWF0Y2hlcyBzaGlmdGVkIHB1bmN0dWF0aW9uIGJ5IGl0cyBwcm9kdWNlZCBrZXk — `apps/web/src/features/shortcuts/shortcut-registry.test.ts :: "matches shifted punctuation by its produced key"`
  - verify: vitest:apps/web/src/features/shortcuts/shortcut-registry.test.ts#bWF0Y2hlcyBldmVyeSByZWdpc3RlcmVkIGJpbmRpbmcgYnkgdGhlIGtleSBjb21iaW5hdGlvbiBpdCBkb2N1bWVudHM — `apps/web/src/features/shortcuts/shortcut-registry.test.ts :: "matches every registered binding by the key combination it documents"`

### Scenario 3 — the reference page and the palette join the shell

The reference page documents bindings and lists enabled features off the same capability gates
the owning components use; the palette mounts in the top nav ([studio-chat](studio-chat.md); [ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md)).

**Acceptance:**
- AC3.1: the help-feature rows read every capability as disabled when the daemon reports nothing on, flip on with the capability, gate the Memory row on the user-model capability, and read memory consolidation off the exact gate the Consolidate button uses.
  - verify: vitest:apps/web/src/features/shortcuts/help-features.test.ts#cmVhZHMgZXZlcnkgcm93IG9mZiBhcyBkaXNhYmxlZCB3aGVuIHRoZSBkYWVtb24gcmVwb3J0cyBub3RoaW5nIG9u — `apps/web/src/features/shortcuts/help-features.test.ts :: "reads every row off as disabled when the daemon reports nothing on"`
  - verify: vitest:apps/web/src/features/shortcuts/help-features.test.ts#ZmxpcHMgYSBmbGFnIHJvdyBvbiB3aGVuIHRoZSBjYXBhYmlsaXR5IGlzIHRydWU — `apps/web/src/features/shortcuts/help-features.test.ts :: "flips a flag row on when the capability is true"`
  - verify: vitest:apps/web/src/features/shortcuts/help-features.test.ts#cmVhZHMgbWVtb3J5IGNvbnNvbGlkYXRpb24gb2ZmIHRoZSBleGFjdCBnYXRlIHRoZSBDb25zb2xpZGF0ZSBidXR0b24gdXNlcw — `apps/web/src/features/shortcuts/help-features.test.ts :: "reads memory consolidation off the exact gate the Consolidate button uses"`
  - verify: vitest:apps/web/src/features/shortcuts/help-features.test.ts#Z2F0ZXMgdGhlIE1lbW9yeSByb3cgb24gdGhlIHVzZXItbW9kZWwgY2FwYWJpbGl0eSwgbm90IGdlbmVyaWMgbWVtb3J5 — `apps/web/src/features/shortcuts/help-features.test.ts :: "gates the Memory row on the user-model capability, not generic memory"`
- AC3.2: `/workspace/shortcuts` renders the reference page; the top nav shows the search button with its keycap; the about section's "Keyboard shortcuts" link returns; the palette's shortcuts (open search, open settings, open shortcuts) are registered once through the shortcut provider.
  - verify: inspection — route file, `top-nav.tsx`, `settings-workspace.tsx`, and `global-search.tsx`; `pnpm --filter @mecatl-studio/web typecheck` proves the typed links.
- AC3.4: while the palette is open only its own shortcuts can fire; its input and results follow the combobox/listbox pattern with the highlighted option kept in view; a keypress during an IME composition never activates a result; a schedule's prompt is never searchable or displayed.
  - verify: vitest:apps/web/src/features/shortcuts/shortcut-registry.test.ts#YmxvY2tzIHNob3J0Y3V0cyBhbiBvcGVuIHNjb3BlIGRvZXMgbm90IHBlcm1pdA — `apps/web/src/features/shortcuts/shortcut-registry.test.ts :: "blocks shortcuts an open scope does not permit"`
  - verify: vitest:apps/web/src/features/shortcuts/shortcut-registry.test.ts#YWxsb3dzIGV2ZXJ5IHNob3J0Y3V0IHdoZW4gbm8gbW9kYWwgc2NvcGUgaXMgb3Blbg — `apps/web/src/features/shortcuts/shortcut-registry.test.ts :: "allows every shortcut when no modal scope is open"`
  - verify: vitest:apps/web/src/features/search/search-keyboard.test.ts#bmV2ZXIgYWN0aXZhdGVzIHdoaWxlIGFuIElNRSBjb21wb3NpdGlvbiBpcyBjb21taXR0aW5n — `apps/web/src/features/search/search-keyboard.test.ts :: "never activates while an IME composition is committing"`
  - verify: vitest:apps/web/src/features/search/search-keyboard.test.ts#YWN0aXZhdGVzIHRoZSBoaWdobGlnaHRlZCByZXN1bHQgb24gYSBwbGFpbiBFbnRlcg — `apps/web/src/features/search/search-keyboard.test.ts :: "activates the highlighted result on a plain Enter"`
  - verify: vitest:apps/web/src/features/search/search-index.test.ts#bmV2ZXIgZXhwb3NlcyBhIHNjaGVkdWxlIHByb21wdCBhcyBzZWFyY2hhYmxlIG9yIGRpc3BsYXllZCB0ZXh0 — `apps/web/src/features/search/search-index.test.ts :: "never exposes a schedule prompt as searchable or displayed text"`
- AC3.3: `task studio:check` passes; `task ac-trace` resolves every proof named here; the generated contracts are unchanged.
  - verify: inspection — the CI `studio` job and an empty `git diff` under `apps/contracts`.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Server-side search over transcripts or memory bodies | a later plan if the product asks | the index is browser-owned by decision |
| Customisable key bindings | never for this plan | the registry is static |
| `user-docs/` pages for Studio | the top layer of the stack | bootstrap plan's documentation decision |

## Definition of done

1. `task studio:check`, `task docs`, and `task ac-trace` pass; `apps/contracts` has no diff.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `docs/architecture.md`'s Studio section names the two browser-only surfaces.
4. The implementation layer records the exact commit of this plan it built against.

## Deferred decisions and known risks

- The palette fetches five inventories on open; on a large deployment the sessions list is paged by the chat layer's BFF logic, so the palette sees what the sidebar sees.
- The `kbd` primitive the palette and reference page use returns in this layer (deleted as unused in the chat layer).
