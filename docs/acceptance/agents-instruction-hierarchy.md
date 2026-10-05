# AGENTS instruction hierarchy - acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — changes instruction applicability, pre-dispatch ordering, source mapping, and project-context freshness.
**Decision record:** [ADR 0374](../adr/0374-agents-instruction-hierarchy.md)
**Phase:** Target-scoped project instructions
**Status:** draft, 2026-10-05. Design for human review; no implementation approval.
**Delivery:** Split. Plan/interface PR first; implementation follows explicit human approval. The operator requested an implementation PR stacked on the plan PR. Before implementation, record the approved plan commit and explicit approval of that pre-merge checkpoint; otherwise plan merge remains the approval event.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#2090](https://github.com/stacklok/mecatl/issues/2090).

For a concrete file target, automatically deliver the applicable admitted source's
root and nested `AGENTS.md` before executing the model's decision. Preserve
non-conflicting ancestors, frame local conflict precedence explicitly, and keep
sibling instructions scoped. A new target can require a second model decision;
reading guidance after an edit is insufficient.

The reader is the reviewer approving behavior and interfaces. This plan owns the
proposal. The implementation will update the existing
[public instruction guide](../../user-docs/features/project-instructions-and-rules.md)
and [context architecture](../architecture/context-and-compaction.md). Those guides
must not describe this proposal as implemented in the plan PR.

The human-decision checklist gives concrete recommendations, not recorded human
approval. Unchecked items intentionally keep this plan draft.

## Human decisions

- [ ] Approve whole-batch reconsideration when new or changed applicable guidance was absent from the generating request, including new-file writes and same-batch reads/mutations.
- [ ] Approve the bounded guarantee: structured file tools are covered; Shell, MCP/custom tools, ListDir, Glob, and Grep remain opaque. Their ordinary authorization stays unchanged; the model and operator receive an explicit coverage limitation rather than a claim that arbitrary Shell writes are covered.
- [ ] Approve lexical, source-relative hierarchy and trusted subtree mapping, including blocking covered out-of-root operands instead of silently editing without applicable guidance.
- [ ] Approve project-chain refresh at request and action boundaries, replacing the root-only/per-run contract only for the new scoped integration. Other ephemeral sources retain their lifetime.
- [ ] Approve limits of 32 distinct batch targets, 64 directory components, 64 KiB per instruction file, and 256 KiB total framed project instructions, with visible failure rather than truncation or fallback.
- [ ] Retain existing project-trust admission, first-encounter probing, and remembered-trust anchor policy; AGENTS-only repositories require an existing explicit trust mechanism if no current probe triggers a prompt.
- [ ] Approve additive bounded-read transport operations, coordinated MicroVM runtime support, and fail-closed behavior with older runtimes; the external MicroVM implementation/release is a delivery dependency, not satisfied by an offline daemon fixture.
- [x] Reuse existing contracts instead of parallel interfaces — Decision: the operator requested reuse and minimal additions. Extend the existing assembler and manifest, reuse operand extraction and bounded reads, and keep one `Deps.Instructions` path; no new interfaces or instruction result types.
- [ ] Approve the exact breaking assembly signature, metadata fields, and lifetime/migration rules below, including hierarchy through RootAssembler and global custom-source refresh.

## Interface contract

