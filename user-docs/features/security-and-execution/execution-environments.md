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
one workspace. Choose the default profile for workspace tasks or `no-fs` for
sessions that need no local file access.

## Availability

The default profile is available in `mecated`, `mecak8s`, `mecatui`'s embedded
server, and engine embeddings. A local deployment can set `microvm-local` as that
default so filesystem and shell tools run in a repository-scoped VM. Linux amd64
with KVM is the qualified path. An experimental Darwin arm64 path exists for Apple
Silicon macOS 15+ with Hypervisor.framework, but native and signed-release
qualification remain pending, so it is experimental rather than released support.
See [Local microVM environments](/operating/microvm-environments.md) for
platform and artifact requirements.

A session can instead select the `no-fs` profile
for research, coordination, or remote deployments that must not expose a local
filesystem.

## Default workspace

Create a default session with a workspace root. `Read`, `ListDir`, `Write`,
`Edit`, `Copy`, `Move`, `Remove`, `Grep`, `Glob`, and `Shell` all use that root.
The workspace requires a read before overwriting an existing file. Edits use
exact, unique matches, and writes fail if the file changed after the read.
Removal is non-recursive, copy accepts only regular files, and copy and move
refuse an existing destination.

A new run or process may require another `Read` before `Edit` or an
existing-file `Write` because the version record belongs to the live
environment. This fail-safe check does not mean file data was lost.

## The no-filesystem profile

Create a no-filesystem session by setting `profile: "no-fs"`:

```sh
curl -s -X POST http://127.0.0.1:8081/v1/sessions \
  -d '{"profile":"no-fs"}'
```

The profile requires an empty `workspace`. Any other profile value is rejected;
there is no silent fallback. A no-FS catalog removes `Read`, `ListDir`, `Write`,
`Edit`, `Copy`, `Move`, `Remove`, `Grep`, `Glob`, `Shell`, `ShellStatus`,
`Parallel`, and `SkillDraft`. It retains web tools, memory, MCP tools, skills,
`Subagent`, and `Team`; children use the same file-less tool set and cannot
create a shell or fork a workspace.

The profile is fixed at session creation. The model cannot switch it during a
run. See [Core tools](/features/sessions/tools.md) for the complete catalog.

## Child environments

Different delegation modes use different environment strategies:

- **Read-only Subagent and team members** get a child environment. When the
  workspace is trusted, the harness can use a worktree so child changes are
  isolated. An untrusted workspace withholds the read-only child shell because
  creating a worktree requires operating on the repository's `.git`.
- **Parallel branches** use hardened force-copy environments. The initial copy
  does not invoke Git; each branch then works in its own copied namespace.
- **Mutating members and direct-write Subagents** use the parent environment and
  modify the real workspace. They are serialized against sibling tool calls;
  there is no merge-back step.

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
foreground commands only.
It has no force-takeover recovery and is not a hostile multi-tenancy boundary.

Remote deployments retain operator-global prompt rules and skills. A deployment can
programmatically compose independently configured harness context sources for
instructions and slash commands without acquiring native execution files. PVC-backed
context remains unselected in this slice. Permission rules
from enabled user-global settings and explicit permission files also apply,
including configured Deny and Ask rules under `auto` posture. Project permission
files are excluded. Local project trust cannot admit host-local `AGENTS.md`,
rules, skills, commands, or Git context into a remote session. Explicit `no-fs`
sessions keep their file-less catalog, allocate no execution environment, and
can retain independently configured harness context sources.

The remote filesystem preserves the existing Read/Edit/Write version checks and
non-clobbering Copy/Move behavior. Copy, Move, and Remove do not require prior
content reads; Remove remains non-recursive.

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

Persisted prototype resources use an explicit administrator migration. Normal
operations reject schema versions other than 2. `MigrateEnvironment` accepts
only recognized versions 0 and 1, verifies the exact live Pod and PVC identities,
and requires a healthy, idle environment. Unknown or malformed state is retained
unchanged rather than reset. Completed migration receipts expire when executor
replacement publishes a new Pod identity. After replacement, retrying the old
migration operation returns a conflict with either the original or replacement UID.

### Production security material

The production chart requires one projected Secret containing the TLS identity,
client CA bundle, and Ed25519 grant keys, plus `provider.securityManifest`. The
chart does not generate keys or certificates. The manifest is strict JSON with
this shape:

