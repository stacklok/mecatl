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

1. Add `session.AuxiliaryUsage`, a complete auxiliary ledger result containing purpose-keyed `TokenUsage` buckets, and an owned-copy `Merge` operation. A result can represent multiple purposes, providers, and models.
2. Break pre-v1 public engine APIs so every engine-backed auxiliary call returns `session.AuxiliaryUsage`: compaction, reflection, child-ask review, branch judging, guardrail checking, model routing, and the `Deps.SubagentModelRouter` seam. Existing custom implementations must explicitly return zero usage when they perform no model work.
3. Producers assign their own purpose and exact model attribution. Parent callers validate or remap purpose buckets but preserve a valid producer attribution. An LLM router reports its selected provider/model; Jev reports `jev/<classifier-model>` and remains a bounded decision backend, not a provider-registry entry.
4. The parent synchronously merges returned results while it holds its run/session ownership. The Service capability fence admits this mutation only while ownership remains valid; a report arriving after lease loss is dropped with bounded diagnostics. It is never reloaded, replayed, or merged by a former owner.
5. Router-budget accounting sums the complete `router` bucket regardless of backend. `Session.Usage`, normal result usage, team budgets, conversation history, and all non-router auxiliary buckets remain unchanged.

## Consequences

The accounting obligation is visible in every relevant Go API, without hidden provider wrappers or callbacks that outlive a call. This is an intentional pre-v1 breaking engine API change and requires API snapshots and a Changed/minor changelog entry.

Asynchronous server-owned title generation and recovery persistence retain their own ownership-fenced lifecycle; they may consume returned `AuxiliaryUsage`, but do not retain a stale parent-session callback.

## See also

- [ADR 0350](./0350-purpose-attributed-auxiliary-token-usage.md)
- [ADR 0352](./0352-jev-delegated-model-router.md)
- [Issue #1216](https://github.com/stacklok/mecatl/issues/1216)
