# ADR 0374 - Target-scoped AGENTS instruction hierarchy

- Status: Draft; exact interfaces and discovery/retention ceilings remain open in the acceptance plan
- Date: 2026-10-05
- Scope: project-instruction applicability, admitted-source mapping, and ephemeral context
- Supersedes: proposed partial supersession of [ADR 0359](0359-harness-context-source-authority.md)'s root-only discovery and [ADR 0043](0043-ephemeral-turn0-instruction-fragments.md)'s once-per-run project guidance
- Superseded by: none

## Context

[AGENTS.md](https://agents.md/) describes nearest-file conflict precedence. User instructions outrank repository guidance, subject to Mecatl's higher-priority safety and tool authorization. The upstream specification does not choose when a harness loads guidance, how it budgets discovery, or how it maps a target onto an operator-selected source. [Issue #2090](https://github.com/stacklok/mecatl/issues/2090) cites upstream commit `d001185d792eb6402a58e4cbef1c228b309ec25d`.

Root-only guidance misses instructions for packages encountered later in a run. Eager recursive loading would consume repository-wide context and mix sibling guidance. A strict pre-effect visibility protocol, however, would turn advisory instructions into a second execution gate with batch retry and approval machinery. The operator authorized best-effort delivery instead: new scope instructions inform the *next* model decision, without retroactively controlling an already authorized tool call.

Source authority remains separate from execution placement. A selected source may be local, virtual, or independently stored. A child checkout or inferred host parent cannot replace the admitted source or grant a native execution run read access.

## Proposed decision

The [acceptance plan](../acceptance/agents-instruction-hierarchy.md) records authorized choices and the still-unchecked exact interfaces and numeric discovery budget. This ADR is draft; it does not state implemented behavior or full contract approval.

### Use one admitted composition path

Extend the existing `InstructionAssembler`, manifest-aware assembly, `RootAssembler`, and `DiscoverInstructions` in place to carry encountered targets. Keep one `Deps.Instructions` path and existing source registration, trust admission, ordering, exclusions, and whole-source `combine`/`replace` policy. Reuse `LocalFileOperands` from [PR #2095](https://github.com/stacklok/mecatl/pull/2095) for the covered structured file tools. The composition seam must handle value and pointer `MultiAssembler`, `RootAssembler`, and `RulesAssembler` forms without dropping unrelated contributors. Do not add a parallel legacy assembler, new instruction result type, or redundant effective-call deferral interface. Exact exported signatures, manifest fields, and wrapper migration remain open in the plan.

### Discover lexical scopes inside the selected source

Load guidance from the admitted source root through the starting folder for the first provider request. Covered structured file tools then reveal additional directory chains within the same trusted source/subtree mapping. Probe ancestors within that source only, without recursive scanning, upward host walks, or Git-root inference. Preserve nonconflicting ancestors and give the nearest directory precedence on conflicts. Within *each* directory, prefer nonblank `AGENTS.md`; use `CLAUDE.md` when AGENTS is missing or blank. A genuine read error cannot select CLAUDE as a fallback. Frame sibling scopes independently so one sibling does not govern another; lexical in-root aliases keep lexical applicability, while the source backend confines its reads.

Source precedence is separate from directory precedence. Whole-source `replace` selects on the retained target set, not separately for each target. If source A has only `website/AGENTS.md` and lower-priority source B has root guidance, a retained website-and-services view can select A and omit B entirely. Services gets no B fallback in that view, although a services-only run can select B. The website instructions still apply only to website. The implementation must disclose this consequence in diagnostics and the owning public guide, rather than promise per-target fallback. `combine` keeps operator source order. Target strings can discover scopes, but cannot select a source or mint ReadLedger evidence.

### Deliver guidance on the next request, not as an execution gate

The initial root-to-starting-folder chain and newly encountered scopes appear as ephemeral project context in provider requests. Automatic discovery does not write guidance bodies into persisted tool results, history, or snapshots; an ordinary explicit Read retains its normal tool result and history. Keep encountered scopes within the run subject to bounded context rather than replacing them after each batch. A new run starts against current admitted sources, and compaction does not turn automatic fragments into conversation messages.

New guidance need not have appeared in the request that generated the current batch. Same-batch Read/Edit and first-touch Write, Copy, Move, or Remove may execute before it reaches the model. Do not defer the batch, retry instruction-specific calls, recheck guidance before each effect, or bind approval to instruction visibility fingerprints. Existing permissions, effective-argument authorization after rewriting, read-before-edit, CAS, create-only writes, and read-parallel/mutate-serial dispatch remain authoritative. Instructions are guidance, not a filesystem sandbox or authorization boundary.

Shell, MCP/custom tools, and search/listing operations do not supply exhaustive structured affected-path discovery. A real factory must give the model a visible limitation and encourage structured tools for concrete scope discovery; do not infer shell effects from command strings. Neither chat mentions nor opaque-only tasks promise nested activation.

### Bound instruction loading without blocking tool execution

Use a 32 KiB **total instruction-content** starting default for automatically selected contributions. Truncate safely on UTF-8 boundaries or omit excess text, label partial/omitted scopes in model context, and warn the user through existing injected diagnostics/projection surfaces. Do not admit invalid UTF-8, traverse outside the selected root, or perform unbounded discovery. Count candidate reads, including missing and blank files, and establish a finite numeric discovery-work limit from adapter-cost evidence before plan approval; the earlier speculative 4,096/16,384 counts are not adopted. At a work or content bound, stop further automatic instruction loading, not ordinary authorized tool execution. Keep already encountered scopes as far as the approved retention policy allows. The exact retention/eviction rule and discovery ceiling remain unchecked decisions in the plan.

Use existing source read operations and transport envelope protections. Content truncation after a read is not a backend preallocation bound; this design does not add bounded-read protobuf/native/MicroVM operations or require an external MicroVM release. Read faults, containment problems, and unavailable sources must be reported without silently switching to host storage or executing instruction-specific denial. Ordinary provider context-window handling still applies to the complete request; it is distinct from an instruction discovery ceiling.

Native execution-backed instructions require existing binding and active run access. A missing BindingID or a reattached workspace without a run grant makes that selected source unavailable; warn and continue best-effort authorized work. Enabling and qualifying native source reads through the actual authorization and factory-to-provider path is a separate issue/dependency before claiming native support. It is not a completion gate for this hierarchy and does not authorize a run-authority redesign.

### Preserve trust and explain selection

Retain project trust admission, remembered anchors, and first-encounter probing. AGENTS-only repositories gain no implicit trust grant; headless posture is not project trust. Keep scope metadata logical, bounded, escaped, and scrubbed in existing injected diagnostics, without instruction bodies, host paths, or raw backend errors. Existing request manifests continue to account for emitted provenance and bytes. No new inspection API, durable event, watcher, or shared scope cache is required. Run-owned retention and restart loss must be reflected in [ADR 0027](0027-cloud-native.md)'s maintained inventories during implementation if they outlive a call.

## Alternatives

Root-only discovery does not cover later package work; eager recursive discovery adds unrelated sibling text and repository-scale cost. Model-only advice to read guidance cannot guarantee a first-touch edit is informed, but this best-effort proposal deliberately accepts that gap in exchange for ordinary tool progress. A strict pre-effect retry protocol would require whole-batch deferral, per-effect freshness review, and instruction-specific approval evidence despite guidance having no authorization role.

Parsing Shell commands cannot reliably enumerate paths; blocking opaque calls would change existing authority. New bounded-read transport operations and an external runtime release would add implementation and deployment dependencies without providing access to native sources lacking a binding or run grant. Persisted instruction snapshots would require revocation and child/restart semantics that ephemeral context avoids.

## Consequences

Starting-folder instructions appear on the first request; later scopes are encountered through covered tools and delivered in subsequent requests, subject to measured discovery work and bounded content. Retention keeps earlier encountered guidance available but cannot promise completeness after truncation or omission. Whole-source replacement can leave a sibling without lower-priority fallback. Structural tests establish selection and delivery, not model obedience.

A tool may mutate its target before its local instructions are delivered. Source reads and external writers are not transactional with file effects. Existing backend reads may materialize a whole file before client-side truncation, and unavailable native sources need separate enablement. The implementation updates the existing exported engine contracts, API snapshots, and classified changelog only after the open interface decisions are approved; frozen predecessor ADRs stay intact.
