# Engine compatibility policy

The `github.com/stacklok/mecatl/engine` module is the importable core of mecatl
(ADR 0036). This document is its **public API stability contract**: what is
covered, how changes are versioned, and how the contract is mechanically
enforced (issue #114, [ADR 0037](../docs/adr/0037-engine-stability-contract.md)).

## The public surface

The contract covers the exported identifiers of these **seven core packages**:

| Package             | Role                                                      |
| ------------------- | -------------------------------------------------------- |
| `engine/session`    | the `Session` aggregate, value objects, event taxonomy   |
| `engine/governance` | permission `Effect`/`Scope`/`Rule` + `Evaluator`, hooks  |
| `engine/tool`       | `Tool`/`Catalog`, the `FileSystem`/`Workspace` + source ports |
| `engine/prompt`     | prompt assembly + discovery ports                        |
| `engine/port`       | the port interfaces the loop consumes                    |
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
  `search`, `memlease`, and the `*conformance` suites). These ship as offline
  test doubles and sane defaults, not as a stable API. **Note:** the conformance
  suites' real CONTRACT is the port interfaces in `engine/port` / `engine/tool`,
  which ARE guarded — the suites merely exercise it.
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
   It re-derives the exported surface of the seven packages and fails on any
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

## See also

- [ADR 0037 — engine stability contract](../docs/adr/0037-engine-stability-contract.md)
- [ADR 0036 — `engine/` is its own Go module](../docs/adr/0036-engine-module.md)
- [Project README](../README.md)
