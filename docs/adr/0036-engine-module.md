# ADR 0036 — `engine/` is its own Go module (monorepo via `go.work`)

- Status: Accepted
- Date: 2026-06-19
- Scope: Repository module structure — the importable core (`engine/`) and its dependency closure; build/lint/test tooling and CI.
- Supersedes: —
- Superseded by: [ADR 0330](./0330-isolated-redis-follow-capacity.md) (Go compatibility floor only)

## Context

`engine/` is the importable core: the domain packages, the port interfaces, the
agent loop, and the in-tree reference adapters (`engine/adapter/*`). It is
designed to be consumed as a library by external projects — the immediate driver
is the downstream-consumer convergence, where the downstream consumer wants to import `engine/agent` and the
domain types directly.

Until now the whole repository was a SINGLE Go module
(`github.com/stacklok/mecatl`). That module's `go.mod` carries mecatl's full
require cone: the OpenAI and Anthropic SDKs, gRPC, the Charm/Bubble Tea TUI
stack, Kubernetes client-go, ToolHive, OpenTelemetry, and a long tail of
indirect dependencies (~250 lines of `require`). Go's Minimal Version Selection
(MVS) is computed per-CONSUMING-module: any project that imported
`github.com/stacklok/mecatl/engine/agent` would pull the ENTIRE mecatl `go.mod`
into its own build graph and SBOM — even though the engine code touches none of
those heavy dependencies. That is a real cost to a consumer: a bloated dependency
graph, a noisier vulnerability surface (govulncheck/dependabot over deps the
engine never links), and licence-audit weight for code that never runs.

The engine's ACTUAL external dependency closure is tiny and was verified before
this change:

- `golang.org/x/sync` — direct (`engine/agent` uses `errgroup`).
- `github.com/bmatcuk/doublestar/v4` — direct, via `engine/adapter/memfs`.
- `github.com/robfig/cron/v3` — direct, via `engine/adapter/cronparse` (the
  scheduled-tasks parser, ADR 0059).