- **gRPC / protobuf:** No public harness RPC or inspection endpoint. Add `FILE_OPERATION_READ_BOUNDED = 12` to `mecatl.execution.v1.FileOperation`, reusing the existing `FileRequest.limit = 8` field as a byte maximum for this operation only. Existing numbers, field types, and ordinary READ semantics remain unchanged. Native execution adds `executionenv.OpFileReadBounded Operation = "file.read_bounded"` and reuses its existing `FileRequest.Limit`; no new protobuf or native request field. The MicroVM private workspace request has no limit field, so add operation `read_bounded` and `MaxBytes int64` (`json:"max_bytes,omitempty"`) there. Both additions follow the bounded transport contract below; regenerate protobuf/reference output in implementation.
- **Exported Go APIs / interfaces:** Extend existing `InstructionAssembler.Assemble` and its manifest-aware path with explicit target input, extend `InstructionManifest` with scope metadata, and update the existing `RootAssembler` as declared below. Reuse `tool.LocalFileOperands`, `tool.WorkspaceReader`, and `tool.BoundedWorkspaceReader`. `agent.Deps.Instructions` remains the only assembly dependency. No new interface or instruction result type; this deliberately changes the existing exported assembly API rather than adding a compatibility pipeline.
- **Tool schemas:** None — existing input fields and JSON schemas remain unchanged. Use `tool.LocalFileOperands` only for the covered built-in names Read/Edit/Write/Remove (`path`) and Copy/Move (`source`, `destination`). Deliberately exclude its Shell branch and ListDir from activation. Preserve the helper's existing behavior for its other callers. Validate the complete required operand set before accepting coverage: the helper's empty or partial return on malformed input is not success. No command parsing or arbitrary JSON-field inference supplies targets; custom/MCP tools remain opaque. Deferral uses ordinary error tool results with the fixed reason token `instruction_context_changed`, saying the calls did not execute and require reconsideration.
- **CLI / config:** None — no user/model source-path selector, feature toggle, or new YAML key. Existing trusted source registrations implement the revised assembly contract; target-independent sources return global contributions. Production repository registrations use the updated RootAssembler. Existing instruction source order, exclusions, and `combine`/`replace` policy remain operator-owned.
- **Events / persistence:** None — scoped bodies, active target sets, and visibility fingerprints are ephemeral. Ordinary paired deferral results may persist, but contain no instruction bodies. Existing request manifests retain kind/provenance/byte accounting. Injected diagnostics report safe logical scope metadata; no new durable event is introduced. The run owns the bounded active-target/fingerprint state and releases it at termination. No watchers, background refreshers, or persistent scope cache.
- **Security / authority:** Resolve only selected and admitted sources under their retained binding. Project context stays user-role/project provenance. Explicit user instructions override repository guidance, never system/developer safety or tool authorization. Target metadata cannot register or select a source, open host ancestors, change placement, or mint execution ReadLedger evidence. Admission is not a synthetic Read tool invocation and does not bypass existing source authorization. Deny/Ask, effective-argument checks, CAS, create-only writes, and child restrictions remain independent.
- **Compatibility / migration:** Extend the existing assembler and discovery APIs in place. Their target parameters, RootAssembler source type/behavior, and refresh/error rules are deliberate exported-engine changes requiring updated call sites, API snapshots, and a classified Changed changelog entry. External implementations accept explicit targets (ignoring them if global) and forward them through wrappers. No deprecated assembler, old-signature shim, frozen root-only discovery API, or parallel assembly dependency. Nil/empty targets retain the root case within the same implementation. Preserve root AGENTS-first/blank-or-missing CLAUDE fallback and exclusion of nested CLAUDE. ADR 0374 partially supersedes ADRs 0359 and 0043 and the root-only/per-run assertions of the harness-context acceptance record. Extend existing discovery and RootAssembler tests with hierarchy scenarios rather than maintaining legacy variants. No stored-session migration and no rewriting frozen decisions.

### Existing engine contracts to extend

Reuse `tool.LocalFileOperands` and `tool.BoundedWorkspaceReader`; neither needs a
new interface. Keep `agent.Deps.Instructions` as the single composition input,
`InstructionAssembler` as the single assembly contract, and messages plus aligned
`InstructionManifest` rows as the result. The exact changed declarations are:

```go
// engine/prompt: change the existing assembly contract.
type InstructionAssembler interface {
    Assemble(ctx context.Context, targets []string) ([]session.Message, error)
}

// Extend the existing metadata; no second instruction result type.
type InstructionManifest struct {
    Kind       string
    Provenance string
    SourceID   string // trusted registration ID, or engine-assigned composition identity
    SourcePath string // canonical source-relative instruction file
    ScopePath  string // canonical execution-relative directory; "." includes the root
}

// Keep the existing concrete source-bound assembler.
type RootAssembler struct {
    Source       tool.WorkspaceReader
    TargetPrefix string // trusted source-relative directory; empty means "."
}

func (a RootAssembler) Assemble(ctx context.Context, targets []string) ([]session.Message, error)
func (a RootAssembler) AssembleWithManifest(ctx context.Context, targets []string) ([]session.Message, []InstructionManifest, error)
func AssembleWithManifest(ctx context.Context, a InstructionAssembler, targets []string) ([]session.Message, []InstructionManifest, error)
func DiscoverInstructions(ctx context.Context, ws tool.WorkspaceReader, targets []string) ([]session.Message, error)
```

Every existing implementation's `Assemble` and optional `AssembleWithManifest`
method gains the same `targets []string` parameter, including `MultiAssembler`,
`SoulAssembler`, `MemoryIndexAssembler`, `RulesAssembler`, `UserModelAssembler`,
and the internal registration, policy, and generation wrappers. Target-independent
leaves ignore it. `MultiAssembler` retains its existing fields and constructor;
wrappers forward targets without altering source order, admission, or ownership.
There is no new tool interface, reader interface, assembly interface, result type,
assembly context object, or second `Deps` instruction field.

