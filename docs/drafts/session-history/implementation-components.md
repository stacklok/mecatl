# Implementation note: separate learning and compaction policy from execution

**Status:** Architectural follow-up to explore during the session-history rework, not an approved implementation plan or an extraction to include in the identity PR.

Execution needs to control when work can proceed and which facts become authoritative.
It does not need to contain every policy for deriving learning proposals or reducing model context.
Explore moving those implementations behind explicit interfaces while retaining execution lifecycle, authority, and history invariants in the engine and its commit coordination.
A package extraction can be sufficient; separate services, processes, or Go modules are not implied.

The motivation comes from Joe's supplied review handoff for [identity PR #2404](https://github.com/stacklok/mecatl/pull/2404).
The implementation observations below describe that review context, not an independent qualification of its branch or this checkout.
Recheck concrete interfaces against the source selected for implementation; the proposal does not depend on the handoff's line numbers.
Use the [design review map](README.md) for the core contracts and the [ownership note](implementation-ownership.md) for consistent snapshot and cloning patterns at these interfaces.

## Learning observes completion without owning execution

The identity review describes a completion path from owned session history and run context into a learning `Trajectory`, selected and bounded reflection `Input`, and a sanitized reflection-model `Projection`.
A tool-free model request then produces evidence-backed proposals for operator facts, project facts, or reusable procedures.
Review, evaluation, publication, and activation are downstream responsibilities; proposal generation alone does not publish memory or activate a skill.
The trajectory can contain more than the latest run's messages: its `Current` span identifies the latest run within that history.
Evidence citations link a proposal to supporting source content and its integrity, not to a proof that the proposal is true or the procedure correct.

Much of the domain logic already resides in `engine/learning` in that review context.
The extraction question concerns remaining implementations in `engine/agent`, the completion-observation contract, and host/storage coordination, rather than a greenfield learning subsystem.
Relevant source locators from the handoff are `observeCompletion` in `engine/agent/loop.go`, `Trajectory` in `engine/learning/learning.go`, reflection `Input` and `Projection` in `engine/learning/reflection.go` and `evidence.go`, and `EvidenceReflector` in `engine/agent/evidencereflector.go`.

The execution-facing interface should be narrow enough to observe an eligible completion without exposing live mutable session state.
Learning policy outside the execution package can own policy filters and signal detection for authoritatively eligible completions, evidence selection and bounds, sanitization and citation handling, reflection prompts and provider requests, proposal generation and recovery, and coordination with memory/skill evaluation and publication.
Moving those policies does not permit the learning component to invent eligibility or evidence authority.

Carry forward these constraints from the identity work when designing the interface:

- Observers cannot mutate live session history through shared references; learning failure cannot change the execution result.
- Legacy sessions remain permanently learning-ineligible across restart, later runs, and compaction.
- Deserialized display projections do not establish evidence authority.
- Source coordinates distinguish run-local from session-global positions.
- Inactive candidates with obsolete evidence cannot activate; already-active historical skills remain readable and supported.

