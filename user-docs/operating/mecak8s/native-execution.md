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

For private registry pulls, create pull Secrets in the execution namespace
before installing the provider chart. Import a locally prepared Docker config
without putting credential contents in Helm values or command arguments:

```sh
kubectl --namespace <NAMESPACE> create secret generic provider-registry \
  --type=kubernetes.io/dockerconfigjson \
  --from-file=.dockerconfigjson=<LOCAL_DOCKER_CONFIG_JSON>
```

Set `provider.imagePullSecrets: [provider-registry]` for the provider image and
`profiles.<name>.imagePullSecrets: [workload-registry]` for each workload image
that needs one. Each optional list accepts up to eight unique Kubernetes Secret
names. The chart creates a release-derived executor ServiceAccount with no RBAC
and token automount disabled, retained while executor Pods may survive provider
removal; executor Pods also disable token automount and use only their profile's
pull Secret names. Keep registry credentials out of workload environment
variables and the security material Secret. Changing a profile's pull Secret
names changes its immutable identity and is incompatible with retained
allocations; quiesce and retire them before changing that profile. Provider-only
pull Secret names are not workload identity and may change in a compatible
quiesced upgrade.

## Configure the provider and workload profile

The provider validates every configured RuntimeClass and StorageClass with
cluster-scoped `get` requests before it becomes ready. RuntimeClasses must have
no scheduling selectors or tolerations: only the operator profile may define
executor scheduling. The chart grants those requests only for the names present
in `profiles`; it grants no cluster-wide list or watch access.

The following values provide the required provider, security, and workload
configuration. Save it as `execution-values.yaml` and replace each placeholder:

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
      "issuer": "https://execution.example.com",
      "audience": "mecatl-execution",
      "activeKeyID": "k1",
      "grantTTL": "1m",
      "clockSkew": "5s",
      "keys": [{
        "id": "k1",
        "version": 1,
        "file": "grant-k1.pem",
        "publicKeySHA256": "0000000000000000000000000000000000000000000000000000000000000000",
        "activateAt": "2027-01-01T00:00:00Z",
        "verifyUntil": "2027-01-02T00:00:00Z",
        "state": "active"
      }],
      "tls": {
        "certificateFile": "tls.crt",
        "privateKeyFile": "tls.key",
        "clientCAFile": "clients.pem"
      },
      "clients": [{
        "uri": "spiffe://cluster.example.com/ns/mecatl/sa/mecak8s",
        "mayAttestOwner": true,
        "administrator": false
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

profiles:
  coding:
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
    # Optional operator-only scheduling constraints.
    nodeSelector:
      node.kubernetes.io/instance-type: <WORKER_TYPE>
    tolerations:
      - key: dedicated
        operator: Equal
        value: build
        effect: NoSchedule
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

`nodeSelector` and `tolerations` are optional, immutable, operator-only profile
settings; clients cannot select or change them. Omit either setting when unused:
the chart rejects explicit empty maps and lists. A profile accepts at most 32
qualified label selectors and 16 unique tolerations. Tolerations use `Exists`
(with an empty value) or `Equal` (with a key); `tolerationSeconds` must be
between 0 and 86,400 seconds and only applies to `NoExecute`. New executor Pods
explicitly include the 300-second `NoExecute` tolerations for
`node.kubernetes.io/not-ready` and `node.kubernetes.io/unreachable` unless the
profile already specifies that key and effect; previously created Pods may
retain cluster-configured default durations of up to one day. The controller
rejects other scheduling or affinity mutations. Scheduling changes the immutable
profile identity. While retained allocations or configuration remain, the chart
rejects profile changes even after quiescing and retiring allocations; use a new
provider release in a dedicated namespace instead.

## Prepare signing and TLS material

Replace the all-zero `publicKeySHA256` with the lowercase hexadecimal SHA-256
hash of the **raw 32-byte Ed25519 public key** corresponding to `grant-k1.pem`,
not the PEM text or DER encoding. Replace the sample activation and verification
dates with a current, reviewed rotation window. Increase `generation` for every
authority change, including a CA, client policy, issuer/audience, key state, key
window, or TLS identity change.

To split security files across Secrets, replace `securitySecretName` in the
provider values with an explicit mapping. For example, use these sources with
the manifest above:

```yaml
provider:
  securitySecretName: ''
  securitySources:
    - name: grant-keys
      items:
        - { key: signing.pem, path: grant-k1.pem }
    - name: server-tls
      items:
        - { key: cert, path: tls.crt }
        - { key: key, path: tls.key }
        - { key: ca, path: clients.pem }
```

Create the named Secrets in the provider namespace before installation. Each
`path` is a unique basename in the mounted directory, and every manifest key
file and TLS file must have a mapping. The chart projects only the listed Secret
keys and `manifest.json` from its ConfigMap. It accepts up to eight unique
Secret names and 32 mappings total; source names, keys, and paths are bounded.
It rejects duplicate keys within a source, duplicate file paths, `manifest.json`
as a Secret destination, and paths with directories. Secret values belong in
Kubernetes Secrets, not Helm values. While allocations remain, preserve the
single-Secret versus multi-source mode and all files still named by the current
manifest. To rotate, add new Secret mappings and keys in a quiesced chart
upgrade before publishing the higher-generation manifest; the authority ledger
rejects same-generation material drift. With `securitySecretName`, the chart
projects the entire single Secret, including files staged for rotation.

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
  profile: coding
  tlsSecret: <MECAK8S_EXECUTION_MTLS_SECRET>
  caKey: ca.crt
  certKey: tls.crt
  keyKey: tls.key
```

An enabled client conflicts with `workspace`, `redis.filesystem.enabled`,
Parallel, and Team. Remote sessions receive the filesystem tools and foreground
Shell, but do not ingest project instructions, rules, skills, or source from the
remote PVC. Schedules, SkillDraft, background Shell, and delegated filesystem
execution are outside this deployment.

## Verify allocation and diagnose failures

Apply the updated values through your normal `mecak8s` Helm upgrade, then start
a new session on the configured execution profile. Confirm that its allocation
reaches `Ready=True` before using filesystem or Shell tools.

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

Run claims and signed grants renew before expiry, including with a short grant
TTL. Renewal receipts retain the newest 32 identities. An exact retained retry
preserves the claim and original expiry, although its signature can have a new
nonce. Conflicting or expired replay is denied and cannot extend or resurrect
the claim.

## Next steps

- [Maintain execution-provider authority and state](/operating/mecak8s/execution-provider-lifecycle.md).
- [Configure session state](/operating/mecak8s/state-and-execution.md).
