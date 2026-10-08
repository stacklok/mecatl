# Memory and learning

Mecatl remembers across sessions in two stores: **project memory** holds facts about one
workspace, and the **user model** holds facts about the operator across every workspace.
**Consolidation** (dreaming) tidies either store. **Evidence-backed reflection** proposes
new facts and skills from completed work, and only promotes them under strict rules. For
what operators see and configure, read [Memory and knowledge](../../user-docs/features/agent-behavior/memory.md).

## One contract, two stores

`tool.MemoryStore` is the seam for both stores. Every write is compare-and-swap (CAS: the
write succeeds only if the caller names the exact current state). Remembering without an
expected version only creates; updating, forgetting, and undoing need the exact opaque
current version, which the model copies from a Recall, Inspect, or mutation receipt.
Forget appends a tombstone and Undo appends a compensating revision, so history is never
rewritten. Each implementation must pass `engine/adapter/memconformance`.

| Store | Model tools | Scope |
| --- | --- | --- |
| Project memory | `Remember`, `Recall`, `SearchMemory`, `InspectMemory`, `ForgetMemory`, `UndoMemory` | One workspace |
| User model | `RememberUser`, `RecallUser`, `SearchUserModel`, `InspectUserMemory`, `ForgetUserMemory`, `UndoUserMemory` | One operator, all workspaces; keys under `user/` |

`engine/adapter/memorytools` implements both tool sets. Built-in permissions allow every
memory tool except the two Forget tools, which ask: using its own memory isn't a
workspace mutation. Configuration can tighten any of them.

The local adapter, `internal/adapter/memory`, keeps one JSON document per directory behind
a cross-process file lock and writes it by temp file and rename, so a crash can't truncate
it and concurrent processes can't lose updates. Fixed ceilings bound each locked
transaction. When retention drops a key's oldest revision, Undo stops at that boundary
rather than mistaking the oldest kept revision for the key's creation. Composition refuses
to start when the two stores' directories resolve to the same place, symlinks included.

## How memory reaches the model

- **Project memory** contributes a value-free index (keys, descriptions, update times) as
  an ephemeral turn-0 fragment through `prompt.MemoryIndexAssembler`. It is bounded in
  entries and bytes, never persisted, and the model calls `Recall` for a value it needs.
- **The user model** is reloaded before every provider request and rendered by
  `engine/prompt` as an `<operator-profile-data>` block in the volatile system suffix. It
  never enters conversation history or the cache-stable prompt prefix, so a fact written
  mid-run shows up on the next request. The block states that facts are data, that a
  current user instruction wins, and that facts can't change permissions or tools. Main,
  subagent, and team engines get it; internal reviewer, router, and judge engines don't.
  If a reload fails, the run keeps its last good snapshot.

## Safety at the memory boundary

Memory is model-written text that returns in later prompts, so it is untrusted both ways.
`tool.CanonicalMemoryText` repairs UTF-8 and strips invisible, bidirectional, and control
characters before classification or rendering. Writes reject secret-shaped values and
enforce the namespaced key grammar; user-model facts also reject instruction-shaped text.
The operator-profile renderer repeats these checks for imported or remote facts.

## Scoping and multiple callers

Without ownership enforcement, a process has one project store per workspace and one user
model, both belonging to whoever runs the process. With ownership enforcement on
(see [Deployment and hardening](deployment-and-hardening.md)), `memory.NewCallerStore`
partitions both stores by verified caller, and project memory also by workspace. In that
mode composition refuses a remote memory driver or a remote learning store, because
neither carries a caller namespace, and manual dreaming is unavailable.

## Consolidation

`internal/adapter/dream` separates planning from mutation. Planning sends a bounded,
rotating window of whole entries (values are never truncated) to the configured model and
accepts only a strict JSON plan of two operation types: retire exact duplicates, or
replace several entries with one synthesized entry. Each plan binds the exact versions it
inspected, so a concurrent edit turns into a conflict instead of a lost write.

- **Automatic consolidation** runs on separate opt-in schedules for each store. It applies
  only byte-identical duplicates, through the local store's atomic retirement operation,
  never rewrites the survivor, and skips stores that lack that operation.
