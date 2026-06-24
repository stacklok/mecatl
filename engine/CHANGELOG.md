# Changelog — `github.com/stacklok/mecatl/engine`

All notable changes to the engine module's public API are recorded here. The
format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); changes
are classified per [COMPATIBILITY.md](./COMPATIBILITY.md) (Added = minor;
Changed/Deprecated/Removed = breaking, pre-v1 a minor bump).

The covered surface is the seven core packages (`session`, `governance`, `tool`,
`prompt`, `port`, `team`, `agent`); their committed API snapshots live in
[`engine/api/`](./api/).

## [Unreleased]

### Added

- **`session.HookAdvisory`.** A new `HookDecision` value (`"advisory"`) for an
  `EvHook` carrying an advisory guardrail finding — client-visible (rendered as
  a warning notice), model-invisible (the tool result is byte-unchanged). The
  advisory arm of `modelhook.enforce` now returns a `HookOutcome{Message:...}`
  (instead of an empty outcome), and dispatch recognises the
  `Message!="" && !Block && len(Mutated)==0` shape as an advisory outcome,
  emitting an `EvHook` with `HookAdvisory`. Classified Added per
  COMPATIBILITY.md (a new exported const + a new wire `HookDecision` enum
  value). (#170)

- **`agent.WithAgentModelEngineFactory`.** A new `SubagentOption` injecting a
  composition-supplied factory `func(agentName, model string) (*Engine, bool)`
  that rebuilds a named specialist's scoped engine on a per-call override model,
  so a Subagent call may now set BOTH `agent` and `model` (previously rejected).
  The override model runs on the def's resolved provider; the specialist's
  catalog/prompt/skills/hooks/memory are preserved (NOT the generic explorer
  set); the provider-closing Deps are re-derived for the override model via the
  contamination-safe per-provider path. A def with inline MCP servers is
  declined on the agent+model path (v1 scope limit); reference-only MCP is
  supported. Classified Added per COMPATIBILITY.md (a new exported Option).

### Changed

- **`tool.MaxAgentDescriptionBytes` raised 800 → 2000; `tool.MaxAgentBodyBytes`
  raised 8192 → 32768 (32 KiB).** The two agent-def caps got more headroom, with an
  asymmetric rationale: the DESCRIPTION rides `Subagent`'s `Spec().Description` on
  every request, summed across all registered agents, and is part of the byte-stable
  prompt-cache prefix, so it stays conservative (2000 B); the BODY is in-context only
  for that one specialist engine's own turns, so it can afford 32 KiB. The constant
  identifiers and types are unchanged — only their VALUES move. Classified Changed
  per COMPATIBILITY.md (an exported const value is part of the API snapshot): an
  external consumer relying on the exact old numbers, or on the old truncation point,
  must re-baseline. Discovery stays deterministic; the only runtime effect is a
  one-time prompt-cache-prefix invalidation. (#156)

## [0.1.0] - 2026-06-22

### Changed

- **Behaviour (no API change): turn-0 instruction fragments are now EPHEMERAL.** The
  soul / project-instruction / memory-index / user-model fragments produced by the
  `prompt.InstructionAssembler` chain are no longer persisted into
  `session.Conversation.Messages` at turn 0. The agent loop now assembles them ONCE per
  run and PREPENDS them to the per-request `port.LLMRequest.Messages` on every turn
  (including resume), never writing them into the conversation, event-carrying them, or
  snapshotting them. The genuine user prompt is still recorded and event-carried
  unchanged. This fixes resume-time history bloat (resumed runs no longer re-append the
  fragments), keeps the persisted conversation clean (so compaction anchors on the
  genuine first instruction), and converges the snapshot + `eventsource.Fold`
  rehydration paths fragment-free. No exported surface changes:
  `RecordUserPromptWithParts` keeps its `instr` parameter (now called with nil) and
  `prompt.IsInjectedTurn0Fragment` is retained (defense-in-depth for legacy persisted
  history). See `docs/adr/0043-ephemeral-turn0-instruction-fragments.md`.

### Added

- `session.PendingAsk.Call` (`session.ToolCallID`) — the opaque gated tool-call id on
  the ask REQUEST half, mirroring `session.ApprovalPayload.Call` on the verdict half.
  It round-trips in the sessnap snapshot, giving a host durable, grammar-free
  correlation of a pending ask back to its tool call (no askID grammar parse). It is
  an opaque identifier, not secret content (it is already implicitly encoded inside the
  askID), so surfacing it opens no new leak surface. (#148)
- `agent.RunOptions.AskIDDiscriminator` (`string`) — an opt-in, host-supplied trailing
  askID component that REPLACES the process-global `"r<serial>"` suffix, making the
  askID `"<sessionID>:<n>:<callID>:<discriminator>"` reconstructable across processes
  from persisted state. The host contract requires it to be unique-per-attempt,
  stable-per-attempt-across-processes, and colon-free (a colon-containing value is
  ignored with a WARN and falls back to the serial). Empty (the zero value) preserves
  the `"r<serial>"` fallback with no behaviour change. See
  `docs/adr/0044-host-supplied-askid-discriminator.md`. (ADR-0044, #117)
- `prompt.IsInjectedTurn0Fragment(text string) bool` — reports whether a string is
  the body of a harness-injected turn-0 context fragment (project instructions /
  soul / memory index / user model) rather than a genuine user instruction. The four
  turn-0 `InstructionAssembler`s record their output as `RoleUser` messages, so a
  consumer that must anchor on "the user's genuine first instruction" (the compaction
  first-user pin; the resume re-injection guard) calls this to skip them. It
  recognises each fragment by the header its renderer prepends (the shared
  source-of-truth constants in `engine/prompt/turn0.go`), so a header reword is
  reflected automatically. Additive function in `engine/prompt`. See ADR 0012.
- `prompt.DefaultTone() string` — returns the built-in tone/style block `Build`
  uses when `Config.Tone` is empty. Mirrors `DefaultRole()`; lets the composition
  layer compose an output-economy tier delta (ADR 0041: the "terse" posture
  appends an answer-length clause) onto the SAME default tone the prompt package
  uses, rather than carrying a private verbatim copy that could silently diverge.
  Additive; no frozen domain type changes.

### Removed

- **BREAKING:** `agent.WithWritableChildForker(f tool.WorkspaceForker) SubagentOption`
  and `agent.WithSubagentAutoMerge(m tool.ForkMerger) SubagentOption` — removed. The
  writable Subagent (`mode:"read-write"`) no longer forks the workspace or merges a
  diff back: it now writes DIRECTLY to the real parent workspace, exactly as the main
  agent does, and git is the rollback layer (ADR 0041, which supersedes the
  writable-subagent decision in ADR 0040). `agent.WithWritableChildEngine` is RETAINED
  (a read-write call still selects a separate Edit/Write-bearing engine); it now runs
  against the parent workspace with the main session's command runner, no forker, no
  merger. The dispatcher still runs a read-write call mutate-serial — `SubagentTool.
  MutatesParent` is now decoupled from any merger (true whenever the writable engine is
  wired and the call is `mode:"read-write"`). Composition no longer wires a forker or
  merger into the Subagent tool; the shared `tool.ForkMerger` remains for Parallel's
  single-branch auto-merge only. See `docs/adr/0041-direct-write-subagent.md`.

## [0.0.4] - 2026-06-21

### Added

- `tool.ForkMerger` — an OPTIONAL port (`Merge(ctx, forkRoot, parentWS) error`)
  by which a preserved winning fork's changes are merged BACK into the parent
  workspace. Additive interface in `engine/tool` (next to `WorkspaceForker`); no
  frozen domain type changes. See `docs/adr/0039-parallel-auto-merge.md`.
- `agent.WithAutoMerge(m tool.ForkMerger) ParallelOption` — wires a merger into
  the Parallel tool. When set AND a `join=first` or `join=judge` run has exactly
  one branch with a successful winner, the winner's diff is auto-merged into the
  parent workspace after the run. nil (the default for the option itself) keeps
  the no-auto-merge behaviour for that tool instance; composition wires a merger
  unconditionally (default-on — see `docs/adr/0039-parallel-auto-merge.md`).
  Multi-branch runs never auto-merge. The merge is a POST-RUN step, so
  `ParallelTool.ReadOnly()` stays `true`. On a conflict the tool returns an error
  naming the conflict + the preserved fork path; it never forces. The `forker.Merger`
  adapter runs `git diff --no-textconv` and refuses `.gitattributes`-touching
  patches (closes attacker-named `diff.*.textconv`/`filter.*.smudge` RCE from an
  untrusted fork `.git`). See `docs/adr/0039-parallel-auto-merge.md`.
- `agent.WithWritableChildEngine(e *Engine) SubagentOption`,
  `agent.WithWritableChildForker(f tool.WorkspaceForker) SubagentOption`, and
  `agent.WithSubagentAutoMerge(m tool.ForkMerger) SubagentOption` — wire the
  WRITABLE-subagent path (the `mode:"read-write"` Subagent arg). A read-write call
  runs the writable child engine (an Edit/Write-bearing explorer) in a force-copy
  fork; its working-tree diff is merged back into the parent workspace after the
  run via the injected merger (the SAME composition-owned, process-wide serialized
  `tool.ForkMerger` Parallel's `WithAutoMerge` uses). The merge is a POST-RUN step,
  so `SubagentTool.ReadOnly()` stays `true`. On a conflict the tool returns an error
  naming the preserved fork and does not force; nil options leave the writable path
  unwired (a `mode:"read-write"` arg then surfaces a model-addressable "not
  supported" error). `mode:"read-write"` is rejected with `background`/`agent` and
  composes with `fork`/`model`/`resume`/`output_schema`.
- `agent.(*SubagentTool).MutatesParent(call session.ToolCall) bool` and
  `agent.(*ParallelTool).MutatesParent(call session.ToolCall) bool` — implement an
  internal optional `parentMutatingCaller` seam the dispatcher consults: a `ReadOnly()`
  tool stays read-only for fan-out, but a CALL that will merge a fork diff back into
  the parent workspace (a `mode:"read-write"` Subagent, or a single-branch
  `join=first`/`judge` auto-merging Parallel) reports `true` and is excluded from the
  concurrent read batch (dispatch-serial), so its post-run merge never overlaps a
  sibling parent read. Returns `false` for read-only fan-out and for malformed args.

## [0.0.3] - 2026-06-21

### Hygiene

- `go.mod`: the `go` directive is now the minor version `go 1.26`, not the patch
  `go 1.26.4`. A library's `go` directive sets the language version it requires,
  and Go raises a consumer's own directive to match the highest one in its module
  graph — so a patch-level directive forces every consumer to a patch directive
  too. The engine uses no Go 1.26.4-specific language feature, so `go 1.26` is the
  correct floor. This unblocks consumers (e.g. Atrium) whose CI forbids a
  patch-level `go` directive. No public-API change. (A `toolchain` directive, if
  ever added, may stay patch-pinned — only the `go` language directive must be
  minor.)

## [0.0.2] - 2026-06-20

### Added

- `prompt`: `Builder` func type (`func(Config) Layered`) — the system-prompt
  assembly seam. `agent.Deps.PromptBuilder` (optional; nil → `prompt.Build`,
  byte-identical to v0.0.1) lets a host embedding the engine for a non-coding
  agent fully own the system prompt (role, tone, safety, tool inventory) with no
  coding-agent defaults and no `Available tools:` block. Only the MAIN loop's
  `buildRequest` routes through it; the compaction summarizer (`cascade.go`)
  builds its own `prompt.Layered` directly and is unaffected. (#127)

## [0.0.1] - 2026-06-19

This is the **initial baseline** (tagged `engine/v0.0.1`). The entries below record the establishment of
the module and its compatibility contract, not a change to a previously-published
surface.

### Added

- The engine became its own Go module, `github.com/stacklok/mecatl/engine`,
  importable independently of the host repo (monorepo via `go.work`). (#113,
  [ADR 0036](../docs/adr/0036-engine-module.md))
- A public API stability contract for the seven core packages: this CHANGELOG,
  [COMPATIBILITY.md](./COMPATIBILITY.md), the committed text snapshots under
  [`engine/api/`](./api/), and the `api-compat` freshness gate that fails CI on
  an unflagged change to the exported surface. (#114,
  [ADR 0037](../docs/adr/0037-engine-stability-contract.md))
- `engine/adapter/eventsource` reference fold (event-sourced `SessionStore.Load`
  rehydration): `Fold` reconstructs a `*session.Session` from a `port.EventLog`
  stream plus out-of-band creation metadata (`SessionMeta`), for hosts whose system
  of record is an append-only event log. It is an EXCLUDED reference adapter (no
  guarded-surface change) and ships with the documented reconstruction contract in
  [COMPATIBILITY.md](./COMPATIBILITY.md) (the only residual limitation is the
  provider-private `Reasoning`/`ProviderPhase`/`ItemID` replay fields). (#115,
  [ADR 0038](../docs/adr/0038-event-sourced-rehydration.md))
- `session`: `EvUserPrompt` event (+ `UserPromptPayload` + `Event.UserPrompt`). The
  durable event log now records the user-role messages the loop adds — the genuine
  client prompt and the harness-authored synthetic continuations (nudges/notices) —
  so an event-sourced fold reconstructs user-role turns (closing the "the log can't
  show what the user asked" gap, ADR 0027 row 11). It is LOG-ONLY: the relay appends
  it and skips it on the live client wire (the EvApproval/EvCompactionArchive
  precedent; wire `type` string passthrough, no proto enum). (#115,
  [ADR 0038](../docs/adr/0038-event-sourced-rehydration.md))

### Hygiene

- CI now runs `govulncheck` on the engine module (a reachable-vulnerability scan
  of its own dependency closure, separate from the root module's), and
  `.github/dependabot.yml` keeps the engine `go.mod` current — supply-chain
  hygiene for the engine library's consumers. No public-API change. (#118)
