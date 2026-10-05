# ADR 0374 - Target-scoped AGENTS instruction hierarchy

- Status: Draft; human decisions remain open in the acceptance plan
- Date: 2026-10-05
- Scope: project-instruction applicability, source-relative mapping, and pre-action context
- Supersedes: proposed partial supersession of [ADR 0359](0359-harness-context-source-authority.md)'s root-only discovery and project-instruction freshness, and [ADR 0043](0043-ephemeral-turn0-instruction-fragments.md)'s once-per-run project-fragment assembly
- Superseded by: none

## Context

[AGENTS.md](https://agents.md/) specifies automatic nearest-file discovery and
closest-file conflict precedence. Explicit user requests override repository
guidance, subject to the harness's higher-priority safety and authorization rules.
The upstream specification does not choose activation timing, resource budgets,
source mapping, or refresh semantics for a harness. Those are Mecatl decisions.
The source snapshot cited by [issue #2090](https://github.com/stacklok/mecatl/issues/2090)
is upstream commit `d001185d792eb6402a58e4cbef1c228b309ec25d`.

The current root-only assembler cannot deliver package-specific guidance for a
late-discovered target. Eagerly loading all nested files would mix unrelated
sibling instructions and make context cost proportional to repository size.
Loading guidance after a mutation cannot guide the decision that produced it.

Instruction authority is already independent of execution placement. A selected
source can use local files, a virtual workspace, or independent storage. A child's
checkout must not replace its parent's admitted source. Hierarchy must extend that
contract rather than reopen an execution root through the host filesystem.

## Proposed decision

The [acceptance plan](../acceptance/agents-instruction-hierarchy.md) owns exact
interfaces, limits, error outcomes, and proofs. Its unchecked decisions require
human review before implementation; the following is the recommended design.

### Extend the existing composition

Pass explicit targets through `InstructionAssembler`, `MultiAssembler`, and the
existing manifest-aware path. Extend `InstructionManifest` with applicability and
source metadata. Extend `RootAssembler` and `DiscoverInstructions` in place;
root and nested cases use one discovery implementation. Reuse `WorkspaceReader`
and `BoundedWorkspaceReader` for source reads and `LocalFileOperands` for covered
built-in targets. Keep `Deps.Instructions` as the only assembly dependency.

The signature change is intentional. No deprecated root assembler, compatibility
shim, parallel scoped interface, or second instruction result type is needed.
Static built-in fragments are snapshotted in private run state by their position
in the existing MultiAssembler tree; the selected instruction-source wrapper
remains live and retains its admission, provenance, and combine/replace logic.
Targets are ordinary arguments, not hidden context values or shared mutable state.

### Resolve directory scopes inside an admitted source

Use the source's logical root and a trusted execution-to-source subtree mapping.
For each concrete operand, probe only its source-relative ancestors. Retain
non-conflicting ancestors and give the most local file precedence on conflicts.
Frame each contribution with its logical source, scope, and precedence. Compose
whole source contributions under the existing operator combine/replace policy;
directory depth never changes source priority.

Preserve the root AGENTS-first/CLAUDE-fallback rule. Nested discovery reads only
AGENTS. Keep source-relative lexical aliases as distinct scopes, with source-backend
symlink containment on every opened file. A target cannot select a source, authorize
an ancestor outside its boundary, or create execution read evidence. A session
starting below a repository root sees higher ancestors only when that broader root
and its subtree mapping were explicitly admitted.

### Reconsider actions when guidance was not visible

The engine reuses `LocalFileOperands` for the covered built-in file tools,
validating the complete operand set and excluding Shell inference. It resolves
the whole batch before effects. If applicable instructions
were absent or different in the generating request, close the batch with paired
not-executed results and ask the model for a new decision with current guidance.
This covers first-write creation and same-batch Read/Edit without relying on the
model to remember a manual discovery step.

Revalidate effective operands and current guidance before each covered mutation,
including approval continuation. If a change is discovered after earlier calls
completed, close the unexecuted suffix rather than pretend to roll back effects.
Permission approval and instruction visibility are separate facts. A restarted
run cannot recover ephemeral visibility proof from old tool arguments or approval;
it must obtain a new model decision before a pending covered mutation executes.

Shell, custom/MCP tools, and search/listing tools retain ordinary authorization.
Their effects cannot be exhaustively inferred from command strings or arbitrary
JSON keys. State this coverage limit in model instructions, operator diagnostics,
and the owning guide. The hierarchy protocol is a guidance-delivery guarantee for
covered operands, not a filesystem sandbox or proof of live-model obedience.

### Refresh bounded ephemeral project context

Keep the most recent batch's bounded target set on the run, replacing it at scope
transitions. Assemble its applicable project chain before inference and recheck it
at action boundaries. Direct soul, memory-index, rules, and user-model assembler
leaves retain per-run snapshots; the host's operator-profile facts retain their
separate per-request system-suffix refresh. The exact custom-source migration and
static-leaf failure behavior are specified in the plan. Automatically injected
project instruction bodies never enter persisted
conversation, events, or snapshots; explicit Read tool results retain their normal
history contract. Compaction preserves the genuine user conversation and reassembly
supplies current scoped context independently.

Use source-owned bounded reads and fixed traversal, file, aggregate, and target
limits. MicroVM and native execution need additive bounded-read operations so the
bound is enforced before file allocation at the backend. Unsupported older runtimes
fail closed; ordinary read behavior remains unchanged. The external MicroVM runtime
implementation and compatible managed release are explicit delivery dependencies,
with real backend qualification distinct from Mecatl's offline protocol fixtures.
Missing and blank candidates are normal absence. Genuine read failures,
containment failures, unsupported bounds, and budget exhaustion stop affected work
visibly; no silent truncation or alternative namespace fallback is permitted.
A complete request that cannot fit after compaction is not sent to the provider.

No watch service or durable cache is needed. The run owns target/visibility state;
existing source-binding owners retain acquisition and release responsibilities.
The implementation records the bounded run state and deliberate restart loss in
[ADR 0027](0027-cloud-native.md)'s maintained resource and fidelity inventories.

### Preserve trust and explain selection safely

Keep current project admission, remembered-trust anchors, and first-encounter
probing unchanged. AGENTS-only repositories do not gain an implicit trust grant;
operators use existing explicit trust mechanisms when the current probe does not
prompt. A headless posture is not a project-trust grant.

Use injected diagnostics for logical scope selection and omission, with bounded,
escaped, secret-scrubbed metadata. Keep instruction bodies, content hashes, raw
backend errors, and host paths out of that evidence. Existing request manifests
continue to account for provenance and bytes; no public inspection service is
needed to establish this contract.

## Alternatives

Root-only discovery leaves the documented package-instruction task unsupported.
A recursive eager scan costs repository-wide context and loses sibling isolation.
Model-only advice to read instructions does not cover first-write or same-batch
mutations. Post-tool instruction injection arrives too late. Parsing Shell cannot
provide a complete affected-path set, while blocking all opaque commands would
change tool authority far beyond this issue.

A persisted instruction snapshot would need revocation, migration, and child
snapshot semantics. Rebinding current authorized sources and requiring a new
model decision after restart is smaller and preserves ephemeral instruction
ownership. It costs an additional decision even when a recovered file is unchanged.

## Consequences

Normal package work receives local guidance without a root pointer or explicit
instruction-file read. New scopes and source churn can cost extra model turns.
Sibling isolation is explicit in request framing, but a language model can still
misinterpret guidance; structural tests prove delivery and ordering, not obedience.

External writers can change files after the final observation. The contract does
not provide a transaction between source reads and execution. Directory namespace
operations use operand-parent guidance without scanning all descendants. Opaque
commands retain a documented gap in automatic target discovery.

The implementation extends existing engine contracts instead of deprecating or
duplicating them. API snapshots and a classified Changed changelog entry record
the target-aware assembly/discovery signatures, scope metadata, and behavior.
Callers and adapters migrate together; there is no root-only compatibility branch.
Frozen predecessor ADRs remain intact; this record narrowly replaces their
incompatible discovery and freshness decisions if approved.
