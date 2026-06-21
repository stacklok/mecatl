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
