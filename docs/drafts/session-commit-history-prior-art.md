# Session-history prior art: research shortlist

**Status:** Discovery draft, not a completed survey or a set of recommendations.
**Scope:** Candidate references and questions for the [committed-session-history design](session-commit-history.md).

Deep research is paused pending the web-search credential fix in [#2202](https://github.com/stacklok/mecatl/issues/2202).

This companion will explain how other harnesses approach session persistence, client reload, and recovery, and which ideas are useful for Mecatl.
This first pass identifies sources and a research order without tracing implementations or testing their guarantees.
Repository locations were checked through GitHub discovery; implementation claims attributed to the PR comment remain leads to verify.
Some web searches were unavailable, so this is a focused shortlist rather than an exhaustive survey.

## Questions raised in the PR

[Ozz's comment on #2179](https://github.com/stacklok/mecatl/pull/2179#issuecomment-6057540127) supplies both concerns and starting references:

- Keep persistence behind pluggable storage drivers.
- Preserve session semantics when moving a portable history between deployment types.
- Examine OpenCode's claimed transaction boundary between durable events and registered projections, post-commit notifications, and persisted sequence catch-up.
- Examine OpenHands V1's claimed distinction between streaming progress and persisted events, and its subscribe/buffer/replay/deduplicate reconnect path.
- Borrow ActiveGraph's commit-before-projection and causal inspection ideas without assuming its runtime provides distributed ownership fencing.

These are research questions, not verified statements about the current versions of those projects.
In particular, expected-head checks alone would not establish Mecatl's proposed atomic head, ownership-epoch, and unexpired-claim contract.

## Primary harness references

### OpenCode

**Priority:** First, for the event/projection transaction and client synchronization questions Ozz identified.

Starting points:

- [Repository](https://github.com/anomalyco/opencode).
- [Synchronization implementation](https://github.com/anomalyco/opencode/tree/dev/packages/opencode/src/sync).
- [Storage implementation](https://github.com/anomalyco/opencode/tree/dev/packages/opencode/src/storage).
- [Session implementation](https://github.com/anomalyco/opencode/tree/dev/packages/opencode/src/session).
- [Event bus](https://github.com/anomalyco/opencode/tree/dev/packages/opencode/src/bus).

Investigate whether events and registered projections really commit atomically, and which data is authoritative after a crash.
Trace a reader from initial load through notification loss and persisted catch-up.
Identify which semantics depend on a particular database, and what portability would require from another storage driver.
Do not infer multi-replica fencing from the presence of a database transaction.

### OpenHands V1

**Priority:** First, alongside OpenCode, for reconnect and remote execution.

Starting points:

- [OpenHands application](https://github.com/OpenHands/OpenHands).
- [V1 software-agent SDK repository](https://github.com/OpenHands/software-agent-sdk).
- [SDK](https://github.com/OpenHands/software-agent-sdk/tree/main/openhands-sdk).
- [Agent server](https://github.com/OpenHands/software-agent-sdk/tree/main/openhands-agent-server).
- [Workspace integration](https://github.com/OpenHands/software-agent-sdk/tree/main/openhands-workspace).

Keep V1 SDK/server behavior separate from older application implementations.
Verify Ozz's subscribe-first, buffered replay description and the exact captured-boundary and deduplication rules.
Compare transient progress with persisted conversation events and private continuation state.
Look for evidence about reconnecting to execution in a separate workspace process, pending approvals, and uncertain tool outcomes; do not assume those capabilities from the repository layout.

### OpenAI Codex

**Priority:** Second, for a contrasting coding-harness persistence and client/server design.

Starting points:

- [Repository](https://github.com/openai/codex).
- [Core](https://github.com/openai/codex/tree/main/codex-rs/core).
- [Application server](https://github.com/openai/codex/tree/main/codex-rs/app-server).
- [State storage](https://github.com/openai/codex/tree/main/codex-rs/state).

Search results surfaced rollout/session-persistence descriptions, but those secondary descriptions are not evidence of current upstream guarantees.
Trace the upstream resume and fork paths, distinguishing retained history from the context actually sent to the model after compaction or rollback.
Examine how the application server represents threads, turns, approvals, subscriptions, and disconnects.
Determine whether private provider replay data, tool state, and display data share one durable authority or separate stores.

### Pi

**Priority:** Second, for session branching and extension-facing persistence.

Starting points:

- [Repository](https://github.com/earendil-works/pi).
- [Session documentation](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/sessions.md).
- [Extension documentation](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/extensions.md).
- [RPC documentation](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/rpc.md).

Search results point to JSONL session entries and tree-shaped history; verify the current format and its guarantees against upstream documentation and code.
Compare branch selection, compaction, extension records, and model-context reconstruction with Mecatl's independent forks and retained history.
Check whether extensions persist their own continuation state or only display/context messages.
Do not equate a portable local file format with a distributed append or ownership contract.
The older `earendil-works/pi-mono` repository URL redirects to this repository.

## Adjacent runtime references

These projects can illuminate particular mechanisms, but they are not interchangeable with a multi-client coding harness.

### ActiveGraph

**Priority:** Targeted follow-up for authority and provenance, rather than a full graph-runtime comparison.

Starting points:

- [Agent runtime repository](https://github.com/yoheinakajima/activegraph).
- [Event concepts](https://docs.activegraph.ai/concepts/events/index.md).
- [Operating in production](https://docs.activegraph.ai/guides/operating-in-production/index.md).

Verify the comment's commit-before-projection, reconstruction-without-behavior-reexecution, causal-reference, and runtime-limit claims.
Look for useful inspection patterns for run IDs, tool-call IDs, parent/child links, and fork positions.
Avoid importing its graph ontology merely to preserve provenance.
Use the agent-runtime project above, not the unrelated `neo4jrb/activegraph` Ruby library returned by name searches.

### LangGraph

**Priority:** Targeted follow-up for durable workflow replay, human interrupts, and side-effect boundaries.

Starting points:

- [Persistence documentation](https://docs.langchain.com/oss/python/langgraph/persistence).
- [Interrupt documentation](https://docs.langchain.com/oss/python/langgraph/interrupts).
- [Repository](https://github.com/langchain-ai/langgraph).

The persistence overview describes thread-scoped checkpointers separately from cross-thread stores.
That makes it a useful contrasting model, not evidence that checkpointing alone solves authoritative display history.
Investigate which work can replay on resume, what task results prevent re-execution, how human decisions bind to resumed work, and what external side-effect guarantees remain the application's responsibility.
Separate open-source library guarantees from those of a hosted or separately deployed Agent Server.

## Comparison questions

Use the same questions for each reference so the eventual survey compares behavior rather than project terminology.

| Area | Questions to answer |
| --- | --- |
| Authority and portability | What bytes are authoritative? Can another storage driver or deployment load them with the same continuation semantics? What depends on provider-private data, installation state, or environment identity? |
| Persistence boundary | When do input, model responses, effective tool calls, intents, results, and hook transitions commit? Can storage failure stop execution, or is saving best-effort? |
| Projections and clients | Who builds checkpoints and display views? How does replay join live updates? What happens to provisional output, lost notifications, slow readers, and changed authorization? |
| Ownership and execution | What fences writers? What stops, isolates, or reattaches already-started external work? Distinguish local writer locks, remote task identity, and distributed ownership. |
| Recovery and controls | What survives process loss? What can be rerun, with what evidence? How are steers, approvals, held results, and duplicate hook invocations handled? |
| Extensions and delegation | Which record types and state reducers are extensible? How are child sessions, usage, operation lifetimes, and missing interpreters represented? |
| Compaction, forks, and deletion | Does original history survive compaction? Is a fork independent or a branch selection? How do copying, source deletion, permissions, and retention interact? |
| Operational cost | How are record counts, reload progress, large payloads, checkpoints, and history growth handled? What guarantees change across backends? |

## Research sequence and evidence standard

Start with OpenCode and OpenHands V1 to verify the specific mechanisms already raised in the PR.
Then use Codex and Pi to compare local history, client/server boundaries, branching, and extensions.
Use ActiveGraph and LangGraph for focused questions about provenance and replay rather than expanding this into a general survey of agent frameworks.

For each project, the later research pass should produce:

- A short explanation of its authority, execution, and client-delivery model.
- A pinned upstream revision and links to the relevant code and tests, alongside documentation claims.
- A representative reconnect/crash/side-effect sequence showing what the implementation establishes and what remains unknown.
- Applicable lessons, non-transferable assumptions, and unanswered questions for Mecatl.

Keep documented promises, traced implementation behavior, and experimentally demonstrated behavior distinct.
An absent search result is not evidence that a feature or guarantee is absent.
No architecture decision follows from this discovery pass alone.

## Related information

- [Design proposal](session-commit-history.md).
- [Companion domain model](session-commit-history.modelith.md).
- [Draft index](README.md).
