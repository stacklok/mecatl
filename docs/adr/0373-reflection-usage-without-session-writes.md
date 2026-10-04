# ADR 0373 — Reflection usage without source-session writes

- Status: Proposed
- Date: 2026-10-03
- Scope: evidence-reflection token usage and source-session mutation
- Supersedes: ADR 0350's reflection session-accounting decision; ADR 0354 decision 4 and ADR 0371 decision 6 for reflection only
- Superseded by: none

## Context

Evidence reflection reports provider/model-attributed usage, including usage observed before a provider error or cancellation. Persisting it on the source session adds a second load and a session mutation lock held across explicit reflection, or a post-observer save on automatic inline completion. Detached and recovered reflections have no session mutation ownership. Accounting should not extend the source session's write-critical section or create a new one.

## Decision

Reflection continues returning `session.AuxiliaryUsage` from provider calls and explicit service reflection, retaining partial usage on failure and aggregating reported usage from each physical retry once. The caller does not record reflection usage in `Session.tokenUsage`, regardless of whether a live run or local session lease exists. Explicit reflection still performs its original single authorized source-session load to select and validate reflection work. No reflection path acquires a session mutation lock or lease, reloads a session, or saves a snapshot solely to account for usage. Existing run and lease behavior for other purposes is unchanged.

At each automatic inline, explicit, detached, or recovery result boundary, non-empty returned usage is discarded after one bounded structured diagnostic. Diagnostics carry no raw reflection evidence, model output, credentials, or provider errors; they are not a durable accounting record. A future durable owner must be designed separately in [issue #2069](https://github.com/stacklok/mecatl/issues/2069).

## Consequences

Session and session-summary `token_usage` no longer receive new reflection usage. Previously stored reflection buckets remain readable as ordinary recognized/opaque ledger data; there is no migration or deletion. Other auxiliary kinds retain their current recording and projection semantics.

## See also

- [ADR 0350](./0350-purpose-attributed-auxiliary-token-usage.md)
- [ADR 0354](./0354-returned-auxiliary-usage-results.md)
- [ADR 0371](./0371-contextual-guardrail-usage-accounting.md)
- [Auxiliary usage acceptance plan](../acceptance/auxiliary-token-usage.md)
