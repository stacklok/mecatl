# ADR 0307 — Canonical durable token accounting and run-scoped budgets

- Status: Accepted
- Date: 2026-09-04
- Scope: durable session token accounting, model attribution, compatibility projections, and `MaxRunTokens` budget semantics
- Supersedes: none
- Superseded by: none

## Context

Session token usage has two distinct consumers. Operators and clients need durable accounting for all model work, grouped by its purpose and the server-selected model. The agent loop needs a bounded spend measure for one run. Treating both as one mutable `Session.Usage` value loses attribution and lets an internal helper affect an agent-visible budget or result.

The asynchronous title generator establishes the immediate second kind of spend. It uses a composition-selected provider/model and must persist its tokens without changing the chat's `EvResult.Usage` or consuming `MaxRunTokens`. Existing snapshots and public consumers, however, still read `Session.Usage`; compatibility must be preserved while canonical accounting moves to a durable ledger.

The current synthesis exception resets session usage so a budget-stopped lead can produce its report. Resetting durable lifetime accounting is the wrong mechanism: it discards the compatibility projection, is observable beyond that one run, and makes a budget boundary depend on mutation of the aggregate.

## Decision

Make durable `token_usage` the canonical session token ledger. It maps a closed usage kind to `TokenUsage`, which contains an aggregate `Usage` total and opaque server-produced provider/model attribution entries. Each `TokenUsage.Total` equals the sum of its model entries. The initial closed kinds are `main` and `session_title`; `main` and `main.*` are reserved. Migrate a legacy record whose model attribution is unavailable using the honest `unknown` key, never a current-model guess.

Attribute normal agent-loop model usage to `token_usage[main]` with the selected main model key. Attribute an asynchronous title call only to `token_usage[session_title]` with its composition-selected title model key. Title lifecycle attempts retain only identity, outcome, and time; they do not become a per-attempt usage ledger. The ledger, title lifecycle metadata, snapshots, session summaries, authorized projections, and event-sourced reconstruction round-trip together.

Retain `Session.Usage` and the existing snapshot `usage` field as deprecated compatibility mirrors of the lifetime `token_usage[main]` total. Dual-write them while consumers migrate. They are not canonical and no longer define the accounting model. Auxiliary usage, including `session_title`, never changes the compatibility mirror, `MaxRunTokens`, normal `EvResult.Usage`, or the agent conversation.

### Budget-baseline design

`Session.ResetUsage` is removed. The loop uses an internal, non-mutating per-run
budget baseline; there is no new externally callable reset API.

At the start of an ordinary run, the baseline is zero. The run's budget consumption is the cumulative lifetime main usage accrued above that baseline; normal `Result.Usage` remains the usage accrued by that run. Auxiliary kinds never affect either calculation.

Only the team lead's synthesis run and exactly one cleanup re-drive for a free-text
Subagent that stopped at `StopBudget` set their baseline to the current cumulative
`token_usage[main]` total. This gives each bounded deliverable phase its own
`MaxRunTokens` allowance without resetting or rewriting lifetime accounting. All other
ordinary calls, children, resumes, retries, and auxiliary operations use zero baseline.

The baseline is internal run state, not session state: it is not persisted, projected, exposed by a port, or made callable by clients. A restarted or subsequent run starts according to its own entry rule, preserving the durable lifetime ledger and the deprecated compatibility mirror.

## Consequences

Durable accounting becomes auditable by purpose and exact server-selected model without fabricated attribution. Existing consumers keep reading `Session.Usage` during migration, while new consumers can rely on the canonical ledger and its total invariant. Title spend is visible without being charged to chat work.

The budget gate no longer needs to alter a session-wide cumulative value to make synthesis possible. The implementation must keep the baseline scoped to one invocation and prove that reset semantics are unavailable externally. Consumers that need all model spend must migrate from `Session.Usage` to `token_usage`; the compatibility mirror intentionally excludes auxiliary kinds.

This ADR records tokens, not monetary cost. Pricing, currency conversion, historical rates, new usage kinds, and migration of other auxiliary model callers require their own decisions.

## See also

- [ADR 0306 — Asynchronous session-title generation](./0306-session-title-generation-and-auxiliary-usage.md)
- [ADR 0030 — Layered model-selection heuristics](./0030-model-selection-heuristics.md)
- [ADR 0027 — Cloud-native arc](./0027-cloud-native.md)
- [Session title generation and token usage acceptance plan](../acceptance/session-title-generation.md)
- [Issue #621](https://github.com/stacklok/mecatl/issues/621)
