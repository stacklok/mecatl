# Delegation observability convergence — acceptance plan

**Phase:** capability — converge the delegation observability surface on two tiers.
**Status:** landed, 2026-07-30. Authored from the approved plan for issue #323.
**Issue:** [stacklok/mecatl#323](https://github.com/stacklok/mecatl/issues/323).
**ADR:** [ADR-0079](../adr/0079-delegation-observability-convergence.md) — pins the two-tier model and the narrowing of the no-content invariant.
**Accumulator branch:** `acc/delegation-observability-convergence` (off `main`).

The smallest set of work that lets an operator watch a Subagent or a Parallel branch
with the same bounded-preview fidelity as a Team member, while Team keeps its unique
structures (task board, findings ledger, dispositions, mutating cue, context meter) and
the context-isolation guarantee (gauntlet #7) holds unchanged.

The doc is organized scenario-first because acceptance is about what the running
harness can demonstrate, not which packages exist on disk.

## Why these scope cuts

- [ADR-0079](../adr/0079-delegation-observability-convergence.md) — converge on two
  tiers: bounded previews become common to all three delegation families; the task
  board / findings ledger / dispositions / mutating cue / context meter stay Team-only.
- A dedicated **Parallel inline card** is out of scope: Parallel events feed the ctrl+a
  agents overlay today (the deliverable rides the tool result text), and issue #323's
  AC 3 is satisfied by the overlay's group focus + branch rows carrying previews.
- The **TRIP-WIRE** stands: the three families still differ in aggregation shape and
  share one redaction chokepoint, so no shared `ChildActivity` value object is
  extracted — a 4th family remains the trigger ([`engine/session/event.go`](../../engine/session/event.go)).

## In scope — 4 scenarios, in implementation order

Scenarios are listed in implementation order. Each is independently demoable; later
scenarios assume earlier ones but don't change their acceptance criteria.

### Scenario 1 — Domain payloads carry bounded previews

The domain projection widens: `SubagentPayload` and `ParallelPayload` gain the same
`Text` / `Detail` / `InnerKind` fields `TeamPayload` already carries, with doc-comments
that state the new bounded-preview redaction contract (superseding "metadata only").
This is an additive change to the engine's exported surface, so the api-compat gate is
regenerated and classified per [`engine/COMPATIBILITY.md`](../../engine/COMPATIBILITY.md)
(Added = minor — [ADR-0037](../adr/0037-engine-stability-contract.md)). The TRIP-WIRE
doc-comment and the three-family aggregation note stay accurate per
[ADR-0079](../adr/0079-delegation-observability-convergence.md).

**Work:**
- engine domain (`session`): add `Text`, `Detail`, `InnerKind` to `SubagentPayload`
  and `ParallelPayload`; rewrite the redaction doc-comments from "metadata only" to the
  bounded-preview contract; keep the TRIP-WIRE comment accurate.
- composition: `task api:update` + an `engine/CHANGELOG.md` entry (Added = minor).

**Acceptance:**
- AC1.1: `SubagentPayload` and `ParallelPayload` each expose `Text`, `Detail`, and
  `InnerKind` fields documented as bounded, client-only previews per
  [ADR-0079](../adr/0079-delegation-observability-convergence.md).
  - verify: `TestDelegationObservability_Scenario1_PayloadsCarryPreviewFields`
- AC1.2: `task api:check` passes against regenerated `engine/api/*.txt` and the
  `engine/CHANGELOG.md` note classifies the payload widening as Added (minor) per
  [ADR-0037](../adr/0037-engine-stability-contract.md).
  - verify: none — covered by the Definition-of-done api gate (item 3)

---

### Scenario 2 — The chokepoint projects bounded previews for all three families

`agent.drainChildObserved` — the single redaction chokepoint every delegation family
reuses — currently handles only `EvToolCall` (stores callID→name, discards args) and
`EvToolResult` (reads IsError, discards body). It is widened to also handle
`EvMessageDelta` and `EvResult`, and to read the args/body of the events it already
receives. The projected InnerKind set is exactly four — `message.delta` (child message
text → `Text`), `tool.call` (args → `Detail`), `tool.result` (body → `Detail`), and
`result` (terminal text → `Text`) — each value run through the existing `clampPreview`
(control-byte scrub + rune cap, [`engine/agent/teamtool.go`](../../engine/agent/teamtool.go)).
Parallel inherits the upgrade through the `branchTool` re-tag closure
([`engine/agent/parallel.go`](../../engine/agent/parallel.go)), which is extended to copy
the new already-redacted fields (without this, branch previews would be silently dropped
at the re-tag). Background subagents (`background: true`) drive through the same
`drainChildObserved` on their detached goroutine, so they inherit the widened projection
with no separate work. The permission.ask drop discipline is unchanged — a child's
pending-ask reason is never forwarded — and nothing enters the parent `Conversation`
(gauntlet #7, [`AGENTS.md` — the delegation observability invariant](../../AGENTS.md)).

The sentinel tests change shape with the invariant: the structural allow-list test no
longer bans `Text`/`Detail` by name; it asserts the content fields exist and are only
ever fed through `clampPreview`. The behavioral canary tests (a raw canary string in
child tool args/result/message text must never appear verbatim in any projected field)
become the primary guard and are extended to the new fields
([ADR-0079](../adr/0079-delegation-observability-convergence.md)).

**Work:**
- engine app (`engine/agent`): widen `drainChildObserved` to project bounded previews;
  re-tag the new fields in `branchTool`; keep the permission.ask drop.
- engine tests: re-scope the structural sentinel, extend the behavioral canary suite,
  add the missing Subagent structural sentinel.

**Acceptance:**
- AC2.1: a running Subagent emits `subagent.tool` events whose `Detail` is a
  clamped, control-byte-scrubbed preview of the child tool call's args, and `Text`
  carries the child's capped message text, per
  [ADR-0079](../adr/0079-delegation-observability-convergence.md).
  - verify: `TestDelegationObservability_Scenario2_SubagentProjectsBoundedPreviews`
- AC2.2: a Parallel branch emits `parallel.branch` tool events carrying the same
  bounded previews via the re-tag closure.
  - verify: `TestDelegationObservability_Scenario2_ParallelInheritsBoundedPreviews`
- AC2.3: a raw canary string in a child's tool args, result body, or message text never
  appears verbatim in any projected `SubagentPayload`/`ParallelPayload` string field;
  only its clamped, scrubbed form may appear.
  - verify: `TestInvariant_delegation_previews_bounded_scrubbed`
- AC2.4: a child's `permission.ask` event is never projected to the parent stream (the
  drop discipline survives the widening).
  - verify: `TestInvariant_delegation_permission_ask_dropped`
- AC2.5: no child content enters the parent `Conversation` — only the tool result text
  does (gauntlet #7 holds under the widened projection).
  - verify: `TestInvariant_gauntlet7_no_child_content_in_parent_conversation`
- AC2.6: a structural sentinel guards `SubagentPayload` the way
  `TestParallelPayloadHasNoContentFields` guards `ParallelPayload` — every content
  field on either payload is provably fed only through `clampPreview`.
  - verify: `TestInvariant_subagent_payload_previews_bounded`

---

### Scenario 3 — The wire and the TUI carry the previews

The proto `Subagent` and `Parallel` messages grow `text` / `detail` / `inner_kind`
fields (additive, backward-compatible); `contracts/gen/` is regenerated and the server
mappers copy the new fields verbatim
([`internal/adapter/server/mapper.go`](../../internal/adapter/server/mapper.go)). The
mecatui client messages and conversation model thread the fields through; the renderers
converge on the Team trace format — meaning the full Team-style interleaved trace (tool
chips with `detail` previews AND capped message lines), which moves the Subagent/Parallel
trace model from `[]subToolChip` (name+isError) to the richer `teamTrace` shape.
The engine-side `clampPreview` cap (200 runes) and the TUI-side `maxTeamDetailLen` (80
runes) double-truncation is intentional defense-in-depth, kept consistent with Team: the
wire carries ≤200 runes, the TUI draws ≤80 — the cap constant is renamed to reflect that
it is no longer Team-only. A collapsed Subagent card shows the live current-tool
name, the ctrl+t expanded trace shows Team-style chips with bounded previews and capped
message lines, and the Subagent / Parallel focus panes rise to the Team focus level.
Every "args/results hidden" honesty note flips to an accurate "bounded previews" framing
([`cmd/mecatui/ui/render.go`](../../cmd/mecatui/ui/render.go),
[`cmd/mecatui/ui/agents_overlay.go`](../../cmd/mecatui/ui/agents_overlay.go)). The prior
assertion that a collapsed Subagent card must NOT name the current tool
(`cmd/mecatui/ui/subagent_test.go`) is deliberately inverted per
[ADR-0079](../adr/0079-delegation-observability-convergence.md). The mecatui goldens
whose Subagent/Parallel traces change are refreshed in this scenario (`task test:golden`).

**Work:**
- contracts: add `text` / `detail` / `inner_kind` to the `Subagent` and `Parallel`
  proto messages; `task generate`.
- adapters (`internal/adapter/server`): map the new fields in `toProtoSubagent` /
  `toProtoParallel`.
- mecatui client (`cmd/mecatui/client`): add the fields to `SubagentMsg` /
  `ParallelMsg` and their translators.
- mecatui ui (`cmd/mecatui/ui`): thread the fields into the conversation model;
  converge `renderSubagent`, `renderSubagentFocus`, and `renderParallelGroupFocus` on
  the Team trace format; flip the honesty notes; update golden + unit tests.

**Acceptance:**
- AC3.1: a collapsed, running Subagent card EXTENDS its existing line with the child's
  live current-tool name — `subagent · <tool> · ↑<in> ↓<out> · N tools · ctrl+t trace`
  — keeping the token/tool counts, with no heartbeat ticker (the line changes only when
  the child's tool actually changes, so scrollback flicker is bounded by tool-call
  frequency, matching the fleet roster row's behavior).
  - verify: `TestDelegationObservability_Scenario3_CollapsedCardShowsCurrentTool`
- AC3.2: ctrl+t on a Subagent card shows bounded args/result previews per child tool
  call and capped child message lines, in the Team chip format.
  - verify: `TestDelegationObservability_Scenario3_ExpandedCardShowsBoundedPreviews`
- AC3.3: the Parallel ctrl+a group focus renders each branch's interleaved trace (tool
  chips with bounded previews + capped message lines) below its roster line, in the
  Team focus format — not just flat roster rows.
  - verify: `TestDelegationObservability_Scenario3_ParallelViewsShowBoundedPreviews`
- AC3.4: no "args/results hidden" string remains in the TUI; the honesty note reads
  "bounded previews" wherever a Subagent or Parallel trace is shown.
  - verify: `TestDelegationObservability_Scenario3_HonestyNoteIsBoundedPreviews`
- AC3.5: Team-unique structures — the task board, findings ledger, per-member
  dispositions, mutating cue, and context meter — are never rendered on Subagent or
  Parallel surfaces (a TUI rendering assertion; the engine layer already guarantees the
  data never arrives on those event types).
  - verify: `TestDelegationObservability_Scenario3_TeamUniqueStructuresStayTeamOnly`

---

### Scenario 4 — The docs tell the new story

The living docs stop describing Subagent/Parallel as metadata-only:
`docs/architecture/domain-model.md`'s delegation-families note, the
`SubagentPayload`/`ParallelPayload` doc-comments (landed in Scenario 1), and the
delegation-families section of
[the delegation event contract](../architecture/domain-model.md) all state the two-tier
model per [ADR-0079](../adr/0079-delegation-observability-convergence.md). Because this
is operator-visible TUI behavior, the relevant `user-docs/` page notes the change in
the same PR, per the [`AGENTS.md` user-docs checklist](../../AGENTS.md).

**Work:**
- docs: `domain-model.md`, `IMPLEMENTATION-NOTES.md`, a short `user-docs/` note.
- regenerate the configuration reference and run the strict link gate (`task docs`).

**Acceptance:**
- AC4.1: no living doc describes Subagent or Parallel observability as "metadata only";
  the two-tier model is stated with a citation to
  [ADR-0079](../adr/0079-delegation-observability-convergence.md).
  - verify: inspection — `matlatl check . --strict` + a grep for the stale phrasing
- AC4.2: `task docs` regenerates the configuration reference and the matlatl strict link gate is green
  with the new plan + ADR linked.
  - verify: none — covered by the Definition-of-done docs gate (item 2)

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| A dedicated Parallel inline card (today Parallel renders via generic tool args) | later, if operators ask | [ADR-0079](../adr/0079-delegation-observability-convergence.md) |
| Extracting a shared `ChildActivity` value object | the 4th delegation family (TRIP-WIRE) | [`engine/session/event.go`](../../engine/session/event.go) |
| Live current-tool name on collapsed Parallel branch rows | later; branch rows already show it in focus | [ADR-0079](../adr/0079-delegation-observability-convergence.md) |

## Cross-cutting deliverables

- `task generate` (proto regen) and `task api:update` + `engine/CHANGELOG.md`.
- `task test:golden` to refresh the mecatui goldens whose Subagent/Parallel traces
  change, then re-run.
- The new ADR is added to `docs/adr/README.md` and this plan to
  `docs/acceptance/README.md` (the matlatl gate fails on an unreachable doc).

## Sequencing recommendation

Scenario 1 (domain payloads) lands first and unblocks Scenario 2 (the chokepoint).
Scenario 3 (wire + TUI) depends on both. Scenario 4 (docs) lands last but in the same
PR. The sentinel-test re-scope (Scenario 2) must land in the same commit as the
projection widening, or the structural guard fails on the new fields.

## Named tests landing in this plan

- `TestDelegationObservability_Scenario1_PayloadsCarryPreviewFields`
- `TestDelegationObservability_Scenario2_SubagentProjectsBoundedPreviews`
- `TestDelegationObservability_Scenario2_ParallelInheritsBoundedPreviews`
- `TestInvariant_delegation_previews_bounded_scrubbed`
- `TestInvariant_delegation_permission_ask_dropped`
- `TestInvariant_gauntlet7_no_child_content_in_parent_conversation`
- `TestInvariant_subagent_payload_previews_bounded`
- `TestDelegationObservability_Scenario3_CollapsedCardShowsCurrentTool`
- `TestDelegationObservability_Scenario3_ExpandedCardShowsBoundedPreviews`
- `TestDelegationObservability_Scenario3_ParallelViewsShowBoundedPreviews`
- `TestDelegationObservability_Scenario3_HonestyNoteIsBoundedPreviews`
- `TestDelegationObservability_Scenario3_TeamUniqueStructuresStayTeamOnly`

## Definition of done

1. `task lint` and `task test` pass (both modules, `-race`).
2. `task docs` — configuration reference regenerated and the matlatl strict link gate green.
3. `task api:check` passes (`task api:update` was run and the `engine/CHANGELOG.md`
   note is present) — the plan touched the engine's exported surface.
4. `task ac-trace-strict` — every AC's `verify:` proof resolves (this plan is
   `landed`).
5. The named tests (`TestInvariant_*`, `TestDelegationObservability_Scenario*`) are
   green and grep-locatable by their identifiers.
6. `go run ./cmd/mecademo` still prints a full offline session.
7. `task test:golden` re-run clean after the mecatui trace changes; `task generate`
   produces no diff after the proto change.
8. `task site:build` passes if `user-docs/` was touched.

## Deferred decisions and known risks

- **Sentinel-test shape.** The structural guard moves from banning content fields to
  asserting they're only fed through `clampPreview`; the behavioral canary suite is the
  primary guard. Resolved in Scenario 2 per
  [ADR-0079](../adr/0079-delegation-observability-convergence.md).
- **Parallel inline card.** Deliberately deferred; AC 3 of the issue is read as the
  overlay focus + branch rows, not a new inline card.

## Exit criteria

When every point under *Definition of done* holds on the accumulator, this plan is
satisfied.