Coverage follows the effective dispatch registration, not just the call's name.
Use the existing run-overlay/catalog resolution: an `ExtraTools` shadow is opaque,
even when named `Write`. Registering a catalog tool under one of the six covered
built-in keys is the trusted composition's explicit commitment to that key's file-
operand semantics; custom tools with different semantics must use different keys.
No new registration mechanism is introduced. Do not call `Spec()` again to infer
coverage; use existing registration keys and overlay resolution information.

Targets are sorted, deduplicated, canonical slash-separated execution-relative
operands. The engine rejects empty operands, escapes, and invalid paths before
assembly; absolute operands are normalized as described below. A nil/empty slice
means no file-specific target: load the source-root-to-`TargetPrefix` chain that
governs the execution root. Sources remain construction-bound; target input cannot
select a workspace. Do not hide targets or caches in `context.Context`.

`RootAssembler.Source` narrows from `tool.Workspace` to the existing read-only
`tool.WorkspaceReader`. A nil source still contributes nothing. For a non-nil
source, hierarchy reads require its existing optional `BoundedWorkspaceReader`
capability, use `ReadVersionBounded`, and discard the version without entering a
ReadLedger. Read-only source wrappers forward that bounded capability without
exposing mutation or runner capabilities. No unbounded fallback is allowed.
`DiscoverInstructions` gains target input and uses the same hierarchy discovery
with identity mapping. `RootAssembler` supplies its trusted prefix to that shared
implementation. Empty targets exercise the root case, not a retained legacy path.
The existing name denotes the admitted source root; it does not limit discovery
to that root's single file.

RootAssembler's value and pointer views retain instruction/project provenance.
Registration wrappers stamp `SourceID` and validate provenance before accepting
scope metadata; a fixed driver registration cannot promote project content. For
direct engine embeddings without a registration ID, the engine assigns a stable
within-run composition-position identity. Empty `SourcePath` and `ScopePath`
identify a global contribution. Scoped rows require both fields, project provenance,
and user-role text messages; reject malformed metadata before canonical scope
framing. Keep every message paired with exactly one manifest row, including global
operator/driver messages in their existing representation and tier.

### One composition path, run-owned static snapshots

The run traverses the existing `MultiAssembler` value/pointer tree in order and
snapshots direct `SoulAssembler`, `MemoryIndexAssembler`, `RulesAssembler`, and
`UserModelAssembler` leaves once per run. Cache by composition position, not content
or shared adapter identity, in private run-owned state. Preserve message/manifest
pairs together. An initial static-leaf error is warned once and cached as an empty
contribution for that run; discard partial results. There is no shared mutable
cache on assemblers, source bindings, or `Deps`, and no exported cache API.

Every other leaf, including the existing opaque selected-source policy/generation
wrapper, is assembled at each specified boundary with the current targets. The
engine must not unwrap that wrapper to cache individual selected sources: admission
and whole-source combine/replace still execute inside the existing composition.
A selected source that produces global guidance remains part of that live chain,
even if its internal implementation uses a target-independent assembler. General
custom assemblers migrate to this boundary-refresh contract; no compatibility
pipeline is added to retain their old signature or implicit per-run caching.

Publish refreshed fragments and their manifests atomically after successful
assembly. Provider requests, request-token accounting, request manifests, and
contextual action-review instruction facts must consume the same effective view.
Scope metadata is assembled even when durable request evidence is disabled; that
flag controls evidence emission, not activation correctness. A hierarchy/source
failure stops affected work as specified below, rather than using stale guidance.
Discard static snapshots, active targets, and visibility evidence at run end;
children and subsequent runs create their own state.

Repeated assembly must not retain another child generation hold on every call.
The existing child/binding lifecycle owns one retained hold per active child run,
released on completion or cancellation; each assembly uses a separately balanced
short-lived observation borrow. Do not install a new cancellation callback per
refresh or deduplicate across children with a shared `sync.Once`. Parent retirement
still waits for existing holders, and final cleanup waits for in-flight observations.
This is internal lifetime accounting, not a new source or assembly interface.

Contextual reviews combine shared genuine task/plan-approval facts with the current
run's action-applicable instruction facts. Do not overwrite shared `reviewRoot`
instructions with a child scope or keep the initial project slice as an additional
stale fact. Keep local instruction facts separate from shared task state and bind
cached reviews/repeat approvals to the existing action digest plus run identity and
its effective instruction fingerprint. At execution admission, check both the
existing shared principal revision and that local fingerprint. An effective change
invalidates affected local evidence; an identical refresh does not increment a
revision or cause a review loop. Concurrent parent/sibling scope changes must not
replace one another's facts. These are private state changes using the existing
review request/fact types, not new exported interfaces.

