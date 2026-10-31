# Execution kernel and capability boundaries

This design keeps execution in a small kernel whose invariants remain understandable and testable as capabilities change.
Trusted extensions act as drivers: they supply domain behavior through explicit interfaces while the kernel admits actions, orders transitions, and enforces the applicable contracts.
This is a responsibility boundary, not an OS isolation claim or a requirement to split the `engine` Go module.
An in-process extension is trusted code, not a sandboxed plugin.

Separating capability behavior from enforcement reduces coupled changes between history, features, and the loop.
The aim is explicit responsibility, not merely moving files or minimizing the kernel's line count.
See [capability composition](components.md#extensions-compose-capabilities-through-different-seams) for delegation versus learning and [ownership patterns](ownership.md) for stable data at the interfaces.

## What the kernel must retain

The kernel is the execution mechanism together with the narrow coordination needed to uphold its invariants.
Backend implementation, host composition, and subsystem policy remain separate responsibilities under the existing [design layers](../session-commit-history.md#design-layers-and-responsibilities).

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

## Coupled kernel responsibilities and selective resumability

Use `loop`, `coordinator`, and `session` as the working responsibility and package vocabulary defined in the [shared terms](../vocabulary.md#kernel-and-driver-vocabulary).
The loop and coordinator evolve together as kernel implementation, not replaceable drivers.
Host composition installs them alongside storage adapters; no public custom-coordinator compatibility contract is intended.
One active coordination authority governs each session history under root-tree ownership, without fixing Go object counts or package nesting.

Prefer ordinary Go control flow with explicit acceptance waits at commit gates.
The coordinator can perform asynchronous I/O and bounded batching internally; a waiting execution path must not block prompt cancellation signaling, independent ready results, or completion of already-admitted work.
Independent progress still obeys every prerequisite commit gate; it does not permit dependent execution or confirmed publication around unresolved acceptance.
Acceptance is distinguished from rejection and unresolved commitment; uncertainty must be resolved under the same transition identity before dependent work advances.
No session-ordering lock should be held across storage I/O in a way that prevents those obligations.

The selected append discipline permits only one unresolved append batch per session history.
Prepare a bounded batch against the accepted head, establish acceptance, and reflect accepted facts in session state before submitting the next batch.
Other session histories can commit independently; already-admitted tools can run concurrently and produce bounded pending output.
A slow read-only tool does not reserve an append slot: another read-only tool's permitted result can become ready and commit first.
This preserves existing concurrent read-only dispatch and mutate-serial barriers without speculative append pipelines.

The default state-update discipline is to check a proposed change against accepted state, establish commitment, then reflect those facts in memory.
These are ordering requirements, not mandatory `Validate`/`Apply` interfaces or a generic transaction framework.
Accepted facts cannot be abandoned after commitment; a crash before their in-memory application is repaired by reconstruction, not rollback.
Private computation and explicitly live controls remain distinct from the accepted-state snapshot.

Resumability is selective rather than a persisted program counter for every phase or function.
Committed facts reconstruct established knowledge, and supported durable waits retain their explicit continuation data.
Crash recovery interrupts active runs rather than reconstructing arbitrary Go stacks or automatically repeating uncertain effects.
That accepts loss of uncommitted progress, not loss of confirmed committed facts or permission to bypass execution gates.
Long-running work needs an explicit driver lifecycle because maintenance is routine even when crashes are rare.

## Tool-call admission, interruption, and execution evidence

Final tool admission and cancellation share the coordinator's local ordering domain.
If cancellation wins, the loop must not invoke the tool, even if an earlier uncertain intent append is later confirmed accepted.
If admission wins, subsequent cancellation requests interruption of admitted work rather than retroactively preventing it.
Admission is not proof that external execution began and does not require another durable write merely to mark the live decision.

In-flight cancellation support is optional.
A short tool invocation can simply finish rather than implement cooperative interruption; accepting a cancellation-bearing context does not promise that the tool will stop.
All tools remain subject to pre-admission cancellation and the kernel's bounded settlement policy.
The kernel closes further admission promptly, signals admitted work, and processes returned evidence/output normally; a tool that ignores interruption cannot force an unbounded shutdown or a false claim of cessation.
At a settlement deadline, preserve explicit uncertainty and fence invalid late advancement under the existing lifecycle rules.

Execution metadata is separate from response content.
The kernel records what it observes, such as admission, interruption requests, and return; a driver supplies additional execution evidence only where it can substantiate it.
Neither cancellation nor an error string proves that no external effects occurred.
Produced output still passes normal processing and disclosure checks before canonical result acceptance; it does not become history-safe because it accompanies execution metadata.
Metadata and persisted evidence also remain subject to the safe-evidence rules, so arbitrary errors cannot smuggle withheld output into history.
Concrete report types and any tool-interface changes remain open.

## Stable invocation contracts

Apply the [snapshot and ownership conventions](ownership.md) at the loop/coordinator/driver boundaries.
An invocation receives a detached, stable description of the exact effective action and its bound execution facts, not mutable session state or a reference that later configuration changes can rewrite.
Preserve representation-sensitive argument bytes and bindings; a stable contract does not justify normalization that changes their meaning or replay fidelity.
Cancellation and other explicitly live controls remain separate from that description; freezing action data neither freezes authority nor removes admission/freshness checks.
Execution-environment capabilities remain controlled access to live resources, not a promise that the external filesystem or process state is immutable.
Produced output handed back for processing has explicit independent ownership: the driver cannot mutate it afterward through retained aliases.
The boundary should make those guarantees locally recognizable and test input/output independence, rather than forcing callers to infer them from nested helper implementations.
This does not require a universal immutable wrapper or an immediate change to every tool signature.

## Capability seams in the engine

The source assessment on October 30, 2026 inspected checkout `e4f4f8519`, not the separate identity branch in #2404.
The source links locate implementations and seams as assessed then; they do not claim that the proposed drivers exist.
Learning, delegation, and compaction are covered in the [extension report](components.md).
Additional candidates are:

| Candidate | Existing seam and remaining coupling | Responsibility boundary |
| --- | --- | --- |
| Background Shell and ShellStatus | Command runners already come from `tool.Environment`, but process orchestration, status, output tails, and Shell-specific registry state live in [`shelltool.go`](../../../../engine/agent/shelltool.go), [`shellstatus.go`](../../../../engine/agent/shellstatus.go), and [`childregistry.go`](../../../../engine/agent/childregistry.go). | Keep generic supervision in the kernel and process/output behavior in a driver. This seam intersects delegation and durable operation design. |
| Scheduling tools | [`scheduletool.go`](../../../../engine/agent/scheduletool.go) already adapts an injected `ScheduleManager`; scheduler execution and delivery policy are host-owned. Tool origin, mode, and placement currently depend on private agent context in [`sessionorigin.go`](../../../../engine/agent/sessionorigin.go). | An optional capability package can consume a kernel-issued execution view. Preserve exact origin/placement and backend admission; do not infer authority from tool arguments. |
| Coding persona and context sources | [`prompt.Builder`](../../../../engine/prompt/prompt.go), [`InstructionAssembler`](../../../../engine/prompt/instructions.go), [`OperatorProfileSource`](../../../../engine/prompt/operatorprofile.go), and [`CommandSource`](../../../../engine/prompt/commandsource.go) already exist. Coding defaults and capability-specific instructions remain mixed into [`builder.go`](../../../../engine/prompt/builder.go) and composition. | Compose a coding profile and capability guidance using existing interfaces; leave trust, provenance, framing, bounds, and effective-request capture enforced. Do not flatten sources with different refresh lifetimes into one callback. |
| Structured deliverables | [`structuredoutput.go`](../../../../engine/agent/structuredoutput.go) owns a schema-validation tool and instructions, currently used by delegation; `RunRequest.ExtraTools` already supplies run-local tool overlays. | Extract a reusable deliverable capability using the overlay seam. Keep run isolation, corrective-run limits/accounting, and authority enforcement. Do not expose the existing private trusted-tool exemption as an extension option. |
| Model-routing and approval-review implementations | [`modelrouter.go`](../../../../engine/agent/modelrouter.go) and [`askadjudicator.go`](../../../../engine/agent/askadjudicator.go) contain concrete model prompts/implementations behind existing router and reviewer interfaces. Much contextual-review policy already lives in host adapters. | Consolidate implementations behind those interfaces rather than inventing new routing or approval machinery. Keep selector precedence, reviewer eligibility, exact-action binding, failure policy, and accounting outside experimental rubrics. |
| Progressive tool discovery | [`Catalog`](../../../../engine/tool/catalog.go) and [`ToolSearch`](../../../../engine/tool/toolsearch.go) already implement most mechanics, but the loop installs/selects disclosure behavior and dispatch recognizes the concrete search tool. | Useful for domain-specific discovery/ranking, but resolve authority-filtered hydration before replacing the implementation. Search results must not disclose definitions omitted by authority policy. This is an existing optional engine feature, not a claim of enabled production composition. |
| No-progress recovery wording/strategy | [`finishTurnNoTools`](../../../../engine/agent/loop.go) mixes bounded recovery nudges with pending-input/background obligations and provider stop handling. | A small replaceable policy, not necessarily a full installed extension. Permit bounded continuation suggestions or stop decisions; keep benign-stop classification, attempt ceilings, budgets, cancellation, pending-input obligations, and truthful terminal reasons in the kernel. |
| Request/debug evidence construction | [`request_manifest.go`](../../../../engine/agent/request_manifest.go) builds optional structural evidence; the loop captures it after compaction and before provider invocation. Existing diagnostic/event ports do not expose all selection facts. | A bounded observation component can own manifest construction. Supply the actual request basis and authoritative selection decisions rather than recomputing authority; do not turn optional debugger evidence into a second history authority or a default private-payload feed. |

Routing, review, discovery, and recovery are policy/implementation boundaries rather than reasons for a new extension framework.
Prompt-template slash commands are already a source/expander boundary; client API commands remain a different integration point.

`PresentPlan` couples presentation and workflow customization to approval machinery.
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

Drivers participate in shutdown/handoff through explicit lifecycle contracts, analogous to suspend/resume participation rather than general workflow replay.
They stop admission, account for active work, report settlement or blockers, and identify supported reconstructible safe points; the kernel/coordinator establishes the committed handoff state.
Not every driver supports parking before its invocation finishes.

A long-running command can execute in an external VM or sandbox with its own maintenance lifecycle.
Model it as a background job rather than keeping its launch tool call live for the process lifetime.
The launch tool call is short-lived: it establishes the job identity and returns an accepted started-result; monitoring or cancellation uses separate tool calls against that identity.
The job's later progress and terminal outcome belong to its operation lifecycle and never rewrite the launch call's canonical result.
An accepted started-result establishes launch, not successful completion of the command.

Harness-replica replacement and external-process maintenance are distinct concerns; the kernel must not presume that every command is a local child process or that every shutdown requires killing it.
Whether a driver can safely detach from a job and later reattach, and what durable identity, authority, result-retention, and fencing guarantees that requires, remain open.
The [later handoff contract](../session-commit-history.md#compatible-replica-active-tree-handoff-later-phase) still requires invocation settlement and every extension's reconstructible park point.
A completed launch call removes that invocation from the in-flight set, but does not by itself establish that the job's driver can park safely.

## History identity and textual IDs

The kernel uses a stable `HistoryID` for one linear, append-only history, independently of the kind of state reconstructed from it.
IDs are server-assigned, never reassigned after deletion, and do not encode a session, run, owner, or process lifetime.
Accepted heads cannot rewind; no separate history incarnation is needed.
This does not change existing client-assigned session IDs.

The agreed history ID format is exactly **25 ASCII characters**: `HST` followed by 22 independently and uniformly selected characters from the Bitcoin base58 alphabet (`123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz`).
Generate characters using cryptographically secure randomness and unbiased selection (for example, rejection sampling), not random-byte modulo 58.
This is a random string, not an encoding of an integer or UUID; no base58 encode/decode dependency or padding is needed.
Parsing requires only the exact case-sensitive prefix, body length and alphabet.
The alphanumeric form supports whole-ID selection and copying without separators.
There is no timestamp or checksum.

The body has approximately 128.88 bits of entropy; at one trillion generated IDs, the probability of any random collision is approximately `8.0 × 10⁻¹⁶`.
Creation must nevertheless be create-only: regenerate on an ID collision, never attach to or overwrite the existing history, and fail if secure randomness is unavailable.

The shared `engine/id` package owns the opaque comparable `ID`, registered `Kind` values, secure generation and strict parsing; unknown kinds are rejected.
The distinct `HistoryID` wraps an `id.ID` privately and accepts only the `HST` kind.
Its defining type belongs in the `kernel/history/id` leaf; `kernel/history` re-exports an alias and constructor/parser wrappers so [format DTOs can use the strong type without importing higher-level history contracts](codecs-and-dtos.md#public-dtos-private-codec-dependencies).
Both types expose their full spelling through `String`; their zero values are unset, not valid identities.
`id.New` and `history.NewHistoryID` return values directly and panic if secure randomness is unavailable; `id.New` also panics for an unknown kind.
Parsing returns an error for malformed input instead of panicking.
Keep domain-specific ID types distinct, rather than accepting any prefixed string interchangeably.
Commit content addresses have a separate format and integrity contract; they are not random resource IDs.

Only full IDs are authoritative.
Possible human-facing abbreviations must resolve uniquely within an explicit scope and are not another accepted `HistoryID` spelling.
Do not use truncated history IDs as persistent filesystem identities: keep paths shallow, avoid repeated IDs, or allocate short local names with exclusive creation and an explicit mapping to the full ID.

## Runtime isolation and reconstruction evidence

There is one authoritative runtime/storage path for a session, not dual writes, a fallback backend, format migration, or per-session hot switching.
The [delivery and cutover contract](../session-commit-history.md#delivery-boundary) owns integration constraints; development adapters do not establish interchangeability with legacy session bytes.

Acceptance-protocol behavior and physical durability are different claims.
An in-memory adapter can exercise accepted/rejected/unresolved outcomes without proving persistence across crashes or distributed ownership fencing.
Tests must exercise the actual kernel with controlled dependencies, including cancellation on either side of admission, independent read-only result readiness, mutation barriers, and snapshot independence.
For durable record handlers, full-history reconstruction and checkpoint-plus-suffix reconstruction must establish the same accepted state.

Package relocation alone establishes neither new recovery guarantees nor compatibility.
Go source, stored-data, and remote API contracts have separate compatibility obligations.
Missing configured security drivers follow their explicit failure policy rather than silently disabling enforcement; missing continuation-critical record handlers fail closed.
An optional capability is removable only where no unresolved authoritative state requires its interpreter or lifecycle handling.

The [history review packets](../README.md) own authorization, lifecycle, and storage mechanics; these boundaries explain where those contracts meet capability code.
