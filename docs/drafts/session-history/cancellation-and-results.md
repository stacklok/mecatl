# Cancellation and results

**Review question:** How can execution stop while produced output still awaits settlement or release?
This draft separates a run's execution lifecycle from each tool call's execution evidence, processing, and disclosure disposition.
It does not change guardrail decision or failure semantics.

## Imported assumptions

[Commit gates and final admission](execution-and-recovery.md) require durable prerequisites and a last eligibility check before invocation; committed intent or model input alone does not authorize invocation.
[Hooks and authorization](authorization-and-hooks.md) still govern normal output processing and bind decisions to the exact action or release ask.
The [command protocol](commands-and-lifecycle.md) preserves a cancellation's original resolved target on protected retry.
Private history is authoritative, but unreleased output and evidence containing its bytes stay only in process memory; [capture fidelity](model-continuation.md) cannot override that restriction.

## Settled rules and why

Cancellation has three distinct boundaries:

1. Promptly close local execution admission and signal admitted work, descendants, and extensions; do not wait for storage or ordinary batching delay.
2. Commit cancellation against the resolved target, with pending-source cancellations and an optional receipt, before confirmed acknowledgement.
3. Finish bounded shutdown and active result processing, or record honest unresolved obligations at the settlement deadline, before terminal settlement and successor admission.

Acknowledgement means accepted/signaled, not proven external cessation or rollback.
The deadline does not turn an unknown external effect into failure or safe retry.
If cancellation wins admission, no new model request or tool call starts; if admission wins first, cancellation signals that admitted work instead of retroactively preventing it.

Already-produced output follows the normal bounded post-tool hook, UTF-8 repair, inbound-review, and release pipeline under a settlement context separate from canceled execution.
Cancellation, timeout, or processing error cannot bypass required disclosure checks.
No new autonomous result-release approver or hook-criticality classification is introduced.
Settlement never submits another model request or dispatches dependent work in the canceled run.
Permitted accepted results can enter a later explicitly started run.

A **held result** is process-local output awaiting an exact release decision.
Its ask and safe evidence can be durable; its unreleased bytes and payload-bearing hook inputs, outputs, and traces cannot.
History, checkpoints, provider replay, diagnostics, audit, receipts, and client feeds are not alternative retention paths.
A terminal run can still await release; do not fabricate a final withheld result merely to terminate the run and later replace it.
Each tool call gets one canonical tool result, and each ask one winning disposition.

## Ordinary settlement with produced output

```text
order session-current cancel and resolve its actual target
promptly close execution admission; signal descendants/extensions
[C cancellation + all pending-source cancellations + optional receipt]
finish bounded normal processing of available output
[C permitted outcomes or honest safe unresolved dispositions]
report terminal run settlement without another model request
```

Ordinary session-current cancellation requires no client reload to discover a newer active run.
If normal steer promotion admitted a follow-up first, that follow-up is its target.
An explicitly run-qualified control never retargets a successor.
Canceling pending input from every source does not erase already committed operation outcomes or retroactively remove input already inserted into conversation.

## Release and process loss

```text
tool already executed; normal result pipeline requires release approval
exact output remains in a bounded process-local hold
[C pending release ask + safe evidence, excluding payload-bearing content]
exact release verdict or deny
[C approved canonical result OR synthetic withheld result + resolution]
```

Release can commit after run termination without reopening the run.
Clients choose whether to show post-cancel release controls; the server requires no new interrupting prompt.
Denial discards bytes without undoing effects.
Process loss instead closes the ask and commits an explicit unavailable/withheld result under the original tool-call identity.
Intact files or durable ask metadata cannot restore the hold.
Never recreate bytes through traces or tool reexecution; unavailable output does not prove execution failed.
An execution ask with durably reconstructible continuation is a different case and can survive unloading.

## Race: release versus a successor run

A successor waits for bounded predecessor shutdown/result settlement, not optional release approval.
At successor admission, invalidate remaining holds, expire their asks, discard bytes, and fence late release answers and old processing, ordered against any winning release decision.
Old processing cannot create a new actionable release ask after that boundary.
If release won while its hold was valid, retain that canonical result.
If invalidation won, reject stale advancement and retain the explicit withheld disposition.
Before successor model input, every required predecessor tool-call/result pairing must be complete.
A protected retry can return its old decision without applying it again.

## Open decisions and detail

**Whether predecessor result closure and successor admission must share one atomic commit remains open.**
Ordering hold invalidation against release and completing pairing before successor input are settled; bundling those result records with admission is not.
Concrete records/aggregates for independently terminal execution and pending disclosure, settlement/deadline ownership, deadline values, and late-completion fencing APIs also remain open.
No encoding choice may rewrite an already canonical result.

The [cancellation transition table](session-commit-history-contracts.md#coordinated-state-machine-with-cancellation) owns exhaustive guards and winning races.
The [settlement](session-commit-history.md#result-settlement-after-cancellation) and [memory-only hold](session-commit-history.md#memory-only-held-results) sections own detailed processing/disclosure requirements.
The [cancellation and failed-run contract](session-commit-history.md#user-cancellation-and-failed-runs) distinguishes user cancellation from shutdown/owner loss, which preserve pending input for explicit recovery.
[Acceptance cases](session-commit-history-contracts.md#acceptance-scenarios) cover blocked result hooks, terminal/pending-release states, deadlines, and admission races.
