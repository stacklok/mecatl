# Acceptance plans

Each document here is an acceptance plan or completed acceptance record: the
smallest set of work that makes one substantive issue or capability demonstrable
on the running harness. They are organized scenario-first — acceptance is about what the
harness can show, not which packages exist on disk. A focused issue may use one
scenario and one orchestration task; plans must not manufacture complexity. Each
document names its scope and cites the ADRs / [architecture](../architecture.md) /
[AGENTS.md](../../AGENTS.md) invariants that pin its decisions.

Plans are authored by `/to-acceptance-plan`, human-reviewed as behavioral and
interface contracts, and implemented by `/plan-orchestrate` only after the applicable
checkpoint. New plans must be linked from this README; the matlatl gate (`task docs`)
fails on an unreachable doc. Explicitly requested exploratory/spike work, and any request the
directing human explicitly waives the spine for, need none of this.

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

## Human decisions

Every plan has one non-empty `## Human decisions` section with exactly one shape:

- `None — <rationale>` when no human judgment remains; or
- checklist items, with `- [ ] ...` for unresolved judgments and
  `- [x] ... — Decision: ...` for resolved judgments.

Place every material behavior/interface choice there, not under deferred decisions. The
bundled checker rejects missing, empty, placeholder, or malformed sections and ties unchecked
items mechanically to `draft` status. New plans and materially amended legacy plans after ADR 0306
must declare exact `**Contract:** human-reviewed/v1` metadata; unmarked historical plans are
grandfathered until materially amended and are not bulk-migrated.

## Interface contract

Every new plan has exact `**Contract:** human-reviewed/v1` metadata and a `## Interface contract` section with all seven exact canonical
category labels: gRPC/protobuf, exported Go APIs/interfaces, tool schemas, CLI/config,
events/persistence, security/authority, and compatibility/migration. Every category needs
non-placeholder content. `None` is valid only as `None — <rationale>` (hyphen, en dash, or
em dash); bare `None`, `TBD`, and `<...>` placeholders fail the bundled checker. The checker
also requires `**Delivery:** Split|Combined` and an allowed `**Status:** draft|proposed|approved|in-progress|landed`
prefix. Public or material decisions cannot be deferred to implementation; unresolved
material decisions keep the plan in `draft`.

Substantive interface-bearing plans use a Plan / Interface PR before implementation.
Combined is limited to a compact one-task plan with exactly one `### Scenario`. It declares
exact `**Expected tasks:** 1` metadata and a non-placeholder `**Combined rationale:**`
explaining why separate plan review adds no value. **gRPC / protobuf**, **Exported Go APIs /
interfaces**, **Tool schemas**, **CLI / config**, **Events / persistence**, and **Security /
authority** each begin `None — <rationale>`.
**Compatibility / migration** may describe workflow migration, and splitting must add no
review value. A workflow-only meta-change may treat process documents and skills as the
interface reviewed in the same PR. Combined preparation opens no separate plan PR.

Material contract drift stops orchestration with `blocked-contract-drift`; the orchestrator
cannot create, commit, push, or open an amendment. A separate, explicitly authorized
`/to-acceptance-plan` amendment invocation uses the Split Plan / Interface PR flow and must
pass checker/docs verification, human review and merge, and return to `approved`. The
orchestrator records the amendment PR and full merged commit before resuming.

## Status lifecycle

A plan moves `draft → proposed → approved → in-progress → landed`:

- `draft`: material behavior or interface judgments may remain as unchecked Human decisions.
- `proposed`: every human decision needed to implement the contract is resolved and recorded;
  the plan is validated and ready for human plan/interface review.
- `approved`: the plan PR merged into the target branch — merging is the approval
  event; the contract is approved, not shipped. `/plan-orchestrate` proves this by git
  ancestry, not by the literal status word, and corrects the label to `approved` on entry if
  a merged plan still reads `proposed`.
- `in-progress`: autonomous implementation is underway against the recorded baseline.
- `landed`: after all verification passes, the implementation/Combined candidate carries
  the proposed transition in its PR diff; it becomes authoritative only when that PR
  merges. Until then the target branch remains `approved` or `in-progress`.

Only an authoritatively `landed` plan is gated by `ac-trace --strict`; earlier target-branch
states do not claim the behavior shipped. `/plan-orchestrate` records the approved plan PR
and commit baseline for split work and places the `landed` edit directly in the completing
PR after verification. There is no cleanup or status-only PR.

## Plans

- [Human-reviewed development contracts](human-reviewed-development-contracts.md) —
  plan/interface review before autonomous implementation, with exact interface
  declarations, run-local orchestration state, and a final human code-review gate.
  Status: landed in this Combined candidate; authoritative on merge.
- [Mecatui live-feed reconnect](mecatui-live-reconnect.md) — regression closure for bearer-backed first-Recv authentication rejection, existing `/connect` recovery, cross-loop reconnect continuity/backoff, and real-event recovery without weakening generation, cancellation, or catch-up invariants. Status: landed.
- [Mecatui double-Escape draft clearing](mecatui-double-escape.md) — a fail-closed,
  enhanced-key-event-only, silent press-release-press idle prompt gesture with an exact
  500 ms generation-tagged expiry; it reuses `clearPrompt`, preserves surface ownership,
  and documents universal `ctrl+u`. Status: landed.
- [Mecatui card layout](mecatui-card-layout.md) — width-bounded card rendering
  that wraps raw dynamic rows before styling, preventing Lipgloss alignment
  padding from becoming vertical whitespace while retaining intentional
  Markdown and input-rail exceptions. Status: draft.
