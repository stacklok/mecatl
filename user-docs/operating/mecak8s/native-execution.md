---
title: Configure Kubernetes execution
description:
  Provision session workspaces with the separate Kubernetes execution provider.
sidebar_position: 2
---

# Configure Kubernetes execution

Give selected sessions filesystem tools and foreground Shell commands through an
operator-controlled Kubernetes execution provider. The provider runs as a
separate service and controller with its own `mecatl-execution` chart. `mecak8s`
is its client and receives no Pod, PVC, custom-resource, or controller
management permissions.

This deployment is intended for trusted teams. It does not establish general
production maturity, hostile-tenant isolation, or force-takeover recovery. Keep
the default no-filesystem placement for sessions that do not need execution
tools.

<span id="kubernetes-execution-provider"></span>

## Prerequisites

Start with an [authenticated mecak8s deployment](identity-and-client-access.md)
and a namespace dedicated to this provider release. You need Helm 3.16 or newer,
`kubectl`, and an operator-owned system for delivering images and Secrets.
Production isolation requires a CNI that enforces NetworkPolicy and a
RuntimeClass that supplies your platform's required isolation.

Before installation, prepare:

- Digest-pinned provider and workload images. The workload image must contain
  `/mecatl-executor`, `/bin/sh`, and the toolchain used by foreground commands.
- Security files in operator-owned Kubernetes Secrets. Use `securitySecretName`
  for one Secret or `securitySources` for mappings from multiple Secrets. The
  provider mounts projected files without `subPath`; your Secret delivery system
  continues to own their contents.
- A `mecak8s` client mTLS Secret containing `ca.crt`, `tls.crt`, and `tls.key`.
- Provider-client pod and namespace selectors, API-server CIDRs, and DNS
  resolver CIDRs.

