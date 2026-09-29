# Mecatui saved-memory bounded browser - acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — this changes the local, user-visible layout, browsing, and overlay ownership of one existing mecatui inspector without changing the memory store or a public interface.
**Decision record:** None — the existing `surface` and bounded controls cover this client-local interaction; no durable architecture decision is required.
**Phase:** bounded-inventory adoption, saved-memory slice
**Status:** proposed, 2026-09-29. Drafted for human Plan / Interface review; no behavior has shipped from this plan.
**Delivery:** Split. Selection, wheel ownership, responsive geometry, and surface migration warrant separate contract review before implementation.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#1896](https://github.com/stacklok/mecatl/issues/1896).

`/usermodel` remains an idle-only, read-only inspector over a live user-model index with lazy exact-value/history fetch. Its inventory uses a pointer-owned `bounded.List` with each exact fact key as stable identity, a one-marker-cell selection gutter rendered through `presentListRow`, and physical-row scrolling independent of selection. Its detail/history uses a separate pointer-owned `bounded.Viewport`. A modal `surface` owns only the open panel state and its input; the parent centers and frames its responsive card. This replaces the fixed fourteen-line window without changing the server's memory, RPC, or permission contracts.

## Human decisions

- [x] Surface ownership — Decision: migrate `/usermodel` from the root-owned overlay to the existing single `surface` lifecycle in this slice. The open-only inventory/detail state belongs to the surface; retain a Model-lifetime request token for close/reopen fencing. Remove competing root key/message/render paths. Parent-owned placement remains a centered card, not a full-region view.
- [x] Geometry and controls — Decision: cap the normal card's outer width at `min(128, offered width)` and use the offered conversation height after measured card frame, title, metadata, overflow chrome, and fixed footer. Inventory selection moves by logical item, paging moves the selected item by physical page, and wheel scrolls one physical row without changing selection. Detail navigation moves physical lines/pages. If normal chrome and one body row cannot fit, render a width-clipped close-only fallback; nonpositive geometry renders nothing.
- [x] Interaction boundary — Decision: Enter inspects the selected exact key, Escape from detail returns to the same inventory selection/window, and Escape from inventory closes and refocuses the prompt. No pointer selection or activation, editing, explicit refresh, or lifecycle mutation is added. A visible panel consumes wheel and primary clicks without moving the conversation; hover and releases do not initiate hidden selection. Existing global right-click copy and middle-click paste behavior remains unchanged. Compact geometry only accepts Close for panel navigation.
- [x] Exact-detail consistency — Decision: a current, matching detail response replaces the index and its selected-key identity together. Show a returned detail only when its current key matches the requested key, even if the refreshed active index no longer contains that key (a deleted fact can retain history). A missing or mismatched detail shows the requested key with no claimed value/history, as the existing missing-detail fallback does. Returning to the index selects the retained key or clamps to a valid surviving entry.

## Interface contract

- **gRPC / protobuf:** None — keep the existing `GetUserModel` live-index and exact-key detail requests, fields, and server capability bit; no wire change.
- **Exported Go APIs / interfaces:** None — consume existing package-private `surface`, `bounded.List`, `bounded.Viewport`, and `presentListRow`; no public Go contract changes.
- **Tool schemas:** None — `RememberUser`, `InspectUserMemory`, `ForgetUserMemory`, and `UndoUserMemory` retain their existing agent-side contracts; this UI remains read-only.
- **CLI / config:** None — `/usermodel` retains its command name, idle/capability gating, and remappable bindings. Normal inventory Up/Down select logical keys, ScrollU/ScrollD page selection through physical rows, ScrollTop/ScrollBottom select endpoints; normal detail uses the same configured actions for physical-line/page/end scrolling. Wheel scrolls the visible inventory or detail by one physical row per event. No new flag, setting, or binding is introduced.
- **Events / persistence:** None — selection, viewport offsets, and layout are ephemeral client state. The user-model store, revision history, and index/entry retrieval remain unchanged.
- **Security / authority:** None — preserve exact-key detail lookup, terminal sanitization of all server-derived content before styling, idle and capability gates, and request-token correlation; opening the browser grants no memory-write authority. A closed or older surface must not accept delayed index/detail results.
- **Compatibility / migration:** Replace root-owned `userModelState` and numeric cursor/scroll windows with one modal-owned inspector containing separate pointer-owned list and viewport. Preserve live fetch once per open, lazy detail fetch, current value/provenance/available-history presentation, loading/error/empty copy, and Escape/focus behavior. Normal geometry grows to available height and is capped at 128 columns; page keys and wheel become bounded physical browsing without conversation leakage. Update the existing [`/usermodel` behavior reference](../tui.md) and the public [TUI usage guide](../../user-docs/mecatui/using-the-tui.md) on implementation, not in this plan PR.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — responsive saved-memory card

