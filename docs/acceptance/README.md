# Acceptance plans

Each document here is an acceptance plan or completed acceptance record: the
smallest set of work that makes one capability demonstrable on the running
harness. They are organized scenario-first — acceptance is about what the
harness can show, not which packages exist on disk. Each document names its
capability and cites the ADRs / [architecture](../architecture.md) /
[AGENTS.md](../../AGENTS.md) invariants that pin its decisions.

Plans are authored by the `/to-acceptance-plan` skill and driven to completion
by `/plan-orchestrate`. New plans must be linked from this README; the matlatl
gate (`task docs`) fails on an unreachable doc.

## The verification contract

Every numbered acceptance criterion carries a `verify:` sub-line naming its
proof — one or more Go test names, or a non-test method with a reason:

```markdown
- AC1.1: a second owner acquiring the session lease gets FAILED_PRECONDITION.
  - verify: `TestInvariant_lease_single_owner`
- AC1.2: reserved, no producer yet.
  - verify: none — reserved with no producer
```

The [`ac-trace`](https://github.com/stacklok/ac-trace) gate
(`task ac-trace`, `task ac-trace-strict`) checks that every named proof
resolves in the tree. On a `landed` plan the strict gate fails when a named
test is missing, a cited `ADR-NNNN` doesn't resolve, or a scenario test
exists that no landed plan tracks.

Named-test conventions ac-trace recognises:

- `TestInvariant_<id>` — an invariant from `AGENTS.md` ("Things That Will
  Bite You") or `docs/design/IMPLEMENTATION-NOTES.md`, id kebab → snake.
- `TestADR_NNNN_*` — a rule codified in `docs/adr/NNNN-*.md`.
- `Test<Plan>_Scenario<N>_*` — a scenario test a plan scenario claims.
- Descriptive test names are accepted in a `verify:` line, but prefer the
  pinned forms when the AC defends a rule.

## Status lifecycle

A plan's `**Status:**` line moves `draft → in-progress → landed`. Only a
`landed` plan is gated by `ac-trace --strict`; drafts are reported but never
fail the build. `/plan-orchestrate` flips the plan to `landed` on the
accumulator once the aggregate gate passes, so the strict gate bites exactly
when the code that satisfies the plan has landed.

## Plans

- [Spine convergence](spine-convergence.md) — bring the
  to-acceptance-plan / plan-orchestrate / test-writer spine + ac-trace into
  mecatl. Status: draft.
- [Schedule tool](schedule-tool.md) — in-chat scheduled tasks: a model-facing
  `Schedule` tool, the scheduler on by default, and the removal of the
  declarative settings block + `mecated schedules` CLI. Status: landed.
- [Fire-result delivery](fire-result-delivery.md) — a scheduled fire reports its
  result back into the originating conversation ("ping me when X"), closing ADR
  0073's deferred delivery channel. Status: landed.
- [Schedule shared catalog](schedule-shared-catalog.md) — the `Schedule` tool on
  every schedule-capable session: a store-shaped pre-Service schedule manager so
  the shared-engine fast path (the default mecatui session) carries the tool too.
  Status: landed.
- [Path-escape posture](path-escape-posture.md) — relax the osfs out-of-root FS
  rejection by operator posture (allow at `auto`/`yolo`, ask at
  `strict`/`trusted`), keeping the untrusted-child hard-deny, the symlink
  containment, and a pseudo-fs never-relaxed rule. Status: landed.
- [Delegation observability convergence](delegation-observability-convergence.md) —
  bounded previews for Subagent/Parallel, converging the delegation observability
  surface on two tiers (Team-unique structures stay Team-only). Status: landed.
- [Caller identity](caller-identity.md) — completed acceptance record for optional
  OIDC caller attribution: a verified principal, durable session/schedule ownership,
  and log-only event actors; no authorization. Status: landed.
- [Caller separation](caller-separation.md) — enforce OIDC caller isolation over
  application sessions, schedules, teams, memory, event streams, live runs, and
  model-facing object access; remote-driver enforcement is deferred to #452. Status:
  draft.
- [Session continuity UX](session-continuity-ux.md) — authoritative session-family
  metadata, honest replay/inspection, an active-session copy surface, startup resume,
  and a final session-ID handoff for mecatui. Status: landed.
- [Session storage continuity](session-storage-continuity.md) — bounded current
  snapshots, indexed progressive inventory, resumable migration/cleanup, configurable
  retention, and explicit legacy-chat adoption. Status: draft.
- [Steer-while-running](steer-while-running.md) — inject a user message into an
  in-flight run (Claude Code's "steer"): an engine-side supersedable inbox drained
  at the turn boundary, a gRPC `Converse` frame, the authoritative drain echo, and
  the mecatui capability flip; gRPC-only v1 (HTTP deferred). Status: draft.

## See also

- [Development process](../development-process.md) — the spine end to end.
- [Architecture guide](../architecture.md) · [ADRs](../adr/)
