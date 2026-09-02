---
id: 01-session-header-contract
title: Shared session header contract and provider projection
blocked_by: []
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/session-affinity-and-handoff
---

# Task brief

Define the one exact `X-Mecatl-Session-ID` contract in the engine port and migrate all three real provider modules to it. Start with the named legal-value and concurrent-isolation tests, then remove the private provider constants/predicates and prove initial attempts, retries, OpenAI fallback, nested runs, compaction, resume, and recovery all read the authoritative run context.

**Likely scope:** `engine/port/sessioncontext.go` and tests; `provider/openai/`, `provider/openaichat/`, `provider/anthropic/` request code and session-header tests; `engine/agent` run-context tests. Because provider modules import the engine as a separate module, the shared symbols must live in `engine/port`, not `internal/`. If exported symbols are required, update `engine/CHANGELOG.md` and the engine API snapshot in this task so its API gate can pass; Task 07 performs final assembled-surface reconciliation.

**Invariants:** `port.LLMRequest` remains unchanged and provider-neutral; providers omit absent/illegal optional metadata without failing inference; no transport ingress value is copied into provider context; every header option is per request, never shared-client mutation. Tests are offline (`httptest`, scripted SDK responses, `mockllm`) and the concurrency proof runs under `-race`.

## Acceptance criteria

- AC1.1: The shared contract accepts a non-empty legal HTTP field value byte-for-byte
  and rejects empty, control-bearing, newline-bearing, or otherwise illegal values;
  it never trims, encodes, truncates, or normalizes a session ID.
  - verify: `TestADR_0290_SessionHeaderLegalValue`

- AC1.2: OpenAI Responses, OpenAI Chat Completions, and Anthropic send the exact
  run-bound session ID on the initial request and every retry or provider-specific
  fallback, using per-request options rather than mutating a shared client.
  - verify: `TestADR_0290_ProviderSessionHeaderExact`

- AC1.3: When the run context has no session ID or carries an illegal value, each
  provider omits the field and continues inference, preserving ADR-0216's availability
  behavior.
  - verify: `TestADR_0290_ProviderSessionHeaderOptional`

- AC1.4: A child, member, compaction, resumed, or recovered run projects the
  authoritative session ID bound by that run; an ingress value cannot replace it and
  `port.LLMRequest` gains no routing field.
  - verify: `TestADR_0290_ProviderUsesAuthoritativeRunContext`

- AC1.5: A race-enabled concurrent test shares one provider client between two distinct
  sessions and interleaves their initial requests, retries, and provider-specific
  fallbacks. Every captured outbound request carries only its originating run-bound
  session ID; no per-request state leaks across sessions.
  - verify: `TestADR_0290_ProviderSessionHeaderConcurrentIsolationRace` (run with `-race`)
