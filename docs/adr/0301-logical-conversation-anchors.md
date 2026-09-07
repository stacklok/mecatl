# ADR 0301 — Logical conversation anchors in mecatui

- Status: Accepted
- Date: 2026-09-06
- Scope: `cmd/mecatui/ui` conversation rendering, scrolling, selection, and scrollback-performance invariants
- Supersedes: none
- Superseded by: none

## Context

mecatui projects the event stream into an in-memory `conversation`, renders its
ordered blocks into a Bubble Tea viewport, and currently retains position as a
physical viewport `YOffset` plus a separate `stuck` auto-follow flag. Selection
uses absolute rendered-line and grapheme-column coordinates into the viewport
content. This makes the reader's position a property of one particular render,
rather than of the conversation being read.

The design is vulnerable whenever the render changes above or within the visible
area. A late tool or delegation result can revise a non-tail block; terminal-width
changes rewrap text; and the global details toggle changes a tool card between a
summary and its full result. Replacing the viewport content while preserving a
numeric row offset can show unrelated earlier or later text. Content shrink can
also clamp the viewport to its bottom while the separate `stuck` flag remains
false, disabling later tail follow. Selection gestures historically exposed the
same seam by re-splicing a stale pre-stream render.

The current scrollback implementation already has deliberate performance work
that must survive this change. It retains raw conversation blocks, caches rendered
blocks by revision/width/expansion state, coalesces streaming renders, memoizes
unchanged joins, and uses an incremental cached line prefix on the normal
streaming path. It is a full in-memory transcript and full-line viewport, not a
virtualized or paged document. Collapsed cards cap display but retain their raw
content. This decision must not quietly add a second full transcript, make an
unchanged settled history re-render on each delta, or turn logical-anchor work
into an unreviewed retention or virtualization redesign.

## Decision

### 1. Make a logical conversation anchor authoritative

Replace the independent `stuck` state with one conversation-view position model:
either explicit tail-follow mode or an anchored reading position. `YOffset` is a
transient projection into the current rendered frame and MUST NOT be the durable
reading identity.

The anchor is UI-local and has this conceptual shape:

```go
type readingAnchor struct {
    mode         followMode // followTail or anchored
    blockID      uint64     // monotonically assigned once per UI document block
    region       regionID   // semantic region within the block
    sourceOffset int        // canonical-text position when the region has source text
    row          int        // rendered-row fallback for a non-textual/derived region
    bias         edgeBias   // start or end: choose a deterministic surviving neighbour
}
```

`blockID` is not a monotonically allocated line ID. Rendered lines are unstable:
wrapping, markdown layout, streaming growth, card expansion, and terminal resize
can create, remove, or move them. The block ID is assigned only in the TUI when a
document block is created; it is neither a protocol field nor persisted state.
`region` identifies a stable display area where one exists. The initial closed
set is card chrome, primary body, reasoning, tool arguments, tool result, and the
changed-files appendix. Changed-file membership, the
appendix's first-observation identity allocation, and its document-block
lifecycle belong to `conversation`; the root Model delegates its event routing
to that owner. The appendix receives one UI-local ID when the session first
observes a changed file, participates in provenance only while expanded and
non-empty, and falls back to the preceding conversation block when collapse
hides it.

For a text-bearing region, `sourceOffset` is a grapheme-safe position in its
canonical visible text: ANSI-free text after its ordinary presentation
transformation and terminal normalization, with soft-wrap boundaries excluded.
This deliberately is not a raw-Markdown source map; the renderer maps this
coordinate to current wrapped output while using the existing
`x/ansi` grapheme segmentation and width primitives. `row` is only the
intentional fallback for a derived/non-textual region with no meaningful source
coordinate, such as card chrome or a collapsed summary.

### 2. Render line provenance with the existing frame

The renderer MUST produce one concrete package-private rendered frame containing
the existing rendered lines and lockstep per-row provenance. That provenance
must support both mapping the first visible viewport row to a `readingAnchor`
and resolving an anchor back to its current row, including canonical source
spans for text-bearing regions. It references the existing rendered strings and
block identity; it MUST NOT duplicate rendered text or retain another full
transcript. Provenance has the same document lifetime as the cached render it
describes and MUST be reset or rebuilt whenever the conversation is
reconstructed, so an index-reused block can never retain an old document's
identity.

Before replacing viewport content, the conversation-view owner captures either
tail-follow mode or the current first-visible anchor. After rendering, it restores
that anchor's current row and then projects the result to Bubble Tea's viewport.
If restoration or relayout leaves an anchored projection bottom-aligned, the
controller promotes it to `followTail`; the next appended content then remains
visible. The only source of truth for whether the next update follows the tail
is this model, not a cached boolean independently inferred at selected mutation
sites.

### 3. Define deterministic changed-content fallback

Anchor restoration follows this order:

1. restore the exact `{blockID, region, sourceOffset}` for a text-bearing region,
   or `{blockID, region, row}` for a derived region;
2. otherwise restore the nearest surviving row in that same region according to
   `bias`;
3. otherwise restore the nearest surviving region in the same block;
4. otherwise select the closest adjacent surviving block: search backward first
   for `bias == towardStart`, forward first for `bias == towardEnd`, then search
   the opposite direction; within a surviving block select its final or first
   row respectively;
5. only then fall back to the document top (`towardStart`) or tail
   (`towardEnd`).

