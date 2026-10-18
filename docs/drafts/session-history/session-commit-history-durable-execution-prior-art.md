# Durable execution and recovery: prior art for session history

**Status:** Research companion and recommendations, not an approved architecture or dependency selection.
**Scope:** Established execution/recovery models, workflow runtimes, and durable-agent integrations relevant to the committed-session-history contract.
**Research date:** October 18, 2026.

## Findings

The closest established model is **durable orchestration backed by an authoritative history**, with explicit persistence barriers before dependent execution.
The literature on log-based recovery supplies more precise explanations for those barriers than an analogy to a DAG or database WAL alone.
Nothing in this survey establishes a need for a new general distributed-execution model.

Temporal is the strongest practical reference for the execution protocol, particularly because it supports single-attempt activities as well as retryable activities.
Restate is a strong reference for log-first acceptance, journal acknowledgements, and ownership fencing.
Their existing agent integrations bring the comparison closer to harnesses than generic workflow examples do.
Neither observation selects a runtime dependency for Mecatl or proves a stock integration satisfies the contract.

Three distinctions control the comparison:

- **Reconstruction versus re-execution:** rebuilding committed orchestration state is different from invoking a tool again.
- **Retry policy versus crash recovery:** disabling retries of returned errors does not necessarily stop an unfinished step from running again after process loss.
- **Durable control versus volatile payload:** an approval ask can be durable while its sensitive, unreleased output remains deliberately memory-only.

The supported conclusion is narrower than either "adopt an agent wrapper unchanged" or "build a new recovery paradigm": apply established recovery models, then specify and qualify the harness's authorization, uncertainty, disclosure, and cancellation protocol.

## Evidence and scope

This is a focused search for an execution model to adopt, not another general harness survey.
The [existing prior-art artifact](session-commit-history-prior-art.md) covers pinned OpenCode, OpenHands, Codex, Pi, ActiveGraph, and LangGraph implementations, client reload, extensions, and active-tree handoff.
This artifact complements it with recovery literature and durable-execution systems.

Evidence distinguishes **documented** behavior, **source-traced** behavior, and **inference** about applicability.
The research inspected five full literature sources, first-party runtime documentation, and selected SDK/integration code and tests.
No upstream runtime, crash experiment, cancellation race, or adoption prototype was executed.
A missing verified guarantee is an open qualification question, not a demonstrated upstream defect.

| Source trace | Revision or evidence boundary |
| --- | --- |
| Temporal AI integrations | `78e845a9e379ac18b4b69593d62b5dc3873700ab`; OpenAI Agents and Deep Agents integration documentation and selected source |
| Restate SDK shared core | `9ce1d8e8b136e95a89d19d77b00579e51479c401`; incomplete-run replay and completion-acknowledgement tests |
| Restate documentation source | `5d7f07f2d9e52961b133e9ad2eae0d615c5d47e7`; current architecture and lifecycle documentation, rather than the archived documentation repository |
| Restate Python SDK | `63d2fe5745c0369bae2adc2641b571bb5b5bea28`; run-block input/result boundary and agent tool wrapping |
| DBOS documentation source | `5c118aacd4b07a8988c63041cf3e61482299403a`; Go steps, transactions, and cancellation documentation |
| Microsoft durable-agent extension | `95d82146a1f2f593563c8019d6019e0aa7e904c0`; state architecture and run-local invocation guards |
| Other hosted documentation | Retrieved October 18, 2026; mutable sources, not claims about every historical version, SDK, or storage backend |

The initial vocabulary check against event sourcing, WAL, Lamport, and Temporal was not this deeper survey.
In particular, default activity retry advice was insufficient evidence for a structural mismatch with Temporal.

## The established execution model

The coordinator reconstructs its state from accepted history, decides what work is eligible, records the required intent, and only then admits the external work.
It records an acceptable outcome before dependent execution consumes that outcome.
Recovery reuses accepted outcomes; it does not ask models, tools, or hooks to reproduce them merely to rebuild state.

```text
Accepted model response and requested calls
    -> prepare and authorize effective action
    -> accept exact effective intent
    -> admit and invoke tool
    -> privately process/review returned output
    -> accept permitted canonical result
    -> assemble and accept complete next model input
    -> admit and invoke model
```

