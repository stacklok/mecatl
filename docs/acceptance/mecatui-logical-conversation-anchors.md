# Mecatui logical conversation anchors — acceptance plan

**Phase:** mecatui conversation-view correctness refactor
**Status:** landed, 2026-09-06. Derived from the still-proposed ADR after operator decisions.
**ADR:** [ADR-0301](../adr/0301-logical-conversation-anchors.md) — UI-local logical anchors, provenance, selection, and performance constraints.
**Accumulator branch:** `acc/mecatui-logical-conversation-anchors` (off `main`).

The smallest set of work that keeps a reader and a live selection attached to
logical conversation content rather than an unstable rendered row while the UI
streams, reflows, or changes card detail state. It stays entirely in
`cmd/mecatui/ui`; it neither widens protocol state nor changes transcript
persistence.

## Why these scope cuts

- [ADR-0301](../adr/0301-logical-conversation-anchors.md) confines identities,
  provenance, follow state, and selection to the mecatui process. Reconnect,
  transcript reconstruction, and session replacement intentionally reset to
  tail-follow and clear selection.
- [`docs/tui.md`](../tui.md) defines mecatui as a pure client rendering
  client-layer event messages; this work must not reach engine, server, or
  protocol layers.

## In scope — 1 scenario, in implementation order

### Scenario 1 — Logical reading and selection position survive a changing frame

A manually scrolled reader remains at the same UI-local block/region location
when an earlier card resolves, a card expands or collapses, or the terminal
reflows. The renderer produces one cache-aware frame containing the existing
lines and lockstep forward/reverse provenance; a package-private conversation
view controller owns capture, restore, selection projection, tail-follow, and
viewport replacement. This implements the client-only boundary and fallback
order in [ADR-0301](../adr/0301-logical-conversation-anchors.md) while retaining
mecatui's client-only rendering boundary in [`docs/tui.md`](../tui.md).

**Acceptance:**

- AC1.1: `conversation` owns monotonically allocated UI-local identities for
  ordinary blocks and changed-file membership plus its synthetic appendix block;
  the appendix receives one identity at first observed change, is represented in
  provenance only when expanded and non-empty, and retains that identity across
  visibility changes. A reset or transcript reconstruction clears the selection,
  starts tail-follow, and resets every render/provenance cache so index reuse
  cannot retain an old document identity.
  - verify: `TestADR_0301_UIBlockIdentityResetsWithConversation`
- AC1.2: A rendered frame carries exactly one provenance record per rendered
  line, permits both row-to-anchor capture and anchor-to-row restoration, and
  has byte-identical visible output to the existing renderer for collapsed,
  expanded, cached, and fresh render paths.
  - verify: `TestADR_0301_RenderedFrameProvenanceMatchesLines`
- AC1.3: Text-bearing regions map grapheme-safe offsets in canonical visible
  ANSI-free text through reflow using the existing `x/ansi` grapheme semantics;
  derived chrome and collapsed summaries restore by local rendered row.
  - verify: `TestADR_0301_ReflowRestoresCanonicalVisibleTextOffset`
- AC1.4: A reader manually anchored inside a card remains in its block and
  region when earlier content grows or shrinks, and an unavailable exact region
  follows ADR-0301's same-region, same-block, then bias-directed adjacent-block
  fallback order.
  - verify: `TestADR_0301_AnchorFallbackIsDeterministic`
- AC1.5: Expanding or collapsing a tool card retains an anchor in a surviving
  region of that card; an anchor in the expanded changed-files appendix falls
  back to the preceding conversation block when collapse hides the appendix.
  - verify: `TestADR_0301_CardAndChangedFilesAppendixFallback`
- AC1.6: A resize or restored anchor that is bottom-aligned enters tail-follow,
  and later streamed content remains visible; scrolling above the bottom enters
  anchored mode.
  - verify: `TestADR_0301_BottomAlignedAnchorPromotesTailFollow`
