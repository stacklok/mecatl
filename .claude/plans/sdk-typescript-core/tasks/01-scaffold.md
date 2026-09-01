---
id: 01-scaffold
title: TypeScript SDK scaffold, toolchain, and CI
blocked_by: []
status: in-progress
branch: ""
worktree: ".scratch/plan-sdk-11-scaffold"
issue: "909"
retries: 0
last_error: ""
accumulator: sdk/10-architecture-adr
---

# Task brief

Create the in-repo `@stacklok/mecatl-sdk` package at `sdk/typescript/` and wire it
into the root Taskfile + CI. This is M1 Scenario 1. Do **not** generate proto,
do **not** implement Client/Session/Run, do **not** touch
`contracts/proto/mecatl/v1/harness.proto` or `internal/adapter/server/`.
Do **not** change `website/` (it stays on npm). No root-level `package.json`.

Read ADR 0279 (`docs/adr/0279-typescript-sdk-architecture.md`) Decision 4
before choosing tools. The settled toolchain: exactly-pinned **pnpm 11** via
`packageManager` (CI installs pnpm explicitly at that version — never assume
corepack); **TypeScript 6** for development, declarations compatible with TS
5.7+; **biome** for lint + format (not eslint/prettier); **vitest**; plain
**tsc** ESM-only unbundled build with declarations + source maps; **API
Extractor** — one report per entry point for `.` and `./node`; `./gen` is
deliberately not report-governed. Apache-2.0 license field + LICENSE file.
Subpath exports exactly `.` / `./node` / `./gen`. Node 24+.

**Work:**
- `sdk/typescript/`: package.json (`name` is `@stacklok/mecatl-sdk` per
  ADR 0279 Decision 6; exports map, `packageManager`, Apache-2.0),
  tsconfig, biome config (exclude generated `src/gen/` from formatting),
  vitest config, API Extractor configs + committed reports for `.` and
  `./node`, local `.gitignore` (node_modules, dist, API Extractor temp),
  LICENSE, README. Stub entry points so `.` and `./node` actually resolve
  (empty-but-valid modules are fine; `./gen` may be an empty committed
  placeholder directory or a tiny stub — Scenario 2 replaces it with real
  protobuf-es output).
- Root: `sdk:` Taskfile include (lint / typecheck / test / build / pack)
  using the existing `includes:` pattern (`site:` / `website/` is the
  precedent). Hybrid Node+Go `sdk` job in `.github/workflows/ci.yml`:
  install pnpm at the pinned version, cache on the SDK lockfile, set up Go
  + buf (Scenario 2/9 will need them; wiring the setup now is OK even if
  this job's first slice only runs install/lint/typecheck/test/build/pack
  + API Extractor verify). Do not perturb existing Go jobs.
- `.actrace.yml` (or the mechanism ac-trace v0.0.3 supports) so this plan's
  vitest `path :: "title"` verify lines actually resolve. If the resolver
  cannot express vitest titles, record the limitation in the plan's
  Deferred decisions — never a silently vacuous pass. Do **not** flip the
  plan status (orchestrator owns that).
- Short SDK section in `docs/architecture.md` (what the SDK is, the
  transport split, where the trees live). Skip dense IMPLEMENTATION-NOTES
  (later tasks). Run `task docs` if you touch markdown.

Branch off `sdk/10-architecture-adr` as `sdk/11-scaffold` (not
`plan-sdk-typescript-core/01-scaffold`). Do not push.

## Acceptance criteria

- AC1.1: A clean checkout with only pnpm 11 and Node 24 runs install
  (frozen lockfile), lint, typecheck, test, build, and pack through the root
  `task sdk:*` targets; no root-level package.json appears and `website/`
  is untouched.
  - verify: inspection — the CI job executes exactly these targets from a
    clean runner; `website/` diff is empty in the scaffold PR.
- AC1.2: The built package is ESM-only with declarations and source maps,
  and its exports map exposes exactly `.`, `./node`, and `./gen` — a CJS
  `require()` of the package fails, and no other subpath resolves.
  - verify: `sdk/typescript/test/package.test.ts :: "exports map exposes exactly ., ./node, ./gen"`
- AC1.3: `pnpm pack` produces a tarball containing the built output, license,
  and package metadata — and no test files, config, or generated-source
  duplicates outside the intended layout; the license field and file are
  Apache-2.0.
  - verify: `sdk/typescript/test/package.test.ts :: "packed tarball carries dist and license only"`
- AC1.4: The committed API Extractor reports — one for `.`, one for `./node`
  (API Extractor is single-entry-point, so one report per subpath; `./gen`
  is deliberately not report-governed, its surface being machine-generated
  and already gated by codegen freshness) — match the built public surface;
  an unreviewed public-API change fails the CI job with a report diff.
  - verify: demonstration — the CI job runs API Extractor in verify mode
    against the committed reports.
- AC1.5: A `.actrace.yml` resolver makes this plan's vitest `verify:` lines
  actually resolve (a deleted test title fails `task ac-trace-strict` once
  the plan is landed); if the resolver mechanism cannot express it, the
  limitation is recorded in this plan's Deferred decisions instead — never
  silently vacuous.
  - verify: demonstration — `task ac-trace` output shows vitest proofs
    resolving (or the recorded limitation).
- AC1.6: `task lint && task test` at the repo root remain green and
  byte-identical in behaviour for the Go modules — the SDK tree adds gates,
  it does not perturb existing ones.
  - verify: inspection — the scaffold PR touches no Go source; the full
    suite passes.
