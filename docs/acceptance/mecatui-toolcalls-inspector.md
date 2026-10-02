# Mecatui tool-call inspector - acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — a new client-local conversation browser changes visible navigation and detail presentation but not durable ownership, protocol, or trust boundaries.
**Decision record:** None — the existing TUI surface, typed scrollback, and bounded navigation contracts contain this feature without a new durable architecture decision.
**Phase:** issue #1361, first slice: additive tool-call inspection
**Status:** proposed, 2026-10-02. The directing operator resolved the interaction and scope decisions before plan review; approval requires the Plan / Interface PR merge.
**Delivery:** Split. The new command, live browsing behavior, and detail contract benefit from human review before implementation.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#1361](https://github.com/stacklok/mecatl/issues/1361).

The main conversation remains unchanged. A local `/toolcalls` browser lets a reader inspect one call's complete available arguments and results without expanding every card. It works during a run and on the loaded transcript of the current session. The browser is a second view of the canonical UI [scrollback](../../cmd/mecatui/ui/internal/scrollback/scrollback.go), not another history store. This slice does not change `ctrl+t`, shrink cards, or add pointer activation; later issue #1361 slices require separate plans after hands-on review.

## Human decisions

- [x] Scope and order — Decision: include top-level calls represented in the main conversation, including the parent `Subagent` and `Team` calls, but not the calls inside delegated work or calls in other sessions. List oldest first and open at the newest call; show a useful empty state when none exist. No search or new global shortcut in this slice.
- [x] Navigation and live updates — Decision: `/toolcalls` is available during idle and running phases. Up/Down select calls; the configured page/top/bottom actions navigate the list; Enter inspects the selected call; Escape returns to the same call in the list, then closes. New calls are followed only if the reader was at the newest call; otherwise selection stays with the same call. A selected live call's detail updates in place and follows added content only while already at its bottom. The inspector must not move the hidden conversation's reading position. Replacing or clearing the active session closes the inspector and discards its old selection and detail.
- [x] Layout — Decision: list and detail take the offered conversation region while the existing header, prompt, and footer remain. Normal layout gives the list compact rows and the detail a scrollable area for long content. If even a usable row and its navigation hints cannot fit, show a width-safe message that the view is too small with Escape to close; do not add a miniature browser.
- [x] Detail boundary — Decision: show complete available text arguments and result, pending/completed/failed state, and readable typed result content, including text-bearing embedded resources. Show structured JSON as a labeled value and resource links as labels and URIs; summarize byte-bearing media and embedded resources without dumping bytes. Do not dereference links, fetch more data, or imply a summary contains hidden raw media. Preserve existing display-safe content and status boundaries.

## Interface contract

- **gRPC / protobuf:** None — use current `tool.call`, `tool.result`, and typed result fields; no wire change or new RPC.
- **Exported Go APIs / interfaces:** None — only private `cmd/mecatui/ui` and UI-internal component contracts may change; the engine and SDK exports remain unchanged.
- **Tool schemas:** None — no model-facing tool changes; `/toolcalls` is a client command, not a model tool.
- **CLI / config:** Add the exact no-argument, slash-palette-visible local command `/toolcalls` in mecatui. It opens for the current session while idle or running, with no capability-dependent server lookup, new flag, settings key, or global binding. Configured existing list and scroll bindings work inside the browser; `ctrl+t` retains its conversation-wide behavior when the browser is closed.
- **Events / persistence:** None — no new events, session store, or durable migration. The existing UI scrollback remains the only conversation projection. It retains or projects structured content supplied in an existing result, including a field-only result when no corresponding typed block exists. Browser selection, list/detail offsets, and follow state are ephemeral and reset on session replacement.
- **Security / authority:** None — this read-only browser uses only the current session's already received display-safe result projection. No permission bypass, URL fetch, raw binary display, new workspace access, or cross-session browsing. Sanitize all untrusted names, arguments, text, JSON, and resource metadata at the rendering boundary; do not reconstruct content withheld by the server.
- **Compatibility / migration:** Additive local UI. Existing cards, global `ctrl+t`, conversation scroll/selection, approvals, keyboard routing outside the browser, and transcript rehydration remain intact. The implementation updates the owning public TUI usage guide for command discovery and corrects its existing focused-card `ctrl+t` wording to describe current global behavior.