Expanding or collapsing a card therefore retains the reader in the same card when
possible. If an expanded-only result row is hidden by collapse, its exact row no
longer exists; the view restores the nearest surviving tool-result summary or
header rather than a numerically coincidental row elsewhere in the transcript.
Reflow restores a text-bearing region by canonical source position; derived card
regions fall back to a clamped local row. Neither path preserves a global
wrapped-line number.

### 4. Give selection its own stricter correctness rule

Selection and reading position share block/region coordinates, but they do not
share the same survival policy. A reading position may use the fallback above.
A selection records both endpoints, the exact ANSI-free copied text, and up to
16 grapheme clusters of context on each side of each endpoint. It survives only
when the rendered frame resolves endpoint contexts and the selected visible text
is still identical. This permits a live card to append around a selection without
clearing it, but clears the selection rather than copying altered text when the
identity cannot be proved.

### 5. Isolate the conversation-view controller inside the TUI

Introduce one package-private concrete conversation-view owner that contains
follow state, reading-anchor capture/restore, selection projection, and viewport
content replacement. The root Bubble Tea `Model` continues to own event routing,
input routing, layout, and the underlying conversation. The conversation continues
to own block creation and mutation, and the renderer continues to own block
layout/caching.

Do not introduce a broad interface or alter the gRPC/client boundary merely to
abstract this package-internal collaboration. The required boundary is ownership
of invariants: root `Model` must no longer distribute scroll semantics among
`refreshView`, mouse/key handlers, selection helpers, and relayout paths.

### 6. Preserve the current performance contract

The first implementation is a correctness refactor, not viewport virtualization.
It MUST preserve the normal inactive-selection, collapsed-card fast path:

- settled blocks reuse existing per-block rendered output;
- streamed tail changes render only the invalidated suffix at the existing
  frame-coalesced cadence;
- provenance is built alongside the existing rendered-line handoff and shares its
  strings;
- unchanged spinner/input frames do not re-render the conversation or viewport;
- changing an earlier block invalidates no more cached rendering than today's
  revision/width/expansion contract requires.

Logical-anchor metadata may grow linearly with rendered rows, matching the
existing line-slice model, but it MUST NOT add another O(rendered-bytes) copy per
frame or increase steady-state retention by another full rendered transcript.
Selection and expanded-card paths may retain their existing full-frame costs;
this decision does not claim to optimize them. The established scrollback
allocation and bytes-per-operation metrics have a 5% review target against a
recorded baseline. Existing TUI render benchmarks are advisory because their
runtime and process-wide allocation sampling are demonstrably noisy; therefore
cache-equivalence and depth-scaling tests, rather than an unreliable numeric CI
threshold, enforce that this change does not increase the asymptotic per-frame
cost. RSS remains monitored.

A later virtualization, raw-content retention, eviction, or on-demand-history
design is a separate decision. It must be justified by profiling and must not be
smuggled into this migration.

### 7. Preserve client-only ownership

This decision is confined to `cmd/mecatui/ui`. It adds no engine API, gRPC/HTTP
or protobuf field, durable session/event-log state, server behavior, or restart
persistence. A client rebuilds its UI-local block identities and starts in the
existing normal tail-follow posture when it reconstructs a transcript.

## Consequences

**Easier.** Streaming updates, delayed non-tail results, reflow, card expansion,
selection gestures, and clipboard interaction share one explicit reading-position
invariant. The implementation stops relying on ad hoc `YOffset` preservation and
separate `syncStuck` calls. Tests can assert semantic location rather than a row
number that varies with width and card layout.

**Harder / costs.** The renderer now has a second output, and every render/cache
invalidation path must keep its line provenance byte-for-byte aligned with the
lines handed to the viewport. Block indices cannot serve as identities because a
rebuilt conversation reuses them; a new local monotonic block ID is required.
Semantic regions need disciplined definitions, especially for tool-card summaries
and expanded output. The current conservative selection-drop behavior remains
necessary when exact identity cannot be proved.

The first implementation keeps the current full-transcript viewport and therefore
does not solve unbounded transcript retention or make streaming fully sublinear in
block/line count. It must retain and extend the existing cache-equivalence,
scrollback allocation, and RSS benchmarks. New regression coverage must exercise:

- a pending stream delta followed by selection and copy;
- a manually anchored reader while earlier blocks grow or shrink;
- expand/collapse while the reader is inside the affected card;
- terminal-width reflow while manually scrolled;
- selection preservation only when the selected visible text is unchanged;
- tail-follow re-entry after a content clamp; and
- equivalence of provenance-carrying frames with the existing rendered output.

## See also

- [ADR 0096 — mecatui live-feed reconnect](./0096-live-feed-reconnect.md) — an
  earlier client-only decision that deliberately avoided protocol widening.
- [ADR 0071 — Seamless model switch: always keep the conversation](./0071-seamless-model-switch.md)
  — conversation continuity across session replacement.
- [`docs/tui.md`](../tui.md) — the mecatui client boundary.
- [`cmd/mecatui/ui/conversation.go`](../../cmd/mecatui/ui/conversation.go) — the
  current in-memory block projection.
- [`cmd/mecatui/ui/render.go`](../../cmd/mecatui/ui/render.go) — the scrollback
  render and cache layers.
- [`cmd/mecatui/ui/selection.go`](../../cmd/mecatui/ui/selection.go) — current
  rendered-line selection coordinates.
- The lifecycle convention in [ADR 0002](./0002-documentation-lifecycle.md).
