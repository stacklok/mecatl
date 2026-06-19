# ADR 0035 — Surface the per-delegation model for ALL children, not just routed ones

- Status: Accepted
- Date: 2026-06-19
- Scope: the delegation-observability wire contract — `contracts/proto/mecatl/v1/harness.proto` (`Subagent`, `Parallel`, `TeamMemberSpec` messages), `contracts/gen` (regenerated), `engine/session/event.go` (`SubagentPayload`, `ParallelPayload`, `TeamMemberSpec`), the engine emit sites (`engine/agent/{subagent,parallel,teamtool,teamsupervisor}.go` + a new `Engine.Model()` accessor), the server mapper (`internal/adapter/server/mapper.go`), the mecatui client view-model (`cmd/mecatui/client/msgs.go`), and the mecatui render layer (`cmd/mecatui/ui/{conversation,update,render,agents_overlay,team}.go`). No new config, no `port.LLMRequest`, no new trust boundary.
- Supersedes: none (it WIDENS the wire surface [ADR 0031](./0031-subagent-model-router.md) / [ADR 0034](./0034-team-parallel-model-routing.md) introduced — the routed-only `model` indicator — to every delegation)
- Superseded by: <none>

## Context

ADR 0031 and ADR 0034 shipped the opt-in semantic model router and surfaced the model a
delegation ran on **only when the router classified it**: `routed_category` + `routed_model`
on `subagent.start`, `parallel.branch_start`, and the `team.start` roster, rendered in
mecatui as a muted `routed: <category> → <model>` cue. That cue appears **only** when the
operator wired `--subagent-model-router` AND the classifier actually hit (a fail-soft miss
renders nothing).

The common cases therefore show **no model indicator at all**: the router off (the default),
a child on its inherited/default model, an agent-def-pinned model (a specialist subagent or
a defined team member), and a per-call `model` override on a `Subagent` call. So "what model
is this subagent/branch/member running?" was only answerable in the agents UI for the
minority, router-chosen case. An operator watching a fleet (the inline Subagent card, the
ctrl+a fleet/teams roster, the Parallel branch rows) could not see the actual model
otherwise — issue #112.

The router's provenance signal (`routed_*`) is the right shape for "the router chose this",
but it is the wrong shape for "what model is this child actually running" — those are
different questions, and the second has no answer on the wire today.

## Decision

Add a single generic `string model` field to the three delegation-observability payloads —
`Subagent` (proto field 13), `TeamMemberSpec` (field 7), and `Parallel` (field 21) — carrying
the **concrete model id the child ACTUALLY ran on, regardless of how it was chosen**
(inherited default, agent-def pin, per-call override, or the opt-in router). Populate it at
the existing emit sites from the child engine's resolved model, independent of whether the
router fired. Thread it through the one server mapper, the mecatui client view-model, and
the render layer. `routed_category`/`routed_model` STAY as the router-provenance signal;
the new `model` is the *actual* model. When routed, `model == routed_model` by construction.

The single read seam is a new exported `Engine.Model()` accessor (`engine/agent/loop.go`),
returning `deps.Model` — the same string the engine already sends on every request. The
resolved child `*Engine` is already in scope at every emit site: the Subagent
foreground/background `EvSubagentStart` (`selectChildEngine` returns it — covering inherited,
def-pinned, per-call override, and routed in one path), the Parallel `branchStart`
(`branchEngine`, the routed override or the shared `childEngine`), and the Team roster
(`Supervisor.MemberModel(name)` reading the per-member `memberRT.engine`, mirroring
`MemberRouting`). The session layer gains a plain string field; it learns nothing about how
the model is resolved — same posture as `RoutedModel` today. The engine stays
model-string-only; no adapter/proto type crosses into `engine/`.

