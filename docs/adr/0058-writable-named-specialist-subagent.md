# ADR 0058 — Writable named-specialist Subagent (`mode:"read-write"` + `agent`)

- Status: Accepted
- Date: 2026-06-29
- Scope: `engine/agent` (the Subagent tool's `validateMode` / `selectChildEngine` / `resolveEngineAndLimits` / `MutatesParent` + the new `WithAgentWritableEngineFactory` Option and `selectWritableSpecialistEngine` helper), `internal/app` (`buildAgentWritableEngineFactory`, the `allowMutating` plumb in `buildAgentDefEngine`)
- Supersedes: the named-specialists-run-read-only v1 scope restriction shipped alongside ADR 0077's direct-write explorer (lived in `validateMode`, not 0077's prose) — NOT 0077's direct-write mechanics, the `parentMutatingCaller` dispatch-serial seam, or the `isolated:false` posture, all of which are REUSED
- Superseded by: [ADR 0242](./0242-route-unpinned-writable-named-specialists.md) — ONLY the fixed-model selection for an unpinned writable named specialist; direct-write execution, scoped writable catalog, and mutate-serial posture remain authoritative

## Context

ADR 0077 shipped the direct-write writable Subagent (`mode:"read-write"`) but scoped it to the
generic explorer: `validateMode` rejected `mode:"read-write"` + `agent` outright, so a named
specialist could only ever run read-only. The v1 scope cut was incremental, not fundamental — the
machinery to lift it already existed. `agent` + `model` (`buildAgentModelEngineFactory`) rebuilds a
specialist's scoped engine per call via the shared `buildAgentDefEngine` step; a writable specialist
is the same pattern with two knobs moved: `allowMutating=true` (so Edit/Write survive scoping) and
the MAIN session's command runner (direct-write parity, ADR 0077 — no fork, no merge-back).

The tell was a load-bearing line in `resolveEngineAndLimits` (`engine/agent/subagent.go`):
`if writable { engine = t.writableChildEngine }` unconditionally clobbered whichever engine
`selectChildEngine` had resolved, so a specialist selected for a `read-write`+`agent` call was
silently swapped back to the generic writable explorer — the specialist's prompt/skills/catalog were
thrown away. Restructuring the clobber to `if writable && args.Agent == ""` lets a writable
specialist keep the engine its factory minted, while the generic writable explorer path stays
byte-identical for the `agent == ""` case.

## Decision

Add `WithAgentWritableEngineFactory` (`engine/agent/subagent.go` (`WithAgentWritableEngineFactory`))
— a composition-supplied closure `func(agentName string) (*Engine, bool)` injected as a
`SubagentOption`. The full resolution is in `engine/agent/subagent.go`
(`validateMode` / `selectChildEngine` / `resolveEngineAndLimits` / `MutatesParent`), and the
composition wiring is in `internal/app/build.go` (`buildAgentWritableEngineFactory`) over
`internal/app/agentdefs.go` (`buildAgentDefEngine` with its new `allowMutating` parameter).

- `validateMode` now ALLOWS `read-write` + `agent` when the factory is wired. It still REJECTS:
  `read-write` + `agent` + `model` (all three — a v1 scope limit, fired BEFORE the factory-nil arm so
  the model sees the specific message); and `read-write` + `agent` with no factory (no-FS profile or
  an unwired deployment → "not supported in this deployment"). The arm ordering is: the all-three
  rejection first, then the factory-nil rejection, then the allowed path.
- `selectChildEngine` threads `writable` and routes a `read-write` + `agent` call through
  `selectWritableSpecialistEngine` (`engine/agent/subagent.go` (`selectWritableSpecialistEngine`)),
  which calls `agentWritableFactory(wantAgent)`. The pre-built `agentEngines` map is read for
  name-truth (is the agent known?) and per-def limits only — it is NEVER mutated (a fresh engine per
  call, parity with `agent` + `model`).
- `resolveEngineAndLimits` no longer clobbers the specialist engine with `writableChildEngine`: the
  `agent`-set case keeps the factory engine. The clobber is now `if writable && args.Agent == ""`,
  so the generic writable explorer (`buildWritableSubagentChildEngine`) is unchanged.
- `MutatesParent` is an OR gate: `mode == "read-write"` AND (`writableChildEngine != nil` OR
  `agentWritableFactory != nil`). A writable specialist mutates the real tree DURING its run, so the
  dispatcher keeps it mutate-serial (excluded from the concurrent read batch, run alone) — the SAME
  `parentMutatingCaller` seam from ADR 0040/0077, decoupled from any merger (there is none).
- The factory (`buildAgentWritableEngineFactory`) calls
  `buildAgentDefEngine(…, allowMutating=true, …, buildCommandRunner(cfg), …)` — so Edit/Write SURVIVE
  scoping (`allowMutating=true` keeps workspace-mutating tools), the child runs on the MAIN session's
  command runner (`buildCommandRunner`, direct-write parity), on the def's RESOLVED provider/model.
  A def with INLINE MCP servers is DECLINED on the writable-agent path (v1 scope limit — the inline
  manager's live session has no process-lifetime owner on a per-call engine, the same rationale as
  the `agent` + `model` path). Reference-only MCP is SUPPORTED (it borrows `mainMgr`).
- The posture is `isolated:false` (the child shares the real parent workspace), so the A2 isolation
  auto-approve (`governance.IsolationApprovable`) does NOT apply — the specialist's Bash/Edit/Write
  resolve at MAIN-SESSION parity under the operator's posture/policy, exactly as the writable
  explorer does.

## Consequences

- A named specialist can now land edits directly against the real parent workspace, with its own
  prompt/skills/catalog — not the generic explorer surface. The "delegate one focused task to the
  specialist and land its edits" path that ADR 0077 opened for the explorer is now open for named
  specialists too.
- The def's tool allowlist is HONORED: a def that excludes Edit/Write stays non-mutating even in
  writable mode (`allowMutating=true` only KEEPS the mutating tools the def already permits — it does
  not force-inject them). This is documented, not enforced by a separate gate.
- Partial edits survive a crash: a cancelled/errored writable specialist leaves its completed edits
  IN the working tree, with no fork to quarantine them — git is the rollback layer, the same accepted
  trade-off as ADR 0077. The result text honestly warns the edits may be partial.
- Per-def limits + the def's resolved provider/model still bind — the writable specialist is the
  specialist, not a generic explorer wearing its name.
- `read-write` + `agent` + `model` stays OUT of scope: the writable specialist runs on its own
  resolved model; a per-call model override on a writable specialist engine is not wired. The
  all-three rejection fires first in `validateMode`.
- Scope is the serial Subagent ONLY. `Parallel` branches and mutating `Team` members keep the
  force-copy fork + serialized merge — they have genuine concurrency that direct-write would race.
- A fourth per-call specialist factory (the deferred `read-write`+`agent`+`model` combination) is the
  point to extract a shared `specialistOverride` builder — not before (the Rule-of-Three trigger,
  recorded here as a deferred decision so the next implementer knows the threshold).

## See also

- [ADR 0077](./0077-direct-write-subagent.md) — the direct-write writable Subagent (the
  `parentMutatingCaller` seam, the `isolated:false` posture, and the direct-write mechanics are all
  REUSED, not superseded; only its "named specialists run read-only" v1 scope cut is lifted here).
- `docs/architecture/subagents-and-teams.md` — the living description of the subagent writable mode.
- `docs/design/IMPLEMENTATION-NOTES.md` — the per-subsystem writable-Subagent entry.
- The documentation lifecycle convention in [ADR 0002](./0002-documentation-lifecycle.md).
