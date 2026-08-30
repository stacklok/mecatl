# ADR 0244 — Count the full request and expose durable manual compaction

- Status: Accepted
- Date: 2026-08-29
- Scope: automatic compaction accounting, manual session compaction API, and mecatui `/compact`
- Supersedes: ADR 0025's `/compact` deferral only
- Superseded by:

## Context

A session could reach a provider's context limit even though the automatic trigger said
there was room. The trigger counted persisted conversation messages, but the provider
also receives the system prompt, ephemeral instruction fragments, and advertised tool
schemas. Typed tool results could also carry their useful payload in `ToolResult.Parts`
rather than the flattened text field. None of that was fully represented in the trigger.

Automatic compaction is intentionally a turn-boundary operation. Operators also need a
way to reduce a long stored session before the next turn, without sending a prompt and
hoping the model initiates a workflow.

## Decision

Estimate the complete model-visible `LLMRequest` before each automatic compaction check:
the rendered system prompt, ephemeral fragments plus persisted messages, and every
advertised tool's name, description, schema, and envelope overhead. Count typed message
parts and typed `ToolResult.Parts`; for a tool result, conservatively use the larger of
the flattened content estimate and the typed-parts estimate so alternate projections are
not double-counted. Both counters include replayed call/result IDs, provider phase,
reasoning-item IDs, and provider item IDs as payload; fixed constants cover framing only.
Inline image/audio bytes are counted at their base64-expanded size. Resource metadata is
counted conservatively because the shared routing projection may render it into
model-visible text. `engine/agent/loop.go` (`estimateRequestTokens`),
`engine/agent/tokencount.go` (`HeuristicTokenCounter`), and
`internal/adapter/tokenizer/tokenizer.go` (`Counter`) implement those rules.

Keep the trigger at 0.8 of the resolved context window. For an automatic cascade,
derive a request-local target from that same live window: the complete request should fit
0.6 of the actual window, so subtract the already-measured system, ephemeral-fragment,
and tool-definition overhead and pass the safely floored remainder as the persisted-history
budget. This prevents a configured 128k cascade target from becoming a no-op for a smaller
live model or context override. Manual compaction retains the configured compactor budget.
Only persisted conversation history is compactible. The system prompt, ephemeral fragments,
and tool definitions are irreducible request overhead, so compaction may be unable to bring
a request below the target. Automatic and manual paths admit a candidate only when it is
non-empty, changed, tool-pairing-valid, and strictly smaller by the persisted-history token
counter. After a successful automatic pass, rebuild only the message suffix and preserve
irreducible overhead byte-for-byte in `engine/agent/loop.go` (`maybeCompact`).

Expose manual compaction as a durable server operation, not as a prompt template. The
engine runs the configured compactor once regardless of the automatic threshold and
creates no conversation turn. `engine/agent/manual_compaction.go` (`CompactSession`)
accepts idle, completed, cancelled, and failed sessions; running and awaiting sessions
are rejected. Empty, identical, or non-reducing candidates are successful no-ops. Tool
pairing remains mandatory.

The service limits the operation to an owned main-chat session. It serializes with run
entry, rejects an in-process live run, acquires the configured mutation lease, then reloads
the authoritative snapshot before selecting the engine. Selector reconstruction currently
uses `engineAndEnvironmentFor`, which also reattaches the persisted environment: the factory
can rebuild the selected engine only against the session's validated live execution
namespace, and splitting those established run-entry responsibilities is outside this
operation. On change it saves the compacted snapshot before appending the existing
`EvCompaction` and `EvCompactionArchive` events, in that order. These unary-maintenance
appends are a deliberate extension of the relay-only run-event rule: no `Run` relay exists,
so the Service owns the transport-independent events and calls its existing `appendEvent`
choke point. A save failure fails the operation and emits no events. An event-log append
failure does not roll back the already-committed snapshot and is not retried by this
operation, so the durable log can lack the notice or archive even though the snapshot is
compacted. `internal/adapter/server/service.go` (`CompactSession`) owns these gates and
ordering; `engine/session/session.go` (`ReplaceHistoryAtBoundary`) owns the legal-state
and pairing boundary.

Publish the unary gRPC `CompactSession` RPC, the bodyless HTTP
`POST /v1/sessions/{id}/compact`, and the additive `manual_compaction` server capability
in `contracts/proto/mecatl/v1/harness.proto`. A successful response reports only whether
history changed. New mecatui clients expose `/compact` only when the capability and client
collaborator are present. It is a bare, no-argument, idle-only built-in and never reaches
the model. Older servers leave the additive capability false, so the command stays hidden;
a directly typed command is rejected locally, and direct use of the unknown RPC follows
the transport's normal unimplemented-method behavior.

## Consequences

The automatic trigger now tracks what the provider is about to receive instead of only
what the session stores. Large tool catalogs or instruction layers can therefore trigger
earlier, but their cost cannot be removed by history compaction.

Manual compaction can reduce a stored session before another model turn and preserves its
lifecycle state. The configured cascade may still make its own summarization model call,
so a manual pass can consume a compaction-slot model request even though it creates no
chat turn. No-op calls do not save or append events.

The snapshot-first order prevents an event from claiming a rewrite that failed to save.
It cannot make snapshot and event-log persistence atomic across independent backends;
append failure after save remains an explicit limitation. Lease loss cancels a cooperative
long cascade and is checked again immediately before `Save`, preventing a known-lost holder
from writing. This is not fencing: `SessionStore.Save` accepts no lease token, so loss can
still race after that check and before/during the save. Closing that residual requires a
future token-aware CAS storage contract, not a claim that process-local cancellation is
race-free.

## See also

- [Context management and compaction](../architecture/context-and-compaction.md)
- [The API surface](../architecture/api-surface.md)
- [mecatui](../tui.md)
- [ADR 0012](./0012-compaction.md)
- [ADR 0025](./0025-ux-discoverability.md)