## In scope - 3 scenarios, in implementation order

### Scenario 1 - find a call in the current conversation

Use the [TUI surface contract](../../cmd/mecatui/ui/surface.go), [bounded list precedent](mecatui-saved-memory-bounded-browser.md), and [full-region Sessions precedent](../../cmd/mecatui/ui/sessions_surface.go). The browser observes the same ordered [scrollback snapshots](../../cmd/mecatui/ui/internal/scrollback/scrollback.go) used by the conversation renderer. It does not invent a parallel event reducer or disturb the logical reader position specified by [ADR 0301](../adr/0301-logical-conversation-anchors.md).

**Acceptance:**
- AC1.1: The no-argument `/toolcalls` command appears in the local slash palette and opens during idle and running phases. A conversation with no calls shows an empty state and an Escape action. A loaded resumed session offers its reconstructed top-level calls without mixing in any other session.
  - verify: `TestMecatuiToolcallsInspector_Scenario1_OpenEmptyRunningAndResume`
- AC1.2: The list presents top-level calls oldest to newest with readable one-line status, tool name, and brief intent; opening it selects and reveals the newest call. Up/Down select one call, configured page/top/bottom navigate 100+ calls, and Enter opens the selected call's detail. Escape returns to that call and then closes the browser with prompt focus restored.
  - verify: `TestMecatuiToolcallsInspector_Scenario1_ChronologicalNavigation`
- AC1.3: A new call while the list is at its newest selection advances to that call; while the reader is browsing earlier calls, the same call remains selected and visible despite additions or width reflow. A result updates its currently indexed call without shifting the selected row, including when an ID is reused after an earlier call completed; overlapping duplicate call IDs remain outside the existing result-correlation contract.
  - verify: `TestMecatuiToolcallsInspector_Scenario1_StableLiveSelection`

### Scenario 2 - understand the selected call

The current [event projection](../../cmd/mecatui/client/msgs.go) already supplies arguments, text results, error state, typed blocks, and a structured JSON field. Canonical server results derive the structured field from a typed block; use that block when available, while preserving the standalone field-only case. The normal card renderer currently suppresses some typed blocks as presumed text mirrors; the inspector must identify structured data and media without pretending to render binary content. See the [server mapping](../../internal/adapter/server/mapper.go), [typed result rendering](../../cmd/mecatui/ui/render.go), and [AGENTS.md](../../AGENTS.md)'s effective-payload and secret-scrubbing invariants.

**Acceptance:**
- AC2.1: A running call shows its identity, full readable arguments, and a pending state. When a normal, failed, or provisional result arrives, detail updates in place to reflect the current result and status; a later canonical result supersedes provisional detail without duplicating it. A selected call never silently becomes another call.
  - verify: `TestMecatuiToolcallsInspector_Scenario2_LiveResultAndStatus`
- AC2.2: Long arguments and complete received text results, including errors and Edit/Write diffs, remain reachable with existing configured line/page/end navigation. The detail follows appended output when already at bottom and preserves the reading position otherwise, including after resize; Escape restores the list's selected call and window.
  - verify: `TestMecatuiToolcallsInspector_Scenario2_FullScrollableDetail`
- AC2.3: The detail distinguishes labeled structured JSON from accompanying text, whether JSON is an object, array, or scalar. The canonical typed block is used when present; a structured field without such a block is still shown both live and after transcript rehydration. Repeated text mirrors need not be printed twice, but distinct text and structured values remain inspectable. Resource links show sanitized label/URI; text-bearing embedded resources remain readable; image, audio, and byte-bearing embedded-resource blocks have honest bounded descriptions rather than raw binary, base64, or automatic fetch.
  - verify: `TestMecatuiToolcallsInspector_Scenario2_StructuredAndTypedResults`, `TestMecatuiToolcallsInspector_Scenario2_ResumedFieldOnlyStructuredResult`

### Scenario 3 - fit the frame and preserve other owners

