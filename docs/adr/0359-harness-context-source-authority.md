# ADR 0359 - Harness context source authority is independent of execution

- Status: Proposed amendment to the decision approved in #1814; directing-user choices resolved, exact interfaces await approval by merge; implementation has not landed
- Date: 2026-09-24
- Scope: project instructions, commands, rules, skills, agent definitions, source admission, and deployment composition
- Supersedes: none
- Superseded by: none

## Context

Mecatl has logical source ports for commands, rules, skills, and agent definitions. Their adapters
can use files, APIs, databases, or registries. Root instruction assembly and directory-command
invocation instead receive the execution `Workspace`, making execution placement an implicit
source-selection decision.

PR #1799 fixed one consequence: a Redis workspace's virtual root was being reopened as a host
filesystem path. Preserving its adapter was correct, but it did not establish whether execution
files should be the source of harness instructions. PR #580 selected a host composition root for
command discovery while invocation still used the execution workspace. Neither path should infer
source authority from a backend kind or physical location.

The execution model remains unchanged. An `Environment` binds a `Workspace`, a session-scoped
`ReadLedger`, and an optional `CommandRunner` that accesses the same logical files. The harness
consumes separately configured instructions and customizations. Those responsibilities can share
storage without becoming the same capability.

## Proposed decision

### Separate selection, permit shared storage

`HarnessContext` names the deployment-configured composition of admitted instruction and
customization sources used by a session, with explicit content-kind-specific resolution rules.
It is a domain concept, not a requirement for a new exported Go container, registry, or provider
framework.

Sources may read APIs, host files, databases, or files in the execution environment. Reading
execution files is valid when composition explicitly configures that source. The source uses
the real backend capability; it never treats a virtual `Workspace.Root()` as a host directory.

This distinction has two observable cases:

- With an independently configured source, conflicting instructions written only into the
  execution namespace do not enter automatic harness discovery or assembly.
- With an explicitly execution-file-backed source, admitted files there supply context and their
  updates follow that source's freshness contract.

The latter is intentional sharing, not an exception for MicroVM or Redis. A local MicroVM
deployment can select host files, while a Kubernetes deployment can select APIs or other drivers.
Changing the execution backend alone does not select another context source.

### Acquire a selected session source by exact authority

Two sessions can share an owner and profile while using different execution
worktrees. A binder receiving only that owner/profile pair cannot select the
correct worktree. Capturing one workspace before session creation proves backend
preservation but does not establish dynamic per-session acquisition.

A trusted registration explicitly declares whether it needs session execution
files. Only selected, admitted registrations receive a lazy capability capturing
the server-authorized source session and exact environment reference. The
capability accepts no root or selector. It supplies read-only files and a
source-owned release, not the execution owner's cleanup, a runner, or read evidence.
Independent sources never need to exercise it. During creation it can borrow an
authorized provisional binding before the session is persisted; failed binding or
publication releases attempt-owned resources without destroying retained execution.

