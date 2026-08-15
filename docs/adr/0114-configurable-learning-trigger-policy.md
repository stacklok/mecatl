# ADR 0114 — Configurable learning-trigger policy

- Status: Accepted
- Date: 2026-08-15
- Scope: automatic completed-trajectory reflection admission and process-local budgets
- Supersedes: none
- Superseded by: none

## Context

Signal presence alone admitted too many low-value completed trajectories and provided no
operator control over sensitivity or automatic token spend. Admission also needed a hard,
provenance-safe path for a principal's current explicit remember request without letting
assistant, tool, web, MCP, repository, or historical text manufacture that authority.
Cluster-global accounting would require a distributed ledger and transaction boundary that
the existing storage-neutral reflection coordinator does not have.

## Decision

Use a pure `engine/learning` admission policy before the existing reflection coordinator.
The standard threshold policy scores only current-run evidence: repeated correction and a
trusted host contradiction weigh 5, failure recovery 4, a repeated stable tool sequence 3,
and substantial success 2. Four model turns, five successful tool calls, and 12,000 run
tokens each add one only after a base signal exists. Conservative, balanced, and eager
thresholds are 6, 4, and 3; balanced is the default.

A genuine principal-authored explicit remember or learn-procedure request in the verified
current prompt is hard admission. Caller-supplied explicit signal kinds carry no authority;
only deterministic detection over the correctly reindexed current-run slice can establish
hard intent. Every built-in multi-message detector likewise requires its complete pattern in
that slice, so historical/current boundary matches fail closed. It bypasses score, weighted
cooldown, and the deprecated interval downsampler, but not count/token budgets or coordinator
capacity. Weighted
admission accepts only a benign main-session `end_turn`; hard admission additionally accepts
max-turn, max-tool-call, and run-budget stops. Invalid or compacted-away current spans fail
closed. Authenticated explicit `/reflect` uses host-requested provenance and bypasses all
automatic admission state, while remaining subject to coordinator, provider, timeout,
ownership, and staging policy.

Wrap the coordinator with process-local one-hour sliding count and reserved-token budgets,
per-principal limits, a ten-minute weighted cooldown, a bounded 1024-entry cooldown map,
in-flight joins, and a 24-hour/1024-entry completed-digest LRU. Reserve the selected
reflection model token counter's estimate of the exact bounded provider request (system
prompt, signal projection, framing instructions, and message envelope) plus its 4096-token
output cap only after queue capacity succeeds and before provider work. The Build-owned
coordinator is lazy but exists whenever explicit reflection capability is configured,
including effective startup mode `off`; this keeps `/reflect` on the same bounded path and
lets a startup project's tighten-only `off` coexist with automatic observation on another
operator-permitted root. Failures, timeouts, and abstentions consume reservations. Restart resets this state by
design. `Close` rejects new work and cancels and joins workers; there is no startup or
shutdown catch-up sweep.

Expose strict operator `learning.sensitivity` and `learning.automatic` settings. Projects may
only lower mode and sensitivity; their automatic subtree is ignored. Zero maxima disable
automatic reflection under that bound. Budgets are per process, so N replicas can spend up
to N times the configured aggregate.

## Consequences

Automatic token spend is bounded and low-value runs make no provider call. Explicit
principal intent remains responsive without becoming unmetered. Operators running multiple
replicas must divide limits themselves or accept replica multiplication; no durable or
distributed rate ledger is introduced. A restart intentionally grants a fresh process-local
window, while durable proposal idempotency continues to prevent duplicate durable effects.

## See also

- [Architecture](../architecture.md#evidence-backed-reflection)
- [Configuration](../usage/configuration.md)
- [Cloud-native resource inventory](./0027-cloud-native.md)
- [Staged proposals](./0109-staged-learning-proposals.md)