The [measured conversation layout](../tui.md#layout-and-navigation) gives the modal only its conversation-region offer. Existing modal routing owns keys and wheels before the hidden conversation; [ADR 0301](../adr/0301-logical-conversation-anchors.md) owns the latter's reading position. Review the existing list/detail and fill-placement patterns before implementation.

**Acceptance:**
- AC3.1: At ordinary, wide, and narrow sizes the browser fits the offered conversation region, keeps its own controls visible, and does not hide the header, prompt, or footer. At too-small positive geometry it shows only a width-safe too-small/Escape message; at nonpositive geometry it renders nothing. Resizing back restores the valid list or detail selection.
  - verify: `TestMecatuiToolcallsInspector_Scenario3_FullRegionAndCompactFallback`
- AC3.2: While the browser is open, keyboard and wheel input stays with it; the hidden conversation neither scrolls nor starts a mouse text selection. Closing restores the conversation's logical reading anchor and existing `ctrl+t` behavior. Streaming results still reach scrollback while the browser is open. Replacing or clearing the active session closes the browser and no prior-session detail or selection appears under the replacement, even if a new call reuses an identifier. Terminal controls and oversized metadata do not overflow or execute in the terminal.
  - verify: `TestMecatuiToolcallsInspector_Scenario3_InputOwnershipAndSafety`, `TestMecatuiToolcallsInspector_Scenario3_SessionReplacementClosesInspector`
- AC3.3: The owning public TUI usage page explains `/toolcalls` and correctly describes global `ctrl+t`; the browser's visible hints explain its own keys. The contributor [TUI guide](../tui.md) records verified, reusable list/detail and fill-placement conventions rather than presenting planned `/toolcalls` behavior as already shipped.
  - verify: inspection — compare the implemented command and hints to the owning public guide, run `task docs` and `task site:build`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Smaller completed-call cards, grouped calls, or a new `ctrl+t` expansion level | Later #1361 slice after hands-on review | Preserve current conversation rendering. |
| Clicking a conversation card to open the inspector | Optional later #1361 slice | Keep text selection and no-mouse behavior intact. |
| Child tool-call inventory, cross-session history, search/filter, direct previous/next-detail shortcut | Later evidence-driven plans | Browse only top-level current-session calls with existing navigation. |
| Raw media rendering, downloads, URL dereferencing, or new server metadata | Separate product decision | Typed content remains descriptive and read-only. |

## Definition of done

1. Focused scrollback, command routing, modal lifecycle, client projection, and render tests pass; the integrated candidate passes `task test`, `task lint`, `task test:race`, `task docs`, `task site:build`, and `task ac-trace-strict`.
2. The offline demo still shows a tool call, permission ask/approval, and result; intentional visuals receive fixed-size golden review.
3. The implementation PR links this merged Plan / Interface PR and baseline and passes `/panel-review` without ship blockers. The operator reviews real short/long, live, failed, and resumed runs before specifying the next #1361 phase.

## Implementation guidance (non-binding)

- Keep a single canonical UI scrollback; the surface retains only selected identity and bounded view state. Prefer per-card stable `BlockID` for list identity over call ID alone because a completed call ID can recur; keep existing call-ID result correlation rather than claiming `BlockID` resolves ambiguous overlapping duplicates. Use detached snapshots for rendering, not a second tool-event history or raw protobufs.
- A small `ui/internal` presentation package with a documented input/output contract could isolate list/detail formatting and navigation from the large root UI package. If that boundary is pure, it can depend on UI-internal scrollback and bounded controls, while modal wiring, theme and root key maps, and stream reduction stay in `ui` to avoid import cycles. Extract only a contract that survives this feature; do not make a generic future-delegation abstraction.
- Reference [`/memory`](../../cmd/mecatui/ui/usermodel.go) for list/detail and Escape, [`/sessions`](../../cmd/mecatui/ui/sessions_surface.go) for conversation-region fill, and [live Agents](../../cmd/mecatui/ui/agents_overlay.go) for continued streaming. Do not copy their unrelated capabilities, RPC loading, tabs, or browser state.
- Avoid retaining an additional copy of structured JSON when the typed block already carries it. Normalize a field-only structured result into the canonical scrollback result projection, not an inspector side map, for both live results and transcript rehydration. Verify that rendering uses the effective received result and does not resurface withheld original content.

## Deferred decisions and known risks

- Live inspection must not swallow stream messages before the canonical reducer sees them. A provisional result, authoritative replay, and a later conversation replacement each need stable identity reconciliation without leaking a former session's state.
- The verified current text-body mirroring policy for typed MCP blocks is not proof of equality for every external server. Present distinct values honestly without fetching or guessing at omitted data.