The inspector uses the [surface placement contract](../design/surface-migration-plan.md#binding-decisions-already-made--do-not-reopen) and the responsive geometry of the [skills bounded-inventory plan](mecatui-skills-inventory-bounded-viewport.md#human-decisions). Its parent centers and frames the body returned by the surface; both inventory and detail render from one current measured offer.

**Acceptance:**
- AC1.1: Normal inventory and detail cards are centered, fit the offered width and height, and have outer width `min(128, offered width)`; the content width subtracts the measured frame. The normal body receives all available physical rows after fixed chrome and any overflow indicator rather than a fourteen-line cap.
  - verify: `TestMecatuiSavedMemoryBoundedBrowser_Scenario1_ResponsiveCardAndBodyBudget`
- AC1.2: Loading, long terminal-sanitized errors and values, enabled-empty, and read-only history states fit through the same geometry path without hidden or overflowing footer content.
  - verify: `TestMecatuiSavedMemoryBoundedBrowser_Scenario1_AllStatesFit`
- AC1.3: At positive conversation geometry unable to fit the measured card frame, normal chrome, and one body row, inventory and detail show a width-clipped close-only fallback without constructing or moving a list/viewport. The parent may force compact rendering when framed content geometry is zero; the resulting card still fits the conversation offer. Nonpositive conversation geometry renders nothing.
  - verify: `TestMecatuiSavedMemoryBoundedBrowser_Scenario1_CompactFallback` through root render at the frame threshold and at nonpositive dimensions

### Scenario 2 — stable selection and complete detail browsing

The live index's exact key is the stable list identity. The one-cell selection marker and padding use [`presentListRow`](../../cmd/mecatui/ui/list_row.go), while headers, metadata, overflow counts, and footer stay inspector-owned. The detail is a read-only physical viewport over the existing value, provenance, and bounded history described in [ADR 0011](../adr/0011-soul-and-user-model.md).

**Acceptance:**
- AC2.1: Up/Down select logical facts and reveal their wrapped key/description; remappable page/top/bottom select and reveal by physical page/endpoint. A wheel event scrolls one physical line without changing the exact-key selection. The standard one-cell marker/padding remains aligned on wrapped lines, and no fitting row is hidden by indicators when it can be shown.
  - verify: `TestMecatuiSavedMemoryBoundedBrowser_Scenario2_StableSelectionAndReveal`
- AC2.2: Rewrapping, resize, and a current-open detail response that replaces the index preserve the selected exact key and top key/physical-line anchor when retained, and otherwise clamp to a valid fact/window. Empty inventories disable Enter, and no resize or replacement leaves a reachable blank page.
  - verify: `TestMecatuiSavedMemoryBoundedBrowser_Scenario2_IdentityAndAnchorContinuity`
- AC2.3: The index and detail from a matching exact-key response become one coherent view: a matching returned detail may show the requested fact and retained history even if its key is absent from the new active index; absent or mismatched detail shows only the requested key, not another fact's value. Escape returns to the retained or valid clamped inventory selection.
  - verify: `TestMecatuiSavedMemoryBoundedBrowser_Scenario2_ExactDetailConsistency`
- AC2.4: Enter fetches only the selected key's exact detail; its current value, provenance, and available bounded revisions remain terminal-safe and reachable with configured line, page, and endpoint navigation. Escape returns to the inventory without resetting its selection/offset when its anchor survives; reopening the detail begins at its first line.
  - verify: `TestMecatuiSavedMemoryBoundedBrowser_Scenario2_DetailHistoryAndReturn`

### Scenario 3 — modal lifecycle and visible input ownership

The existing [surface lifecycle](../design/surface-migration-plan.md#maintainer-rules-for-future-migrations) owns all open-only state and handles its own unary results. A Model-lifetime generation fence rejects stale results after close, return from detail, and reopen. The inspector remains the read-only view described in [`docs/tui.md`](../tui.md).

**Acceptance:**
- AC3.1: The command appears only with capability and client wiring, opens only while idle, blurs the prompt, requests one live index, and closes from inventory to restore focus. Escape from detail backs out without closing the surface; every other key stays owned by the visible panel.
  - verify: `TestMecatuiSavedMemoryBoundedBrowser_Scenario3_OpenBackCloseAndFocus`
- AC3.2: Old index/detail successes or errors arriving after detail back, close, or reopen cannot alter the current inventory, detail, loading/error state, selection, or scroll position; only the matching current-open request for the selected exact key can show detail.
  - verify: `TestMecatuiSavedMemoryBoundedBrowser_Scenario3_RejectsStaleResults`
- AC3.3: Normal wheel input moves only the visible inventory/detail viewport, remains consumed at endpoints, and does not change selection or the hidden conversation. Compact wheel input is consumed without movement. Primary click, motion, and release cannot start selection or activate a fact; existing global right-click copy and middle-click paste paths retain their current gates and behavior.
  - verify: `TestMecatuiSavedMemoryBoundedBrowser_Scenario3_WheelAndPointerIsolation`
- AC3.4: While open, exactly one surface owns inventory/detail state and their key, render, and RPC-result paths; the root has no parallel user-model overlay field or legacy reducer/render/key branch. Closing tears down that state rather than retaining a Model tombstone.
  - verify: inspection — review `Model`, `renderBody`, `updateInventoryMsgs`, and key routing against the [surface migration checklist](../design/surface-migration-plan.md#maintainer-rules-for-future-migrations), alongside `TestMecatuiSavedMemoryBoundedBrowser_Scenario3_OpenBackCloseAndFocus`.
- AC3.5: The existing TUI behavior reference and public TUI usage guide describe the shipped bounded navigation and compact fallback without presenting this plan as already implemented.
  - verify: inspection — update [`docs/tui.md`](../tui.md) and the [public TUI usage guide](../../user-docs/mecatui/using-the-tui.md) with implementation, then run `task docs` and `task site:build`.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Project-memory browser, memory writes, undo/forget controls, filtering, refresh, or pagination | Separate memory features | `/usermodel` remains read-only with one live index request per open. |
| Smooth-wheel burst coalescing and click-to-select | [#1790](https://github.com/stacklok/mecatl/issues/1790) and separate interaction review | This slice uses only per-event wheel scrolling and no pointer activation. |
| Soul, Help, Dream, Approval, stored-session transcript, and main conversation | [#1742](https://github.com/stacklok/mecatl/issues/1742) follow-ons | No mechanical migration of unrelated views. |

## Definition of done

1. Focused user-model, bounded-control, surface, and UI integration tests pass; then `task lint`, `task test`, and `task test:race` pass on the integrated candidate.
2. The implementation updates the owning behavior references and passes `task docs` and `task site:build`; golden changes, if any, pass `task test:golden`.
3. `task ac-trace-strict` resolves every named proof when this plan becomes `landed`; `go run ./cmd/mecademo` remains green.
4. The implementation PR links the merged Plan / Interface PR and approved baseline; `/panel-review` has no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- Surface migration must remove legacy root ownership rather than leave a competing state copy. A rendered list's frame-sensitive geometry and request lifetime need regression proofs at the actual root update/render boundary.
- Detail lines may rewrap on resize; clamp physical offsets without showing a blank last page. The UI must preserve exact server values in requests while sanitizing only their display.
