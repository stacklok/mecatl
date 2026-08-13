# ADR 0106 — Optional completed-trajectory learning seam

- Status: Accepted
- Date: 2026-08-13
- Scope: importable engine learning policy and host invocation
- Supersedes: none
- Superseded by: none

## Context

Automatic user-model review previously piggybacked on the generic Stop hook and launched a
detached composition goroutine. Embedders had no small engine API for observing a completed
run, while defining candidate, evidence, reflection, or promotion schemas before their real
consumers would freeze speculation into the public surface.

## Decision

Add the stdlib-and-session-only `engine/learning` package. Its closed `Mode` vocabulary is
`off < review < auto`, with `off` as the zero value. `Trajectory` is an owned snapshot of
neutral session values and a cloned message slice. `Observer` is a synchronous host callback.

Install mode and observer additively on `agent.Deps`. Invoke the observer exactly once after a
clean run has established `StateCompleted`; do not invoke it for awaiting, failed, or cancelled
runs. Observer errors are operational diagnostics and never alter the run result. The engine
owns no goroutine, persistence, scheduler, candidate queue, or promotion policy.

Standard composition maps the legacy direct-writing user-model reviewer to `auto`. `review`
remains explicitly inert until a real review queue is designed. Operator `learning.mode` sets
the completed-trajectory observation ceiling; admitted project settings may only lower it.
Project settings are ignored unless the shared `projectIngestionAdmitted` trust seam admits
project content. Consequently, `off` means no automatic completed-trajectory reflection or
review; it does not disable explicit memory tools or separately authorized maintenance.

`--user-model-consolidate-interval > 0` is that separate authorization: it starts the
process-wide dream consolidator when the user-model store and provider are available,
independently of the effective workspace `learning.mode`. The store is cross-project, so a
project learning ceiling must not disable its operator-configured maintenance schedule.

## Consequences

Embedders can observe trajectories using engine-only imports, and disabled deployments retain
the same prompt/provider behavior. Automatic review now blocks the completion boundary while
its observer runs; hosts that need asynchronous work must own that scheduling outside the
engine. Candidate and evidence schemas remain available for issue #509 to design around real
consumers.

## See also

- [Agent loop](../architecture/agent-loop.md)
- [Memory](../architecture/memory.md)
- [Production readiness](../design/PRODUCTION-READINESS.md)
