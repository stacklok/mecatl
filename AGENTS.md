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

Tests are offline: prefer `mockllm`, `memfs`, `memstore`, and the conformance suites.
`task e2e` uses live providers and costs money.

## Module layout

The root is one Go module; `engine/` is its own. Root `go test ./...` does not cross
the module boundary; `task test` does. Focused engine test:
`cd engine && go test ./<pkg>/ -run <Test>`.

## Implementation boundaries

- Dependencies point inward (depguard): core `engine`, `agent`, and `team` code
  never imports adapters, host `internal`, generated contracts, `os`, SDKs, or gRPC.
- Permissions are deny-dominant; configured Ask is never bypassed by posture.
- Default secret scrubbing stays on. Never spawn stdio MCP servers.
- File writes preserve read-before-edit, exact matching, uniqueness, and CAS;
  new-file writes are create-only.
- Use injected `port.Diagnostics`, not package-level slog, in `engine/` and `internal/`.
- `LLMRequest` stays provider-neutral. `governance` stays session-free. Mutate
  `Session` only through its aggregate methods.

## Working conventions

- Work notes and smoke-test artifacts go under ignored `.scratch/`. Disposable
  test files use `t.TempDir()` (Go) or `$TMPDIR` (Shell), not `.scratch/` or `/tmp`.
- Shell is POSIX `sh`.
- Stage explicit paths, never `git add -A`. End commits with `Co-Authored-By`.
- Generated files change only via `task generate`, never by hand.
- Engine exported API changes need `task api:update`, the API snapshots, and a
  classified `engine/CHANGELOG.md` entry.
- A new resource that outlives a call adds a row to
  [`docs/architecture/resource-lifetimes.md`](docs/architecture/resource-lifetimes.md).
- Markdown changes run `task docs`. User-facing changes update the owning
  `user-docs/` page named in [`user-docs/_README.md`](user-docs/_README.md).

Path-scoped invariants live in `.claude/rules/*.md`.
