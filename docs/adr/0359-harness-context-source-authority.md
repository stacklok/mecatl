# ADR 0359 - Harness context source authority is independent of execution

- Status: Proposed amendment to the decision approved in #1814; exact source-acquisition interfaces await approval by merge
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

### Acquire selected execution files by exact session authority

Two sessions may have the same principal and profile but different worktrees. Those values alone
cannot select the right source. A principal-scoped binding therefore receives the authoritative
session ID. A trusted registration may opt in to execution-file acquisition only when it has fixed
project provenance and is selected and admitted by operator policy.

The opt-in registration receives a lazy capability that captures the exact server-authorized source
placement. It accepts no session, root, environment reference, or other selector and exposes only a
read-only workspace, a source-owned release, no runner, and no execution read evidence. Process,
unselected, and independent registrations receive no capability and do not attach execution.

Acquisition supports a newly reserved root or scheduled-fire ID before session publication; it does
not require that unpublished ID to be loadable from the session store. The source retains its own
borrow and releases only that borrow. Failure or cancellation releases attempt-owned resources
without closing the execution owner's binding or deleting retained placement. Actual execution is
still acquired independently. The exact internal shape is defined by the
[interface contract](../acceptance/harness-context.md#interface-contract).

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

Command discovery first loads and owner-authorizes an existing session, then binds from its
stored ID, owner, and profile; it does not reattach an unrelated execution backend. Initial creation
uses the server-reserved ID and authorized placement before publication rather than requiring a store
load. Build owns a concurrency-safe per-session binding cache: first creation is single-flight, failed
creation is retryable, and reuse verifies session/principal/profile consistency. Retirement closes a
binding generation, not the durable session ID forever. It rejects new borrows of that generation
and delays cleanup until its existing borrowers release. Explicit owner-authorized supported reload calls the consumer-local
`CommandSourceResolver.Activate(context.Context, session.SessionID, *session.Principal, string) error`
under the Service's existing per-session lifecycle serialization. It verifies the stored owner/profile
and current source authorization before publishing a fresh generation; failure leaves retirement
intact, and an already-active matching generation remains unchanged. Ordinary or stale queued Borrow
cannot reactivate a retired generation. Each release targets its exact generation, so draining old
borrowers and old cleanup cannot close or evict a replacement. Actual session teardown retires the
current generation; Build shutdown retires all generations and permanently prevents both borrowing
and activation. This preserves existing close/reload compatibility without a public or durable
generation identity. An explicitly execution-file-backed source may still authorize and bind that
source backend. Actual runs separately admit the execution capabilities they need.

Keep existing command precedence as the compatibility default and preserve established miss and
fail-soft behavior within an explicitly configured composition. A missing optional source can
remain empty. Failure to resolve a required source must not silently add execution files, process
cwd, or another unconfigured namespace as fallback.

A fresh child inherits source authority only within its existing specialist, profile, trust, and tool
restrictions. Isolated and direct-write Subagents, Parallel branches, and parented Team members retain
the parent's source through the existing binding references; they do not retarget context to child
execution files. Context inheritance does not copy a parent's broader tool catalog, and parent teardown
cannot invalidate a source still held by an existing child reference. This amendment adds no child
binding callback framework and no new cold-reconstruction or original-anchor recovery contract.
Source reads do not create file-mutation evidence in the execution `ReadLedger`, even when the backing
files are shared.

## Restart and reconfiguration

Harness context is deployment-controlled and intentionally rebound after process restart. `app.Build`
compiles the current operator policy and process-scoped registrations once. A registration is either
process-scoped or principal-scoped. Process sharing is allowed only for a caller-neutral,
concurrency-safe adapter and receives a zero scope. A principal-scoped registration binds the
owned session's authoritative ID, principal, and profile. Independent sessions cannot reuse each
other's source binding, snapshot, live lookup, or cache. Explicitly inherited child references retain
the parent's source binding and acquisition anchor within their existing restrictions, as described
above. Only an admitted execution-file registration receives the
lazy exact-authorized acquisition capability; independent registrations never attach execution.
Snapshot sources bind once for the current Build or principal binding; command sources remain live
only inside that binding. Build-owned resources close from `Built.Close`, and principal/session
resources close when their binding retires or Build shuts down. The implementation inventories every
long-lived connection, cache, and binding in ADR 0027.

Existing sessions and scheduled fires create fresh bindings from the current composition and current
authorization after restart, so an operator configuration change can change the context seen by a
resumed conversation. Per-call code does not accidentally rebuild snapshot sources.

This decision adds no durable `HarnessContextRef`, source selector, event, or snapshot field. Existing
sessions and schedules require no migration and are not rejected because they predate this contract.
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

The approved model defines the domain; this proposed amendment changes only the session-bound
execution-file acquisition interface needed by MicroVM #580 and native Kubernetes #1579 / #1614.
The native plan amendment may proceed in parallel with shared implementation #1875. Redis #1811 is
a separate sibling and does not block either backend. Shared reference proofs do not qualify a live
backend; each integration owns its concrete evidence and retains its separate permission,
governance, placement, and fencing obligations.

Existing inventory APIs, persistence, `SessionLease`, ADR 0294 behavior, resume semantics, and child
lifecycle machinery remain unchanged. This amendment introduces no session-effective inventory
migration, durable recovery recipe, history-transfer framework, `SessionAccess`, or new session lock,
fencing, or store mutation protocol.

Any implementation adding long-lived connections, caches, or bindings must record their owner,
cleanup, and restart behavior in ADR 0027's inventories. This docs-only amendment adds no runtime
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
