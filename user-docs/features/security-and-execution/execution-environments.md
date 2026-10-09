---
slug: /features/execution-environments
sidebar_position: 330
title: Execution environments
description:
  Keep file operations, shell commands, forks, and resumed sessions in the
  correct workspace.
---

# Execution environments

An execution environment keeps a session's file operations and shell commands in
one workspace. Choose the deployment default, no filesystem, or an exact eligible
operator-owned execution template ID and revision.

## Availability

The deployment default is available in `mecated`, `mecak8s`, `mecatui`'s embedded
server, and engine embeddings. A local deployment can set `microvm-local` as
that default so filesystem and shell tools run in a repository-scoped VM. Linux
amd64 with KVM is the qualified path. An experimental Darwin arm64 path exists
for Apple Silicon macOS 15+ with Hypervisor.framework, but native and
signed-release qualification remain pending, so it is experimental rather than
released support. See
[Local microVM environments](/operating/microvm-environments.md) for platform
and artifact requirements.

A session can instead select `execution.none` for research, coordination, or
remote deployments that must not expose a local filesystem. When advertised by
the server, `execution.template` pins an eligible operator-approved ID and
revision; catalog listing does not reserve capacity and selection is rechecked
on creation. The effective filesystem and built-in Shell capabilities are
reported by the bound session, not inferred from its template name.

## Default workspace

Configure a workspace on the server, then create a session using its default
placement. Clients cannot select a workspace in the create request. `Read`,
`ListDir`, `Write`, `Edit`, `Copy`, `Move`, `Remove`, `Grep`, `Glob`, and
`Shell` use the assigned workspace root. The workspace requires a read before
overwriting an existing file. Edits use exact, unique matches, and writes fail
if the file changed after the read. Removal is non-recursive, copy accepts only
regular files, and copy and move refuse an existing destination.

A new run or process may require another `Read` before `Edit` or an
existing-file `Write` because the version record belongs to the live
environment. This fail-safe check does not mean file data was lost.

## No-filesystem execution

Create a no-filesystem session by setting `execution.none`:

```sh
curl -s -X POST http://127.0.0.1:8081/v1/sessions \
  -d '{"execution":{"none":{}}}'
```

Omitting `execution` binds the deployment default. The server rejects invalid
selections rather than falling back to the default. An explicit no-FS catalog
removes `Read`, `ListDir`, `Write`, `Edit`, `Copy`, `Move`, `Remove`, `Grep`,
`Glob`, `Shell`, `ShellStatus`, `Parallel`, and `SkillDraft`. It retains web
tools, memory, MCP tools, skills, `Subagent`, and `Team`; children use the same
file-less tool set and cannot create a shell or fork a workspace.

The selection is fixed at session creation. The model cannot switch it during a
run. See [Core tools](/features/sessions/tools.md) for the complete catalog.

## Child environments

Different delegation modes use different environment strategies:

- **Read-only Subagent and team members** get a child environment. When the
  workspace is trusted, the harness can use a worktree so child changes are
  isolated. An untrusted workspace withholds the read-only child shell because
  creating a worktree requires operating on the repository's `.git`.
- **Parallel branches** use hardened force-copy environments. The initial copy
  does not invoke Git; each branch then works in its own copied namespace.
- **Mutating team members** receive private copied workspaces with their own Git
  state. Their changes are not merged back into the parent.
- **Direct-write Subagents** use the parent environment and modify the real
  workspace. They run serially against sibling tool calls, with no merge-back
  step.

All agent-facing shells use a secret-scrubbed environment. Provider keys,
`MECATL_*` credentials, cloud credentials, and other secret-shaped variables are
removed before a command runs.

## Persistence and reattachment

The environment identity is persisted with a session snapshot. When a session
resumes, Mecatl reattaches that exact identity through the deployment's placement
provider. If the provider cannot reattach it or returns a different environment,
the run fails instead of falling back to a local workspace.

## Native Kubernetes lifecycle and retention

The native provider is designed for an operator-controlled cluster and
foreground commands only. It has no force-takeover recovery and is not a hostile
multi-tenancy boundary.

