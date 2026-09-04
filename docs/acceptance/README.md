# Acceptance plans

Each document here is an acceptance plan or completed acceptance record: the
smallest set of work that makes one substantive issue or capability demonstrable
on the running harness. They are organized scenario-first — acceptance is about what the
harness can show, not which packages exist on disk. A focused issue may use one
scenario and one orchestration task; plans must not manufacture complexity. Each
document names its scope and cites the ADRs / [architecture](../architecture.md) /
[AGENTS.md](../../AGENTS.md) invariants that pin its decisions.

Plans are authored by the `/to-acceptance-plan` skill and driven to completion
by `/plan-orchestrate`. New plans must be linked from this README; the matlatl
gate (`task docs`) fails on an unreachable doc.

## The verification contract

Every numbered acceptance criterion carries a `verify:` sub-line inside that AC's
block, before the next AC or section. Its value must be non-empty and name one or
more Go test names, or a non-test method with a reason:

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

- [Mecatui card layout](mecatui-card-layout.md) — width-bounded card rendering
  that wraps raw dynamic rows before styling, preventing Lipgloss alignment
  padding from becoming vertical whitespace while retaining intentional
  Markdown and input-rail exceptions. Status: draft.
- [Local mecak8s Kind fixture](mecak8s-kind-fixture.md) — cleanly separates an
  operator-run mock/real-provider mecak8s fixture, optional Keycloak caller
  identity, and the deferred ToolHive/vMCP delegation extension. Status: draft.
- [goccy/go-yaml migration](goccy-yaml-migration.md) — replace direct root and
  engine yaml.v3 parsing with goccy/go-yaml while preserving safe diagnostics,
  strict/lenient contracts, frontmatter parsing, and standalone engine closure.
  Status: draft.
- [Operator-defined LLM providers](operator-defined-llm-providers.md) — operator-local,
  truthfully named gateway providers over the existing Responses, Chat Completions, and
  Anthropic Messages adapters, plus persistent built-in endpoint overrides. Status: draft.
- [Surface approval migration](surface-approval-migration.md) — final Phase-2 migration of the mecatui approval UI onto the dynamic surface contract, including ephemeral render-frame hit dispatch. Status: landed.
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
- [Session debugger lineage lock](session-debug-lineage-lock.md) — root debug
  inspection avoids global lineage traversal while related and descendant-scoped
  evidence keeps its existing bounded, fail-closed revalidation. Status: draft.
- [Session storage continuity](session-storage-continuity.md) — historical storage plan: bounded current snapshots, indexed metadata, and maintenance jobs landed; its writable legacy-adoption criteria were superseded by ADR 0291. Status: draft historical record.
- [Session continuity UX](session-continuity-ux.md) — authoritative session-family
  metadata, honest replay/inspection, an active-session copy surface, startup resume,
  and a final session-ID handoff for mecatui. Status: landed.
- [Steer-while-running](steer-while-running.md) — inject a user message into an
  in-flight run (Claude Code's "steer"): an engine-side supersedable inbox drained
  at the turn boundary, a gRPC `Converse` frame, the authoritative drain echo, and
  the mecatui capability flip; gRPC-only v1 (HTTP deferred). Status: draft.
- [Authority evaluator port](authority-evaluator-port.md) — derived capability sets
  narrowed at every delegation seam, with the decision behind one swappable
  evaluator port at the single dispatch chokepoint; Cedar is an opt-in adapter.
  Status: draft.
- [Extensible mecatui status line](mecatui-status-line.md) — user-global
  responsive header/footer templates or one local command over a shared status
  input and theme-integrated StatusML; present chrome is the default templates.
  Status: draft.
- [Predictable mecatui session handles](predictable-session-handles.md) — one fixed,
  terminal-safe, client-resolved escaped raw-ID-prefix handle for ordinary chrome, `/sessions`,
  status-line v2, and `mecatui debug`; debug resolves positional handles against all caller-visible
  projected matches and offers an explicit inventory-free exact-ID path, while server APIs and
  debugger evidence/incarnation handles retain their exact existing identity contracts. Status:
  landed.

- [Server-owned session placement](server-owned-session-placement.md) — removes public
  filesystem-path authority: composition binds default/no-FS/remote environments;
  alternate worktrees use fresh source-scoped opaque selectors only on clear/fork.
  Status: landed.
- [Listener-scoped workspace authority](listener-scoped-workspace-authority.md) — historical
  draft superseded by ADR 0291's path-free contract; retained for context and excluded from
  strict traceability. Status: draft.
- [SDK server enablers](sdk-server-enablers.md) — the Go-side contracts the TypeScript
  SDK is built on: `GetServerInfo` + an open-string feature vocabulary, RFC 9457 typed
  errors, exact-origin CORS, a durable host-minted `run_id` with stale-control guards,
  `port.CursorEventLog` + the Redis LIST→Stream migration, `WatchSessionEvents`, the
  spawned-daemon UDS/ready-file surface, and listener-scoped `mcp_servers`. Status: draft.
- [TypeScript SDK core (M1)](sdk-typescript-core.md) — `@stacklok/mecatl-sdk` M1: the
  `sdk/typescript/` scaffold (pnpm 11, TS 6, biome, vitest, API Extractor), pinned
  protobuf-es generation for `mecatl.v1` with a freshness gate, Connect-ES + HTTP/SSE
  raw transports behind an injected-Transport seam, typed errors and the compatibility
  floor, `Client`/`Session`/`Run` choreography with permissions and strict steer,
  multimodal helpers, and the offline e2e against `mecated --mock`. Status: landed.
- [Session affinity and handoff](session-affinity-and-handoff.md) — exact
  `X-Mecatl-Session-ID` client→gateway→mecak8s→provider correlation, automatic
  official-client propagation, session-scoped lease fencing, graceful drain, and
  killed-owner Redis handoff without chart-owned Gateway policy. Status: landed.
- [TypeScript SDK durable attachment (M2)](sdk-typescript-attach.md) — the client
  half of the durable watch: the `WatchSessionEvents` envelope union with tolerated
  unknown phases, `session.attach()` / `session.activity()`, a serializable
  filter-branded cursor with consumption-time checkpointing, explicit gap and
  cursor-fault errors, a three-arm reconnect authority over a closed terminal code
  set, an HTTP-only attached `cancel` carrying `expected_run_id` (attached
  approval deferred to an ack-only server route), and the offline
  daemon-restart and awaiting-resume e2e. Status: draft.
- [TypeScript SDK local daemon and callback tools (M3)](sdk-typescript-local.md) —
  `@stacklok/mecatl-sdk` M3: `spawn()`'s binary resolution, SDK-owned argv and
  ready-file barrier, the UDS-only tool-capable topology with its lifetime pipe,
  redacted startup-failure reporting, the spawned-versus-connected disposal
  ownership matrix, `query()`'s one-shot lifecycle, callback `tool()` with local
  schema validation and two-layer collision refusal, the hand-written loopback
  streaming-HTTP MCP host, and the offline Node/Bun e2e. Status: draft.

## See also

- [Development process](../development-process.md) — the spine end to end.
- [Architecture guide](../architecture.md) · [ADRs](../adr/)
