---
id: 01-provider-translation
title: OpenAI Responses multipart visible-text projection
blocked_by: []
status: pending
attempt: 0
branch: ""
worktree: ""
issue: "738"
retries: 0
last_error: ""
accumulator: acc/openai-multiple-text-parts
---

# OpenAI Responses multipart visible-text projection

Implement ADR 0301's provider/openai translation: project every non-empty
`response.output_text.delta` through the existing `ChunkText` seam in recorded SSE
arrival order regardless of item, output, or content identity. Preserve the engine's
one-string, separator-free `Message.Text` assembly and deliberately discard those
provider identities at the adapter boundary.

Add recorded SSE fixtures and focused provider tests, an engine-level
ordered-concatenation proof, and an OpenAI adapter + engine + resilience end-to-end
proof for visible multipart failure. Keep phase, reasoning replay, function calls,
usage, terminal stop, and ADR 0239 semantic retry behavior unchanged. Document the
landed ADR 0301 behavior in `docs/architecture/providers.md`. Do not widen any port
or public API.

## Acceptance criteria

- AC1.1: A completed Responses stream whose non-empty `response.output_text.delta` events use distinct item, output, or content identities succeeds rather than reporting a multi-part-text error; it emits every delta in provider stream order through `ChunkText`.
  - verify: `TestADR_0301_DistinctIdentitiesEmitOrderedChunkText`
- AC1.2: The completed assistant turn contains the exact ordered concatenation of all visible text deltas, with no synthetic spaces, newlines, delimiters, dropped bytes, or text-part metadata exposed through `Message.Text`.
  - verify: `TestADR_0301_EngineConcatenatesDistinctPartDeltasWithoutSeparators`
- AC1.3: A multi-text-part turn retains existing independent behavior for opaque message phase, buffered reasoning replay items, function calls, usage, and terminal stop; interleaving those events does not reorder, duplicate, or turn visible text into reasoning/tool output.
  - verify: `TestADR_0301_InterleavedPhaseReasoningAndToolCallPreserveChunkSemantics`
- AC1.4: A distinct-multipart Responses stream that emits two meaningful visible deltas and then fails retryably is exercised through the OpenAI adapter, engine, and resilience wrapper: both deltas are observed in SSE arrival order, the visible commit suppresses a second provider attempt, the terminal result retains the retryable/visible failure semantics, and the incomplete assistant text is not persisted. A failure before visible text remains eligible for the existing precommit retry policy.
  - verify: `TestADR_0301_VisibleMultipartFailureIsTerminalWithoutReplayOrPersistence`

## Verification

- `cd provider/openai && GOWORK=off go test ./...`
- `task lint`
- `task test`
- `go run ./cmd/mecademo`
- `task docs`