This is an orchestration protocol, not an atomic transaction spanning the journal and the outside world.
The execution is nondeterministic; reconstruction of accepted state must be well defined.
A reducer applying committed records can provide that reconstruction without replaying the original imperative workflow program.

### What the journal proves after a crash

| Accepted evidence | Supported conclusion | Unsupported conclusion |
| --- | --- | --- |
| Requested call, no effective intent | The compliant protocol did not invoke the tool. | Pre-intent hooks caused no effects. |
| Effective intent, no established outcome | The tool was authorized; invocation or its outcome can remain uncertain. | The tool failed, never started, or is safe to repeat. |
| Safe evidence of produced output, but lost volatile hold | Execution evidence survives while the unreleased output is unavailable. | The bytes can be recovered from an intact workspace or by invoking the tool again. |
| Canonical result | Reconstruct the accepted result without tool invocation. | Every external resource still exists or every external effect can be reversed. |
| Complete model input, no accepted response | The intended request is retained; provider receipt/completion can remain uncertain. | It is safe to automatically submit another request. |

A lost append response creates a separate uncertainty about storage acceptance.
Resolve that through transition identity and authoritative readback before relying on or retrying the append; it is not permission to rerun the tool.

## Literature: the closest foundations

### Log-based recovery and the output-commit problem