### Instructions and permission sources

Remote deployments retain operator-global prompt rules and skills. A deployment
can programmatically compose independently configured harness context sources
for instructions and slash commands without acquiring native execution files.
PVC-backed context sources are not enabled. Permission rules from enabled
user-global settings and explicit permission files also apply, including
configured Deny and Ask rules under `auto` posture. Project permission files are
excluded. Local project trust cannot admit host-local `AGENTS.md`, rules,
skills, commands, or Git context into a remote session. Explicit `no-fs`
sessions keep their file-less catalog, allocate no execution environment, and
can retain independently configured harness context sources.

The remote filesystem preserves the existing Read/Edit/Write version checks and
non-clobbering Copy/Move behavior. Copy, Move, and Remove do not require prior
content reads; Remove remains non-recursive.

### Run ownership and fencing

The optional Kubernetes execution provider stores environment ownership in a
namespaced `ExecutionEnvironment`. Provider replicas coordinate through
Kubernetes resource-version compare-and-swap; a replica restart does not clear
another replica's operation. When a caller resolves a persisted ordinary
approval after a server restart, the server reacquires native run ownership,
renews it while the resumed run drains, and releases it after the drain. A lost
or cancelled operation can still leave the environment in `FenceUnknown`, which
requires the documented manual recovery path. There is no force takeover.

