# ADR 0037 — engine public-API stability contract

- Status: Accepted
- Date: 2026-06-19
- Scope: the engine module's public API surface (the seven core packages) and its breaking-change governance

## Context

[ADR 0036](./0036-engine-module.md) split `engine/` into its own Go module,
`github.com/stacklok/mecatl/engine`, making the harness core importable
independently of the host repo. With that done, Atrium (and any future external
consumer) now depends on the engine **live** — which means an accidental,
unnoticed change to an exported identifier of a core package is a silent breaking
change shipped to a downstream.

We needed two things: (1) a **written** compatibility policy — what is covered,
how it is versioned, how a break is governed — and (2) a **mechanical** gate that
fails a PR on an unflagged change to the exported API of the seven core packages
(`session`, `governance`, `tool`, `prompt`, `port`, `team`, `agent`), forcing a
deliberate, reviewed baseline update plus a CHANGELOG note.

The constraint that shaped the mechanism: the engine module's dependency closure
must stay tiny (ADR 0036) — an external `go get` of the engine must NOT drag in
`go/tools`. So the API-dumping tooling cannot live in the engine module. A second
constraint: no `engine/vX.Y.Z` tag exists yet, so any tool that diffs against a
published base version (gorelease, apidiff baselines) has nothing to diff
against on a PR.

Alternatives considered and rejected:

- **`apidiff` binary baselines** — committing `apidiff`'s opaque export-data blob
  and diffing against it. Rejected: the committed artifact is unreadable in a PR
  diff (a reviewer learns nothing from it), and the blob format is coupled to the
  Go toolchain version, so a Go bump churns it for non-API reasons.
- **`gorelease` as the PR gate** — rejected: `gorelease` compares the working
  tree against the latest released tag, and there is no `engine/vX.Y.Z` tag yet,
  so it cannot gate a PR today. It is, however, the right RELEASE-time advisory.
- **A hand-rolled AST walker** — rejected: it reinvents `go/types`, and getting
  type identity, embedding, and method-set resolution right by hand is exactly
  the work `go/types` already does correctly.

## Decision

Adopt a written policy plus a committed-text-snapshot freshness gate.

1. **Policy** — `engine/COMPATIBILITY.md` defines the covered surface (the seven
   core packages — `arch.CorePackages` in `engine/arch/surface.go`, the single
   source of truth the gate derives its guarded set from and asserts equal to),
   the exclusions (the `engine/adapter/*` reference adapters, `engine/arch`, and
   the whole root module), the v0.x versioning discipline, and the `engine/vX.Y.Z`
   tag grammar.

2. **Gate** — a root-module test (`internal/apicheck`) loads the seven packages
   with `go/packages`, renders each one's exported surface to a stable,
   human-readable text block via `go/types` object strings, and compares it to a
   committed baseline at `engine/api/<pkg>.txt`. It mirrors the repo's existing
   `-update` golden idiom (`task test:golden`): `task api:update` rewrites the
   baselines; `task api:check` (and `task test`, via `./...`) reads and diffs.
   An unflagged surface change fails with a readable diff and instructions. The
   rendering refines the bare object string in two ways: it **appends each const's
   exact VALUE** (so a wire-protocol enum change — an `EventType` or `StopReason`
   string — is caught, not just a type rename) and it **renders structs from their
   EXPORTED FIELDS ONLY** (so internal layout never ships in the baseline and
   private field churn does not force a spurious update). The dumper lives in the
   ROOT module so the engine `go.mod` stays free of `go/tools`; only the text
   baselines ship inside the engine module, so external consumers can read the
   surface they depend on. The guarded set is **derived from** `arch.CorePackages`
   (`engine/arch/surface.go`) and asserted equal to it, and a drift-guard keeps the
   `engine/api/*.txt` set exactly equal to the guarded-package set — so a new core
   package cannot escape the gate and a removed one is noticed.

3. **Release advisory** — `task api:release-check` runs `gorelease` (advisory,
   never the CI gate) at release time. Cutting the first `engine/v0.0.1` tag is
   deferred to a maintainer action.

Enforcement is wired as a dedicated `api-compat` CI job (a clear named signal,
even though the test also runs in the main test job via `./...`).

## Consequences

- Every intentional change to a core package's exported API now requires a
  reviewed baseline update (`task api:update` → commit `engine/api/*.txt`) and an
  `engine/CHANGELOG.md` entry classified per the policy. The reviewer sees the
  readable surface diff and the classification in the same PR — a break is never
  silent.
- The cost is a gate to keep green and a once-per-Go-MINOR baseline regen (the
  `go/types` object strings are stable across Go PATCH versions but a minor bump
  may reformat them; that regen is not an API change).
- The text baselines are human-readable and version-control-friendly, so the PR
  diff is the documentation of the change — unlike an opaque binary baseline.
- We are NOT yet protected by SemVer tooling (no tag exists); the text gate is
  the guard until `engine/v0.0.1` is cut, at which point `gorelease`/`apidiff`
  become a second, advisory layer.

## See also

- [ADR 0036 — `engine/` is its own Go module](./0036-engine-module.md)
- [engine/COMPATIBILITY.md](../../engine/COMPATIBILITY.md) — the living policy
- [engine/CHANGELOG.md](../../engine/CHANGELOG.md) — the change record
- [Project README](../../README.md)