Operator-profile facts already refresh through `Deps.OperatorProfileSource` into
the volatile system suffix. Preserve that path and its existing failure behavior;
do not move it into instruction-fragment caching. The static `UserModelAssembler`
rule above applies only to embeddings explicitly using that existing adapter.


### Resolution and mapping

Within one source, probe the source root and each ancestor directory of
`TargetPrefix/<target>` through its parent. Preserve nonempty root and ancestor
content in root-to-leaf order. The deepest applicable file wins **conflicts only**;
ancestors still govern non-conflicting concerns. Probe root `CLAUDE.md` only if root
`AGENTS.md` is missing or whitespace-only. A genuine read error stops resolution.
Blank/missing nested AGENTS contributes nothing and never selects nested CLAUDE.

Each scope is framed with its trusted source ID, logical instruction filename,
execution-relative applicability, and within-source precedence. A directory scope
matches on path-component boundaries. Root/ancestor scopes above `TargetPrefix`
map to execution scope `.` while retaining their ordered source paths; descendants
map by removing that trusted prefix. Equal bytes in distinct scopes remain distinct.
Shared hierarchy ancestors are deduplicated by source ID and logical source path,
not content. Global messages are never path-deduplicated: retain every
message and matching manifest row in original order.
Sibling scopes are emitted in lexical path order and explicitly do not govern one
another. The source's entire applicable contribution then participates in the
existing `combine`/`replace` algorithm; directory depth never outranks another
source's operator-selected precedence. `replace` is evaluated on the complete
requested target set, not independently per target.

The compatibility registration explicitly uses identity mapping between selected
source-relative and execution-relative paths. Different physical roots are valid;
they do not establish or deny mapping. A session rooted at `repo/website` with a
source explicitly admitted at `repo` uses trusted `TargetPrefix: "website"`.
If only `repo/website` was admitted, resolution stops there. No upward host walk
looks for another root or infers a Git repository. A non-filesystem implementation
can implement the revised `InstructionAssembler` and its existing optional
manifest-aware method directly over equivalent logical keys.

The engine normalizes relative operands and in-root absolute operands against the
execution namespace's existing root semantics, then passes only relative paths to
the resolver. Escaping `..` and out-of-root absolute operands on covered tools fail
with a coverage error; even broad Shell/posture authority cannot silently waive
this structured-tool contract. Normalization never opens the host filesystem.
The source reader enforces symlink containment on the opened object, including
ancestor-directory symlinks, and rejects escapes and cycles. In-root aliases keep
lexical applicability: `alias/file.go` uses `alias/AGENTS.md`, not an additional walk
of the physical target's parents. The source backend, not core code, resolves links.
Directory Move/Remove operations resolve the **parent scope of each operand**;
this is namespace-operation guidance, not recursive loading of descendants.

### Activation and lifecycle

1. Before the first provider request, assemble root project guidance. Before later
   provider requests, re-resolve the bounded active target set. Render fresh
   ephemeral project context alongside the unchanged other fragments. Record which
   scopes and bytes this exact request exposed to the model.
2. Before dispatching a model batch, collect covered built-in operands through
   `LocalFileOperands`, validate the complete required set, and resolve their union.
   Existing denial and
   argument validation still apply. If a covered action requires any added,
   changed, removed, or differently selected applicable instruction compared with
   its generating request, execute **none of that batch**, pair every call with a
   not-executed result, replace the active target set, and request a new decision.
   This includes unrelated calls in a mixed batch: no hidden partial execution on
   the initial hierarchy deferral. Model turns and tokens remain charged normally.
3. If guidance was already visible for the requested operands, dispatch through
   ordinary read-parallel/mutate-serial ordering. Target-set narrowing alone does
   not require a retry when all applicable guidance is identical. Keep a batch
   composition target union, including completed-prefix operands until the batch
   ends. Before each covered mutation, recompute the full union's source selection
   after effective-argument rewriting, replacing the rewritten call's operands in
   that union, then compare the mutation's applicable projection with its generating
   request. Compare source identity, source path, scope, precedence order, provenance,
   and rendered message representation, not content bytes alone. A change defers
   that mutation and the still-unexecuted batch suffix; already completed calls
   remain completed. Deleting or editing AGENTS in an earlier call therefore cannot
   leave later mutations using stale guidance. No rollback or filesystem transaction
   is claimed. Rechecking a service operand must not choose a fallback source merely
   because a website operand that selected the higher source already completed.