See
[native Kubernetes lifecycle and retention](/features/security-and-execution/execution-environments.md#native-kubernetes-lifecycle-and-retention)
for restart recovery, fencing, and capacity behavior.

## Prepare registry access

Qualify workload derivatives through actual file and command operations, not
just image-reference validation. The Kind fixture builds a positive derivative
with an operator utility and a negative derivative whose `/mecatl-executor`
returns an incompatible protocol. The negative control must fail its bound
command, fence further operations, and retain the exact PVC and sentinel data.
A running Pod or a catalog's declared affordances alone do not prove runtime
compatibility.

For private registry pulls, create pull Secrets in the execution namespace
before installing the provider chart. Import a locally prepared Docker config
without putting credential contents in Helm values or command arguments:

```sh
kubectl --namespace <NAMESPACE> create secret generic provider-registry \
  --type=kubernetes.io/dockerconfigjson \
  --from-file=.dockerconfigjson=<LOCAL_DOCKER_CONFIG_JSON>
```

Set `provider.imagePullSecrets: [provider-registry]` for the provider image and
`templates.<id>.revisions.<revision>.execution.imagePullSecrets: [workload-registry]`
for each workload image that needs one. Each optional list accepts up to eight
unique Kubernetes Secret names. The chart creates a release-derived executor
ServiceAccount with no RBAC and token automount disabled, retained while executor
Pods may survive provider removal; executor Pods also disable token automount.
Keep registry credentials out of workload environment variables. Template
execution definitions are immutable while retained allocations exist; publish a
new pinned revision for changed pull Secrets and retain the old definition.

## Configure the provider and execution templates

The provider validates every configured RuntimeClass and StorageClass with
cluster-scoped `get` requests before it becomes ready. RuntimeClasses must have
no scheduling selectors or tolerations: only an operator template may define
executor scheduling. The chart grants those requests only for names present in
pinned template revisions; it grants no cluster-wide list or watch access.

Prepare `execution-values.yaml` with provider identity and policy, bounded
execution templates, and network and resource limits. Replace every placeholder
below. The revision key and default must be the canonical `v1-` digest of the
validated execution definition; do not invent one. Use the fixture renderer in
`deploy/mecatl-execution-kind/fixture/templates` as a reference for deriving
canonical revisions from operator-reviewed execution recipes:

```yaml
fullnameOverride: mecatl-execution
provider:
  image: <PROVIDER_IMAGE>@sha256:<PROVIDER_DIGEST>
  imagePullPolicy: IfNotPresent
  replicas: 2
  securitySecretName: <PROJECTED_SECURITY_SECRET>
  securityManifest: |
    {
      "version": 1,
      "generation": 42,
      "tls": {
        "certificateFile": "tls.crt",
        "privateKeyFile": "tls.key",
        "clientCAFile": "clients.pem"
      },
      "clients": [{
        "uri": "spiffe://cluster.example.com/ns/mecatl/sa/mecak8s",
        "mayAttestOwner": true,
        "administrator": false,
        "executionTemplates": ["coding"]
      }, {
        "uri": "spiffe://cluster.example.com/ns/mecatl/sa/execution-admin",
        "mayAttestOwner": false,
        "administrator": true,
        "administratorFor": ["spiffe://cluster.example.com/ns/mecatl/sa/mecak8s"]
      }]
    }
  clientIngressSelectors:
    - namespaceLabels: { kubernetes.io/metadata.name: <CLIENT_NAMESPACE> }
      podLabels: { app.kubernetes.io/name: mecak8s }
  apiServerCIDRs: [<API_SERVER_IP>/32]
  dnsCIDRs: [<CLUSTER_DNS_IP>/32]

service:
  port: 8443

templates:
  coding:
    default: v1-<CANONICAL_EXECUTION_DIGEST>
    revisions:
      v1-<CANONICAL_EXECUTION_DIGEST>:
        display:
          name: Coding workspace
          description: Approved development toolchain
        execution:
          image: <WORKLOAD_IMAGE>@sha256:<WORKLOAD_DIGEST>
          storageClass: <STORAGE_CLASS>
          storageSize: 2Gi
          cpuRequest: 100m
          memoryRequest: 128Mi
          cpuLimit: '1'
          memoryLimit: 1Gi
          ephemeralStorageRequest: 64Mi
          ephemeralStorageLimit: 1Gi
          tmpSizeLimit: 256Mi
          runtimeClassName: <RUNTIME_CLASS>
          maxFileBytes: 5242880
          maxCommandBytes: 1048576
          maxCommandDuration: 5m
          maxEnvironments: 100

networkPolicy:
  enabled: true
  dnsPorts: [53]
  apiServerPorts: [443]
  workloadProfiles: {} # default deny; add explicit coding egress only if required

resourceGovernance:
  enabled: true
  pods: '200'
  persistentVolumeClaims: '100'
  executionEnvironments: '100'
  requestsCPU: '20'
  requestsMemory: 40Gi
  requestsStorage: 500Gi
  requestsEphemeralStorage: 40Gi
  limitsCPU: '40'
  limitsMemory: 80Gi
  limitsEphemeralStorage: 80Gi
```

`nodeSelector` and `tolerations` are optional, immutable, operator-only
execution settings; clients cannot select or change them. Omit either when
unused: the chart rejects explicit empty maps and lists. A revision accepts at
most 32 qualified label selectors and 16 unique tolerations. Retain historical
execution definitions while allocations refer to them; publish a new canonical
revision instead of changing an existing definition. Deprecating a revision
stops new selection; revocation also prevents use and renewal. The per-template
capacity bound applies across retained revisions.

## Prepare platform-managed TLS and client policy

The provider's TLS certificate, private key, and client CA bundle come from your
platform PKI. Use short-lived identities, projected Secrets, and independent
renewal. A policy change increments `generation`; certificate and trust renewal
does not. Never place private-key bytes in Helm values.

To split TLS files across Secrets, replace `securitySecretName` with explicit
`securitySources` mappings:

```yaml
provider:
  securitySecretName: ''
  securitySources:
    - name: server-tls
      items:
        - { key: cert, path: tls.crt }
        - { key: key, path: tls.key }
    - name: client-roots
      items:
        - { key: ca, path: clients.pem }
```

Create the named Secrets in the provider namespace before installation. Each
`path` is a unique basename in the mounted directory. The chart projects only
the listed Secret keys and `manifest.json` from its ConfigMap. It accepts up to
eight unique Secret names and 32 mappings total; source names, keys, and paths
are bounded. It rejects duplicate keys, duplicate file paths, `manifest.json`
as a Secret destination, and paths with directories. Secret values belong in
Kubernetes Secrets, not Helm values. The provider reloads valid projected TLS
files without changing the manifest generation. A client-policy change requires
a higher-generation manifest; a same-generation policy digest drift fails closed.

## Install the provider

Install the provider chart separately from `mecak8s`, before allocating any
execution environments. Helm 3.16 is the minimum supported version. Helm 4 uses
server-side apply by default, so each chart lifecycle command selects
client-side mode when needed.

Choose a published chart version from
[GitHub releases](https://github.com/stacklok/mecatl/releases). Replace
`<VERSION>` with its number without the leading `v`. Use a namespace dedicated
to this provider release:

```sh
HELM_APPLY_MODE=
case "$(helm version --template '{{.Version}}')" in
  v4.*) HELM_APPLY_MODE=--server-side=false ;;
esac
helm install mecatl-execution oci://ghcr.io/stacklok/mecatl/charts/mecatl-execution \
  --version <VERSION> \
  --namespace <NAMESPACE> \
  --values execution-values.yaml \
  $HELM_APPLY_MODE
kubectl rollout status deployment/mecatl-execution --namespace <NAMESPACE>
```

## Connect mecak8s to the provider

Add the following block to the existing `mecak8s` values. The endpoint is a
`host:port` gRPC target (no URL scheme) and must use the provider certificate's
DNS identity. The private `mecatl.execution.v1.ExecutionProviderService` uses
protocol `execution-grpc/1` with mandatory mTLS. Keep OIDC caller authentication
enabled; the remote binding is scoped to the verified issuer and subject.

```yaml
execution:
  enabled: true
  endpoint: mecatl-execution.<NAMESPACE>.svc:8443
  templateID: coding
  templateRevision: v1-<CANONICAL_EXECUTION_DIGEST>
  allowedSubjects:
    - <VERIFIED_OIDC_SUBJECT>
  tlsSecret: <MECAK8S_EXECUTION_MTLS_SECRET>
  caKey: ca.crt
  certKey: tls.crt
  keyKey: tls.key
```

An enabled client conflicts with `workspace`, `redis.filesystem.enabled`,
Parallel, and Team. Remote sessions receive filesystem tools and foreground
Shell only when the selected template grants them. An explicit `execution.none`
gets no filesystem, even when a template default is configured. Remote sessions
do not ingest project instructions, rules, skills, or source from the PVC.
Schedules, SkillDraft, background Shell, and delegated filesystem execution are
outside this deployment.

## Verify allocation and diagnose failures

Apply the updated values through your normal `mecak8s` Helm upgrade, then start
a new session using the pinned default or an eligible catalog template. Confirm
that its allocation reaches `Ready=True` before using filesystem or Shell tools.

If allocation reports `Ready=false` with `PVCUnavailable` or
`ExecutorUnavailable`, inspect namespace quota, admission failures, and the
executor Pod's events. For a private-image pull failure, use
`kubectl --namespace <NAMESPACE> get pods` and
`kubectl --namespace <NAMESPACE> describe pod <POD_NAME>` for the provider or
executor Pod; check the named pull Secret and image digest. Correct the failed
prerequisite and wait for `Ready=True`. The controller keeps retrying through
its rate-limited queue while the allocation exists; no reference edit, restart,
or manual reconciliation is needed. Missing authoritative PVCs or Pods and
ownership mismatches remain fail-closed and are never repaired by creating a
replacement.

## Preserve workspace state during recovery

The provider retains one PVC per logical environment. Session deletion and chart
uninstall preserve committed workspace data. Retirement requires no live
references and a terminal executor, and retains the PVC until an explicit
storage-deletion operation succeeds.

A missing executor or lost terminal receipt moves the environment to
`FenceUnknown` and requires external operator fencing. The built-in recovery RPC
accepts only a still-observable Pod in `Succeeded` or `Failed` phase with every
container terminated and the exact PVC still present. If that proof is missing,
use your platform's external fencing runbook; there is no acknowledgement flag.

Follow
[execution-provider recovery](execution-provider-lifecycle.md#recover-a-fenced-environment)
when terminal proof is required.

Run claims renew before expiry while the exact peer identity and current
client policy remain valid. Exact retained retries preserve the claim and
original expiry. Conflicting or expired replay cannot extend or resurrect the
claim. Withdrawn trust or expired certificates abort affected in-flight RPCs.

## Next steps

- [Maintain execution-provider authority and state](/operating/mecak8s/execution-provider-lifecycle.md).
- [Configure session state](/operating/mecak8s/state-and-execution.md).
