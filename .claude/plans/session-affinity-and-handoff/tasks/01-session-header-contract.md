---
id: 01-session-header-contract
title: Shared session header contract and provider parity
blocked_by: []
status: in-progress
branch: ""
worktree: ".scratch/task-session-affinity-01"
issue: ""
retries: 0
last_error: ""
accumulator: acc/session-affinity-and-handoff
---

# Task brief

Define the canonical `X-Mecatl-Session-ID` name and legal-value contract in the engine port for root server/client consumers. Do not migrate the three real provider modules in this PR: their standalone `GOWORK=off` dependency remains released engine v0.12.0, and ADR 0093 forbids a local `replace`. Preserve ADR 0216 production behavior by retaining each provider's private header constant and validator. Start with one repository-owned exact legal/illegal vector fixture consumed by engine and provider test suites, plus provider parity tests, then prove initial attempts, retries, OpenAI fallback, nested runs, compaction, resume, and recovery all read the authoritative run context.

**Likely scope:** `engine/port/sessioncontext.go` and tests; `provider/openai/`, `provider/openaichat/`, `provider/anthropic/` session-header parity and concurrent-isolation tests; `engine/agent` run-context tests. Provider production request code and module dependencies are out of scope. The canonical exported symbols belong in `engine/port`, not `internal/`; provider tests consume that exact vector fixture to enforce parity rather than importing unavailable same-PR symbols. If exported symbols are added, update `engine/CHANGELOG.md` and the engine API snapshot in this task so its API gate can pass; Task 07 performs final assembled-surface reconciliation. A future provider release can raise its engine dependency and migrate production code.

**Invariants:** `port.LLMRequest` remains unchanged and provider-neutral; providers retain ADR 0216's byte behavior and omit absent/illegal optional metadata without failing inference; no transport ingress value is copied into provider context; every header option is per request, never shared-client mutation. Tests are offline (`httptest`, scripted SDK responses, `mockllm`) and the concurrency proof runs under `-race`.

## Acceptance criteria

- AC1.1: The canonical port contract accepts a non-empty legal HTTP field value
  byte-for-byte and rejects empty, control-bearing, newline-bearing, or otherwise
  illegal values; it never trims, encodes, truncates, or normalizes a session ID. The
  shared vector fixture proves each provider's retained private validator has the same
  decision.
  - verify: `TestADR_0290_SessionHeaderLegalValue` and provider parity vector tests

- AC1.2: OpenAI Responses, OpenAI Chat Completions, and Anthropic retain ADR-0216's
  private header constants and validators in this PR, yet send the exact run-bound
  session ID on the initial request and every retry or provider-specific fallback, using
  per-request options rather than mutating a shared client.
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