4. On deferral **or successful covered-batch validation**, the next request uses
   that batch's effective target union, replacing previous active scopes rather
   than accumulating them. Keep the generating request's immutable visibility
   evidence separate from this next-request active set. Thus narrowing from website
   plus services to website alone removes services even without a deferral. A new
   sibling requires new guidance; both remain only when both are targeted.
   Empty/opaque-only batches retain the current bounded set, framed
   with its limited applicability. The protocol instruction tells the model to
   use covered tools for edits and not to interpret stale transcript references as
   active guidance.
5. Approval continuation repeats this check before execution. An in-memory request
   fingerprint can prove visibility only within that run. After restart or reopen,
   no such proof survives: a pending covered mutation is closed as not executed,
   current guidance is supplied, and a new model decision passes normal permission
   handling. Old approval does not approve revised arguments. Compaction never
   absorbs the ephemeral scopes; reassembly restores them on the next request.

Opaque calls retain ordinary permissions and tools. Shell command parsing is not
an applicability oracle, including apparently simple scripts. Model instructions
and the public guide explicitly limit the guarantee to covered operands and advise
Read of a concrete target before a package-specific Shell operation. This is
context preparation, not proof of every path the command can touch. A content-safe
once-per-run diagnostic reports opaque filesystem-tool coverage; no capability is
added to no-FS sessions merely to prepare context.

### Bounded transport contract

The existing MicroVM `read` and native execution READ return whole-file payloads;
a client-side length check cannot satisfy this plan's bound. The additive
`read_bounded` operations read a confined regular file at the execution side with
a maximum `0 < bound <= 65536`, allocate at most `bound+1` raw file bytes, and
return all content or an error. Native execution carries that byte bound in the
existing `limit` field; MicroVM carries it in `max_bytes`. Existing list/grep
uses of `limit` retain their item-count meaning. A distinct operation is necessary:
an older server could ignore a limit on ordinary READ and perform an unbounded
read, whereas an unknown bounded-read operation is rejected. The new operations
never return a successful prefix. Responses reuse
existing data/version fields; automatic discovery discards versions. JSON/base64
and protobuf envelope overhead remains covered by existing transport frame limits.
Client decoding must apply a bound derived from the requested maximum before
materializing response data, and reject oversized or malformed responses.

MicroVM returns existing `not_found` for absence and adds `too_large` for content
exceeding the requested bound, `invalid_argument` for an invalid maximum, and
`unsupported` when the backend cannot perform a confined bounded read. Native
execution uses existing NotFound, InvalidArgument, and ResourceExhausted error
classes. Neither path returns content with an error. Unsupported/unknown operations
fail closed, including old daemon/provider versions; there is no retry as ordinary
`read`, host-file fallback, or Shell-based emulation. Existing unbounded read callers
retain their contract. New servers still accept old operations, and new scoped
clients require the bounded operation before they can use execution-backed context.

Carry the native operation through provider and executor boundaries with the same
verified owner, placement, lease/fence, path confinement, cancellation, and read-only
authorization checks as READ. Grant issuance includes this new read-only operation
only for callers already eligible for file reads; an unknown operation is never
implicitly authorized. MicroVM uses the exact existing acquisition/binding checks.
Tests cover rejected stale bindings and unauthorized calls, not just byte limits.

The MicroVM serving runtime is an external dependency. Its coordinated bounded-read
implementation and compatible managed release must be available before claiming
execution-backed hierarchy completion. Offline transport fixtures qualify the
Mecatl client, not that external runtime. The implementation records an immutable
runtime revision and its backend-side bounded-read conformance evidence; missing
runtime support blocks issue completion rather than silently narrowing the claim.
Independent admitted sources with MicroVM execution do not depend on this addition.

### Failure, budgets, and evidence

Probe only ancestor candidate paths, never recursively enumerate a repository.
Maximums are 32 unique operands in one batch, 64 directory components after prefix
mapping, 64 KiB raw bytes per file, and 256 KiB aggregate UTF-8 framed project
context across selected contributions. Duplicated ancestor probes are shared within
one observation. The aggregate counts source IDs, scope framing, and static project
contributions, not only raw bodies. Exactly-at-limit succeeds; above-limit fails.
A bounded reader must reject oversized content before allocating more than its
limit plus one. No `Stat` followed by unlimited `Read` substitutes for this bound.
All probes receive cancellation; no retries/watchers are created by the resolver.

