---
id: 01-conversation-view
title: Logical anchor frame and viewport controller
blocked_by: []
status: in-progress
attempt: 1
branch: plan-mecatui-logical-conversation-anchors/01-conversation-view-attempt-1
worktree: .scratch/worker-mecatui-logical-conversation-anchors-01-conversation-view-attempt-1
issue: ""
retries: 0
last_error: ""
accumulator: acc/mecatui-logical-conversation-anchors
---

# Task brief

Implement ADR 0301 only in `cmd/mecatui/ui`. Introduce a package-private
concrete conversation-view owner for follow state, reading-anchor
capture/restore, selection projection, and viewport content replacement.
Keep event and input routing in `Model`, block creation/mutation (including
changed-file membership and synthetic changed-files appendix identity) in
`conversation`, and rendering/caching in `renderer`. Do not add an interface,
engine/proto/server changes, persistence, virtualization, or a second rendered
transcript.

Renderer output must become a concrete cache-aware frame containing the
existing line slice plus lockstep forward/reverse provenance. Provenance must
reference existing strings and reset with renderer caches when a conversation
is reconstructed. Text coordinates use canonical visible ANSI-free text after
ordinary presentation/normalization, exclude soft-wrap boundaries, and reuse
existing `x/ansi` grapheme helpers. Use the initial regions: chrome, body,
reasoning, arguments, result, artifact, changed-files appendix. Derived chrome
and collapsed summaries use local row fallback.

The appendix has one conversation-owned UI-local ID at first changed file,
appears in frame provenance only expanded and non-empty, and falls back to its
preceding conversation block when hidden. Anchor fallback is exact
block/region/offset or row, nearest same-region, same block, bias-directed
adjacent block, then top/tail. Bottom-aligned restoration enters tail follow.

Selection stores logical endpoints, exact copied ANSI-free text, and up to 16
graphemes on either side of each endpoint. Preserve it through reflow/live
append when contexts and copied text match; clear it only when identity cannot
be proved. Cover the pending-delta then selection-and-copy ordering, not merely
an already-flushed append.

Preserve the inactive-selection, collapsed-card cached-prefix fast path and
frame coalescing. Extend cache-equivalence and depth-scaling tests to reject a
second full transcript or higher per-frame asymptotic work. Compare existing
benchmark allocation/bytes results locally against the 5% review target; do
not add a flaky numeric CI gate for noisy TUI benchmark metrics. Run the
applicable focused UI tests, `task lint`, `task test`, `task docs`, `task
perf:scenarios`, and `go run ./cmd/mecademo`.

## Acceptance criteria

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
