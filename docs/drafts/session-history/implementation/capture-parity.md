# Legacy capture parity before kernel cutover

This assessment compares the isolated kernel at `b40c45289e8f7bbec8d1826f50896b8e735c2021`
with the legacy persisted session snapshot and durable event log on 2026-11-05.
It is an implementation review aid to revisit as the kernel grows and before
production cutover, not a specification of complete kernel behavior. Read it with
[execution kernel and capability boundaries](kernel.md) when assigning missing
facts to the kernel, a driver, or host composition.

The current kernel can retain the legacy user/media content and more complete
reusable context. Attribution, creation metadata, and model-request evidence need
further work. These differences do not expand the current context/inspection
slice into a complete replacement project. Establish the basic contracts first,
then resolve each material gap in its owning layer before cutover.

## Comparison boundary

The legacy driver's [`SessionSnapshot`](../../../../contracts/proto/mecatl/driver/v1/session_store.proto)
is an opaque envelope containing the bytes selected by
[`sessnap.Snapshot`](../../../../engine/adapter/sessnap/sessnap.go).
The comparison includes the actual message DTOs, event payloads, and emission
paths, rather than assuming every in-memory model request field is persisted.

The isolated kernel currently interprets creation, run start, authored user input,
incremental effective input, independent instructions/tool-catalog/model-binding
replacements, and explicit run interruption. It has no production capture wiring.
A field being representable means an explicit producer can retain it; it does not
establish that the production engine captures it through the kernel.

## Content and context coverage

| Area | Legacy durable capture | Kernel representation and remaining qualification |
| --- | --- | --- |
| User text and media | Recorded text and image/audio kind, MIME type, bytes, or URL in message DTOs and user-prompt events. | Ordered parts and tagged media cover those payloads, with additional document metadata and provider-scoped references. No legacy media payload field was found that cannot be represented. |
| Authored versus effective input | The normal prompt path records text after command expansion and submit-hook mutation. It does not separately retain the original submission through that path. | Separate authored input and effective contributions can retain both. Integration must preserve that distinction and apply disclosure rules before capture. |
| Root instructions | Explicitly recorded conversation system messages persist. Normal assembled context fragments are ephemeral model-request inputs. | Complete ordered instruction blocks and source explanations can persist. Legacy records without source evidence need an honest unknown/supplied explanation, not invented provenance. |
| Advertised tools | No tool catalog in the snapshot. Model-request manifests retain names, assembly decisions, and aggregate schema size, not complete definitions. | Ordered names, descriptions, and exact argument-schema JSON cover the legacy model-facing definition fields and improve body retention. Assembly explanations are a separate gap below. |
| Model selection/settings | Snapshot provider/model selectors and reasoning effort; model-request manifests add effective model and context window. | Declared route/API/model and explicit JSON settings can represent the preparation choices. The future producer must define the mapping and preserve unset/default selection where required; capacity is not an implemented capture contract. |

Evidence: [legacy message/media DTOs](../../../../engine/adapter/sessnap/sessnap.go),
[prompt expansion, hooks, and ephemeral fragments](../../../../engine/agent/loop.go),
[model-request manifest producer](../../../../engine/agent/request_manifest.go),
[kernel media](../../../../engine/kernel/session/media.go), and
[kernel tool definitions](../../../../engine/kernel/session/tool_definition.go).

The legacy [`InstructionSnapshot`](../../../../engine/session/instructions.go)
explicitly excludes discovered directories, scopes, partial/unavailable flags,
and source text from persisted session snapshots and conversations. Those live
fields are not old durable data that kernel history is dropping.

## Gaps overlapping the implemented concepts

| Information | Legacy evidence | Current kernel difference | Follow-up before cutover |
| --- | --- | --- | --- |
| Creation time | `sessnap.Snapshot.CreatedAt`. | `Created` retains only history identity; commit envelopes have no wall-clock timestamp. | Acknowledge timestamps as a missing basic fact. Decide event/commit versus creation-specific semantics, clock source, and observation versus acceptance time in a later focused change. Ordering is not a timestamp. |
| Session owner and verified action principal | Snapshot `Owner`; event `Actor` is stamped from the verified request context and can differ from the owner. Principal retains issuer, subject, grant type, and display name. | Neither the supported session facts nor history envelopes retain equivalent principal attribution. Inline `Actor` is a logical content-origin label. | Preserve ownership and action attribution somewhere durable. They can belong to a host/security layer rather than the session aggregate; ownership of the data and correlation to history must be explicit. Do not overload logical content actors. |
| User-input authority provenance | Message `UserPromptProvenance` retains principal/harness/unknown; user-prompt events also carry `Synthetic`. | User submissions and inline contributions distinguish forms of input, but there is no explicit authenticated-principal/unknown provenance field on `UserInput`. | Define an ingress mapping that preserves evidence without promoting unknown or harness-authored content to principal authority. This does not prescribe restoring the legacy enum. |
| Interruption explanation | Snapshot stop/failure metadata and terminal event stop reason, error, retry disposition, and stream progress. | `RunInterrupted` records the run occurrence only. | Review reason/initiator attribution with lifecycle work. Normal completion and model-stream progress belong to the next passive lifecycle checkpoint; interruption must not imply external cancellation or absence of effects. |

