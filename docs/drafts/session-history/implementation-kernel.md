# Implementation note: execution kernel and staged capability extraction

**Status:** Architectural direction and source-based extraction candidates, not approved interfaces or an implementation task plan.

Treat execution as a small kernel whose invariants remain understandable and testable as capabilities change.
Trusted extensions act as drivers: they supply domain behavior through explicit interfaces while the kernel admits actions, orders transitions, and enforces the applicable contracts.
This is a responsibility boundary, not an OS isolation claim or a requirement to split the `engine` Go module.
An in-process extension is trusted code, not a sandboxed plugin.

Establish these boundaries before new history dependencies spread through today's capability implementations.
Otherwise the storage rework will require changing each feature and the loop together, making small, independently verifiable updates difficult.
The aim is to reduce coupled change, not merely move files or minimize the kernel's line count.
See [capability composition](implementation-components.md#extensions-compose-capabilities-through-different-seams) for delegation versus learning and [ownership patterns](implementation-ownership.md) for stable data at the interfaces.

## What the kernel must retain

The kernel is the execution mechanism together with the narrow coordination needed to uphold its invariants.
Backend implementation, host composition, and subsystem policy remain separate responsibilities under the existing [design layers](session-commit-history.md#design-layers-and-responsibilities).

| Kernel responsibility | What drivers can supply without owning enforcement |
| --- | --- |
| Session/run lifecycle, canonical identities, and valid tool-call/result pairing | Tools, domain operations, candidate outputs, and namespaced extension state |
| Admission, cancellation, exact-action authorization, deny/ask precedence, and freshness checks | Policy assessments and authorized approval workflows within explicit eligibility rules |
| Legal input/injection points, dispatch ordering, parent-mutation barriers, and final effective model requests | Domain context, prompt content, tool discovery strategy, and reduction strategies |
| Usage attribution, enforced resource limits, and honest terminal outcomes | Auxiliary model work, strategy-specific limits, and bounded recovery suggestions |
| Required commit gates, accepted-history reconstruction, and authorized publication through the coordinator | Versioned record reducers, approved display details, and bounded observations |
| Generic obligations for work owned by a run or session, including cancellation and settlement coordination | Process/delegation-specific execution, status, cleanup, and safe-point behavior |

Drivers propose work or transformations; the kernel validates and admits them.
That does not mean the kernel owns every subsystem decision: memory publication and skill activation remain governed by their owning subsystems.
A driver never gains those permissions by registering a tool, command, observer, or record handler.

Keep deterministic enforcement even when an assessment implementation is replaceable.
For example, a domain risk reviewer can change its rubric, but it cannot decide that a configured Ask is bypassable, or disclose a withheld result before the publication gate.
A tighter kernel must not achieve its size by exporting these invariants to optional callbacks.

## Candidates found in the current engine

This review inspected checkout `e4f4f8519`, not the separate identity branch in #2404.
The source links identify existing implementations and seams; they do not claim that the proposed drivers already exist.
Learning, delegation, and compaction are covered in the [earlier implementation note](implementation-components.md).
Additional candidates are:

| Candidate | Existing seam and remaining coupling | Direction and relative priority |
| --- | --- | --- |
| Background Shell and ShellStatus | Command runners already come from `tool.Environment`, but process orchestration, status, output tails, and Shell-specific registry state live in [`shelltool.go`](../../../engine/agent/shelltool.go), [`shellstatus.go`](../../../engine/agent/shellstatus.go), and [`childregistry.go`](../../../engine/agent/childregistry.go). | High kernel-reduction leverage, but a coupled extraction. Keep generic supervision in the kernel and process/output behavior in a driver. Define this seam early because it intersects delegation and durable operation design. |
| Scheduling tools | [`scheduletool.go`](../../../engine/agent/scheduletool.go) already adapts an injected `ScheduleManager`; scheduler execution and delivery policy are host-owned. Tool origin, mode, and placement currently depend on private agent context in [`sessionorigin.go`](../../../engine/agent/sessionorigin.go). | A relatively small pilot for an optional capability package and a kernel-issued execution view. Preserve exact origin/placement and backend admission; do not infer authority from tool arguments. |
| Coding persona and context sources | [`prompt.Builder`](../../../engine/prompt/prompt.go), [`InstructionAssembler`](../../../engine/prompt/instructions.go), [`OperatorProfileSource`](../../../engine/prompt/operatorprofile.go), and [`CommandSource`](../../../engine/prompt/commandsource.go) already exist. Coding defaults and capability-specific instructions remain mixed into [`builder.go`](../../../engine/prompt/builder.go) and composition. | High domain-specialization leverage. Compose a coding profile and capability guidance using existing interfaces; leave trust, provenance, framing, bounds, and effective-request capture enforced. Do not flatten sources with different refresh lifetimes into one callback. |
| Structured deliverables | [`structuredoutput.go`](../../../engine/agent/structuredoutput.go) owns a schema-validation tool and instructions, currently used by delegation; `RunRequest.ExtraTools` already supplies run-local tool overlays. | Extract a reusable deliverable capability using the overlay seam. Keep run isolation, corrective-run limits/accounting, and authority enforcement. Do not expose the existing private trusted-tool exemption as an extension option. |
| Model-routing and approval-review implementations | [`modelrouter.go`](../../../engine/agent/modelrouter.go) and [`askadjudicator.go`](../../../engine/agent/askadjudicator.go) contain concrete model prompts/implementations behind existing router and reviewer interfaces. Much contextual-review policy already lives in host adapters. | Consolidate implementations behind those interfaces rather than inventing new routing or approval machinery. Keep selector precedence, reviewer eligibility, exact-action binding, failure policy, and accounting outside experimental rubrics. |
| Progressive tool discovery | [`Catalog`](../../../engine/tool/catalog.go) and [`ToolSearch`](../../../engine/tool/toolsearch.go) already implement most mechanics, but the loop installs/selects disclosure behavior and dispatch recognizes the concrete search tool. | Useful for domain-specific discovery/ranking, but resolve authority-filtered hydration before replacing the implementation. Search results must not disclose definitions omitted by authority policy. This is an existing optional engine feature, not a claim of enabled production composition. |
| No-progress recovery wording/strategy | [`finishTurnNoTools`](../../../engine/agent/loop.go) mixes bounded recovery nudges with pending-input/background obligations and provider stop handling. | A small replaceable policy, not necessarily a full installed extension. Permit bounded continuation suggestions or stop decisions; keep benign-stop classification, attempt ceilings, budgets, cancellation, pending-input obligations, and truthful terminal reasons in the kernel. |
| Request/debug evidence construction | [`request_manifest.go`](../../../engine/agent/request_manifest.go) builds optional structural evidence; the loop captures it after compaction and before provider invocation. Existing diagnostic/event ports do not expose all selection facts. | A bounded observation component can own manifest construction. Supply the actual request basis and authoritative selection decisions rather than recomputing authority; do not turn optional debugger evidence into a second history authority or a default private-payload feed. |

The strongest new capability extractions are Shell supervision, scheduling tools, coding/context composition, and structured deliverables.
Routing, review, discovery, and recovery are mostly policy/implementation boundaries to finish separating rather than reasons for a new extension framework.
Prompt-template slash commands are already a source/expander boundary; client API commands remain a different integration point.

`PresentPlan` is a lower-priority candidate for presentation and workflow customization, not removal of its approval machinery.
Its dispatch interception, exact ask identity, terminal mode transition, and approval receipt are tightly coupled to enforcement.
Moving the tool specification alone yields little; a new control-action interface needs another concrete use case before generalizing.

## Interfaces that prevent coupled changes

Several current interfaces are nominally injected but still require same-package implementation details.
The compaction budget extension has an unexported method; scheduling tools consume private execution context; Shell and delegation share a broad private `parentCaps` structure and concrete child registry.
These are boundaries to redesign deliberately, not private types to export wholesale.

Give each driver the smallest execution capability it needs: immutable identity/placement facts, a bounded history read, a work-registration operation, or a candidate-submission interface.
Separate those capabilities rather than handing every driver the engine, session aggregate, backend store, or complete parent capability bundle.
Kernel-issued facts cannot be replaced by model-supplied origin or authority claims.
The snapshot/clone/borrow contracts should be consistent enough that a driver author need not trace the loop to discover data ownership.

Background work particularly needs a distinction between generic obligations and driver-specific state.
A Shell process has no child session and owns a bounded output tail; delegation owns child-session links; a schedule has a host-owned lifetime beyond the originating run.
A queued delivery is not an active-work registration, and cancellation is not proof that external effects stopped.
Do not force those lifecycles into a single process-shaped interface.

## Staging before and through the storage rework

Agree the responsibilities and evidence for each seam now, while keeping concrete interfaces subject to focused review.
This makes future updates separable without requiring every capability to be extracted before any storage foundation can land.
Prioritize boundaries that the history changes would otherwise cross; do not make unrelated cleanup a prerequisite for the new system.

Use the following progression as a staging discipline, not a fixed implementation backlog:

1. **Characterize the existing boundary.** Identify mutable ownership, authoritative inputs, lifecycle obligations, and failure behavior. Identify separately any exported Go API, persisted record/checkpoint schema, and client-visible command or projection that the seam creates or changes, each under its own compatibility contract. Establish behavioral tests before moving responsibility.
2. **Introduce or complete a narrow seam against the current runtime.** Preserve configured behavior and composition defaults. Exercise the existing implementation through that seam; do not introduce a speculative universal driver API.
3. **Move one implementation and its state behind the seam.** Keep package relocation separate from algorithm changes, archive-format changes, and new recovery guarantees. A small pilot such as scheduling can validate execution-view conventions; high-coupling work such as Shell/delegation needs its own focused boundary review.
4. **Build new history foundations independently.** Keep session-history reads, candidate transitions, and durable extension records behind their owning contracts rather than adding storage-specific branches to every driver. A driver can use an explicitly selected runtime implementation of its seam without selecting or owning the backend itself.
5. **Bind the new runtime explicitly.** Integrate and qualify each required capability against committed-history semantics and fault cases. Make compatibility and lifecycle changes visible rather than treating them as incidental extraction details.

A behavior-preserving package extraction can be integrated into the existing runtime after review; the instruction to defer new-runtime binding does not prohibit that preparatory work.
New-runtime integration still obeys the existing [delivery and cutover boundary](session-commit-history.md#delivery-boundary).
There is one authoritative runtime/storage path for a session, not dual writes, a fallback backend, format migration, or per-session hot switching.
Development adapters do not promise that legacy and new session bytes are interchangeable.

Each stage needs evidence appropriate to its claim: unchanged observable behavior for a pure extraction, ownership-independence tests for snapshot interfaces, and lifecycle/authority/fault-injection tests for new coordination.
For durable record handlers, compare full-history reconstruction with checkpoint-plus-suffix reconstruction; never test only a happy-path callback.
Review Go source, stored-data, and remote API compatibility separately before changing public contracts.
Small PRs can span modules when required to keep the selected source buildable; small must not mean landing incompatible interfaces and leaving consumers broken.

## What is not being added

This direction does not expand the first cutover to require every candidate extraction, learning recovery, or cross-run background execution.
The first cutover still requires registered in-process handlers for all three built-in families: `Subagent`, `Parallel`, and `Team`.
No dynamic plugin loader, unrestricted event/request interceptor, service split, module split, or generic workflow runtime is selected.
Missing configured security drivers must follow their explicit failure policy, not silently disable enforcement; missing continuation-critical record handlers still fail closed.
An optional capability is removable only where no unresolved authoritative state requires its interpreter or lifecycle handling.

The next design step is to select the high-coupling seams needed for storage integration and define their observable contracts before decomposing implementation work.
The [review packets](README.md) continue to own unresolved history, authorization, lifecycle, and storage mechanics; this note identifies where those decisions should meet capability code, not a competing specification.
