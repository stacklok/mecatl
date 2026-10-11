# AGENTS.md: Mecatl

Coding-agent instructions for this harness, built in Go 1.27. `CLAUDE.md` is a
symlink; edit this file, never the symlink.

## Build and test

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

- While iterating, run the smallest focused test that exercises the change and its
  direct integration boundary. Run `task test` once per integrated change set, not
  after every edit.
- Before a PR is ready, `task lint && task test:race` must pass and the offline demo
  must show tool call, permission ask/approval, and result. CI verifies the branch
  independently; it does not replace these local gates. Give full gates a 600-second
  timeout.
- Tests are offline and isolated from operator state: prefer `mockllm`, `memfs`,
  `memstore`, and the conformance suites; real-adapter tests need explicit fixtures.
  `task e2e` uses live providers and costs money; it is not an offline gate.
- See [Taskfile.yml](Taskfile.yml) for golden updates, benchmarks, and other tasks.

## Module layout

The root is one Go module; `engine/` is its own. Root `go test ./...` does not cross
the module boundary; `task test` does. [`engine/`](engine/AGENTS.md),
[`internal/`](internal/AGENTS.md), and [`website/`](website/AGENTS.md) have their own
`AGENTS.md`. When the code doesn't explain how parts fit together, read the chapter
for that area from [`docs/READING.md`](docs/READING.md).

## Implementation boundaries

- Dependencies point inward (depguard): core `engine`, `agent`, and `team` code never
  imports adapters, host `internal`, generated contracts, `os`, SDKs, or gRPC. Core
  tests may use reference `engine/adapter/*`, never host `internal`. Wiring lives in
  `internal/app` and `cmd` mains. Keep depguard, DAG, and standalone-module guards and
  their existing narrow exceptions.
- File writes preserve read-before-edit, exact matching, uniqueness, and CAS;
  new-file writes are create-only. Dispatch stays read-parallel/mutate-serial;
  direct-write children mutate the parent and run behind the barrier.
- Permissions are deny-dominant; configured Ask is never bypassed by posture. Keep
  shell checks substitution-aware and project trust root-aware: posture alone does
  not trust a headless checkout.
- Default secret scrubbing stays on. Never inspect or disclose credential values.
  Scrubbing is not an OS sandbox.
- No-FS children stay file-less. Skills expose logical assets, not extra workspace or
  execution roots.
- Fence untrusted content with the canonical governance fences and keep child
  isolation. MCP is streaming-HTTP only; never spawn stdio MCP servers. Post-tool
  hooks cannot undo execution: enforce incoming-result checks by rewriting the
  effective payload, keeping recorded, streamed, and model-visible content equal and
  repairing UTF-8. Keep secret-shaped headers byte-exact and out of projections.
- Use injected `port.Diagnostics`, not default or package-level slog, in `engine/`
  and `internal/`. Diagnostics, audit, and durable events have distinct contracts;
  logs can contain user data.
- A model-dependent affordance needs a model-visible instruction and a test proving
  it reaches the right system-prompt layer through the real factory. Do not pin
  arbitrary documentation prose in tests.

## Working conventions

- Find unfamiliar symbols with `Grep`, then bounded `Read` calls; avoid parallel
  full-file reads and searching scratch worktrees or dependencies.
- When delegating, pass known paths and context. Omit `max_run_tokens`, `max_turns`,
  `max_tool_calls`, and `timeout_ms` unless the task needs a bound, and omit
  `authority` unless deliberately reducing it.
- Work notes and smoke-test artifacts go under ignored `.scratch/`. Disposable test
  files use `t.TempDir()` (Go) or `$TMPDIR` (Shell), not `.scratch/` or a hard-coded
  `/tmp`: the managed Shell lease is reclaimed even if the test process is killed.
- Shell is POSIX `/bin/sh`. For `gh`, put multiline Markdown in a scratch file and
  use `--body-file`; don't build Markdown with Bash-only syntax, `eval`, or command
  substitution.
- Stage explicit paths, never `git add -A`. End commits with `Co-Authored-By`.
  Humans alone merge PRs.
- Generated files change only via `task generate`, never by hand.
- `docs/drafts/` holds proposals and work records, not current behavior; don't rely
  on it for how Mecatl works.
- Markdown changes run `task docs`. User-facing changes update the owning
  `user-docs/` page named in [`user-docs/_README.md`](user-docs/_README.md).
  Changing behavior a `docs/` chapter describes updates that chapter.

Path-scoped invariants live in `.claude/rules/*.md`, including test isolation
(composition helpers and a test-owned `UserModelDir`).