Evidence: [snapshot fields](../../../../engine/adapter/sessnap/sessnap.go),
[principal identity](../../../../engine/session/principal.go),
[user-message provenance](../../../../engine/session/conversation.go),
[user-prompt and terminal event payloads](../../../../engine/session/event.go),
[server actor stamping](../../../../internal/adapter/server/service.go),
[kernel creation](../../../../engine/kernel/session/created.go),
[kernel logical actors](../../../../engine/kernel/session/attributed_input.go), and
[kernel interruption at the assessment revision](https://github.com/stacklok/mecatl/blob/b40c45289e8f7bbec8d1826f50896b8e735c2021/engine/kernel/session/run_interrupted.go).

The legacy general `Event` has no occurrence timestamp. Its creation timestamp,
specialized durations, and backend modification times are separate facts; do not
infer a complete timestamped legacy timeline from their presence. The kernel
[commit envelope](../../../../engine/kernel/history/format/commit.go) establishes
order, not wall-clock time.

## Evidence to revisit with later checkpoints

The legacy model-request manifest is a content-free observation of the assembled
request handed to the provider. It records provider/model/reasoning-effort labels,
context window, ordered advertised tool names, tool source/filtering/shadowing
decisions, aggregate schema bytes, message count/serialized size, and prompt
component kind/provenance/size. It does not retain full prompt or schema bodies,
and emission before invocation does not prove a request reached the provider.

Kernel context facts retain richer bodies but do not establish which exact model
request used them. Revisit manifest evidence, stable/volatile prompt-layer
classification, and assembly explanations during model-request commitment and
versioned reconstruction. Optional diagnostic observations must not become a
second accepted-history authority. See the
[manifest payload and event definitions](../../../../engine/session/event.go) and
[producer](../../../../engine/agent/request_manifest.go).

The legacy event log also retains an optional observed downstream-provider label.
That is distinct from the configured provider selector and the kernel's declared
`ModelBinding.Route`. Retain observations with model-response evidence rather than
putting them into declared settings. See the
[provider-route chunk contract](../../../../engine/port/llm.go) and
[kernel binding contract](../../../../engine/kernel/session/model_binding.go).

Legacy opaque host run IDs, event sequence/turn indexes, and steer client message
IDs support external correlation. Kernel history/run/input identities provide
ordering and scoped identity, but do not preserve those original client tokens.
Resolve the mapping at client/control integration; a different identity scheme is
not itself missing run identity. See [event and steer fields](../../../../engine/session/event.go)
and [kernel run identity](../../../../engine/kernel/session/run_id.go).

Other legacy durable data belong to later or adjacent responsibilities: titles
and their generation metadata; session kind and lineage; environment identity,
placement, and host configuration; permission mode, authority, and pending control
state; model responses, tool calls/results, and private continuation; usage,
limits/counters, and context occupancy. Keep these in the cutover inventory without
adding them all to the current session records.

## Hosting-replica breadcrumbs to consider

Consider retaining session-history breadcrumbs identifying which pod or host is
hosting the session, including changes during handoff or recovery. This may fit
with leader election or session assignment across replicas; the record shape and
owning layer remain open. Distinguish historical hosting evidence from the
backend-authoritative ownership/fencing mechanism: a breadcrumb alone must not
grant execution authority.

## How to use this assessment

Revisit the relevant rows when each lifecycle, request-reconstruction, host,
security, or client-control contract is designed. Before cutover, identify where
each required fact is durably captured, who establishes it, and how reconstruction
or audit follows its history correlation. A fact can live outside the session
aggregate, but a display projection or current configuration is not a substitute
for evidence that must survive restart.

Use three dispositions: retained with a tested mapping; deliberately retired with
an explicit rationale; or still missing with an identified owning contract.
Arbitrary JSON or free-form descriptions can encode almost anything, but do not
count that as structural retention without a defined producer and interpretation.
The present implementation is intentionally incomplete; this note prevents those
gaps from disappearing from review rather than requiring immediate completeness.