Operation lease expiry fences the environment and retains the unresolved
operation identity for administrator recovery. A `503` response with
`placement_unavailable` is generic: check provider readiness, the configured
profile's capacity, and retained allocations. Use the existing
[administrative lifecycle operation](/operating/mecak8s/execution-provider-lifecycle.md#run-an-administrative-lifecycle-operation)
to retire an eligible environment and delete its retained storage when you need
to free capacity.

### Workspace retention and administrator recovery

Workspace PVCs are retained by default. Removing the last session reference,
deleting a session, uninstalling the chart, or deleting the provider does not
delete a workspace PVC. Executor replacement and retirement are private,
mutually authenticated administrator operations. They require exact environment,
execution-epoch, Pod-UID, and PVC-UID values. The provider waits for a kubelet
terminal Pod phase and terminated state for every container before removing its
Pod finalizer. A missing Pod or unreachable kubelet is not termination proof and
leaves the environment fenced.

Retirement stops the executor but retains the PVC. PVC deletion requires the
separate `DeleteRetiredEnvironment` administrator RPC with the exact retained
PVC UID and no references or claims. Deleting the custom resource outside this
provider workflow is unsupported. Do not remove the retention or executor
finalizers manually; follow the operator's external-fencing runbook when the
provider cannot independently observe terminal compute.

Only execution-environment schema version 2 is supported. Old, missing, unknown,
or mismatched schema versions are rejected; no migration is provided. Unsupported
objects and their workspace data are not automatically upgraded, reset, or deleted.

### Production security material

The production chart projects platform-managed provider TLS identity and client
CA files from operator-owned Secrets. It does not mint certificates or issue
signed grants. The separately published `provider.securityManifest` contains
client authorization policy and names the TLS files:

```json
{
  "version": 1,
  "generation": 42,
  "tls": {
    "certificateFile": "tls.crt",
    "privateKeyFile": "tls.key",
    "clientCAFile": "clients.pem"
  },
  "clients": [
    {
      "uri": "spiffe://cluster.example.com/ns/mecatl/sa/mecak8s",
      "mayAttestOwner": true,
      "administrator": false,
      "executionTemplates": ["coding"]
    },
    {
      "uri": "spiffe://cluster.example.com/ns/mecatl/sa/execution-admin",
      "mayAttestOwner": false,
      "administrator": true,
      "administratorFor": ["spiffe://cluster.example.com/ns/mecatl/sa/mecak8s"]
    }
  ]
}
```

The provider checks the original peer certificate and current client policy at
admission, renewal, and completion. A policy change increments `generation`;
TLS certificate or trust renewal is independent of that generation.

### Administrator scope

`administratorFor` permits administration of environments created by the listed
client URIs, alongside the administrator's own environments. A nonempty list
requires `administrator: true` and accepts at most 256 unique canonical URIs:
nonempty scheme and host, lowercase scheme and host, and no userinfo, query,
fragment, or wildcard. Matching is exact, with no namespace or prefix grant. An
absent or empty list preserves self-administration only. The creator need not
remain in the client allowlist.

The scope applies to `RetireEnvironment`, `ReplaceExecutor`,
`RecoverEnvironment`, `DeleteRetiredEnvironment`, and `RevokeEnvironment`. It
preserves each operation's exact owner and identity
checks and grants no `mayAttestOwner`, attach, file, command, run, or reference
authority. Scope order does not change the authority digest; adding or removing
a creator does. Follow the
[administrative runbook](/operating/mecak8s/execution-provider-lifecycle.md#run-an-administrative-lifecycle-operation)
for quiesced upgrades and scope removal.

### Policy rotation and readiness

An external secret manager owns `tls.crt`, `tls.key`, and `clients.pem` (the
provider's projected TLS identity and client CA bundle). Kubernetes projected
`..data` symlinks are supported, but paths escaping the mounted directory are
rejected. The provider reloads renewed valid TLS material independently of the
client-policy manifest. For client-policy changes, publish a higher-generation
manifest and verify provider readiness. A stale replica denies requests rather
than using a rolled-back policy. TLS trust withdrawal or certificate expiry
aborts affected in-flight operations; old RPCs drain only while their original
peer identity remains authorized. See the
[policy and TLS rotation procedure](/operating/mecak8s/execution-provider-lifecycle.md#rotate-execution-provider-authority).

### Revocation

`RevokeEnvironment` is an administrator-only, exact-reference CAS. Supply the
current positive `grant_generation` (the wire name for the durable revocation
fence); success increments it without changing the execution epoch. Old operations then cannot renew or authorize more work.
Revocation is not executor termination proof; a new run needs a fresh claim
after the old claim is released or fenced.

### Network and resource limits

The gRPC port remains the only execution data plane. `/live` and `/ready` are
separate, unauthenticated operational HTTP endpoints intended only for
Kubernetes probes; do not expose that port outside the cluster. Readiness
requires current security material, the authoritative generation, synchronized
controller caches, and Kubernetes access. Liveness reports process health only.

Production values require two or more provider replicas, explicit
provider-client selectors, API-server and DNS CIDRs, and finite
ResourceQuota/LimitRange values. Executor pods are default-deny for ingress and
egress. Add only profile-specific CIDR and port egress under
`networkPolicy.workloadProfiles`; there is no implicit DNS, metadata-service,
API-server, provider, or peer access. Each profile requires an explicit
RuntimeClass supplied by the cluster, but do not treat a RuntimeClass name alone
as a hostile-workload isolation guarantee. CPU, memory, ephemeral storage, and
`/tmp` are explicitly bounded per profile. Each profile also requires
`maxEnvironments`; the namespace quota is the final global bound.

### Upgrade state

Preserve the authority ConfigMap and current manifest across upgrades and
same-release reinstalls. A missing ledger with retained allocations is not a new
installation: do not bootstrap it at generation 1. See the
[provider lifecycle procedure](/operating/mecak8s/execution-provider-lifecycle.md#upgrade-uninstall-and-reinstall-the-execution-provider)
for ownership checks, retained network protection, and quiesced CRD/provider
upgrade order. Verify that retained environments use schema version 2 before
enabling new sessions.

## Limitations

- No-FS sessions cannot use local file tools, shell commands, workspace forks,
  or parallel branches.
- Read-only child shells depend on project trust; this is separate from the
  parent's ability to run its own shell.
- Direct-write children can leave partial edits if cancelled or interrupted; the
  parent workspace and Git are the rollback boundary.
- Remote environment reattachment requires an explicit resolver.

## Next steps

- [Background Shell](/features/sessions/tools.md#background-commands)
- [Subagents, teams, and parallel](/features/agent-behavior/subagents-and-teams.md)
- [Workspace trust](/features/security-and-execution/permissions-and-posture.md)