**Render.** The existing `subagentRoutedLabel` helper generalizes to `subagentModelLabel`:
when the router fired (`category != "" || routedModel != ""`), keep the `routed: <category>
→ <model>` cue byte-identical; otherwise, when `model != ""`, show `model: <model>`; else
nothing. The routed case is NEVER also rendered as a `model:` line (the `else if` ordering,
plus `model == routedModel` when routed) — no duplication. Applied at all four render sites
(Subagent card, fleet lane, Parallel branch row, team roster row).

## Consequences

- The agents UI now answers "what model is this child running?" for EVERY delegation —
  routed, def-pinned, per-call-overridden, or inherited/default — not just the router-chosen
  minority. `routed:` becomes the special case; `model: <id>` is the common case.
- **Metadata-only (gauntlet #7) holds.** A model id is bare metadata — never child content
  (args/result/message text) — the same posture as `routed_model`. The structural
  gauntlet-#7 allowlist guards (`TestTeamMemberSpecHasNoContentFields`,
  `TestParallelPayloadHasNoContentFields`) are updated so the field is reviewed explicitly.
- **Wire-backward-compatible.** Three additive proto fields at the next free numbers (13, 7,
  21); no `reserved` ranges, no renumbering. Old clients ignore the new field.
- **session-is-domain holds.** `engine/session/event.go` adds only a plain `string`; no
  proto import, no `internal/` import. **mapper-is-the-crossing holds:** the proto field is
  populated ONLY in `internal/adapter/server/mapper.go`. **generated-code-never-hand-edited:**
  `contracts/gen` is regenerated via `buf generate`. **ui-imports-no-proto holds:** the ui
  layer reads the client view-model structs, never proto. No new diagnostics line (the
  "loop emits exactly THREE lines" invariant holds).
- **ADR 0027 durable-log posture.** The new field rides the same metadata-only events the
  durable `EventLog` already persists (`EvSubagentStart`/`EvParallelBranch`/`EvTeamStart`).
  A model id is metadata, same as `routed_model` — persists fine, no new leak surface, no
  new outlives-a-call resource (no `List 1`/`List 2` row).
- **The gRPC `RunTeam` direct path** does not emit an `EvTeamStart` roster (the consumer
  holds the roster from `CreateTeam`); the only roster projection site is the in-process
  Team tool (`teamtool.go`), which now calls `MemberModel`. The gRPC path is unaffected.
- **Security posture — per-call override.** The `model` field is bare metadata (a model
  id), the same posture as `routed_model`, and the TUI renders it through `sanitizeTerminal`.
  One residual is accepted: a Subagent per-call `model` override is a parent-LLM-authored
  tool-call argument, so it surfaces a raw LLM string on the wire/log/TUI (emitted before
  the provider validates the id), whereas `routed_model` carried a composition-resolved id.
  This is inherent to the issue's ask ("the actual model the child ran on" — for an
  override, that IS the override string). The agent-def-pinned, inherited, and routed cases
  are all operator/composition-controlled and unaffected. A catalog-allowlist validation of
  the override id in `buildSubagentEngineFactory` is a separate hardening decision (it could
  reject legitimate uncatalogued-but-provider-valid ids), out of scope for this ADR.
- Cost: three proto fields + a one-line accessor + four emit-site one-liners + the render
  generalization. The `markerEngine` test helper (which used `deps.Model` as a marker for
  the child's *summary* text) had to be de-conflated in the gauntlet-#7 leak test so the
  routed branch carries a benign model id while the secret lives only in its summary.

## See also

- [ADR 0031](./0031-subagent-model-router.md) — the Subagent model router (the `routed_*` provenance signal this widens).
- [ADR 0034](./0034-team-parallel-model-routing.md) — team/Parallel routing + the routed wire.
- [ADR 0027](./0027-cloud-native.md) — the durable-log posture (metadata-only events persist fine).
- [docs/design/IMPLEMENTATION-NOTES.md](../design/IMPLEMENTATION-NOTES.md) — the per-delegation model surface mechanics.
- [docs/architecture/providers.md](../architecture/providers.md) — the living router/model-surface section.
- The lifecycle convention in [ADR 0002](./0002-documentation-lifecycle.md).
