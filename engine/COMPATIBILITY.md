# Engine compatibility policy

The `github.com/stacklok/mecatl/engine` module is the importable core of mecatl
(ADR 0036). This document is its **public API stability contract**: what is
covered, how changes are versioned, and how the contract is mechanically
enforced (issue #114, [ADR 0037](../docs/adr/0037-engine-stability-contract.md)).

## The public surface

The contract covers the exported identifiers of these **eight core packages**:

| Package             | Role                                                      |
| ------------------- | -------------------------------------------------------- |
| `engine/session`    | the `Session` aggregate, value objects, event taxonomy   |
| `engine/governance` | permission `Effect`/`Scope`/`Rule` + `Evaluator`, hooks  |
| `engine/learning`   | optional completed-trajectory mode, snapshot, observer seam |
| `engine/tool`       | `Tool`/`Catalog`, the `FileSystem`/`Workspace` + source ports |
| `engine/prompt`     | prompt assembly + discovery ports                        |
| `engine/port`       | the port interfaces the loop consumes (incl. `SessionStore`, `PrunableStore`, `SessionLease`, `EventLog`, `ScheduleStore`)                    |
| `engine/team`       | the agent-team domain                                    |
| `engine/agent`      | the loop, dispatch, delegation tools, the team Supervisor |

Every **exported** identifier (const, var, func, type, and exported type
methods/fields) in those packages is part of the contract. The guarded set is
**mechanically equal** to `arch.CorePackages` (`engine/arch/surface.go`) — the
single source of truth the layering tests and the public-API gate both consume:
the gate **derives** its list from `arch.CorePackages` and additionally asserts
equality, so the two can never drift (a new core package without a baseline, or a
removed one, fails the build).

### What the snapshot captures

The committed `engine/api/*.txt` baselines are rendered with `go/types` object
strings, refined so the gate catches what a bare object string would miss and
ignores internal churn:

- **Const VALUES are captured** (not just the type). A wire-protocol enum change
  — e.g. an `EventType` such as `EvApproval`, or a `StopReason`/`State` string —
  is a real break for an external consumer reading the value off the wire, so the
  baseline records the exact value (`= "approval"`).
- **Only EXPORTED struct fields are captured.** Unexported fields (mutexes, maps,
  private sub-structs) are stripped, so internal layout never ships in the
  baseline and internal field churn does not force a spurious baseline update or
  CHANGELOG note. Exported methods and interface methods are likewise enumerated.

## Explicitly excluded (may change without notice)

- **`engine/adapter/*`** — the in-tree REFERENCE adapters (`mockllm`, `memfs`,
  `memstore`, `sessnap`, `permpolicy`, `permstore`, `wallclock`, `nofs`,
  `search`, `webfetch`, `memlease`, `fstools`, `agentfs`, `skillfs`, and the `*conformance`
  suites). These ship as offline test doubles and sane defaults, not as a
  stable API. `fstools` (the Read/ListDir/Edit/Write/Copy/Move/Remove/Grep/Glob/Bash tool bodies),
  `agentfs` (the filesystem `.claude/agents` agent-def discovery adapter), and
  `skillfs` (the read-only `.claude/skills` discovery core + Skill tool body), and
  `search` (the WebSearch tool body + Exa/HTTP/SearXNG providers + offline fake),
  and `webfetch` (the bounded public HTTP(S) text fetch tool),
  graduated into the importable module per #269 / #328 / #363 / ADR 0105, are reference bundles: a
  consumer composes its own sources/catalog and may take, subset, swap-by-name,
  or ignore them (see the package docs), so their surface may change without a
  CHANGELOG note. **Note:** the conformance suites' real CONTRACT is the port
  interfaces in `engine/port` / `engine/tool`, which ARE guarded — the suites
  merely exercise it.
- **`engine/arch`** — test-support (the layering/architecture proofs).
- **The entire root module** — `internal/`, `cmd/`, `contracts/`, `perf/`. These
  are outside the engine module boundary (ADR 0036) and carry no external
  compatibility promise.

## Versioning discipline

The engine is tagged independently of the host repo, with the grammar
`engine/vX.Y.Z` (distinct from the root repo's `vX.Y.Z` tags).

While the engine is **v0.x**:

- **minor** (`v0.Y+1.0`) — additive changes (new exported identifiers) **and**
  breaking changes. Pre-v1, SemVer permits breaking changes in a minor bump; we
  require every breaking change to be CHANGELOG-noted and classified.
- **patch** (`v0.Y.Z+1`) — bug fixes with no surface change.

`engine/v1.0.0` is cut once the `engine/port` set settles; from then,
breaking changes require a major bump per strict SemVer.

## Enforcement

Three machine gates protect the contract, all under `task test` / CI:

1. **`api-compat`** — the `engine/api/*.txt` freshness gate (`task api:check`).
   It re-derives the exported surface of the eight packages and fails on any
   drift from the committed text baselines.
2. **engine-standalone build** — `task test:engine-standalone` (`GOWORK=off`)
   proves the module builds + tests against its own tiny dependency closure.
3. **`layering_test`** — `engine/arch` enforces the inward-only dependency rule
   and owns the source-of-truth `arch.CorePackages` list (`engine/arch/surface.go`)
   that the gate derives its guarded set from and asserts equal to.

## Developer workflow for an intentional break

When you change a core package's exported API on purpose:

1. CI (or `task api:check` locally) **fails** with a readable surface diff.
2. Run **`task api:update`** to regenerate the baselines.
3. **Commit** the changed `engine/api/*.txt` file(s).
4. Add an **`engine/CHANGELOG.md`** entry under `## [Unreleased]`, classified per
   this policy (Added = minor; Changed/Deprecated/Removed = breaking).
5. The reviewer sees the readable `.txt` diff **and** the classification in the
   same PR — the change is deliberate and reviewed, never silent.

## Toolchain note

The text dumps use `go/types` object strings, which are stable across Go **patch**
versions. A Go **minor** bump may reformat them (a type-string rendering tweak);
when that happens, a one-time `task api:update` reseeds the baselines — that
regen is NOT an API change and need not be CHANGELOG-noted.

## Release-time advisory

At release time, `task api:release-check` runs `gorelease` (and, transitively,
`apidiff`) as an **advisory** sanity check. It is **not** the PR gate — it needs
a base `engine/vX.Y.Z` tag, which does not exist yet. The `api-compat` text gate
is the PR guard until the first tag is cut.

## Session reconstruction contract (event-sourced Load)

mecatl persists a session as a **snapshot** (`engine/adapter/sessnap`), and
`port.SessionStore.Load` deserializes it. A host whose system of record is an
**append-only event log** (e.g. a downstream consumer) may instead implement `Load` by **folding**
its event stream into a `*session.Session`. The reference implementation is
[`engine/adapter/eventsource`](./adapter/eventsource) (`Fold`); [ADR 0038](../docs/adr/0038-event-sourced-rehydration.md)
records the decision. This section is the field-by-field contract such a backend must
honour.

### Load-failure classification

Snapshot-backed implementations should wrap retrieval or transport failures with
`port.NewSessionLoadFailure(port.SessionLoadFailureStore, err)` and snapshot decode,
format, identity, or validation failures with `port.SessionLoadFailureSnapshot`. Genuine
absence continues to wrap `port.ErrSessionNotFound` and must not be reclassified.
Consumers inspect the typed `port.SessionLoadFailureError` through `errors.Is`/`errors.As`
or `port.ClassifySessionLoadFailure`; error text is not a classification contract.
Unknown custom-store failures intentionally remain `port.SessionLoadFailureUnknown`.

### What a folded `Load` MUST populate vs. what is safe to lose

| Field on the reconstructed `*session.Session` | Round-trip obligation | Source |
| --- | --- | --- |
| `Conversation` (user prompts, assistant text, tool calls, tool results — tool-pairing-valid) | **MUST** | `EvUserPrompt` (user-role turns — the genuine prompt + harness continuations), `EvMessageDelta` (assistant text), `EvToolCall`, `EvToolResult`; pre-compaction head from `EvCompactionArchive` |
| `State` (idle / running / awaiting / completed / failed / cancelled) | **MUST** | derived from the terminal `EvResult.Stop` (or a trailing unanswered `EvPermissionAsk` → awaiting; no terminal → idle) |
| recorded stop reason (`RecordedStopReason`) | **MUST** | `EvResult.Stop` |
| pending ask (`PendingAsk`, when awaiting) | **MUST** | the trailing `EvPermissionAsk` with no following `EvApproval`/`EvResult` |
| cumulative `Usage` | **MUST** | the **SUM** of every per-run `EvResult.Usage` (each `EvResult.Usage` is PER-RUN; the budget brake reads the cumulative aggregate) |
| `Title`, `TitleProvenance`, `TitleGeneration`, and `TitleRevision` | **MUST** | authoritative values supplied out-of-band via `eventsource.SessionMeta`; for legacy metadata with an empty title, `Title` is seeded from the FIRST genuine `EvUserPrompt` via `session.SetTitle` (set-once + clamped), which also records `first-prompt` provenance. `TitleRevision` is a title-specific durable revision; a missing legacy value remains zero. The bounded title-source prompts, attempts, and auxiliary-usage ledger are likewise supplied via `SessionMeta`, never derived from history or normal `EvResult.Usage`. A synthesised-summary `EvUserPrompt` does not seed. For a compacted session the opener's `EvUserPrompt` was emitted before the compaction, so the fallback survives compaction. An operator-authored title cannot be reconstructed from events and therefore must be supplied. |
| creation metadata: id, mode, limits, exact `EnvironmentRef`, safe placement metadata, profile, provider/model selector, reasoning-effort, debug selected-global-MCP names and exact direct-tool ceiling, **session kind and relationship**, createdAt | **MUST** (supplied out-of-band) | **NOT in any event** — provided by the caller via `eventsource.SessionMeta`; an absent legacy kind restores as fail-closed `unknown` |
| identity labels: `Owner` (the verified caller) and `Authority` (Track C, inert) | **MUST** (supplied out-of-band) | provided by `eventsource.SessionMeta`; neither is inferred from event content |
| identity label: `ExternalBinding` (opaque host runtime binding) | **MUST** (supplied out-of-band) when exact external-runtime reattachment is required; otherwise safe to omit on a pure fold | provided by `eventsource.SessionMeta.ExternalBinding`, restored by direct assignment exactly as `sessnap` does; the event annotation is log-only and the fold neither requires nor re-derives it, so an omitted field simply stays the zero value (**never** fabricated or backfilled) |
| `BrokerCredentialCustody` (opaque retained-credential evidence) | **MUST** (supplied out-of-band) when protected pre-prompt recovery is supported; otherwise safe to omit | provided only by trusted `eventsource.SessionMeta.BrokerCredentialCustody` and restored before lifecycle reconstruction. It is not event-carried, is never inferred from `ExternalBinding`, owner, or provider labels, and contains no token, handle, or broker witness. Legacy absence grants no recovery authority. |
| `Counters` (turns / tool calls / consecutive failures) | run-scoped — reflects the **latest run segment** (they reset on `Reopen`), derived from the latest run's events | `EvTurnStart` (turns), `EvToolResult` (tool calls / consecutive failures) |
| run plumbing (diagnostics binding, askID serials, ctx) | safe to lose — rebuilt fresh | n/a |

**Creation metadata is not in events.** No event carries the session id, mode, limits,
exact environment identity, safe placement metadata, profile, provider/model selector, reasoning-effort, debug selected-global-MCP names and exact tool ceiling, authoritative title/provenance,
session kind/relationship, owner/authority, or createdAt. The caller — who created or discovered the session —
supplies them alongside the stream (there is deliberately no `EvSessionCreated`; ADR 0038
notes it as a possible future). `eventsource.SessionMeta` is the reference shape.
Kind/relationship combinations are validated during folding; missing legacy kind metadata
becomes `unknown`, never `main`. Legacy metadata may omit title/provenance; only that case
falls back to the first genuine user-prompt event.

User-role turns (the genuine client prompt AND the harness-authored synthetic
continuations — the no-progress nudge, the background-pending nudge, the
background-completion notice) **are** event-carried, via the log-only `EvUserPrompt`
event the loop emits at every user-message record site. So a fold reconstructs the
**complete** conversation, in stream order — closing the "the log can't show what the
user asked" gap (ADR 0027 row 11). (Turn-0 project-instruction messages discovered from
AGENTS.md/CLAUDE.md are not event-carried; they are derivable from the workspace and are
outside the reconstructed conversation.)

### Replay-fidelity limitation (the one honest boundary)

The conversation a fold rebuilds is complete **except** for the provider-private opaque
replay fields. Three fields reach the conversation **only** via
`Session.RecordAssistant` in the agent loop and are **never emitted on the event
stream**:

- `Message.Reasoning` — the provider reasoning REPLAY blob (OpenAI encrypted reasoning
  content, Anthropic `(thinking, signature)`);
- `Message.ProviderPhase` — the OpenAI Responses phase marker;
- `ToolCall.ItemID` — the provider-assigned item id.

(The `EvReasoningDelta` event carries a human-readable reasoning *summary*, which the
loop deliberately never places on `Message.Reasoning` — so a fold must not either.)
Consequently a session reconstructed by folding mecatl's own event stream is
**byte-identical-replay faithful ONLY for providers that do not use those fields**: it
replays byte-identically for plain-chat providers (e.g. the mock provider) but **not**
for a reasoning provider, whose `Reasoning`/`ProviderPhase`/`ItemID` would be empty where
the snapshot would carry them. This is why mecatl's **own** resume uses the snapshot
(which carries those fields); the fold is for event-log-SoR hosts that accept this
boundary or carry those fields in their **own** richer event schema. This is a documented
contract limitation, not a bug.

## See also

- [ADR 0038 — event-sourced rehydration](../docs/adr/0038-event-sourced-rehydration.md)
- [ADR 0037 — engine stability contract](../docs/adr/0037-engine-stability-contract.md)
- [ADR 0036 — `engine/` is its own Go module](../docs/adr/0036-engine-module.md)
- [Project README](../README.md)