The [interface contract](../acceptance/harness-context.md#interface-contract)
defines the internal request, registration, acquisition, and lifetime signatures.
It keeps execution workspaces out of the engine's per-call prompt interfaces.

### Show the context selected for the session

Skill and agent inventories are session-effective, like command discovery.
Human discovery and model consumption borrow the same resolved binding and retain
its winning metadata/body pairs. Principal sources are never published by adding
them to a process-wide snapshot. Learned skills use the same session-specific
catalog and owner/project partitions as the Skill tool, without rediscovering
external sources.

Existing inventory RPCs require the target session ID and authorize ownership
before binding. Empty IDs fail instead of selecting global defaults. The client
uses its active session and discards stale replies after switching sessions.
Inventory membership describes admitted definitions, not available execution tools
or permission to invoke them. This allows a restricted native session to inspect
context without enabling delegation. No extra global inventory API is introduced.

### Compose sources and resolve each content kind explicitly

Source selection can include several sources of the same content kind. A helpdesk agent in
Kubernetes can combine instructions mounted into mecak8s with instructions, rules, or skills
provided by gRPC services implementing the source contracts. It needs no execution repository.

A coding agent can combine deployment instructions, organization skills from a service, and
repository instructions, rules, and skills from a mounted checkout that also serves execution.
The operator can permit a repository-specific skill to replace an organization default, select
a deployment command over a same-name repository command, combine instruction contributions,
or disable repository context without disabling repository execution.

The composition contract uses trusted deployment-registered source IDs and an operator-only policy.
For each of the five closed content kinds, that policy names a highest-precedence-first source order,
`combine` or `replace` mode, exact exclusions, and permitted exact-name overrides. Source IDs are
validated composition identities, not paths, endpoints, or values supplied by source content. Each
registration supplies a trusted provenance policy: homogeneous sources stamp one fixed existing tier,
while a trusted mixed compatibility adapter may preserve only adapter-produced per-entry tiers from a
validated allowed set. Remote drivers are fixed to the driver tier and cannot promote a payload claim;
root project instructions remain project-tier, and project admission runs before resolution. Unknown,
duplicate, disabled, and kind-incompatible references fail startup. A project settings file
cannot register a source or set this policy, even after project trust is granted.

Instructions are ordered contributions without entry names. `combine` appends their messages by
source order; `replace` selects the first nonempty post-exclusion contribution. Commands, rules,
skills, and agent definitions use their existing case-sensitive logical `Name` as the collision key.
In `combine`, normal resolution chooses the first source. When a named override's winner is present,
resolution removes only present candidates explicitly named in `replaces`, then chooses the first
configured candidate among the winner and every non-replaced source. An earlier non-replaced source
therefore still blocks the winner. If the winner is absent, normal order applies. Exclusions remove one
named contribution from one source. Duplicate, unknown, self-replacing, and structurally no-op
declarations fail startup. In `replace`, the first nonempty source supplies the complete kind.
Resolution never recursively merges command bodies, rule fields, skill bundles or assets, or agent
definitions.

Given the same policy and one stable source observation, resolution is deterministic. Transport,
storage location, and discovery timing do not choose a winner. Listing and consumption use the same
visible-name set and winner algorithm; conformance rejects a listed name without a retrievable body or
a retrieved name absent from that stable listing. Freshness remains a separate source contract, so a
live update between separate List and Expand calls can legitimately change the next observation; no
cross-call transaction snapshot is implied. Existing source validators and per-entry caps run before
resolution; existing consumer aggregate limits run afterward.

Resolution retains the contributing source provenance and effective admission constraints. Content
cannot raise its own priority or claim a more privileged source tier. An override changes content
selection only. It cannot override a governance permission deny, grant execution capabilities,
change hooks or credentials, or expand a child's allowed tools. The operator's composition policy
is separate from the content it resolves.

### Reuse existing consumer contracts

Keep `CommandSource`, `RulesSource`, `SkillSource`, and `AgentDefSource`. The existing
`InstructionAssembler` is also an injection seam for project instruction providers; independence
does not by itself require another project-instruction interface.

The [interface contract](../acceptance/harness-context.md#interface-contract) makes the public
prompt interfaces workspace-free. Instruction assembly, command expansion, and command listing no
longer accept an execution workspace per call. `RootAssembler` and `DirCommandExpander` instead bind
their source `tool.Workspace` at construction. This is an intentional exported-engine break: the
implementation updates API snapshots and the engine changelog without a compatibility-adapter
window.

A root-file adapter preserves AGENTS.md-first, CLAUDE.md-fallback behavior, including empty-file
fallback and genuine read errors. It introduces no ancestor traversal or new body limits.
Command sources remain live per List/Expand; project instructions refresh once per run; rules,
skills, and agent definitions retain their existing snapshot lifetimes. Provenance manifests and
project-instruction framing must survive the interface change.

### Preserve admission, attenuation, and failure behavior

Source admission follows configured provenance and authorization. A source body cannot grant
itself a trust tier. Host locality does not establish trust, and an explicitly selected execution
file does not bypass project admission. Operator policy, hooks, credentials, memory, soul, and
user-model ownership remain outside this change.

No-FS describes execution capability, not the storage used by a logical context adapter. It does
not prohibit an admitted source from reading host files. Conversely, an adapter explicitly bound
to unavailable execution files still depends on that backend and follows its source error policy.

Command, skill, and agent listing first load and owner-authorize the session,
then borrow its effective binding from authoritative session/source metadata.
Independent sources do not reattach execution. The Build-owned resolver checks
consumer identity, owner/profile, and the exact source anchor on reuse. It creates
a generation single-flight, retries failed creation, and snapshots each source at
its defined lifetime. Retirement rejects new borrows but drains existing users.
Supported owner-authorized reload prepares a private candidate, constructs its
engine against that candidate, and commits both under the session publication
barrier only after construction and registration preconditions succeed. Failure
aborts the candidate and preserves the prior engine/inventory pair. Retirement
checks the expected binding's exact generation identity, so delayed teardown,
cleanup, and release cannot retire or close replacements. Stale borrows cannot
reactivate retirement. Build shutdown separately retires all owned generations,
cancels pending acquisition, and permanently closes admission; a successfully bound source
outlives its initiating request. The exact internal interfaces live in the
acceptance contract, not a parallel signature specification here.

Keep existing command precedence as the compatibility default and preserve established miss and
fail-soft behavior within an explicitly configured composition. A missing optional source can
remain empty. Failure to resolve a required source must not silently add execution files, process
cwd, or another unconfigured namespace as fallback.

A child inherits source authority only within its existing specialist, profile,
trust, and tool restrictions. A lifecycle callback on the selected child engine
registers its effective binding and holds an independent source borrow after its
identity and restrictions are known. It retains the parent's external snapshots
rather than rebinding changed sources. Live commands and per-Run instructions keep
their freshness semantics. Parent cleanup cannot release a still-borrowed source
or invalidate its backing files. An isolated execution fork does not change the
source anchor. Reconstructed child inventories require the actual attenuated child
binding, not a broader parent or global snapshot. Source reads never create
execution read evidence, even when storage is shared.

In-place resume preserves the child identity and original parent lifetime and
requires current source authorization and containment of the saved capability set.
The directing user selected durable recovery for default, named-specialist, and
fork-created children. A minimal private creation recipe records semantic engine
selection and whether creation used eligible managed authority; existing labels carry
profile, explicit-versus-floating model selection, limits, and capabilities. Reconstruction
uses current authorized configuration rather than compactable messages or inherited
identity labels. Only eligible operator-managed definitions add authority ceilings;
ordinary definitions constrain the engine catalog without converting it into a grant
set. A managed definition cannot be replaced by a same-name lower-tier definition on
resume. Check saved authority before applying this call's requested read-only/writable
view, then use the fully attenuated specialist engine without a generic-engine swap.
Current content can change but saved rights cannot expand. Missing/revoked definitions
or narrower eligible authority refuse without fallback. Floating model defaults remain
floating across restart, while saved explicit selections retain precedence. Forked
history is not a reason to refuse reconstruction.

The same amendment provides explicit new-child history transfer. The receiving
parent independently authorizes new delegation; source capabilities neither grant
nor veto it. This permits a read-only investigation to inform explicitly authorized
read-write implementation without pretending to resume the original child. Source
context grants, approvals, read evidence, usage, and recipe are not imported as
current authority. An eligible owned transcript does not need its old parent,
backend, context grant, or capability record merely to serve as historical data.
The recovery rationale of [ADR 0200](./0200-resume-a-failed-subagent.md) is preserved
without retargeting the saved child or triggering automatic fallback.

Capture and durable child start/resume share exclusive session access with a real
commit/takeover fence. Context checks, refcounted liveness, and same-owner lease
acquisition do not fence a write already admitted before expiry. The local store
pairs its lease domain with stable OS exclusion retained through all admitted IO;
expiry cannot transfer ownership beneath it. Expiring remote pairs require atomic
current-epoch validation in the same backend transaction as every mutation, including
metadata/events and deletion. Unfenced drivers or split backends without that guarantee
remain unsupported. The plan owns exact availability, interfaces, and proof obligations.

Work cancellation and persistence ownership are distinct. A cancelled child retains
its valid exclusive hold through bounded terminal persistence so it remains resumable;
real lease loss instead forbids detached writes. Shutdown stops work/admission first,
drains still-authorized persistence, then closes ownership. Guarded source reload
and incarnation comparison prevent capture from adopting a replacement. Stable local
lock identities and backend fence records retain their safety role across cleanup
and restart; the implementation records their lifecycle in ADR 0027.

Initial transfer content is bounded text/structured text with Parts precedence and
inert resource links. Media and oversized seeds are explicit errors, not silent loss,
asset fetches, or an unbounded fallback. Bounded preflight precedes copying/encoding;
canonical framing keeps roles, instructions, and tool records historical. Copy-local
pairing projection retains completed pairs and assistant text when another call in
the final batch is unanswered, without repairing the source.

New delegation consumes the normal receiving-parent hop and starts fresh destination
usage. Imported input and new output count against destination limits; old usage
remains on the source. Parent own limits and per-call admission remain ordinary.
Subagent usage reporting does not imply folding all child spend into the parent main
session's budget; this amendment adds no descendant aggregate/reservation accounting.
Existing Team/Parallel aggregate rules remain local to their existing paths.
Same-ID resume preserves cumulative usage and consumes no extra hop, following
[ADR 0234](./0234-authority-evaluator-port.md).

Legacy missing lineage never licenses retroactive parent adoption or transcript
deletion. Missing recipes prevent cold in-place reconstruction, but authorized
terminal Subagent history remains transferable; unknown producer kinds and active
or idle records remain ineligible. This is not universal legacy recovery.
Original-source cross-parent reconstruction remains outside this amendment.

## Restart and reconfiguration

Harness context is deployment-controlled and intentionally rebound after process restart. `app.Build`
compiles the current operator policy and process-scoped registrations once. A registration is either
process-scoped or principal-scoped. Process sharing is allowed only for a caller-neutral,
concurrency-safe adapter. A principal-scoped registration binds the owned session's principal and
profile and cannot share its source, snapshot, live lookup, or cache with another principal. Snapshot
sources bind once for the current Build or principal binding; command sources remain live only inside
that binding. Build-owned resources close from `Built.Close`, and principal/session resources close
only after their generation retires and its final borrower releases, including during Build shutdown.
The implementation inventories every long-lived
connection, cache, and binding in ADR 0027.

Existing sessions and scheduled fires create fresh bindings from the current composition and current
authorization after restart, so an operator configuration change can change the context seen by a
resumed conversation. Per-call code does not accidentally rebuild snapshot sources.

This amendment authorizes one additive private Subagent creation recipe in snapshots
and event-source creation metadata, reusing existing profile/provider and authority
fields. No durable `HarnessContextRef`, frozen external snapshot, or public source
selector is added. Recipe absence is preserved, never reconstructed from historical
text; new children stamp their own selections. History-transfer provenance uses
existing tool calls/results and the seeded conversation, while the new child's normal
relationship names only its receiving parent. No destructive migration is required.
Missing lineage/recipe/authority can prevent in-place continuation without preventing
authorized inspection or eligible history transfer.
Stored execution placement never supplies a fallback context binding. Rebinding owner-authorizes the
session or schedule under current policy; it cannot preserve a revoked source grant. The existing
local/system-principal behavior remains valid when an unauthenticated local deployment has no external
principal.

The compatibility default constructs explicit source bindings from existing settings. The configured
startup project source may share files with execution, but composition binds it independently rather
than discovering it from the session `Environment`. Existing commands retain directory, skill,
driver, then MCP precedence. Existing rules, skills, and agent definitions retain their current
admission, ordering, caps, and failure contracts. An explicit `harness_context` policy replaces that
synthesized policy with operator-registered source IDs.

## Consequences and delivery

The model separates source selection from execution while allowing explicit storage sharing.
Both palette listing and invocation consult the same admitted source chain. File tools retain
the exact execution backend, and source reads retain their own admission and freshness rules.

The model defines domain responsibilities; the acceptance plan owns exact
interfaces, approval status, and backend proof obligations. Its dependency stack
is the shared amendment, #1875, then sibling MicroVM #580, Redis #1811, and native
Kubernetes #1614 integrations. #1875 delivers same-parent resume enforcement and
explicit new-child history transfer together, including model-visible recovery
instructions; it does not defer the replacement for cross-parent recovery.
Native plan #1579 must replace backend-selected
context restrictions with explicit source policy. A first native slice can leave
PVC context unselected while admitting independent deployment/API sources.

The directing operator authorized amending this decision in place before
implementation lands. It is renumbered from the colliding 0357 to 0359; the
unrelated model-stream ADR retains 0357. Shared reference proofs do not establish
real-backend qualification. Plan approval and implementation merge remain separate
human checkpoints.

Any implementation adding long-lived connections, caches, or bindings must record their owner,
cleanup, and restart behavior in ADR 0027's inventories. This model-only PR adds no such runtime
resource and no production code.

## See also

- [Harness context acceptance plan](../acceptance/harness-context.md)
- [Architecture guide](../architecture.md)
- [Formal domain model](../architecture/mecatl.modelith.md)
- [ADR 0013 - Agent definitions](./0013-agent-definitions.md)
- [ADR 0081 - RulesSource port](./0081-rules-source-port.md)
- [ADR 0108 - Logical skill assets](./0108-on-demand-logical-skill-assets.md)
- [ADR 0211 - Execution-environment runtime seam](./0211-execution-environment-runtime-seam.md)
- [ADR 0291 - Server-owned session placement](./0291-server-owned-session-placement.md)
