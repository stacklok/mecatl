# ADR 0354 — Returned auxiliary-usage results

- Status: Proposed
- Date: 2026-09-23
- Scope: engine auxiliary-model call result contracts and ownership-safe accounting
- Supersedes: ADR 0350 decision 4
- Superseded by: none

## Context

ADR 0350 requires purpose-attributed accounting for auxiliary model calls. Its context-carried reporter implementation lets provider wrappers retain a parent session mutation path beyond the helper call. That obscures accounting from public helper contracts and can race run cancellation or lease loss.

The Jev delegated-router backend (ADR 0352) is not an `LLMProvider`. It returns token usage with its own classifier identity, so a parent session cannot safely manufacture provider/model attribution from its own selected chat provider.

## Decision

1. Add `session.ProviderModelID`, the opaque provider/model identity selected by composition for one auxiliary call, and `session.AuxiliaryUsage`, a complete auxiliary ledger result containing purpose-keyed `TokenUsage` buckets, with an owned-copy `Merge` operation. `ProviderModelID` carries no selector/default, context-window, reasoning-effort, provider-instance, or credential semantics. A result can represent multiple purposes, providers, and models.
2. Break pre-v1 public engine APIs so every engine-backed auxiliary call returns `session.AuxiliaryUsage`: compaction returns `(compacted, summary, usage, err)`; reflection returns `(outcome, usage, err)`; child-ask review returns `(review, usage, err)`; branch judging returns `(winner, rationale, usage, err)`; and guardrail checking returns `(text, usage, err)`. Model routing carries the complete value in `ModelRouteResult.Usage`, replacing `session.Usage`; the `Deps.SubagentModelRouter` seam follows that result. Existing custom implementations explicitly return zero usage when they perform no model work.
3. Producers assign their own purpose and exact `ProviderModelID` attribution. Parent callers validate or remap purpose buckets but preserve valid producer attribution. An LLM router reports its selected provider/model; Jev reports `ProviderID: "jev"` and its configured classifier as `ModelID`, while remaining outside the provider registry.
4. A current Service owner synchronously merges returned results only through an already-valid run/session capability. `port.HookRunner.Run` returns `port.HookResult{Outcome, AuxiliaryUsage}`; a model-hook runner merges inner and checker usage before returning, and the owning Engine validates/remaps and records that result. Permission evaluation returns `port.PermissionResult{Decision, AuxiliaryUsage}` so the composition-owned escape-policy checker has the same explicit return path without making governance session-aware. No hook or policy receives a parent-session callback it could retain. A detached queued/recovery or post-lease-loss report is dropped with bounded diagnostics; it is never made durable by reacquiring a lease, loading/reloading, replaying, or persisting a source session solely for accounting, and never mutates a stale pointer.
5. Router-budget accounting sums the complete `router` bucket regardless of backend. `Session.Usage`, normal result usage, team budgets, conversation history, and all non-router auxiliary buckets remain unchanged.

## Consequences

The accounting obligation is visible in every relevant Go API, without hidden provider wrappers or callbacks that outlive a call. This is an intentional pre-v1 breaking engine API change and requires API snapshots and a Changed/minor changelog entry.

Asynchronous server-owned title generation and recovery persistence retain their own ownership-fenced lifecycle; they may consume returned `AuxiliaryUsage`, but do not retain a stale parent-session callback.

## See also

- [ADR 0350](./0350-purpose-attributed-auxiliary-token-usage.md)
- [ADR 0352](./0352-jev-delegated-model-router.md)
- [Issue #1216](https://github.com/stacklok/mecatl/issues/1216)