An important open choice is whether authoritative eligibility and source provenance travel in a completion record or are resolved by the learning component through authoritative storage.
Neither a generic completion notification nor caller-supplied history is sufficient authority on its own.
Moving the check out of the loop without replacing its authoritative basis would create a bypass.
The [storage-access contract](session-commit-history.md#storage-access-and-commit-coordination) already allows authorized, bounded, read-only learning access without starting session execution; it does not grant publication or activation authority.
Completion delivery and proposal recovery semantics also need an explicit contract rather than an assumption of cross-crash exactly-once observation.

## Compaction proposes a lossy replacement; execution admits it

The handoff reports an existing injected `agent.Compactor` interface, with heuristic/cascade implementations and helpers still inside `engine/agent`.
The cascade progressively drops older history, truncates tool results, collapses file reads, and optionally summarizes with a model request.
Relevant locators are `engine/agent/compaction.go`, `cascade.go`, `manual_compaction.go`, and the automatic integration in `loop.go`.

Explore separating responsibilities along this boundary:

| Execution and commit coordination retain | Compaction implementation owns |
| --- | --- |
| Triggering at a legal lifecycle boundary and calculating available history budget | Retention and reduction strategy |
| Validating candidate identity, tool-call/result pairing, and size reduction | Truncation, collapse, and summarization mechanics |
| Accepting and committing replacement continuation under history invariants | Summarization prompt and provider request |
| Usage accounting, persistence coordination, and failure policy | Algorithm-specific configuration and returned candidate history, summary, and usage |

A package such as `engine/compaction` is a candidate home for implementations that do not import `agent`.
The shared interface location and dependency direction still need design; moving files alone does not establish that separation.

The reported automatic-compaction budget path uses an optional interface with an unexported `compactToBudget` method.
An implementation outside `agent` cannot satisfy that package-private method contract.
An extraction therefore needs a coherent request/result interface that carries the target budget, rather than preserving an inaccessible extension point.
Inputs and candidate results need explicit ownership contracts so a strategy cannot mutate accepted history while constructing a replacement.
The interface design must also address how a candidate identifies the history version it reduced and how admission rejects a stale candidate.
Summarization model requests remain subject to the applicable execution gates, cancellation, accounting, and failure rules; extracting the strategy does not authorize bypassing them.
The invocation interface still needs design.

Compaction is intentionally lossy; its interface must not promise semantically equivalent history.
Execution still validates the candidate before acceptance, and a replacement of the model's working context must not delete retained history.
The [history and checkpoint contract](session-commit-history.md#checkpoints-replay-and-missing-history) remains authoritative for reconstruction; strategy extraction does not redefine retention or provider replay.

## Reconcile storage observations with the existing design

The [storage and observation packet](storage-and-observation.md) and its linked contracts remain authoritative for this proposal.
The identity-review handoff did not account for the full redesign; its storage suggestions are diagnostic input, not a canonical decomposition or an alternative storage architecture.
Committed history remains the authority for accepted session facts and reconstruction, with checkpoints and authorized views derived from it.
Compaction reduces model context, not retained history.

The identity review reports an asymmetry in the existing activity/event log: ordinary execution events omit opaque provider replay, while compaction archive events embed the full replaced conversation, including private replay and the retained tail.
Those archives therefore contain more than just dropped messages.
The handoff reports uneven event-only provider-replay fidelity and repeated history storage as a result.
These observations motivate checking the extraction against the redesign; they are not guarantees supplied or repaired by the identity PR.

Use the handoff's four purposes as review questions within the existing design:

| Reported purpose | Question to reconcile with existing contracts |
| --- | --- |
| Resumable session state | How does reconstruction from committed history preserve required private provider replay and continuation state? |
| Client and inspection activity/history | Which authorized derived views serve these readers under the specified completeness and disclosure rules? |
| Learning source material | How does learning obtain bounded, permitted source content with stable coordinates and authoritative provenance and eligibility? |
| Historical archives/checkpoints | Which derived checkpoints or indexes are useful, and what completeness and private-data contracts apply without duplicating authority? |

This list establishes neither four independent contracts nor four physical stores or new architectural layers.
A display-safe view is not automatically valid learning evidence, and a reflection-evidence view does not automatically include all private continuation.
The completion-record versus authoritative-read choice remains open within the existing design; no separate learning source of truth is selected here.
Nothing here permits persisting unreleased memory-only output as learning material or an archive.

## Extraction boundaries and review questions

Name transformations by destination: provider-request projection, reflection-evidence projection, or client-display projection rather than an unqualified "projection" where these paths meet.
Use the same recognizable ownership conventions across completion input, compaction input, and candidate output instead of introducing different implicit borrowing and cloning rules for each subsystem.

Keep package extraction separate from changes to archive formats, retention, and replay guarantees unless those changes are deliberately coordinated.
Assess Go source compatibility, stored-data compatibility, and remote API compatibility separately.
Neither the reported implementation nor a package split establishes cross-crash exactly-once execution or atomic snapshot/event-log persistence.

Before choosing interfaces, work through how completion eligibility and provenance stay authoritative, how detached inputs remain stable, how candidate compaction is admitted, and how failures affect execution and usage accounting.
Then decide which responsibilities belong in the execution package, learning/compaction packages, or host/storage coordination.
The objective is a smaller execution implementation with explicit, enforceable contracts, not a generic plugin framework or a service split.