- [Headless mecatui credential storage](headless-client-credential-storage.md) —
  Linux-first, read-only Secret Service detection for a root-pinned keyring or
  owner-only plaintext local credential backend; selection and upgrade-only
  compatibility decisions are settled. Status: proposed.
- [Persistent read-before-write ledgers](persistent-read-before-write-ledgers.md) —
  storage-independent, session-scoped read evidence with an in-memory default,
  a durable Redis contract proof, and fail-closed file-tool behavior. Status: draft.
- [Session-load observability](session-load-observability.md) — target-free operator
  classification and metrics for snapshot load failures while preserving ownership
  concealment. Status: landed.
- [Managed temporary command leases](managed-temporary-command-leases.md) — private,
  attributable Linux and macOS command/job temporary storage with a permission-visible
  system escape and deterministic crash-residue reaping. Status: landed.
- [Session title generation and token usage](session-title-generation.md) — mecatui `/title`, an opt-in routed model title after up to three genuine prompts, and durable title-model token attribution. Status: draft.
- [Per-upstream MCP broker OAuth grants](mcp-broker-multi-upstream-oauth.md) — accept multiple broker OAuth upstreams while keeping grants, callback state, authenticated discovery, and workspace-enrollment progression backend-scoped. Status: draft.
- [MCP broker DCR client](mcp-broker-dcr-client.md) — a third `mcp.servers[].auth.oauth.client.mode: dcr`, exposing ToolHive's existing RFC 7591 Dynamic Client Registration upstream-client support for protected MCP servers with no preregistered client or hosted CIMD document. Status: draft.

- [Local mecak8s Kind fixture](mecak8s-kind-fixture.md) — cleanly separates an
  operator-run mock/real-provider mecak8s fixture, optional Keycloak caller
  identity, and the deferred ToolHive/vMCP delegation extension. Status: draft.
- [Agent model discovery](agent-model-discovery.md) — a bounded, read-only,
  model-facing view of the existing resolved inventory, presenting exact
  `(provider_id, model_id)` selection handles without redesigning provider identity.
  Status: draft.
- [Scalable reflection evidence](scalable-reflection-evidence.md) — one versioned,
  deterministic bounded-evidence materializer for automatic and explicit reflection,
  replacing raw retained-size rejection while preserving coordinator and promotion safety.
  Status: draft.
- [goccy/go-yaml migration](goccy-yaml-migration.md) — replace direct root and
  engine yaml.v3 parsing with goccy/go-yaml while preserving safe diagnostics,
  strict/lenient contracts, frontmatter parsing, and standalone engine closure.
  Status: draft.
- [OpenAI Responses multiple visible text parts](openai-multiple-text-parts.md) — accept every ordered visible text delta from distinct Responses item/content identities through the existing `ChunkText`/`Message.Text` path, without changing phase, reasoning, tool-call, or post-visibility no-replay behavior. Status: landed.
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
- [OAuth protected-resource discovery](oauth-protected-resource-discovery.md) — RFC 9728 metadata from mecated/mecak8s and mecatui shorthand enrollment with issuer/audience/client hints. Status: draft.
- [Mecatui server-owned discovery scopes](mecatui-server-owned-discovery-scopes.md) — server-authoritative discovery scope selection: advertised sets are requested exactly, omitted metadata selects the fixed OIDC baseline, and discovery-mode `--scopes` is rejected. Status: proposed.
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
- [Mecatui diagnostic-log retention](mecatui-diaglog-retention.md) — startup
  retention of a bounded recent tail for the default and override diagnostic
  sinks, with atomic failure preservation and fail-closed path handling. Status:
  draft.
- [Predictable mecatui session handles](predictable-session-handles.md) — one fixed,
  terminal-safe, client-resolved escaped raw-ID-prefix handle for ordinary chrome, `/sessions`,
  status-line v2, and `mecatui debug`; debug resolves positional handles against all caller-visible
  projected matches and offers an explicit inventory-free exact-ID path, while server APIs and
  debugger evidence/incarnation handles retain their exact existing identity contracts. Status:
  landed.

- [Opt-in local session context](opt-in-local-session-context.md) — a separate,
  embedded-Mecatui-only privileged gRPC service reveals an owned session's exact
  reattached local root to status templates through their escaped v3 input projection
  and to a configured local status command through raw v3 input and CWD; public
  placement surfaces remain path-free. Status: draft.
- [Server-owned session placement](server-owned-session-placement.md) — removes public
  filesystem-path authority: composition binds default/no-FS/remote environments;
  alternate worktrees use fresh source-scoped opaque selectors only on clear/fork.
  Status: landed.
- [InspectSession scoped read isolation](inspect-session-read-isolation.md) — proposed
  bounded direct-edge lineage reads, self-routing opaque handles, and targeted maintenance that
  keep debugger inspection from blocking unrelated session operations. Status: in-progress.
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
- [TypeScript SDK public surface and v0.0.1 GitHub Packages release (M4)](sdk-typescript-release.md) —
  `@stacklok/mecatl-sdk` M4: the descriptor-to-transport parity gate for all 77
  public RPCs, thin typed namespaces, ergonomic teams, streaming plan
  resolution, TypeScript/Node/browser/macOS compatibility matrices, executable
  examples and public docs, path-qualified GitHub Packages publication, and the
  human-gated `v0.0.1` cut; npmjs `v0.1.0` remains a #821 follow-up. Status: draft.

- [Canonical Shell command tool](canonical-shell-command-tool.md) — canonical `Shell`
  and `ShellStatus` model-facing names, safe legacy `Bash` input normalization, and
  parser-backed portable-POSIX feedback for model-facing shell commands. Status: proposed.

## See also

- [Development process](../development-process.md) — the spine end to end.
- [Architecture guide](../architecture.md) · [ADRs](../adr/)