```json
{
  "version": 1,
  "generation": 42,
  "issuer": "https://execution.example.com",
  "audience": "mecatl-execution",
  "activeKeyID": "k1",
  "grantTTL": "1m",
  "clockSkew": "5s",
  "keys": [
    {
      "id": "k1",
      "version": 1,
      "file": "grant-k1.pem",
      "publicKeySHA256": "0000000000000000000000000000000000000000000000000000000000000000",
      "activateAt": "2027-01-01T00:00:00Z",
      "verifyUntil": "2027-01-02T00:00:00Z",
      "state": "active"
    }
  ],
  "tls": {
    "certificateFile": "tls.crt",
    "privateKeyFile": "tls.key",
    "clientCAFile": "clients.pem"
  },
  "clients": [
    {
      "uri": "spiffe://cluster.example.com/ns/mecatl/sa/mecak8s",
      "mayAttestOwner": true,
      "administrator": false
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

The all-zero fingerprint and 2027 dates are non-secret example values. Replace
the fingerprint with the lowercase hexadecimal SHA-256 hash of the raw 32-byte
Ed25519 public key, not PEM text or DER encoding, and use a reviewed active window.

`administratorFor` permits administration of environments created by the listed
client URIs, alongside the administrator's own environments. A nonempty list
requires `administrator: true` and accepts at most 256 unique canonical URIs:
nonempty scheme and host, lowercase scheme and host, and no userinfo, query,
fragment, or wildcard. Matching is exact, with no namespace or prefix grant.
An absent or empty list preserves self-administration only. The creator need
not remain in the client allowlist.

The scope applies to `RetireEnvironment`, `ReplaceExecutor`, `RecoverEnvironment`,
`DeleteRetiredEnvironment`, `MigrateEnvironment`, and `RevokeEnvironment`. It
preserves each operation's exact owner and identity checks and grants no
`mayAttestOwner`, attach, file, command, run, or reference authority. Scope order
does not change the authority digest; adding or removing a creator does. Follow
the [administrative runbook](/operating/mecak8s/execution-provider-lifecycle.md#run-an-administrative-lifecycle-operation)
for quiesced upgrades and scope removal.

Use only basename file names. The projected Secret keys in this example are
`grant-k1.pem`, `tls.crt`, `tls.key`, and `clients.pem`; an external secret manager
owns their bytes. Kubernetes projected-volume `..data` symlinks are supported,
but paths escaping the mounted directory are rejected. Keep every referenced
filename immutable and use a new name for changed signing, TLS, or CA material.
Stage those files before publishing a higher-generation manifest and retain the
overlap files. Secret and ConfigMap projections are independent; see the
[authority rotation procedure](/operating/mecak8s/execution-provider-lifecycle.md#rotate-execution-provider-authority)
for publication, verification, and recovery from mixed-material digest drift.
Increase `generation` for every authority change. Key IDs and `(id, version)` fingerprints cannot be
reused; the provider persists a bounded high-water ledger in its authority
ConfigMap. Keep retired keys as `verify-only` until all grants expire, then mark
them `revoked`. Invalid, incomplete, rolled-back, or newly expired material makes
readiness fail and denies new RPC authorization until corrected. The provider
re-verifies the peer certificate and URI policy against the current client CA on
every RPC, including RPCs on an existing HTTP/2 connection. Profile-resource or
controller-cache startup failures terminate the provider with that bounded reason
class before it accepts traffic. A failed `/ready` response reports
`security-authority-or-expiry`. Inspect provider logs and the named RuntimeClass,
StorageClass, and authority ConfigMap metadata; the endpoint never returns key or
certificate contents.

`RevokeEnvironment` is an administrator-only, exact-reference CAS. Supply the
current positive grant generation; success increments it without changing the
execution epoch. Old grants then cannot authorize another operation or renewal.
An operation already accepted may still finish and clean up, so revocation is not
termination proof and a new run needs a fresh claim after the old claim is
released or fenced.

The gRPC port remains the only execution data plane. `/live` and `/ready` are
separate, unauthenticated operational HTTP endpoints intended only for Kubernetes
probes; do not expose that port outside the cluster. Readiness requires current
security material, the authoritative generation, synchronized controller caches,
and Kubernetes access. Liveness reports process health only.

Production values require two or more provider replicas, explicit provider-client
selectors, API-server and DNS CIDRs, and finite ResourceQuota/LimitRange values.
Executor pods are default-deny for ingress and egress. Add only profile-specific
CIDR and port egress under `networkPolicy.workloadProfiles`; there is no implicit
DNS, metadata-service, API-server, provider, or peer access. Each profile requires
an explicit RuntimeClass supplied by the cluster, but do not treat a RuntimeClass
name alone as a hostile-workload isolation guarantee. CPU, memory, ephemeral
storage, and `/tmp` are explicitly bounded per profile. Each profile also requires
`maxEnvironments`; the namespace quota is the final global bound.

Preserve the authority ConfigMap and current manifest across upgrades and
same-release reinstalls. A missing ledger with retained allocations is not a new
installation: do not bootstrap it at generation 1. See the
[provider lifecycle procedure](/operating/mecak8s/execution-provider-lifecycle.md#upgrade-uninstall-and-reinstall-the-execution-provider)
for ownership checks, retained network protection, and quiesced CRD/provider
upgrade order. Schema-2 environment migration remains an explicit operation;
complete it before enabling new sessions.

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
