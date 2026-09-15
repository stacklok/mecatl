# ADR 0342 — Gate runs on unresolved live context windows

- Status: Accepted
- Date: 2026-09-15
- Scope: live context-window discovery and server run admission.
- Supersedes: [ADR 0016](./0016-multi-provider.md), only for its acceptance of running before the initial live-model swap; its provider-neutral composition and resolve-at-use decisions remain in force.

## Context

A cold process seeds an operator-defined gateway model with no exact context window. Before live model discovery settled, the engine used its 128K unknown-model floor. Reopening a durable session whose real model window was much larger could therefore compact valid history before the live swap corrected the resolver. Compaction is persistent and lossy, so a later metadata correction cannot restore the active conversation.

## Decision

Composition owns a close-once live-refresh settlement signal. Server run admission asks composition to await the effective provider/model window after engine and mode resolution but before prompt recording, failed-step retry preparation, approval resumption, compaction, or inference. The entire wait and any recovery refresh share the request context and one ten-second operation bound; shutdown cancellation does not claim that discovery settled.

A positive global override, exact `models.context_windows` entry, live value, or catalog value bypasses the wait. Providers without live listing retain unknown-model compatibility. Successful non-empty-listing evidence is published atomically with the resolver metadata produced by that refresh; an earlier last-known-good/status update cannot admit a run before the matching metadata snapshot is visible. Once that publication succeeds, a selected passthrough model absent from the inventory or present without a window retains the settled 128K fallback. An unreachable, unauthorized, or empty initial listing with no known window instead returns the retryable `context_window_unavailable` error. The first rejection does not duplicate the startup request; a later run uses the existing bounded stale-model refresh path and retries admission.

The engine and provider-neutral ports remain unchanged. The server receives only an optional callback over provider/model strings, and both gRPC and HTTP map the sentinel to Unavailable/503.

## Consequences

Cold gateway sessions cannot lose history through speculative compaction. Admission can now fail before a run starts when required discovery is unavailable; operators restore discovery or configure the exact final provider/model window and retry. A rejected prompt is not recorded, retry metadata and pending approval remain available, and the provisional run registry is cleaned up. A session lease newly acquired during the attempt remains held under the pre-existing session-lifetime ownership contract; it is released only by session teardown or shutdown. Terminal-state reopening that occurred before the gate is not rolled back: required recovery may close dangling tool calls, but it does not remove genuine user or assistant history.

The settlement channel and retry bookkeeping are Build-owned process-local state. They are derived again after restart and hold no durable session content.

## See also

- [Context management and compaction](../architecture/context-and-compaction.md)
