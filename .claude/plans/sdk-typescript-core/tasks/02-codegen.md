---
id: 02-codegen
title: protobuf-es codegen for mecatl.v1
blocked_by: [01-scaffold]
status: pending
branch: ""
worktree: ""
issue: "910"
retries: 0
last_error: ""
accumulator: sdk/10-architecture-adr
---

# Task brief

`task generate` must emit committed TypeScript for `mecatl.v1` alongside the
existing Go, per ADR 0278 Decision 2. Scenario 2.

**Hard constraints:**
- Second buf template (e.g. `buf.gen.ts.yaml`), NOT a plugin added to the
  existing Go `buf.gen.yaml`. Buf v2 `inputs`/`paths` is template-wide; the
  Go template must keep generating `mecatl/driver/v1`.
- TS template `inputs` scoped to `mecatl/v1` only. No TS for `mecatl/driver/v1`.
- TS template carries its **own** managed-mode block (managed-mode rewrites
  are stamped into generated `*_pb` descriptors).
- Output under `sdk/typescript/src/gen/`, exported as `./gen`.
- `contracts/gen/go` must stay byte-unchanged.
- Do not edit `harness.proto` or `internal/adapter/server/`.
- Pin `protoc-gen-es` remote plugin. `task generate` runs **both** `buf
  generate` invocations; operator surface stays one task.
- Freshness: regenerate then `git diff --exit-code` over both generated trees
  (CI step; the hybrid `sdk` job from Scenario 1 should grow this).
- biome already excludes `src/gen/` — keep it that way.
- If PR #903 (`sdk/09-listener-scoped-mcp`) merged and proto drifted, re-run
  `task generate` and note the order in the PR body.

Generated descriptors must construct a typed Connect-ES client with no
hand-written glue (AC2.3).

Branch off the stack tip (`sdk/11-scaffold` once merged) as `sdk/12-codegen`.
Do not push.

## Acceptance criteria

- AC2.1: `task generate` on a clean checkout reproduces the committed TS
  generated output byte-identically; a hand-edit or a stale commit fails the
  CI freshness step.
  - verify: demonstration — the CI freshness step (regenerate → `git diff
    --exit-code`).
- AC2.2: Generation is scoped to `mecatl.v1` — no TypeScript is emitted for
  `mecatl/driver/v1` or any other proto package.
  - verify: `sdk/typescript/test/gen.test.ts :: "no driver protocol output is generated"`
- AC2.3: `./gen` exports message types and service descriptors for
  `HarnessService` and `ScheduleService` that a Connect-ES client consumes
  directly — a generated descriptor constructs a typed client without
  hand-written glue.
  - verify: `sdk/typescript/test/gen.test.ts :: "generated descriptors construct a typed Connect client"`
- AC2.4: The committed Go generated output (`contracts/gen/go`) is
  byte-unchanged by the plugin addition — adding TS generation does not
  churn the Go tree.
  - verify: inspection — the codegen PR's diff under `contracts/gen/` is
    empty.
