# Mecatui soul inspector bounded viewport — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — this changes the local, user-visible geometry and browsing behavior of an existing TUI inspector without changing a public API, persistence boundary, protocol, security boundary, or architecture.
**Decision record:** None — the responsive card, pointer-owned viewport, and modal input behavior remain private to `cmd/mecatui/ui`; their rationale belongs in this plan.
**Phase:** bounded browsing adoption, soul inspector slice
**Status:** landed in this implementation candidate, 2026-10-01. Authoritative when the implementation PR merges; approved plan baseline `0e396dc4631d94bf773062ac85cf9c9544ea332f`.
**Amendment:** 2026-10-01 — during implementation PR #2041 review, the directing operator authorized removing redundant public controls prose and `/soul`-specific contributor guidance. Existing command discovery and general TUI design guidance remain the owners; no runtime or interface behavior changes.
**Delivery:** Split. Responsive geometry, physical paging, and wheel ownership change the visible interaction contract and merit separate review before implementation.
**Expected tasks:** 1
**Issue:** [stacklok/mecatl#1899](https://github.com/stacklok/mecatl/issues/1899).

`/soul` retains its read-only modal surface. Its parent supplies the conversation-region offer and frames a centered card capped at 128 outer cells; the modal measures its title, metadata, footer, and overflow indicator before giving all remaining height to a pointer-owned `bounded.Viewport`. A short or narrow offer shows a clipped close-only fallback. Preserve the existing `ansi.Wrap` line breaks before styling, then give complete physical rows to the viewport with `bounded.Clip`; `bounded.Wrap` uses different break rules. This adopts the implemented [bounded browsing control](mecatui-bounded-scroll-selection.md) without moving the modal into a new lifecycle.

## Human decisions

- [x] Browsing distance — Decision: configured `Up`/`Down` move one physical row, `ScrollU`/`ScrollD` page by the current visible viewport height, and `ScrollTop`/`ScrollBottom` reach physical endpoints. This intentionally replaces the current page keys' one-raw-line step.
- [x] Size and compact fallback — Decision: replace the fixed twelve-row window with the height available after measuring the card frame and all visible chrome. Cap the centered card at 128 outer display cells, derive content width from the theme's measured frame, and reserve an overflow indicator row when needed. If the offer cannot fit chrome and at least one body row (or two when an overflow indicator is needed), show only a clipped close action with no viewport; nonpositive offers render nothing. This intentionally replaces the old zero-text-budget unwrapped rendering.
- [x] Wheel ownership — Decision: each up/down wheel event scrolls one physical row in the visible soul viewport, remains consumed at either endpoint, and cannot scroll the hidden conversation. Compact state consumes the wheel without moving any content.

## Interface contract

- **gRPC / protobuf:** None — `GetSoul` and its response fields remain unchanged.
- **Exported Go APIs / interfaces:** None — the inspector uses the existing private `cmd/mecatui/ui/internal/bounded.Viewport` without adding an exported API.
- **Tool schemas:** None — no model-facing tool or instruction changes.
- **CLI / config:** None — `/soul`, its visibility gate, and binding names/defaults remain unchanged. Configured `Up`/`Down` move one wrapped physical row, `ScrollU`/`ScrollD` page by the current viewport height, and `ScrollTop`/`ScrollBottom` reach physical endpoints. Each wheel event moves one physical row without changing selection or scrolling the hidden conversation; compact state only consumes the event.
- **Events / persistence:** None — the browsing offset and measured geometry remain transient modal state; soul loading and provenance retain their existing owners.
- **Security / authority:** None — the inspector remains read-only, idle-only, and capability-gated; it sanitizes server-derived content before styling, and trust and drift remain server-owned.
- **Compatibility / migration:** Replace the local numeric scroll/window helpers and fixed twelve-row card with one pointer-owned bounded viewport and parent-measured, centered `askCard` capped at 128 outer cells. Preserve `ansi.Wrap` layout by passing already wrapped, sanitized, styled physical rows to `bounded.Clip`; do not rewrap them with `bounded.Wrap`. Retain metadata, ownership footer, overflow wording, loading/error/empty/untrusted copy, and existing modal open/close/focus behavior. Bind the existing `GetSoul` reply to its open so a closed or superseded request cannot overwrite a later modal. Responsive height, physical page keys, one-line wheel scrolling, and close-only compact fallback are intentional behavior changes. The public TUI guide already lists `/soul` under command discovery, and the inspector itself shows its controls; the contributor TUI guide's general bounded-card and modal-input guidance applies without a `/soul`-specific section.

## In scope — 2 scenarios, in implementation order

### Scenario 1 — responsive physical browsing

The parent frames the existing modal as a centered, width-capped card; the modal calculates all rendered title, provenance/metadata, ownership footer, separators, and overflow chrome within the offer before allocating body rows. The [bounded viewport](../../cmd/mecatui/ui/internal/bounded/viewport.go) owns the physical offset. The server-derived body is sanitized before wrapping and styling, consistent with [AGENTS.md](../../AGENTS.md)'s trust-boundary rules. No physical continuation row is omitted at the end of a long wrapped persona.

**Acceptance:**
- AC1.1: At narrow, ordinary, and wide positive widths, the normal card is centered, capped at `min(128, offered width)` outer cells, derives content width from the measured `askCard` frame, and renders no ANSI-stripped line beyond the offered width.
  - verify: `TestMecatuiSoulBoundedViewport_Scenario1_WidthCapAndFrameAccounting`
- AC1.2: At short, ordinary, and tall positive heights, the complete normal card fits the offered height, and the visible viewport uses every available body row after title, metadata, footer, separators, and any needed overflow indicator. The same height governs physical navigation and the surface-owned `lines X–Y of N` indicator, which appears only when content overflows. If fixed chrome plus one row cannot fit (or overflow requires an indicator and leaves no content row), a width-clipped close-only fallback contains no viewport or body; nonpositive geometry renders nothing.
  - verify: `TestMecatuiSoulBoundedViewport_Scenario1_UsesAvailableHeightAndCompactFallback`
- AC1.3: Long wrapped content is completely reachable by physical-line movement, clamps at both ends without a blank page, and clamps after resize. Whitespace, long-token, Unicode-width, and continuation cases retain the current `ansi.Wrap` line breaks rather than adopting `ansi.Hardwrap` behavior.
  - verify: `TestMecatuiSoulBoundedViewport_Scenario1_WrappedPhysicalEndpointsAndResize`
- AC1.4: Loading, empty, untrusted-project, and sanitized-error states retain their distinct copy and fit the same offered geometry or show the close-only fallback; a successful new result resets browsing to the first physical row without exposing stale content.
  - verify: `TestMecatuiSoulBoundedViewport_Scenario1_PresentationAndNonBodyStates`

### Scenario 2 — modal input and result ownership

The existing [`soulState` surface](../../cmd/mecatui/ui/soul.go) still owns the single-shot `GetSoul` result and all modal input. Navigation operates on the current physical window without scrolling the hidden conversation. Follow [AGENTS.md](../../AGENTS.md)'s focused-test and final-gate requirements for the changed UI path.

**Acceptance:**
- AC2.1: The idle and collaborator guards, fetch on open, successful current-open result reset to the first row, Escape-to-close and refocus, and swallowing of unrelated keys remain intact. A result from a closed or prior open cannot replace the new open's loading, error, content, or scroll state, even if it arrives after the new modal opens.
  - verify: `TestMecatuiSoulBoundedViewport_Scenario2_OpenResultAndClose`
- AC2.2: Configured `Up`/`Down` step by one physical row; configured `ScrollU`/`ScrollD` page by the current measured viewport height; configured `ScrollTop`/`ScrollBottom` reach physical endpoints, including after wrapping. Remapped bindings, not only default keys, exercise the modal routing path.
  - verify: `TestMecatuiSoulBoundedViewport_Scenario2_RemappableNavigation`
- AC2.3: Each wheel-up/down event moves the soul window one physical row, including within wrapped content; at either endpoint and in compact mode the event remains consumed without scrolling the hidden conversation or constructing a viewport.
  - verify: `TestMecatuiSoulBoundedViewport_Scenario2_WheelOwnershipAndCompactIsolation`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Soul selection, editing, refresh, or activation | Existing read-only soul boundary | This slice changes browsing, not soul authority. |
| Soul trust, provenance, drift, discovery, or RPC contracts | Existing server and soul owners | This slice retains the current snapshot and attribution. |
| Smooth-wheel coalescing, click, drag selection, or horizontal panning | [#1790](https://github.com/stacklok/mecatl/issues/1790) and future interaction work | Wheel movement is one physical row per event; no gesture scheduler is added. |
| Conversation scrolling and other inventory surfaces | [#1742](https://github.com/stacklok/mecatl/issues/1742) | Their independent state and controls are unchanged. |

## Definition of done

1. Focused soul, bounded, and modal-routing tests pass, then `task lint`, `task test`, `task test:race`, `task docs`, `task site:build`, and `task ac-trace-strict` pass on the candidate.
2. The offline demo still shows tool call, permission ask/approval, and result.
3. The implementation PR links the approved plan baseline, reports interface conformance, and passes `/panel-review` without ship blockers.

## Deferred decisions and known risks

- The directing operator explicitly selected responsive sizing, page-height movement, and one-row-per-event wheel scrolling, superseding issue #1899's original fixed twelve-row and consumed-but-static wheel requirements; this plan requires human plan/interface review before implementation.
- Current input clamping counts raw lines but rendering counts wrapped physical lines. Navigation and projection must use the same sanitized physical rows so final continuations remain reachable.
- The footer and metadata may wrap to several lines; count their visible rows before allocating the viewport or deciding on compact fallback. Bind `GetSoul` responses to the open that issued them, independently of the shared request-token follow-up tracked in #713.
