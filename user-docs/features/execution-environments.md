---
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
server, and engine embeddings. A session can instead select the `no-fs` profile
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
run. See [Core tools](/building/what-you-get/core-tools.md) for the complete
catalog.

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

The environment identity is persisted with a session snapshot. Mecatl
reconstructs local and no-FS environments. A non-local identity can be
reattached only when the deployment supplies an `EnvironmentResolver`; a missing
resolver, mismatched identity, or nil workspace returns an error instead of
using a local workspace.

## Native Kubernetes lifecycle and retention

The optional Kubernetes execution provider stores environment ownership in a
namespaced `ExecutionEnvironment`. Provider replicas coordinate through
Kubernetes resource-version compare-and-swap; a replica restart does not clear
another replica's operation. Operation lease expiry fences the environment and
retains the unresolved operation identity for administrator recovery.

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
unchanged rather than reset.

### Production security material

The production chart requires one projected Secret containing the TLS identity,
client CA bundle, and Ed25519 grant keys, plus `provider.securityManifest`. The
chart does not generate keys or certificates. The manifest is strict JSON with
this shape:

```json
{"version":1,"generation":7,"issuer":"https://issuer.example","audience":"mecatl-execution","activeKeyID":"grant-2026-09","grantTTL":"1m","clockSkew":"5s","keys":[{"id":"grant-2026-09","version":7,"file":"grant-2026-09.pem","publicKeySHA256":"<hex SHA-256 of Ed25519 public key>","activateAt":"2026-09-17T00:00:00Z","verifyUntil":"2026-09-17T01:00:00Z","state":"active"}],"tls":{"certificateFile":"tls.crt","privateKeyFile":"tls.key","clientCAFile":"client-ca.pem"},"clients":[{"uri":"spiffe://example/mecatl","mayAttestOwner":true,"administrator":false}]}
```

Use only basename file names. Kubernetes projected-volume `..data` symlinks are
supported, but paths escaping the mounted directory are rejected. Increase
`generation` for every change. Key IDs and `(id, version)` fingerprints cannot be
reused; the provider persists a bounded high-water ledger in its authority
ConfigMap. Keep retired keys as `verify-only` until all grants expire, then mark
them `revoked`. Invalid, incomplete, rolled-back, or newly expired material makes
readiness fail and denies new RPC authorization until corrected. The provider
re-verifies the peer certificate and URI policy against the current client CA on
every RPC, including RPCs on an existing HTTP/2 connection.

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

For an existing single-key installation, first create the authority ConfigMap and
a generation-1 manifest that names the existing TLS and grant files, then upgrade
the chart. Confirm `/ready` before rotating. Do not reuse the old key ID with new
key bytes. Schema-2 environment migration is independent of keyring migration:
complete both before enabling new sessions.

## Live qualification (experimental)

The repository keeps real-provider qualification separate from the default mock
suite. It is explicit opt-in, uses an already-owned retained Kind cluster, and is
not part of `task test`:

```sh
MECATL_EXECUTION_CREDENTIAL_FILE=/absolute/path/to/provider-key \
MECATL_EXECUTION_QUAL_STATE=/absolute/path/to/owned-state \
task e2e:k8s:execution:live
```

The credential file must be a private regular file (no group or other access).
A trusted helper loads it only at runtime and creates a run-scoped Kubernetes
Secret through the API; it never renders the value into a manifest or reads the
Secret back. Only the `mecak8s` harness container receives the OpenRouter key.
The execution provider, controller, OIDC fixture, and executor workloads do not.
The task first runs the deterministic mock qualification, then runs one bounded
real-model coding smoke against `https://openrouter.ai/api/v1`. It verifies
positive token usage, required file and shell tool calls, file contents, and a
successful `go test` independently through the typed gRPC execution service.
Finally it restores the mock deployment and removes only its run-scoped Secret;
the owned cluster and execution workspaces remain for inspection.

## Limitations

- No-FS sessions cannot use local file tools, shell commands, workspace forks,
  or parallel branches.
- Read-only child shells depend on project trust; this is separate from the
  parent's ability to run its own shell.
- Direct-write children can leave partial edits if cancelled or interrupted; the
  parent workspace and Git are the rollback boundary.
- Remote environment reattachment requires an explicit resolver.

## Next steps

- [Background Shell](/building/what-you-get/core-tools.md#background-commands)
- [Subagents, teams, and parallel](/building/what-you-get/subagents-teams-parallel.md)
- [Workspace trust](/features/permissions-and-posture.md)
