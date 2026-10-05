# AGENTS.md: Mecatl

Coding-agent instructions for this harness, built in Go 1.27. `CLAUDE.md` is a
symlink; edit this file, never the symlink. Use the [reader map](docs/READING.md) for
subsystem documentation, not a full-repository reading pass.

## Working conventions

- Find unfamiliar symbols with `Grep`, then bounded `Read` calls. Avoid parallel
  full-file reads and searching scratch worktrees or dependencies.
- Delegate with known paths and context. Omit `max_run_tokens`, `max_turns`,
  `max_tool_calls`, and `timeout_ms` unless the user/task requests a bound.
  Omit `authority` unless deliberately reducing it for a documented reason.
- Keep retained work notes and smoke-test artifacts under ignored `.scratch/`;
  orchestration state belongs in `.scratch/orchestrate/<slug>/`. Use `t.TempDir()`
  for disposable Go test files and `$TMPDIR` for Shell test files, not
  repo scratch or hard-coded `/tmp`: the managed Shell lease is reclaimed even
  if the test process is killed before its cleanup runs.
- Shell is POSIX `/bin/sh`. For `gh`, put multiline Markdown in a scratch file
  and use `--body-file`. Avoid Bash-only syntax, `eval`, and command substitution
  to construct Markdown.
- Stage explicit paths, never `git add -A`. End commits with `Co-Authored-By`.
  Humans alone merge PRs.

## Commands and verification

Build through the Taskfile; bare root `go build` leaves stray binaries.

```sh
task build              # binaries in bin/
task test               # complete offline suite, all modules and standalone proofs
task test:race          # complete suite with race detection
task lint               # root, engine, authn/oidc, and provider modules
task docs               # regenerate references and run strict documentation checks
task generate           # regenerate protobuf contracts and configuration reference
go run ./cmd/mecademo    # offline session smoke test
```

`engine/` is its own Go module. Root `go test ./...` does not cross module
boundaries. For a focused engine test:

```sh
cd engine && go test ./agent/ -run TestFullCycle
```

During iteration, run the smallest focused test that exercises the changed behavior,
including its direct integration boundary. Run `task test` once after an integrated
change set, not after every edit or worker attempt. Before a PR is ready,
`task lint && task test:race` must pass and the offline demo must show tool call,
permission ask/approval, and result. CI independently verifies the submitted branch;
it does not replace focused local verification or these final gates. Start timeout-bound
full gates with 600 seconds. `task e2e` uses live providers and costs money; it is not
an offline gate. See [Taskfile.yml](Taskfile.yml) for golden updates, benchmarks, and
other tasks.

Tests are offline and isolated from operator state. Prefer `mockllm`, `memfs`,
`memstore`, and existing conformance suites; real-adapter tests need explicit
fixtures. Apply `.claude/rules/test-isolation.md`, including composition helpers
and test-owned `UserModelDir` even when user-model features are disabled.

## Implementation boundaries

- Dependencies point inward. Core production code (`engine` domain, `agent`,
  and `team`) must not import adapters, host `internal`, generated contracts,
  `os`, SDKs, or gRPC. Core tests may use reference `engine/adapter/*`, never host
  `internal`. Keep wiring in `internal/app` and `cmd` mains. Preserve depguard,
  DAG, and standalone-module guards; use their existing narrow exceptions.
- `FileSystem`, `Workspace`, and `Environment` belong to `engine/tool`, not
  `engine/port` (cycle). `governance` stays session-free. Mutate `Session`
  through aggregate methods and preserve valid tool-call/result pairing.
- Keep `LLMRequest` provider-neutral and provider replay stateless. Re-derive
  provider/model-dependent dependencies through factories, not clone-and-swap.
- File writes preserve read-before-edit, exact matching, uniqueness, and CAS;
  new-file writes are create-only. Dispatch remains read-parallel/mutate-serial;
  direct-write children mutate the parent and must run behind the barrier.
- Permissions remain deny-dominant; configured Ask is never bypassed by posture.
  Preserve substitution-aware shell checks and root-aware project trust:
  posture alone does not trust a headless checkout.
- Preserve default secret scrubbing. Explicit operator grants can reach built-in
  main runners and direct-write children, but not hardened children/internal
  Git; harness credentials remain protected. Never inspect or disclose credential
  values. Scrubbing is not an OS sandbox.
- Placement is server-owned: exact `EnvironmentRef{Kind, ID, Revision}`, fail-closed
  reattachment, no public paths or cwd-based inference. No-FS children remain
  file-less. Skills expose logical assets, not extra workspace/execution roots.
- Use canonical governance fences for untrusted content and preserve child
  isolation. MCP is streaming-HTTP only; never spawn stdio MCP servers.
  Post-tool hooks cannot undo execution: enforce incoming-result checks through
  effective-payload rewriting, preserving recorded/streamed/model equality and
  UTF-8 repair. Keep secret-shaped headers byte-exact and out of projections.
- Use injected diagnostics, not default/package-level slog in engine/internal.
  Diagnostics, audit, and durable events have distinct contracts. Stamp event
  actors from verified caller context, not ownership; logs can contain user data.
- A model-dependent affordance needs a model-visible instruction and a test
  proving it reaches the appropriate system-prompt layer through the real factory.
  Do not pin arbitrary documentation prose in tests.
