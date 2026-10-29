# Shared vocabulary

These foundational terms apply across the design drafts.
They describe concepts, not final Go types, wire encodings, or package boundaries.
Specialized terms are introduced in their owning packet or detailed section; the domain models remain the [full entity reference](session-commit-history.modelith.md).

Use **model request/response** (or **LLM request/response**) for LLM interaction, **tool call/result** for tool execution, and **API command/receipt** for client controls.
Where these boundaries meet, qualify request, response, call, result, and identity; local shorthand is fine once its meaning is clear.
Preserve actual API/type names and attributed source language rather than renaming them to match this convention.

| Term | Meaning |
| --- | --- |
| Session / run / turn | A durable conversation / a sequence of model turns within it / one model request and response. A run can end normally, by cancellation, failure, limits, or interruption. |
| Model request / response; tool call / result | A request to the LLM and its response; an invocation of a tool and its outcome. One LLM response can request several tool calls. A model request is not a tool call. |
| Committed history | The private, versioned, append-only ordered authority for a session. Continuation and retained display facts derive from it. |
| Commit | Acceptance of an append by the authoritative storage adapter under its head, ownership, and declared durability contract. Event emission, memory mutation, and buffered writes do not establish acceptance. |
| Commit gate | A boundary at which dependent work or actionable/confirmed publication waits for prerequisite facts to be committed. Ready facts may batch only if no gate separates their establishment. |
| Phase | Work or waiting in a lifecycle. A phase can contain several gates; a phase transition need not require a write. |
| Admission | An authorized, ordered eligibility decision, not mere network receipt. Final execution admission checks eligibility immediately before invocation; committed intent alone does not win that race. |
| Tool request / intent / result | A model's requested tool call / its effective authorized action after trusted rewrites / its canonical permitted or explicit recovery outcome. Intent proves neither invocation nor completion. |
| Execution metadata / produced output | Evidence about the invocation, distinct from its response content. Output requires processing and disclosure admission before canonical result acceptance; metadata also remains subject to safe-evidence rules. |
| Prevent execution / request interruption | Before admission, cancellation prevents invocation. After admission, cancellation requests interruption; support is optional and the signal proves neither cessation nor absence of effects. |
| Position / head | A boundary in session commit order / its latest boundary. C denotes a selected load position. Record identity identifies a transition, not its order. |
| Owning replica | The server process with the backend-authoritative right to drive the root session and its active delegation tree. A client connection is not ownership. |
| Replay / checkpoint / projection | Applying retained facts to reconstruct state / derived private continuation at a known position / an authorized server-produced display view. None reruns external work or creates another history authority. |
| Provisional observation | Permitted live information not established as a committed fact. It can be reconciled or discarded and is not a durable cursor or execution authority. |

## Kernel and driver vocabulary

These are agreed responsibility names and candidate Go package names; exact paths and dependency direction remain open.

| Term | Meaning |
| --- | --- |
| Kernel | The coupled execution, coordination, and session-state machinery that enforces core invariants. Not an OS sandbox or a separately deployable service. |
| Loop (`loop`) | Drives model turns and tool execution, prepares semantic transitions, waits at commit gates, and rechecks admission before invocation. |
| Coordinator (`coordinator`) | Coupled kernel machinery that orders transitions and controls, arranges commits and bounded batching, and resolves uncertain acceptance. Not a custom-coordinator plugin contract. |
| Session (`session`) | The existing aggregate concept: execution identities and state, legal transitions, valid pairing, and reconstruction from accepted facts. No new generic `state` abstraction is implied. |
| Driver | A trusted, host-composed capability implementation consuming the specific extension points it needs. Its external execution system can have a lifetime independent of the harness replica. |

There is one active coordination authority per session history under the root delegation tree's ownership, not one per client or run.
This does not prescribe one coordinator Go object per session or a separate child ownership claim.
Drivers participate in shutdown and handoff by stopping admission and reporting their lifecycle obligations, settlement, and supported safe points; suspend/resume does not imply portable Go call stacks or transferable external processes.

See [execution](execution-and-recovery.md) for gates versus admission, [storage](storage-and-observation.md) for ownership and cursor distinctions, and [model continuation](model-continuation.md) for provider replay, which is private retained data rather than reexecution.