- `go.yaml.in/yaml/v3` — direct, via `engine/adapter/agentfs` /
  `engine/adapter/skillfs` (the agent-def and SKILL.md frontmatter parsers,
  #328).
- `go.uber.org/goleak` — test-only, via the engine's leak-check `TestMain`.

No `engine/...` package imports a root non-engine package, an LLM SDK, gRPC, or
anything outside the `engine/` subtree. This is not an aspiration — it is
machine-enforced two ways that predate this ADR: the per-file depguard allowlist
in `.golangci.yml`, and the whole-graph DAG + self-containment test in
`engine/arch/layering_test.go`. The carve was always the intended end state of
that layering discipline.

The constraint that shaped the decision: the repo must STAY a monorepo. The root
module (`internal/app`, the `cmd/` mains, the heavy adapters) consumes the engine
and is developed in lockstep with it; a one-PR-per-module-bump workflow across
two separate repositories would be friction with no upside while both move
together.

## Decision

Split `engine/` into its own Go module, kept in the same repository as a monorepo
via a committed Go workspace.

- **`engine/go.mod`** declares module `github.com/stacklok/mecatl/engine`, `go`
  directive `1.26.3` (matched EXACTLY to the root), and exactly the requires the
  engine closure needs: `doublestar/v4`, `robfig/cron/v3`, `go.yaml.in/yaml/v3`,
  `x/sync`, `goleak` (plus goleak's small test-only transitive set —
  testify/go-spew/go-difflib/yaml.v3). `engine/go.sum` is correspondingly
  tiny. The versions are pinned to the root's so the two graphs agree under the
  workspace.
- **`go.work`** is COMMITTED at the repo root (`use ./` + `use ./engine`). The
  in-repo workflow resolves the engine require via the workspace, so a developer
  editing both modules sees one coherent build.
- The root `go.mod` gains `require github.com/stacklok/mecatl/engine v0.0.0-...`
  + `replace github.com/stacklok/mecatl/engine => ./engine`. The replace makes
  the root build against the in-tree engine; the placeholder require version is
  never resolved over the network. The root require cone is otherwise UNCHANGED —
  the win is entirely on the CONSUMER's graph, not the root's.
- **Linting stays unified.** The engine has NO engine-local `.golangci.yml` and
  no symlink to one; it is linted with the SHARED root config
  (`cd engine && golangci-lint run --config ../.golangci.yml ...`). The depguard
  allowlists, which key off the `github.com/stacklok/mecatl/engine/...` import
  paths as strings, keep working across the module boundary unchanged.
- **A dedicated hygiene gate proves the closure.** `task test:engine-standalone`
  (and a CI `engine-standalone` job) run `cd engine && GOWORK=off go build ./...`
  and `... go test ./...`. With the workspace OFF, the engine resolves against
  its OWN `go.mod`/`go.sum` alone — exactly the view an external
  `go get github.com/stacklok/mecatl/engine` consumer gets. A green run is proof
  the tiny closure is complete and the engine drags in nothing from the host
  repo.
- The module boundary is now a THIRD enforcement layer for the inward-only
  layering rule, alongside the depguard allowlist and the DAG test: a stray
  engine→host-repo import would break the standalone build outright.

The engine module is RELEASED under submodule tags of the form `engine/vX.Y.Z`
(Go's required convention for a module in a subdirectory), separate from the root
`v*` container-image tags. The stability/breaking-change contract and the
apidiff CI gate over those tags are issue #114 — out of scope here (see
[ADR 0037](./0037-engine-stability-contract.md)).

## Consequences

- **Easier for consumers.** An external project importing `engine/agent` now
  resolves only the engine's small closure (`doublestar`, `robfig/cron`,
  `go.yaml.in/yaml/v3`, `x/sync`, `goleak` + goleak's test transitive), not
  mecatl's ~250-line require cone. Their SBOM, vulnerability surface, and
  licence-audit weight shrink to what the engine actually links. This is the
  whole point of the change — it unblocks the downstream-consumer convergence cleanly.
- **Two `go.mod` files to keep tidy.** `task tidy` now tidies the root, runs
  `go work sync`, then runs `cd engine && GOWORK=off go mod tidy` (in that order —
  the sync can drop `/go.mod` hash lines from the engine `go.sum` that the
  standalone tidy restores). `task lint`, `task test`, and `task build` each gain
  an engine pass. The cost is real but bounded and fully scripted.
- **CI gains an `engine-standalone` job** and every Go-building job's
  `cache-dependency-path` now lists both `go.sum` and `engine/go.sum`. A drift
  between the two modules' shared versions, or an accidental engine→host import,
  fails CI deterministically.
- **`go work sync` aligns shared transitive versions across the two modules.**
  The engine's goleak-test transitive (testify et al.) is pulled UP to the root's
  versions under the workspace. That keeps the two graphs consistent; the
  standalone gate confirms the engine still builds and tests at those versions in
  isolation.
- **A `go.work.sum` IS generated and committed.** It holds the handful of
  host-transitive hashes the per-member `go.sum` files don't cover — the
  workspace-MVS transitive closure the root module's own `go.sum` doesn't pin
  (currently `lestrrat-go/jwx` v1, `lestrrat-go/option` v1, `pelletier/go-toml`
  v1, and `google.golang.org/genproto`). The build regenerates it identically, so
  it is genuine, stable across builds, and committed alongside the member `go.sum`
  files. If a future workspace member adds a hash the members don't cover,
  `go work sync` extends this file and the change should be committed.
- **A consumer who vendors or builds without the workspace is unaffected by the
  `replace`.** The `replace => ./engine` directive only applies to the root
  module's own builds; a downstream `require github.com/stacklok/mecatl/engine
  vX.Y.Z` resolves the published submodule tag normally.

## See also

- [Architecture guide](../architecture.md) — the layers and the importable core.
- [Usage & operator guide](../usage.md) — the workspace build note and the
  "consuming engine as a library" subsection.
- `engine/arch/layering_test.go` — the whole-graph self-containment guard the
  carve relies on.
- The documentation lifecycle convention in
  [ADR 0002](./0002-documentation-lifecycle.md) and the design-record
  consolidation in
  [ADR 0003](./0003-consolidate-design-records-as-adrs.md).
