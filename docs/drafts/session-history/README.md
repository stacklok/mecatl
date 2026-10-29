# Session-history design review

These are design drafts, not shipped behavior or authorization to implement.
Start with the [overview](overview.md); the short [shared vocabulary](vocabulary.md) is available for lookup.
**Choose one packet next; there is no required six-packet reading sequence.**
Each packet states the assumptions it needs from other topics locally; links offer optional detail rather than prerequisite reading.
The boundaries are review topics, not code packages or implementation phases.

| Packet | Central review question |
| --- | --- |
| [Execution and recovery](execution-and-recovery.md) | What must commit before work advances, and what can recovery honestly establish? |
| [Cancellation and results](cancellation-and-results.md) | How can execution stop while produced output still awaits settlement or release? |
| [Authorization and hooks](authorization-and-hooks.md) | Which exact action is authorized, and when can hook work be reused or retried? |
| [Model continuation](model-continuation.md) | What must survive to reconstruct the effective model request without incidental drift? |
| [Commands and lifecycle](commands-and-lifecycle.md) | How do shared controls, bounded retries, and catalog decisions remain unambiguous? |
| [Storage and observation](storage-and-observation.md) | What does durable acceptance mean, and how does a client load a complete view and follow it? |

Each packet begins with a narrative and concrete examples, followed by its imported assumptions, detailed constraints, and open questions.
The detailed sections retain the precise limits, exceptions, and interface decisions; you can read them after understanding the story.
Follow its detail links only when reviewing that contract; reading the full references first is unnecessary.

## Detailed lookup, not a second reading path

- [Architecture reference](session-commit-history.md): exhaustive specialized contracts, delivery boundary, extension/fork/handoff details, and costs.
- [Contract reference](session-commit-history-contracts.md): lifecycle phases, commit gates, cancellation transitions, reload/failure matrices, and acceptance cases.

The packets own the review explanations and relocated worked sequences.
The linked detailed sections own exhaustive requirements; summaries do not create a parallel specification.
Open-decision lists live in the packets, not in competing reference backlogs.

## Implementation considerations

- [Snapshots, ownership, and cloning](implementation-ownership.md): candidate patterns for stable reader views and enforceable representation boundaries. This note guides implementation discussion; it does not change core contracts or select an analyzer.
- [Extensions and execution policy boundaries](implementation-components.md): delegation and learning compose capabilities through different extension points and can contribute tools or client commands. Also covers compaction policy extraction and diagnostic storage observations; concrete interfaces and implementation remain unapproved.

## Optional models and research

- [History/execution model](session-commit-history.modelith.md) ([YAML](session-commit-history.modelith.yaml)) and [catalog/command model](session-command-protocol.modelith.md) ([YAML](session-command-protocol.modelith.yaml)): domain invariants and scenarios, not final API types.
- [Durable-execution foundations](session-commit-history-durable-execution-prior-art.md) and [harness comparisons](session-commit-history-prior-art.md): evidence and limits of prior art, not additional requirements.
