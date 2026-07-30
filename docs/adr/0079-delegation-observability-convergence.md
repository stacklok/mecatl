# ADR 0079 — Converge delegation observability on two tiers (bounded previews for Subagent/Parallel)

- Status: Proposed
- Date: 2026-07-30
- Scope: the delegation observability families (`subagent.*`, `parallel.*`, `team.*`) — domain payloads, the `engine/agent` redaction chokepoint, the proto wire, and the mecatui renderers.

## Context

The three delegation families project the SAME child-loop lifecycle through the SAME
redaction chokepoint (`agent.drainChildObserved`), but at different verbosity tiers.
`TeamPayload` forwards member CONTENT — bounded, clamped previews of message text, tool
args, and tool results (`clampPreview`, `maxTeamPreview`) — under the rationale that "a
team is meant to be watched". `SubagentPayload` and `ParallelPayload` were metadata-only:
tool name + error bool + running count, no args, no result bodies, no message text.

In practice the three-tier split read as an inconsistency (issue #323): a single Subagent
is *less* dense than a four-member team, so the scrollback-noise rationale for hiding its
content was weak, and operators who had seen the Team surface expected the same visibility
everywhere. A fresh-context UX review recommended converging on two tiers.

The constraint that mattered: the context-isolation guarantee (gauntlet #7) is about what
enters the parent's `Conversation`, not what a *client* may observe. The Team projection
had already proven that bounded, clamped, client-only content forwarding does not touch
that guarantee — the previews never enter the LLM's context.

## Decision

Converge the delegation observability surface on **two tiers**:

- **Tier 1 — common observability** (Subagent, Team members, Parallel branches): every
  family forwards bounded previews through the single chokepoint. `SubagentPayload` and
  `ParallelPayload` gain the same `Text` / `Detail` / `InnerKind` fields `TeamPayload`
  already carries, populated by `drainChildObserved` via the existing `clampPreview`
  (control-byte scrub + rune cap). The permission.ask drop discipline is unchanged — a
  child's pending-ask reason is never forwarded. The TUI shows the live current-tool name
  on a collapsed Subagent card, and Team-style bounded previews on the expanded trace and
  focus panes. The honesty note changes from "args/results hidden" to "bounded previews".
- **Tier 2 — Team-unique**: the task board, findings ledger, per-member dispositions, the
  mutating cue, and the per-member context meter remain Team-only — genuinely team-shaped
  structures with no Subagent/Parallel analogue.

This supersedes the "metadata-only" half of the delegation-families note recorded in
`docs/architecture/domain-model.md` and the `SubagentPayload`/`ParallelPayload`
doc-comments: those payloads are no longer metadata-only; they are bounded-preview, like
Team. The TRIP-WIRE (a 4th delegation family triggers extracting a shared `ChildActivity`
value object) stands — the three families still differ in aggregation shape, and the
redaction remains shared in one place.

## Consequences

- **Easier:** operators watch a Subagent or Parallel branch with the same fidelity as a
  Team member; the surface reads as one design, not three. The TUI renderers converge on
  the Team trace format (wiring, not a new UX surface).
- **Harder / committed to:** the "no child content crosses" invariant narrows from "no
  content fields on the payload" to "content fields present but provably bounded +
  scrubbed + client-only". The structural sentinel tests change shape: they no longer ban
  `Text`/`Detail` by name, they assert every content field is fed only through
  `clampPreview`. The behavioral canary tests (a raw canary string must never appear in
  any projected field) become the primary guard. This is a weaker *structural* guarantee
  traded for a better operator experience, mitigated by the chokepoint being single and
  the scrub being shared.
- The proto `Subagent` and `Parallel` messages grow `text` / `detail` / `inner_kind`
  fields (additive, backward-compatible on the wire). `engine/api/*.txt` drifts (Added =
  minor per `engine/COMPATIBILITY.md`).
- **Perf cost (accepted):** the widened projection costs ~+20 allocs/op on the
  `background_subagents` scenario (~1.34%, under the 1.02 gate threshold vs the
  pre-feature baseline). This is the intrinsic cost of the feature — per background
  child, four projected events (message.delta, tool.call, tool.result, result) each
  pay one `SubagentPayload` alloc + one `session.Event` send, the same per-event cost
  Team already pays per member event. The cost was reduced from +62/op to +35/op by
  making `clampPreview` single-pass and by skipping the payload alloc for
  non-projected events in `drainChildObserved`; a further +15/op was recovered by
  adding an inlinable zero-alloc `isCleanASCII` fast-path sentinel (the old
  single-pass shape was too complex for the gc to inline, so the `strings.Builder`
  escaped on every call even on a clean input). The residual is the accepted price
  of the observability the ADR exists to deliver, not a leak. Recorded here so the
  gh-pages baseline shift on merge is read as intentional, not drift.

## See also

- [Issue #323](https://github.com/stacklok/mecatl/issues/323) — the motivating UX review.
- `docs/architecture/domain-model.md` § the delegation families (updated by the plan).
- `engine/session/event.go` — the `SubagentPayload` / `ParallelPayload` / `TeamPayload`
  redaction contracts and the TRIP-WIRE doc-comment.
- The lifecycle convention in [ADR 0002](./0002-documentation-lifecycle.md); the engine
  stability contract in [ADR 0037](./0037-engine-stability-contract.md).
