---
id: 05-typescript-raw-affinity
title: TypeScript raw session-binding helpers
blocked_by: [02-grpc-affinity-validation, 03-http-affinity-validation]
status: done
branch: "plan-session-affinity-and-handoff/05-typescript-raw-affinity"
worktree: ".scratch/task-session-affinity-05"
issue: ""
retries: 0
last_error: ""
accumulator: acc/session-affinity-and-handoff
---

# Task brief

Add the minimal public raw-client helper that binds one explicit session ID to a call on either Connect-ES gRPC or the hand-written HTTP/SSE transport, without changing generated protobuf types. Ensure the binding composes with caller headers and credentials and reaches unary and streaming calls.

**Likely scope:** `sdk/typescript/src/raw.ts`, `credentials.ts`, `http.ts`, public exports, raw transport tests, and the relevant API Extractor report if the task-local SDK API gate requires it.

**Invariants:** additive API only; callers that do not opt in retain byte-compatible behavior; exact header bytes are preserved; no protobuf/codegen edits; one header-name contract in the SDK rather than transport-specific spellings; no implicit authority. Use injected Connect router/fetch fakes and offline vitest TDD.

## Acceptance criteria

- AC4.4: Additive TypeScript raw helpers can bind an explicit session ID on either
  transport without changing generated protobuf code; calls without the helper retain
  their current behavior.
  - verify: `TestADR_0290_TypeScriptRawHelperCompatibility`