- AC1.7: A selection records logical endpoints, copied ANSI-free text, and up
  to sixteen grapheme clusters of endpoint context. It survives a reflow or live
  append when the contexts resolve and copied text is unchanged, and clears
  rather than copying altered text when they do not. A pending streamed delta
  followed by selection and copy uses current conversation content rather than a
  stale pre-flush frame.
  - verify: `TestADR_0301_SelectionPreservesLiveStableText`
- AC1.8: The normal inactive-selection, collapsed-card path retains cached
  prefix reuse and frame coalescing without a second rendered transcript.
  Cache-equivalence and depth-scaling tests reject an increased asymptotic
  per-frame cost; the existing scrollback allocation and bytes-per-operation
  benchmarks are reviewed against a recorded 5% target, with RSS reported as
  monitoring data rather than a flaky numeric gate.
  - verify: `TestADR_0301_ScrollbackFrameRetainsLinearMetadataOnly`
- AC1.9: A reader can select and copy stable visible text while a subagent card
  continues to append, and the viewport continues to present the selected text
  at its resolved logical coordinates.
  - verify: `TestLogicalConversationAnchors_Scenario1_ChangingFrame`

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| Virtualized, paged, evicted, or on-demand conversation history | Separate profiled design | [ADR-0301](../adr/0301-logical-conversation-anchors.md) |
| Protocol, gRPC/HTTP, session snapshot, or event-log anchor persistence | Not needed for UI-local state | [ADR-0301](../adr/0301-logical-conversation-anchors.md) |
| Restoring scroll or selection across reconnect, session replacement, or transcript reconstruction | Reset-to-tail-follow posture | [ADR-0301](../adr/0301-logical-conversation-anchors.md) |

## Sequencing recommendation

First make conversation identities and the renderer's concrete frame/provenance
output cache-equivalent to today's lines. Then route every viewport replacement,
scroll action, resize, and selection projection through one package-private
conversation-view owner. Add behavior and allocation-scale tests before changing
or tightening the performance gate.

## Named tests landing in this plan

- `TestADR_0301_UIBlockIdentityResetsWithConversation`
- `TestADR_0301_RenderedFrameProvenanceMatchesLines`
- `TestADR_0301_ReflowRestoresCanonicalVisibleTextOffset`
- `TestADR_0301_AnchorFallbackIsDeterministic`
- `TestADR_0301_CardAndChangedFilesAppendixFallback`
- `TestADR_0301_BottomAlignedAnchorPromotesTailFollow`
- `TestADR_0301_SelectionPreservesLiveStableText`
- `TestADR_0301_ScrollbackFrameRetainsLinearMetadataOnly`
- `TestLogicalConversationAnchors_Scenario1_ChangingFrame`

## Definition of done

1. `task lint` and `task test` pass.
2. `task docs` passes after the ADR and plan changes.
3. `task ac-trace-strict` passes after this plan is landed.
4. The named tests are green, pin the ADR-0301 behavior, and are grep-locatable.
5. `task perf:scenarios` reports the existing scrollback metrics for comparison
   with the recorded 5% allocation/bytes review target; the deterministic
   depth-scaling test is green.
6. `go run ./cmd/mecademo` still prints a full offline session.
7. The completed diff receives `/panel-review` with no ship blockers.

## Deferred decisions and known risks

- **Performance baseline mechanics.** The implementation records a reproducible
  local 5% allocation/bytes review comparison. TUI render benchmark sampling is
  advisory on shared CI; cache-equivalence and depth-scaling tests are the hard
  complexity guard, while RSS remains monitoring data.
- **New visual regions.** The initial closed region set is intentionally small.
  A future card surface must either map to one of those regions or amend ADR
  0301 with its semantic anchor and fallback behavior.

## Exit criteria

When every point under *Definition of done* holds on the accumulator, this plan
is satisfied.