Missing and whitespace-only files are normal absence. A successful read of an
opened file remains a valid observation even if its pathname was concurrently
unlinked. A later `fs.ErrNotExist` observation is absence and changes the visibility
comparison. Other read faults, containment, unsupported-reader, malformed-source-
metadata, and limit failures abort scoped assembly, never select another namespace
or silently fall back to a lower-precedence source. Discard partial bytes returned
with any error. Reject invalid UTF-8 before trimming, framing, fingerprinting, or
token estimation; raw-file limits apply before text validation and framed limits
afterward. No partially assembled hierarchy reaches the provider. A scoped assembly failure
before inference stops that run; a failure after calls were emitted pairs the
unexecuted calls with safe errors before stopping. A new user run can retry.

Before provider submission, include all framed guidance in the existing complete
request token estimate. After the normal compaction attempt, if the complete request
plus configured output allowance exceeds the resolved window, stop visibly rather
than discard scopes or send an over-budget request. Churning instructions can force
reconsideration until existing turn/token budgets stop the run; never bypass the
gate to make progress. A file can change after the final observation: this plan
promises ordered observation/reconsideration, not atomicity with external writers.

Injected diagnostics report selected/omitted/error outcomes with trusted source ID,
source-relative filename, execution-relative scope, bytes, and bounded reason
codes. Paths are escaped, length-bounded, and secret-scrubbed; raw backend errors,
absolute host paths, contents, and content hashes are excluded. Existing visible
run/tool errors explain how to recover (fix access/size, use an in-root target, or
retry after source repair). Persisted history does not accumulate automatically injected instruction bodies;
explicit Read tool results keep their existing history behavior. Source exclusion
and untrusted admission are recorded
without reading rejected files. Request manifests retain project provenance and
accurate byte counts; no new public inspection API is required.

## In scope — 7 scenarios, in implementation order

### Scenario 1 — Hierarchy reaches the actual provider request

Extend [existing discovery](../../engine/prompt/builder.go) and RootAssembler in
place under [ADR 0374](../adr/0374-agents-instruction-hierarchy.md); root and nested
cases share the same target-aware implementation.

**Acceptance:**
- AC1.1: A nested target automatically receives root, package, and deeper AGENTS in order, with nearest-conflict framing and explicit user-priority framing, without a manual instruction-file read or root pointer.
  - verify: `TestADR_0374_AgentsHierarchy_Scenario1_ProviderHierarchy`
- AC1.2: Missing/blank root AGENTS uses root CLAUDE, genuine errors do not, and nested CLAUDE never contributes. DiscoverInstructions and RootAssembler resolve root and nested targets through the same extended discovery. Root and nested messages retain project provenance, including when a fixed driver registration tries to promote them.
  - verify: `TestADR_0374_AgentsHierarchy_Scenario1_FallbackCompatibility`
- AC1.3: Sibling-only transitions replace active scopes; a two-target request frames both independently, deduplicates shared hierarchy ancestors, and preserves identical text from different scopes and all global messages. Narrowing `[website, services]` to `[website]` removes services even without deferral. Operator source combine/replace precedence remains separate from directory precedence.
  - verify: `TestADR_0374_AgentsHierarchy_Scenario1_SiblingsAndSourcePrecedence`

### Scenario 2 — Guidance precedes the decision that executes