Elnozahy et al., [*A Survey of Rollback-Recovery Protocols in Message-Passing Systems*](https://www.cs.utexas.edu/~lorenzo/corsi/cs380d/papers/SurveyFinal.pdf), 2002, gives the most useful theoretical vocabulary.
Sections 2 and 4 distinguish checkpoints, determinants of nondeterministic events, pessimistic logging, and output commit.
The full manuscript was inspected.

Under **pessimistic or synchronous logging**, an event's determinant reaches stable storage before computation can depend on it.
The **output-commit problem** concerns making the state responsible for externally visible output recoverable before releasing that output to a world that cannot roll back.
These explain why live observations cannot automatically become recoverable inputs, and why dependent effects must wait for durable prerequisites.

The mapping is an application of the ordering principles, not a claim that the harness implements the survey's complete message-logging protocol.
Persisting the state that caused a tool invocation does not establish whether the tool actually performed its effect.

### Ambrosia: a concrete durable input barrier

Goldstein et al., [*A.M.B.R.O.S.I.A: Providing Performant Virtual Resiliency for Distributed Applications*](https://www.microsoft.com/en-us/research/wp-content/uploads/2018/12/AmbrosiaPaper.pdf), MSR-TR-2018-40, 2018, makes the barrier concrete.
The full report was inspected, especially sections 3 and 4.
This is a technical report, not a claim of a peer-reviewed conference publication.

Its coordinator flushes incoming log pages before delivering their requests to application code, preventing outgoing effects from outrunning committed inputs.
Deterministic reconstruction and protocol sequence/consumption state support recovery among participating components.
Checkpoints include protocol state, not just application values.

That communication protocol does not automatically cover arbitrary shell commands or APIs.
The report's external command example relies on idempotent submission at the managed endpoint.
Its treatment of nondeterministic "impulses" also illustrates that an input not durably admitted can be lost, but it is not a ready-made sensitive-output disclosure policy.

### RIFL: the boundary of exactly-once effects

Lee et al., [*Implementing Linearizability at Large Scale and Low Latency*](https://web.stanford.edu/~ouster/cgi-bin/papers/rifl.pdf), SOSP 2015, identifies stable RPC IDs, completion records, retry rendezvous, and safe garbage collection as parts of recoverable exactly-once RPC semantics.
The full paper was inspected, especially sections 3 and 4.

The decisive requirement is that completion evidence be created **atomically with the operation's mutations**.
A retry can then return the retained result without repeating the mutation.
Stable IDs alone do not supply that atomicity, and discarding deduplication evidence without controlling subsequent retries can reintroduce duplicate execution.

An ordinary harness tool generally does not participate in such a protocol.
A future tool-specific recoverable adapter could provide deduplication or authoritative outcome lookup, but a journaled intent followed by a separate result is not equivalent to RIFL.

### Beldi and Boki: stronger workflows require participating operations

Zhang et al., [*Fault-tolerant and Transactional Stateful Serverless Workflows*](https://arxiv.org/pdf/2010.06706), OSDI 2020, describes Beldi.
The full extended paper was inspected, especially sections 2 through 4.
Beldi records operations/results, couples mutations to logging within the required atomicity scope, and coordinates invocation/result retention between participating functions.
Its exactly-once semantics concern resulting state, not one physical execution of every function body.
Section 4.5 explicitly limits calls outside Beldi's API to at-least-once guarantees.

Jia and Witchel, [*Boki: Stateful Serverless Computing with Shared Logs*](https://www.cs.utexas.edu/users/witchel/pubs/jia21sosp-boki.pdf), SOSP 2021, describes BokiFlow in section 5.1.
The full paper was inspected.
Where log and database do not share atomicity, BokiFlow uses log sequence numbers and conditional database writes to make updates idempotent.
That is an additional endpoint capability, not a property obtained from the shared log alone.

Together these systems show what stronger tool contracts would need: participating storage, conditional mutation, recoverable results, or cooperative invocation identities.
They do not justify transactional or exactly-once claims for arbitrary tools.

## Runtime comparisons

### Temporal and Cadence

Temporal's [event-history model](https://docs.temporal.io/workflow-execution/event) records scheduling and completion, while [workflow processing](https://docs.temporal.io/tasks) reconstructs state using recorded activity results.
Its [history-event schema](https://github.com/temporalio/api/blob/master/temporal/api/history/v1/message.proto) includes activity inputs and completion results.
This supplies a direct structural mapping: a scheduled activity can carry the final authorized tool intent or complete effective model input, and its permitted result can precede dependent orchestration.

**Retries are a policy, not a mandatory property of Temporal replay.**
The [retry policy](https://docs.temporal.io/encyclopedia/retry-policies) specifies that `MaximumAttempts = 1` permits one attempt and no retries; the [activity lifecycle](https://docs.temporal.io/activity-execution) describes the corresponding timeout behavior.
That is compatible with refusing to automatically repeat an uncertain ordinary tool.
It does not prove the worker stopped, prevent a tool's own internal retries, or turn a timeout into known failure of the external effect.
A workflow must handle uncertainty explicitly rather than scheduling replacement work through another path.

The [OpenAI Agents integration](https://github.com/temporalio/ai-integrations/tree/78e845a9e379ac18b4b69593d62b5dc3873700ab/python/openai_agents) places model calls in activities and provides explicit wrappers for I/O tools.
Its [tool wrapper](https://github.com/temporalio/ai-integrations/blob/78e845a9e379ac18b4b69593d62b5dc3873700ab/python/openai_agents/src/temporalio/openai_agents/workflow.py#L42-L163) exposes retry and cancellation options.
The [Deep Agents integration](https://github.com/temporalio/ai-integrations/tree/78e845a9e379ac18b4b69593d62b5dc3873700ab/python/deepagents) covers filesystem/shell backends, subagents, and approval interrupts; it is labeled pre-release at the inspected revision, unlike the GA OpenAI Agents integration.
A wrapper's existence does not prove complete effective-input capture or our review ordering.

The application still defines what the activity payload means.
In particular, [replay determinism](https://docs.temporal.io/workflow-definition#deterministic-constraints) does not verify byte-exact activity-input equality: some parameter changes are permitted without a nondeterminism error.
Execution must consume the retained effective envelope, not recompute it from current defaults or middleware.

[Workflow Updates](https://docs.temporal.io/develop/go/workflows/message-passing) offer durable accepted/completed stages and request IDs for shared controls.
Approver authority, immutable ask binding, one winning disposition, and result-release semantics remain application rules.
A closed workflow cannot accept later Signals or Updates, so a terminal harness run should not automatically mean the longer-lived session/disclosure coordinator is closed.
[Cancellation](https://docs.temporal.io/develop/go/workflows/cancellation) is cooperative, with activity delivery dependent on heartbeat behavior; native cancellation is not the complete prompt-signal/commit-acknowledgement/bounded-settlement contract.

Cadence supplies a closely related event-sourced workflow/activity model and optional [activity retry policies](https://github.com/cadence-workflow/Cadence-Docs/blob/master/docs/03-concepts/02-activities.md).
Temporal-specific Update APIs, SDK defaults, and cancellation details should not be attributed to Cadence without separate verification.

**Assessment:** strongest reference for adopting the execution model; a dependency/adoption decision still needs deployment, storage-authority, retention, and cancellation/result-race qualification.

### Restate

Restate's [architecture](https://github.com/restatedev/docs-restate/blob/5d7f07f2d9e52961b133e9ad2eae0d615c5d47e7/docs/references/architecture.mdx) documents a log-first system: quorum append acceptance establishes commitment, materialized state derives from the log, and journal acknowledgements precede dependent handler progression.
It also documents invocation epochs and rejection of superseded attempt events.
These are close references for acceptance barriers and fenced history advancement.
Physical log trimming and snapshots mean this is not automatically an indefinitely retained client transcript.

There are two qualifications to mapping an ordinary tool to `ctx.run`.
First, the [Python SDK](https://github.com/restatedev/sdk-python/blob/63d2fe5745c0369bae2adc2641b571bb5b5bea28/python/restate/server_context.py#L947-L984) retains callable arguments locally and proposes a serialized result; this alone does not prove complete effective inputs were committed before execution.
An explicit durable invocation/input record is a different boundary.

Second, the shared-core [incomplete-run replay test](https://github.com/restatedev/sdk-shared-core/blob/9ce1d8e8b136e95a89d19d77b00579e51479c401/src/tests/run.rs#L355-L403) expects execution when a journaled run has no completion.
An ordinary unfinished run is therefore eligible for re-execution on recovery.
A run-block error-retry limit alone does not establish no re-execution after abrupt process loss.
Invocation-level stop/pause controls are relevant, but this survey did not qualify a complete single-attempt tool protocol using them.
This is a limitation of the naive mapping, not proof that the substrate cannot support the desired protocol.

[External events and workflow promises](https://github.com/restatedev/docs-restate/blob/5d7f07f2d9e52961b133e9ad2eae0d615c5d47e7/docs/develop/go/external-events.mdx) support durable approval coordination and handlers callable after workflow-run completion within retention.
[Concurrent tasks](https://github.com/restatedev/docs-restate/blob/5d7f07f2d9e52961b133e9ad2eae0d615c5d47e7/docs/develop/go/concurrent-tasks.mdx) record completion ordering for replay.
Those primitives do not themselves authenticate approvers, bind verdicts to exact authorized revisions, or preserve volatile holds.
The Pydantic AI integration's optional [`RestateContextRunToolSet`](https://github.com/restatedev/sdk-python/blob/63d2fe5745c0369bae2adc2641b571bb5b5bea28/python/restate/ext/pydantic/_toolset.py#L55-L108) uses a turnstile to serialize auto-wrapped function tools until the preceding output is received.
That wrapper is selected by [`auto_wrap_tools=True`, which defaults to false](https://github.com/restatedev/sdk-python/blob/63d2fe5745c0369bae2adc2641b571bb5b5bea28/python/restate/ext/pydantic/_agent.py#L98-L129); it is not a general restriction on Restate Python agents.
Runtime parallelism and the behavior of a particular integration/configuration need separate verification.

**Assessment:** strong architectural reference and plausible substrate; explicit committed input and conservative unfinished-operation handling require qualification beyond convenience run blocks.

### Other close systems and integrations

| Reference | Useful established mechanism | Important boundary |
| --- | --- | --- |
| [DBOS Go steps](https://github.com/dbos-inc/dbos-docs/blob/5c118aacd4b07a8988c63041cf3e61482299403a/docs/golang/tutorials/step-tutorial.md) | Database-backed workflow inputs and completed-step results. | Docs explicitly permit crash re-execution after effects but before checkpointing, despite a default of zero automatic returned-error retries. [Transactions](https://github.com/dbos-inc/dbos-docs/blob/5c118aacd4b07a8988c63041cf3e61482299403a/docs/golang/tutorials/transaction-tutorial.md) provide stronger atomicity only for participating database writes. |
| [Azure Durable Task / Durable Functions](https://learn.microsoft.com/en-us/azure/azure-functions/durable/durable-functions-orchestrations) | Append-only execution history; checkpointed scheduling, messages, results, and replay. | [Activities are at least once](https://learn.microsoft.com/en-us/azure/azure-functions/durable/durable-functions-types-features-overview), including the effect-before-recording crash window. [Termination](https://learn.microsoft.com/en-us/azure/azure-functions/durable/durable-functions-instance-management) is queued and does not propagate to activities/sub-orchestrations under the documented contract. |
| [Dapr Workflow](https://docs.dapr.io/developing-applications/building-blocks/workflow/workflow-architecture/) | Event-sourced orchestration with actor/reminder recovery. | [Activities are at least once](https://docs.dapr.io/developing-applications/building-blocks/workflow/workflow-features-concepts/); disabling a retry policy does not remove recovery re-execution. Continue-as-New and instance-ID reuse also affect retained history. Azure termination semantics are not automatically Dapr semantics. |
| [LangGraph durable execution](https://docs.langchain.com/oss/python/langgraph/durable-execution) | Checkpoints, task-level pending writes, synchronous durability, interrupts, and reuse of completed task results. | An [unfinished task can run again on resume](https://docs.langchain.com/oss/python/langgraph/functional-api#idempotency), and code before an interrupt can repeat. Retry-policy settings do not replace a durable uncertainty guard. |
| [Microsoft Agent Framework durable extension](https://github.com/microsoft/agent-framework-durable-extension/blob/95d82146a1f2f593563c8019d6019e0aa7e904c0/docs/features/durable-agents/durable-state-architecture.md) | Durable per-session entities, conversation state, and delivery/completion distinctions. | The normal boundary surrounds an entire agent invocation; model/tool effects occur outside the entity transaction. Meeting per-model-input/per-tool-intent gates requires finer boundaries, not just a different retry setting. Newer schema-2 state mechanisms are gated, not uniformly active. |

## Where the harness-specific protocol belongs

### Authorization and exact effective inputs

A durable runtime schedules values; it does not know whether they are the final approved action.
Pre-tool rewrites, permission review, principal/policy freshness, environment binding, and the exact arguments consumed by execution belong to the harness's admission protocol.
Retained intent must not be reconstructed from current configuration when execution or inspection resumes.

Hook invocation identity and exact reconstructible inputs support a separately authorized at-least-once hook contract.
Stable identity only enables external deduplication when the receiver participates; it does not make an effect idempotent by itself.
An accepted hook completion is reused, while deliberately unpersisted lost input ends the possibility of an exact retry.

### Disclosure before durable result acceptance

The ordinary wrapper sequence can be wrong for our contract:

```text
execute tool -> persist raw activity/step result -> review -> release
```

The required placement is:

```text
commit authorized intent -> execute tool
    -> hold raw output only in live memory
    -> process and review
    -> commit permitted canonical result or safe disposition
```

[Azure's sensitive-data guidance](https://learn.microsoft.com/en-us/azure/azure-functions/durable/durable-functions-serialization-and-persistence) explicitly distinguishes activity-private values from persisted orchestration inputs/outputs.
That supports the boundary placement, not a claim that standard durable-agent integrations implement our complete release policy.
Review inside a worker can return a permitted envelope instead of raw bytes; durable execution does not require storing every intermediate value.

A long-lived release ask additionally needs safe durable metadata and a bounded volatile payload hold.
Raw bytes must not leak through activity results, heartbeats, interrupt payloads, traces, errors, checkpoints, or a supposed "temporary" durable blob reference.
On loss, recovery settles unavailable under the original identity rather than recovering prohibited bytes or rerunning the tool.

### Cancellation and fencing

Cancellation request, committed acknowledgement, execution settlement, and disclosure disposition are separate concepts.
A longer-lived session coordinator can retain an approval obligation after a run becomes terminal.
There is no need to invent a general recovery model to express that, but none of the inspected stock agent integrations establishes the entire protocol.

An ownership epoch can fence stale state acceptance without stopping a stale process from making external changes.
**History fencing** and **external-effect fencing** require separate claims; the latter needs participation by the affected resource.
Likewise, a cancellation deadline bounds local settlement without proving external cessation.

### Session history versus runtime journal

A runtime's execution history is not automatically the complete session authority required for retained client display, forks, compaction, and bounded API receipts.
Workflow closure, retention, purging, Continue-as-New, and log trimming have different semantics across these systems.
A runtime adoption would need to explain which store is authoritative and how client/session records share atomic acceptance with execution decisions.
An unexplained second authoritative ledger would recreate the dual-write problem rather than solve it.

## Vocabulary to adopt

| Term | Meaning in this design |
| --- | --- |
| Durable orchestration | Recoverable coordination of work, not automatic repetition of every external operation. |
| Authoritative history / event sourcing | Accepted records determine state; projections and checkpoints are derived. |
| Replay / reconstruction | Rebuild state from retained facts without invoking external work. |
| Re-execution / retry | Make another execution attempt; separate from reconstruction and storage-append retry. |
| Durability barrier / commit gate | Establish prerequisite facts in durable history before allowing dependent execution. |
| Durable intent | Exact admitted operation retained before invocation, not proof of execution. |
| Indeterminate or unknown outcome | Accepted evidence cannot establish the external operation's outcome. |
| Single-attempt operation | No automatic second execution attempt; not an exactly-once external-effect guarantee. |
| Logical invocation identity / attempt identity | Stable intended work versus a particular attempt. |
| Fencing token / ownership epoch | A participating boundary rejects stale owners or attempts. |
| Cancellation request / acknowledgement / settlement | Interruption intent, accepted control decision, and accounting for outstanding work. |
| Execution approval / disclosure approval | Authority to act versus permission to release produced content. |

Pessimistic logging and output commit are explanatory foundations with specific literature meanings, not names for a newly invented universal transaction protocol.
"Saga" would imply defined compensating actions; arbitrary tool chains do not gain rollback by receiving that label.
Unqualified "exactly once," "transactional tools," and "replay-safe effects" conceal the external-effect boundary and should be avoided.

## Recommendations and remaining qualification

Use the established durable-orchestration model as the explanatory foundation of the [contract draft](session-commit-history-contracts.md).
Describe the harness contribution as domain-specific admission, evidence, disclosure, and lifecycle contracts.
Do not start from a requirement for a new DAG scheduler, generic rollback layer, deterministic LLM, or custom general workflow engine.
The design direction is to apply these established semantics through narrowly scoped coordination within Mecatl, without requiring an external workflow platform or recreating one internally.
Temporal and Restate remain reference systems, not candidates for dependency adoption in this work.
Any focused library reuse must justify its operational and maintenance cost, preserve straightforward open-source distribution and licensing, and fit the existing harness contracts.
These research findings do not reopen settled behavior or authorize implementation, an implementation breakdown, or a substrate migration.

Before asserting an equivalent implementation, the unresolved evidence is concrete:

| Qualification question | Required observation |
| --- | --- |
| Effect succeeds, worker dies before completion acceptance. | No automatic second ordinary-tool invocation; outcome stays uncertain unless authoritative evidence exists. Test worker crash separately from returned-error retry. |
| Final rewrite/approval precedes execution. | Committed intent and executed values match exactly; current middleware/defaults cannot silently substitute values. |
| Raw output requires review. | Poisoned sensitive bytes never enter durable or client-visible channels before permission, including failure/trace/heartbeat paths. |
| Two clients race approval, denial, cancellation, or successor admission. | One guarded disposition; immutable ask/action binding; stale decisions rejected. |
| Ownership changes while an old worker/tool survives. | Stale history advancement is rejected, while remaining external-effect uncertainty is represented honestly. |
| One concurrent result is ready while another is held. | The ready permitted result can be accepted independently without violating model ordering or dispatch barriers. |
| Run execution ends while output awaits release. | Disclosure remains independently resolvable while the hold survives; successor admission/loss invalidates it without reopening execution. |
| Runtime history is purged, rolled over, or compacted. | Retained session authority, complete client views, and receipt semantics remain explicit; no silent replay gap or dual authority. |

These are qualification cases, not tests run by this survey or demonstrated upstream failures.
The next design step is to map the contract's terminology and lifecycle rules to these established models; any semantic prototype needs separate authorization.

## Related information

- [Committed-session-history proposal](session-commit-history.md)
- [Contract vocabulary, lifecycle, and acceptance boundaries](session-commit-history-contracts.md)
- [Harness storage, client reload, and extension prior art](session-commit-history-prior-art.md)
- [Draft index](../README.md)