- **Manual `/dream`** shows every proposed operation, including synthesis, for one
  whole-plan apply or dismiss. Plans live in a small process-local registry and expire
  after a few minutes, so a restart or a request routed to another replica means
  generating a fresh plan. Operations apply independently, so a receipt can be partial.

Settings and the review workflow are in [Dreaming and memory consolidation](../../user-docs/features/agent-behavior/dreaming.md).

## Evidence-backed reflection

Reflection lets Mecatl learn from finished work without letting a model rewrite its own
memory or skills on a hunch. Every proposal must cite evidence from the source session,
every step is bounded and durable, and anything that can't be verified fails closed.
`engine/learning` holds the storage-neutral domain; `internal/app` composes it. Modes and
budgets are in [Learning](../../user-docs/features/agent-behavior/learning.md).

1. **Admission.** `learning.ThresholdPolicy` scores signals from the verified current span
   of a main session that ended cleanly: repeated correction, trusted-host contradiction,
   failure recovery, a repeated stable tool sequence, substantial success. A genuine user
   prompt that explicitly asks to remember or learn a procedure admits directly. Text from
   the model, tools, or repository never counts as intent. Explicit `/reflect` bypasses
   automatic admission and accounting but keeps every other control.
2. **Budget.** A durable automatic-admission ledger applies cooldown, count and token
   windows, and trajectory deduplication across cooperating processes. The ledger
   backend owns the policy and the clock, so a client can't widen its own limits.
3. **Attempt.** Admission creates a deterministic `learning.AttemptRecord` that references
   the source session and run; it never copies the transcript. One worker discovers
   queued and expired-claim attempts from the `AttemptRepository`, holds a renewable
   claim while it works, and resumes after a restart. The repository is the only queue.
4. **Evidence.** `learning.MaterializeEvidence` selects whole tool turns (a message, its
   calls, and their results) by priority, projects them without reasoning, raw tool
   arguments, credentials, or binary data, and seals the selection in a digest-bound
   manifest.
5. **Reflection.** `agent.EvidenceReflector` makes one provider call with no tools, no
   filesystem, and no persistence, inside the untrusted-content fence. Its parser rejects
   the whole response on any malformed, unknown, or unsafe content. Abstaining is a valid
   result.
6. **Staging and promotion.** Proposals go to a `learning.ProposalRepository`. The
   `memorypromotion` policy never overwrites a different or user-explicit fact. `auto`
   mode promotes a fact only when it cites the user's own explicit remember request in
   the current run (project facts also need the exact trusted root); everything else
   stages for review. Procedures go to the skill lifecycle (`skilllifecycle.Pipeline`):
   an evaluator FAIL or error rejects, and activation needs a PASS (`evaluated`) or,
   under `validated`, an evidence-backed ABSTAIN. Review mode always stages.

**What it may change:** memory facts, through ordinary CAS writes tagged with the proposal
ID, and body-only learned skills published per caller and project through
`skillfs.AtomicCatalog`. **What it may not change:** permissions, tools, workspace files,
or skill assets. A project's settings can lower learning autonomy or require `evaluated`
activation; they can never raise either.

### Failure modes

- Missing, unauthorized, mismatched, or compacted-away evidence ends the attempt as
  `evidence_unavailable` before any proposal or skill changes.
- A source run whose final event hasn't landed yet stays claimed with backoff, and becomes
  `retry_exhausted` after three failed setup claims instead of retrying forever.
- Proposal detail and approval re-derive the exact manifest from the source session; a
  changed source fails the precondition and never promotes.
- A batch can promote partially: each candidate converges on its own, and a crash during
  promotion reconciles by proposal ID without writing twice.
- Once an attempt exists, its ledger charge stays even if reflection fails, times out, or
  abstains, so failures can't spend past the budget.

Local learning stores (attempts, ledger, proposals, learned skills) live under the
user-model directory, which the learned-skill store resolves even with the user model
disabled; that's why tests inject a temporary `UserModelDir`
([test isolation](../../.claude/rules/test-isolation.md)).

## Related

- [Agent loop](agent-loop.md)
- [Governance](governance.md)
- [Extensibility](extensibility.md)
- [Memory and knowledge](../../user-docs/features/agent-behavior/memory.md)
- [Learning](../../user-docs/features/agent-behavior/learning.md)
