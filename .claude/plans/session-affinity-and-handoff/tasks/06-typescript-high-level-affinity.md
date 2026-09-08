---
id: 06-typescript-high-level-affinity
title: TypeScript Session and Run automatic affinity
blocked_by: [05-typescript-raw-affinity]
status: done
branch: "plan-session-affinity-and-handoff/06-typescript-high-level-affinity"
worktree: ".scratch/task-session-affinity-06"
issue: ""
retries: 1
last_error: ""
accumulator: acc/session-affinity-and-handoff
---

# Task brief

Thread automatic affinity through the ergonomic TypeScript `Session`, owned `Run`, and attached-run paths. Every high-level operation that already owns a session identity must invoke the raw seam with that exact binding, including unary lifecycle calls, prompt/retry streams, approval/cancel/steer controls, replay, live/watch attachment, and HTTP/SSE equivalents supported by the API.

**Likely scope:** `sdk/typescript/src/client.ts`, `run.ts`, `http.ts`, attachment/watch modules and tests, public exports, and task-local API reports when required by `sdk:api:check`.

**Invariants:** `Session.id` is the sole high-level affinity source; run IDs never replace it; controls remain bound to the run's owning session; generated protobufs remain untouched; both transports share the raw helper rather than duplicating header mechanics. Use injected transports/fetch fakes, strict TDD, and all SDK lint/typecheck/test/e2e gates offline.

## Acceptance criteria

- AC4.3: The TypeScript SDK high-level `Session`, owned `Run`, and attached-run APIs
  automatically propagate the exact session ID on every session-bound unary,
  server-stream, HTTP/SSE, prompt, retry, approval, cancel, steer, watch, and replay
  operation supported by that API.
  - verify: `TestSessionAffinityAndHandoff_Scenario4_TypeScriptHighLevelPropagation`
