# ADR 0354 - Harness context source authority is independent of execution

- Status: Proposed; composition configuration, interface compatibility, and restart policy remain under review
- Date: 2026-09-23
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

### Compose sources and resolve each content kind explicitly

Source selection can include several sources of the same content kind. A helpdesk agent in
Kubernetes can combine instructions mounted into mecak8s with instructions, rules, or skills
provided by gRPC services implementing the source contracts. It needs no execution repository.

A coding agent can combine deployment instructions, organization skills from a service, and
repository instructions, rules, and skills from a mounted checkout that also serves execution.
The operator can permit a repository-specific skill to replace an organization default, select
a deployment command over a same-name repository command, combine instruction contributions,
or disable repository context without disabling repository execution.

The composition contract identifies enabled sources and their ordering, collision keys for named
entries, permitted overrides, and combination/replacement/exclusion rules for each content kind.
It does not impose one generic deep-merge algorithm on instruction fragments, rule contributions,
commands, skills, and agent definitions. Given the same configuration and source observations,
resolution must be deterministic; transport, storage location, and discovery timing do not choose
a winner. Listing and consumption apply the same resolution rules. Freshness remains a separate
source contract, so live updates between calls can legitimately change their results.

Resolution retains the contributing source provenance and effective admission constraints.
Content cannot raise its own priority or claim a more privileged source tier. Overriding a prompt
template or instruction rule cannot override a governance permission deny, grant execution
capabilities, or expand a child's allowed tools. The operator's composition policy is separate
from the content it resolves.

The exact composition configuration and collision/combination schemas remain an explicit human
review item in the interface plan. The existing command chain supplies a compatibility default,
not a complete specification for multiple sources of every content kind.

### Reuse existing consumer contracts

Keep `CommandSource`, `RulesSource`, `SkillSource`, and `AgentDefSource`. The existing
`InstructionAssembler` is also an injection seam for project instruction providers; independence
does not by itself require another project-instruction interface.

The draft [interface contract](../acceptance/harness-context.md#interface-contract) proposes
removing the per-call execution workspace from instruction assembly and command listing/expansion.
File-backed implementations bind their source namespace explicitly at construction. Composition
continues to translate source contents into the existing prompt, catalog, and specialist-engine
inputs. Public API transition treatment requires human review before implementation.

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

Command listing authorizes the session and resolves the configured source chain. It does not
require an unrelated VM or Redis execution backend to be available. Actual runs separately admit
the execution capabilities they need.

Keep existing command precedence as the compatibility default and preserve established miss and
fail-soft behavior within an explicitly configured composition. A missing optional source can
remain empty. Failure to resolve a required source must not silently add execution files, process
cwd, or another unconfigured namespace as fallback.

A child inherits source authority only within its existing specialist, profile, trust, and tool
restrictions. Context inheritance does not copy a parent's broader tool catalog. Source reads do
not create file-mutation evidence in the execution `ReadLedger`, even when the backing files are
shared.

## Decisions still requiring approval

Source independence does not automatically require a new durable `HarnessContextRef`, storage
schema, or binding protocol. Preserving exact source authority across deployment reconfiguration
is a separate decision from consuming explicit sources in one process.

The [Human decisions](../acceptance/harness-context.md#human-decisions) section keeps three choices
open: the exact per-kind composition configuration and resolution schema, public API transition
treatment, and restart/reconfiguration authority. If exact durable binding is selected, the
contract must specify identity, principal/tenant scope, current
revocation checks, storage, and legacy migration before implementation. A binding protocol should
live beside its actual consumer rather than widen `engine/port` without an engine consumer.

This proposal authorizes neither automatic adoption nor rejection of existing sessions and
schedules. It also does not freeze old trust grants across restart. The default selection or
migration policy cannot be improvised by either downstream integration.

## Consequences and delivery

The model separates source selection from execution while allowing explicit storage sharing.
Both palette listing and invocation consult the same admitted source chain. File tools retain
the exact execution backend, and source reads retain their own admission and freshness rules.

The model defines the domain; implementation and approval status belong in the acceptance plan
and this ADR's metadata. The dependency stack is model/contract, then shared implementation,
followed by MicroVM PR #580 and the Redis #1811 sibling integration. Shared acceptance uses
offline reference adapters; real backend qualification belongs to the integration that owns it.

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