Preserve [dispatch and permission invariants](../../AGENTS.md#implementation-boundaries)
while adding reconsideration before effects.

**Acceptance:**
- AC2.1: Read/Edit in one model batch, Edit after an earlier Read, and new-file Write all defer before effects when applicable guidance was unseen. Provider request recording and effect counters prove that only a later model decision executes; every call has one result.
  - verify: `TestADR_0374_AgentsHierarchy_Scenario2_BeforeMutation`
- AC2.2: Copy and Move resolve both operands; Remove and directory namespace operations use operand-parent scopes. An invalid operand cannot turn the gate off. Initial deferral executes no mixed-batch tool.
  - verify: `TestADR_0374_AgentsHierarchy_Scenario2_MultiPathAndMixedBatch`
- AC2.3: Effective-argument rewrites, permission Ask/deny, and changed guidance during approval cannot execute an unreviewed target; a post-dispatch change closes only the unexecuted suffix. An AGENTS edit earlier in a batch forces fresh guidance for later mutations. Under `replace`, a source contributing only website guidance continues to win over a fallback root source when rechecking the service mutation in the same website/service batch. Changed guidance invalidates affected cached reviews/approvals; unchanged refresh does not. Concurrent parent/child review facts stay scoped to their own run.
  - verify: `TestADR_0374_AgentsHierarchy_Scenario2_ReentryAndEffectiveArguments`; `TestADR_0374_AgentsHierarchy_Scenario2_RunScopedReviewFacts`
- AC2.4: Opaque Shell/custom/MCP calls preserve ordinary permission outcomes, do not infer paths from payloads, and carry the coverage limitation through the real factory's model-visible protocol instruction and delivered diagnostics. A run overlay named Write remains opaque; covered catalog registrations follow the explicit built-in operand contract without extra Spec calls.
  - verify: `TestADR_0374_AgentsHierarchy_Scenario2_OpaqueCoverage`

### Scenario 3 — Source authority is independent of execution

Retain [ADR 0359](../adr/0359-harness-context-source-authority.md)'s source admission
and placement separation.

**Acceptance:**
- AC3.1: Different selected-source and execution roots, virtual sources, explicit subtree mapping, and equivalent in-root absolute/relative operands resolve identical logical instructions. Poisoned unselected host/execution files never appear; traversal stops at the admitted source boundary.
  - verify: `TestADR_0374_AgentsHierarchy_Scenario3_SourceMapping`
- AC3.2: Escapes, symlink cycles, swapped symlinks, and out-of-root absolute operands fail without reading outside the selected source. In-root aliases use the documented lexical scope. Automatic source reads never authorize Edit/overwrite, including when source and execution share storage.
  - verify: `TestADR_0374_AgentsHierarchy_Scenario3_ContainmentAndLedger`
- AC3.3: Independent no-FS context still supplies root/global guidance without advertising file tools; a required execution-backed source cannot invent storage. A logical non-filesystem scoped source satisfies the same ordering and limit contract.
  - verify: `TestADR_0374_AgentsHierarchy_Scenario3_NoFSAndLogicalSource`

### Scenario 4 — Children retain source authority and independent scope state

Preserve [ADR 0359](../adr/0359-harness-context-source-authority.md)'s
[existing inheritance proofs](../../internal/app/harness_context_regression_test.go)
and [binding lifetime](../../internal/app/harness_context_generation_test.go).

**Acceptance:**
- AC4.1: Ordinary isolated/direct-write Subagent, Parallel, and parented Team workers map their relative targets through the inherited parent source binding, never poisoned child checkout instructions. Each child assembles its own bounded active set. Specialist/internal-reviewer exclusions and no-FS attenuation remain effective.
  - verify: `TestADR_0374_AgentsHierarchy_Scenario4_WorkerInheritance`
- AC4.2: Concurrent children, repeated boundary refreshes, child cancellation, and parent retirement preserve source-borrow ownership until the final holder releases. Each child run retains at most one lifetime hold/cancellation callback, with balanced per-observation borrows and no growth proportional to refresh count. Scopes from one run cannot affect another. Cold reopen rebinds current authorized context rather than recovering a host anchor from execution paths.
  - verify: `TestADR_0374_AgentsHierarchy_Scenario4_BindingLifetime`

### Scenario 5 — Refresh survives continuation without persisted instruction bodies

Partially supersede [ADR 0043](../adr/0043-ephemeral-turn0-instruction-fragments.md)
only for scoped project context.

**Acceptance:**
- AC5.1: Created, changed, blanked, removed, and renamed instruction files are observed at the next specified boundary; counted static leaves assemble once per run while selected-source wrappers refresh without bypassing admission or replacement. Independent runs sharing the same assembler instances have isolated snapshots. Static-leaf failures warn once and contribute no partial results; live hierarchy failures stop affected work. Compaction and new runs receive current applicable guidance without fragment accumulation in snapshots or event-folded history. OperatorProfileSource retains its independent per-request refresh.
  - verify: `TestADR_0374_AgentsHierarchy_Scenario5_RefreshAndCompaction`; `TestADR_0374_AgentsHierarchy_Scenario5_SingleChainLifetimes`
- AC5.2: In-process approval continuation revalidates visibility; restart/reopen of a pending covered mutation requires a fresh model decision with current source bytes, retains valid pairing, and does not reuse permission approval for changed arguments.
  - verify: `TestADR_0374_AgentsHierarchy_Scenario5_ApprovalRestart`
- AC5.3: The run-owned static snapshots, active set, and fingerprints have explicit cleanup and restart-loss decisions in ADR 0027's maintained inventories; no watcher or durable scope cache is introduced.
  - verify: inspection — review the run owner and both ADR 0027 inventory rows against implementation.

### Scenario 6 — Failures and incomplete coverage are visible

Use [injected diagnostics](../architecture/observability.md), separate from durable
events and audit records.

**Acceptance:**
- AC6.1: Exact/over-limit target count, depth, file bytes, aggregate framing bytes, invalid UTF-8, cancellation, unreadable files, concurrent unlink, partial reads with errors, unsupported bounded reads, and containment faults produce the specified absence/error outcomes with no partial provider context or mutation. Oversized growth does not allocate an unbounded read.
  - verify: `TestADR_0374_AgentsHierarchy_Scenario6_BoundedFailures`
- AC6.2: Context-window exhaustion after compaction stops before provider submission, preserves pairing, and never truncates applicable guidance. Repeated source churn obeys the ordinary run budget.
  - verify: `TestADR_0374_AgentsHierarchy_Scenario6_RequestBudget`
- AC6.3: Selected, excluded, untrusted, opaque, and error outcomes reach the configured diagnostic destination with safe logical metadata and no bodies/host paths/raw backend errors. AGENTS-only repositories demonstrate explicit-trust admission and unchanged remembered-anchor/probe policy, including headless non-trust by posture alone.
  - verify: `TestADR_0374_AgentsHierarchy_Scenario6_EvidenceAndTrust`
- AC6.4: Native provider/executor and MicroVM client conformance cover exact/over-limit bounded reads, invalid maxima, oversized response decoding, old-server rejection without fallback, and unchanged ordinary reads. Unauthorized owners and stale acquisition/fence state cannot use the bounded operation.
  - verify: `TestADR_0374_AgentsHierarchy_Scenario6_BoundedTransports`

### Scenario 7 — Offline conformance and documentation match the guarantee

Follow [ADR 0374](../adr/0374-agents-instruction-hierarchy.md),
[engine compatibility](../../engine/COMPATIBILITY.md), and the
[documentation ownership contract](../development-process.md#documentation-change-review).

**Acceptance:**
- AC7.1: A factory-to-provider offline conformance fixture exercises a fresh Write decision, sibling transition, source/execution separation, and approval continuation with real dispatch through the single Deps.Instructions chain. Scope delivery and action-review facts remain correct with durable evidence enabled or disabled; emitted request manifests match the provider's fragment view. Existing source/execution, generation, and MicroVM harness-context proofs remain green; real client/placement integration against an offline MicroVM protocol fixture includes no-FS, child poison, restart, and ledger separation. This does not prove the external runtime implementation.
  - verify: `TestADR_0374_AgentsHierarchy_Scenario7_FactoryConformance`; `TestADR_0374_AgentsHierarchy_Scenario7_MicroVMConformance`
- AC7.2: The owning public guide documents exact scope, precedence, refresh, limits, trust prerequisites, and opaque-tool limitations; context architecture reflects the shipped lifecycle. API snapshots and classified changelog entries match the approved declarations. Structural provider tests claim delivery/framing/order, not arbitrary live-model obedience.
  - verify: inspection — review the two owning pages and API/changelog diff; run `task api:check`, `task docs`, and `task site:build`.
- AC7.3: The compatible MicroVM runtime release implements confined bounded reads at the backend, including concurrent file growth, link containment, cancellation, and stale binding rejection. Completion evidence identifies its immutable revision and qualification results; a fixture alone cannot satisfy this criterion.
  - verify: inspection — review the external runtime's bounded-read conformance evidence and pinned managed release in the implementation PR; missing evidence blocks completion.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| On-demand path-scoped rule files | [#1974](https://github.com/stacklok/mecatl/issues/1974) | Reuse a compatible activation boundary later; retain current rules behavior here. |
| Real Redis instruction/command qualification | [#1811](https://github.com/stacklok/mecatl/issues/1811) | Shared-source conformance does not claim Redis integration is complete. |
| AGENTS.override.md, imports, new home/global conventions | Separate proposal | Not established by the upstream contract. |
| General Shell affected-path inference or sandboxing | Separate proposal | Explicitly outside this issue's structured-target guarantee. |
| Persisted scope snapshots, watchers, public inspection API | None | No demonstrated need; use run state and existing diagnostics. |

## Definition of done

1. Human decisions are resolved and the exact plan commit is approved before implementation.
2. Focused scenario and integration tests pass offline with isolated stores and user-model directories; `task test`, `task lint`, and `task test:race` pass on the assembled implementation.
3. `task api:update` output and classified changelog entries are included; `task api:check`, `task docs`, `task site:build`, and `task ac-trace-strict` pass when marked landed.
4. `go run ./cmd/mecademo` demonstrates tool call, permission ask/approval, and result.
5. `/panel-review` has no ship blockers or unwaived failures. The implementation PR identifies the approved plan commit, any expressly approved amendments, and the stacked base if used. Humans merge both PRs.

## Deferred decisions and known risks

Only private factoring and test-fixture organization are deferred. Material changes
to these interfaces or limits require an amendment. Reconsideration costs extra
model turns, especially for sibling transitions or churning files. Lexical alias
scope and namespace-only directory moves intentionally do not inspect all physical
descendants. Opaque commands remain a documented gap in target discovery. External
writers can race the last observation; instruction guidance is not transactional
filesystem authorization.
