# Authorization and hooks

**Review question:** Which exact action is authorized, and when can hook work be reused or retried?
This draft covers action binding, approval continuation, and hook evidence.
It introduces no policy decisions or release authority.

## Imported assumptions

[Execution gates](execution-and-recovery.md) distinguish a model's requested call, the effective authorized action retained as intent, and the final eligibility check before dispatch.
[Model input capture](model-continuation.md) retains permitted evidence of transformations so historical inputs can be reconstructed without rerunning hooks.
[Cancellation and settlement](cancellation-and-results.md) can close admission while normal processing of already-produced output continues separately.
Every control attempt is independently authorized; [retry identity](commands-and-lifecycle.md) lets a caller recover a prior command decision, not acquire permission.

## Settled rules and why

Authorization and execution must interpret the same effective arguments.
Preserve the shared semantics merged in [PR #2265](https://github.com/stacklok/mecatl/pull/2265): `governance.CheckToolArgs` rejects ambiguous duplicate or case-variant keys before policy, hooks, approval, or execution can interpret them differently.
`governance.DecodeToolArgs` and `session.ParseArgs` share decoding rules; built-in permission patterns select operands the tool actually uses, not ignored decoys.
This applies to fresh calls, trusted rewrites, and approval resumption.
It does not introduce one persisted decoded snapshot or remove resumption checks.
See the pinned [dispatch gate](https://github.com/stacklok/mecatl/blob/dbec473f9fde003fe0bd8fbb4e04fa7b222fa1e2/engine/agent/dispatch.go#L1206-L1224) and [execution decode](https://github.com/stacklok/mecatl/blob/dbec473f9fde003fe0bd8fbb4e04fa7b222fa1e2/engine/session/toolcall.go#L51-L71).

History retains the exact effective authorized call at the intent boundary, and execution consumes those values.
Inspection does not reconstruct what ran from today's hook configuration, schema interpretation, defaults, or resource extraction.
A trusted rewrite is a different effective action and must be validated and authorized before its intent commits.
Dispatch eligibility never silently substitutes newly evaluated arguments after commitment.

## Ordinary tool call and trusted rewrite

```text
[C completed model response + requested calls + required replay/bindings]
ordinary authorization
[C permitted hook definition/input anchor, if not already reconstructible]
PreToolUse
if arguments changed: authorize effective arguments
existing contextual action review and final authorization checks
[C effective authorized intent + completed pre-dispatch evidence]
dispatch under existing read-parallel/mutate-serial discipline
raw return held privately in memory
PostToolUse -> UTF-8 repair -> inbound review
[C permitted canonical result + safe processing/execution evidence]
eligible next model request, with its own committed logical input
```

A side-effecting hook can affect an integration before tool intent commits.
Absent intent proves no tool dispatch, not absence of hook effects.
A post-hook block does not undo a tool or introduce a result-release approver.
Existing hook/guardrail failure semantics remain in force.

## Execution approval at its actual phase

```text
authorization or existing review behavior requires an exact ask
[C pending ask + permitted reconstructible continuation]
ask becomes actionable to authorized clients
exact authorized verdict
[C approval decision; bundle with later facts only when already ready]
continue from recorded phase without repeating committed hook work
[C effective authorized intent, if not included above]
dispatch
```

An ordinary permission ask can precede `PreToolUse`; rewritten arguments can require another authorization decision.
A hook-originated ask retains its origin and continuation point rather than repeating the hook that caused it.
First valid authorized committed resolution wins across clients; later conflicting answers receive the authoritative resolution.
A reconstructible execution ask can survive unloading, but never overrides cancellation or remaining dispatch checks.

## Race: authority changes after intent acceptance

Intent acceptance is necessary, not an irrevocable instruction to execute.
At dispatch admission, check local cancellation, current ownership, exact effective intent binding, and known authority invalidation.
Preserve the existing contextual-review linearization rule: principal changes observed before admission can invalidate a decision; later changes do not retroactively revoke an in-flight action.
See [`admitPrincipalRevisions`](https://github.com/stacklok/mecatl/blob/e17d8394d/engine/agent/actionreview.go#L179-L195).
Cancellation still signals admitted work without rollback.

Bind decisions to effective action/arguments, environment, and relevant principal/policy context.
Respect each integration's freshness, expiry, version, and invalidation contract; do not dispatch using expired or known-invalid decisions.
Refresh remotely when that provider contract requires it, not through a blanket second evaluation after every commit.
Remote evaluation can be expensive and cannot eliminate every authorization-to-execution interval.
Never repeat side-effecting hooks merely to refresh authorization.
The result-to-model gate separately preserves normal disclosure processing; storage latency does not automatically rerun completed hooks or require blanket external reauthorization.

## Failure: hook effects with lost completion

A **hook invocation** is one logical execution anchored to a durable action, turn, or tool call, with stable registration, phase, and occurrence identity.
Hooks are at least once only under authorized admission/recovery and exact reconstructibility.
Before a recoverably retryable side-effecting invocation, retain its definition, input, and parameters or immutable references where permitted; an existing action anchor can suffice.
If the hook affects an integration and crashes before completion commits, authorized recovery may repeat it with the same identity only when those exact values remain available.
The integration can deduplicate where supported; exactly-once external effects are not promised.
Changed invocation means a new identity, not reinterpretation using current configuration.

Committed completion is reused without reinvocation.
Replay itself never retries hooks, and missing evidence creates no autonomous recovery loop or automatic first-cutover run resume.
Unreleased sensitive inputs and payload-bearing evidence remain memory-only; losing them ends retry eligibility rather than justifying capture or guessed input.
Hook retry never authorizes uncertain ordinary tool reexecution.
Required completed evidence commits before dependent model/tool execution, possibly batched with logical input or intent; no write per hook phase is mandated.

## Open decisions and detail

Decision-binding/freshness representations, stable hook invocation-ID delivery and occurrence encoding, registration/version/configuration capture, and failure-stage APIs remain open.
The safe evidence allowlist must prevent arbitrary error text or messages from quoting withheld payloads.
Existing argument-decoding and provider freshness contracts are constraints, not decisions to reopen.

The [hook execution contract](session-commit-history.md#hook-execution-and-recovery) owns exhaustive hook requirements; the [phase/gate table](session-commit-history-contracts.md#required-commit-gates-by-phase) owns dependent boundaries.
The [held-result contract](session-commit-history.md#memory-only-held-results) constrains evidence, and [acceptance cases](session-commit-history-contracts.md#acceptance-scenarios) require real integration checks rather than source-text assertions.
