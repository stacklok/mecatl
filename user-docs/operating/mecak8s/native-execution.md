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
- A `mecak8s` client mTLS Secret containing `ca.crt`, `tls.crt`, and `tls.key`,
  issued with the [required SAN, EKU, and trust configuration](#prepare-platform-managed-tls-and-client-policy).
- Provider-client pod and namespace selectors, API-server CIDRs, and DNS
  resolver CIDRs.

See
[native Kubernetes lifecycle and retention](/features/security-and-execution/execution-environments.md#native-kubernetes-lifecycle-and-retention)
for restart recovery, fencing, and capacity behavior.

## Build a downstream workload image

Use `ghcr.io/stacklok/mecatl/mecatl-execution-workload` from the same release as
`mecatl-execution-provider`, pinned by digest. The release image matrix is
**Linux AMD64 (`linux/amd64`) and Linux ARM64 (`linux/arm64`)**. Match the executor
binary and every added native utility to the target architecture.

For the **`v1-` execution-template contract**, preserve these runtime requirements:

| Requirement | Downstream image contract |
| --- | --- |
| Executor compatibility | Keep `/mecatl-executor` from the selected release. The provider API version is `execution-grpc/1`; the private executor stdin/stdout JSON protocol has no separate version negotiation. Pair provider and executor from the same release and requalify upgrades rather than assuming cross-release helper compatibility. |
| Identity | The Pod runs as UID **65532**, GID **65532**, with filesystem group **65532**. Added programs must be executable by that identity without privilege escalation or Linux capabilities. |
| Shell and startup | Keep `/bin/sh` and a `sleep` utility supporting `sleep infinity` on the image's startup PATH. The provider overrides the container command with `/bin/sh -c 'trap : TERM INT; sleep infinity & wait'`; the shell must support `trap`, background jobs, and `wait`. |
| Writable paths | `/workspace` is the retained workspace PVC; `/tmp` is a size-bounded ephemeral `emptyDir`. The image root filesystem is read-only. Put caches and generated files on those writable mounts, and install utilities into the image at build time. Image contents under the mount points are hidden by the mounts. |
| Foreground commands | The executor invokes `/bin/sh -c` in `/workspace` with only `HOME=/workspace`, `PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin`, `TMPDIR=/tmp`, and `GOTMPDIR=/tmp`. Image `ENV` additions and provider credentials are not inherited by these commands. |

Add utilities without replacing the executor. For example, after replacing the
digest placeholder, build this Dockerfile with a reviewed executable named
`reviewed-utility` in the build context:

```dockerfile
FROM ghcr.io/stacklok/mecatl/mecatl-execution-workload@sha256:<BASE_IMAGE_DIGEST>
COPY --chown=65532:65532 --chmod=755 reviewed-utility /usr/local/bin/reviewed-utility
USER 65532:65532
WORKDIR /workspace
```

Publish the derivative through your image pipeline, then use its immutable
reference, such as `registry.example.com/team/toolchain@sha256:<DERIVATIVE_DIGEST>`,
as the recipe's `image`. A tag or catalog description is not compatibility proof.

Use the [deployed-image smoke test](#smoke-test-the-deployed-image) to check your
selected derivative through a real session. Repository contributor/base-image
qualification uses `task e2e:k8s:execution:production` from a matching checkout
with the Taskfile's Kind prerequisites. That cluster test builds its own base,
utility derivative, and incompatible-executor control; it does not accept an
arbitrary deployed image. Its tested lifecycle and fencing assertions are
separate from the operator smoke test and do not establish general production
readiness or hostile-tenant isolation.

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
`templates.<id>.revisions.<revision>.execution.imagePullSecrets: [workload-registry]`
for each workload image that needs one. Each optional list accepts up to eight
unique Kubernetes Secret names. The chart creates a release-derived executor
ServiceAccount with no RBAC and token automount disabled, retained while executor
Pods may survive provider removal; executor Pods also disable token automount.
Keep registry credentials out of workload environment variables. Template
execution definitions are immutable while retained allocations exist; publish a
new pinned revision for changed pull Secrets and retain the old definition.

## Author and validate template revisions offline

From a source checkout matching the provider release, use the standalone
`deploy/execution-templates` generator with Go and the module dependencies already
available. It needs no cluster, provider process, or credentials. The Kind harness
uses the same generator; the two-argument form below accepts your own reviewed
recipes without fixture-specific image substitution.

Create `recipes.yaml`. Each `recipes.<ID>` entry contains execution fields plus
`maxEnvironments` and optional `display` metadata. Replace the placeholders:

```yaml
recipes:
  coding:
    maxEnvironments: 1000
    display:
      name: Coding workspace
      description: Approved development toolchain
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
```

Generate a new values file and validate it independently:

```sh
go run ./deploy/execution-templates recipes.yaml template-values.yaml
go run ./deploy/execution-templates -validate template-values.yaml
```

Generation refuses to overwrite an existing file and validates the result with
the provider's loader before publishing it. Validation exits successfully without
output; invalid input exits nonzero. The generated file contains only `templates`,
ready to pass to Helm alongside the provider values below. Validation checks
schema, bounds, and exact revision identity; class existence, image pulls, and
runtime compatibility still require cluster qualification.

The generated values have this structure. Use the complete generator output,
including its exact `v1-...` key in both revision positions; placeholders below
illustrate the mapping, not a deployable digest. The optional revision policy
flags can be added without changing that key:

```yaml
templates:
  coding:
    maxEnvironments: 1000
    default: v1-<CANONICAL_EXECUTION_DIGEST>
    revisions:
      v1-<CANONICAL_EXECUTION_DIGEST>:
        deprecated: false
        revoked: false
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
```

`maxEnvironments` is a required positive integer at the template-ID level.
Quota, display, `deprecated`, and `revoked` are adjustable policy or metadata;
they are outside the immutable execution recipe hash. Keep the emitted revision
identity rather than hashing YAML yourself. A default must name a selectable
revision. Omit `default` only when all of that ID's retained revisions are
deprecated or revoked.

For an update, generate to a fresh filename, then merge the new revision into
your retained `template-values.yaml` and choose its `default`. Preserve every
historical execution definition still required by the
[lifecycle and pruning procedure](execution-provider-lifecycle.md). Rerun
`-validate` on the merged template file before deployment. The generator creates
one revision per input ID; it does not merge or prune history. Keep provider,
network, and other Helm values in `execution-values.yaml`, separate from this
strictly validated template file.

## Configure the provider and execution templates

The provider checks RuntimeClasses and StorageClasses used by selectable
revisions and by **any existing allocation**, including allocations of deprecated
or revoked revisions. It skips ineligible retained definitions with no
allocations. These are global startup checks: one missing required class blocks
the provider, not just that revision's allocations. RuntimeClasses must have no
scheduling selectors or tolerations; only an operator template may define
executor scheduling. The chart grants cluster-scoped `get` only for names in
pinned revisions, with no cluster-wide list or watch access.

Prepare `execution-values.yaml` with provider identity and policy, network limits,
and namespace resource budgets. Replace every placeholder below and use the
separate `template-values.yaml` generated above:

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

networkPolicy:
  enabled: true
  dnsPorts: [53]
  apiServerPorts: [443]
  workloadProfiles: {} # default deny; add explicit coding egress only if required

resourceGovernance:
  enabled: true
  pods: '1100'
  persistentVolumeClaims: '1000'
  executionEnvironments: '1000'
  requestsCPU: '120'
  requestsMemory: 150Gi
  requestsStorage: 2100Gi
  requestsEphemeralStorage: 100Gi
  limitsCPU: '1100'
  limitsMemory: 1100Gi
  limitsEphemeralStorage: 1100Gi
```

`nodeSelector` and `tolerations` are optional, immutable, operator-only
execution settings; clients cannot select or change them. Omit either when
unused: the chart rejects explicit empty maps and lists. A revision accepts at
most 32 qualified label selectors and 16 unique tolerations. Retain historical
execution definitions while allocations refer to them; publish a new canonical
revision instead of changing an existing definition. Deprecating a revision
stops new selection; revocation also prevents use and renewal. The per-template
capacity bound applies across retained revisions. Change `templates.<ID>.maxEnvironments`
without generating a new revision; increasing it admits additional allocations,
while decreasing it below current use only rejects new reservations. The chart
requires explicit nonempty `resourceGovernance` budgets when enabled; the above
example budgets 1,000 2Gi workspaces with provider headroom. Size these quotas for
your own workloads and cluster capacity; the chart does not silently disable
namespace quota. Each template ID has its own retained allocation ConfigMap (name
`mecatl-exec-alloc-<first 32 hex of SHA-256 of ID>`); the ledger for that ID
and the combined rendered templates ConfigMap each have a Kubernetes 1 MiB data
limit. The 1,000-workspace example is a budgeting example, not a measured
throughput guarantee. A large declared quota cannot override either byte limit.
Retain each ID's reservation ledger; missing or inconsistent capacity authority
requires trusted-state recovery, never a reset or copied reservations.

## Prepare platform-managed TLS and client policy

The provider's TLS certificate, private key, and client CA bundle come from your
platform PKI. Use short-lived identities, projected Secrets, and independent
renewal. Arrange the following before installing:

| Material | Required identity and trust |
| --- | --- |
| Provider `tls.crt` and `tls.key` | A matching, currently valid certificate/key pair with server-auth EKU and a DNS SAN matching the client endpoint, such as `mecatl-execution.<NAMESPACE>.svc`. Include the serving certificate chain. |
| Provider `clients.pem` | PEM CA bundle that trusts the issuing chains of the `mecak8s` and administrator client certificates. |
| Each client's `tls.crt` and `tls.key` | A matching, currently valid certificate/key pair with client-auth EKU and **exactly one URI SAN** matching its `clients[].uri`. Include the client certificate chain. Use a separate identity for administration. |
| Each client's `ca.crt` | PEM CA bundle that trusts the provider's serving chain; server DNS verification remains enabled. |

Use a canonical URI with nonempty scheme and host, lowercase scheme and host,
and no userinfo, query, fragment, or wildcard, for example
`spiffe://cluster.example.com/ns/mecatl/sa/mecak8s`. The URI is an exact
allowlist identity; a Kubernetes ServiceAccount name alone does not issue or
authorize that certificate.

This mTLS authenticates the **service** connecting to the execution provider.
The user's identity is the OIDC `(issuer, subject)` verified by `mecak8s` and
attested under `mayAttestOwner`. The workload helper receives neither these
credentials nor a Kubernetes API token. This setup is not a complete agent
identity system.

A policy change increments `generation`; certificate and trust renewal does
not. Never place private-key bytes in Helm values or command arguments.

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
  --values template-values.yaml \
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
Parallel, and Team. Copy `execution.templateRevision` from the generated
revision key. Changing `templates.coding.default` on the provider does not
update this explicit `mecak8s` pin; change the client values deliberately when
choosing a different default for new sessions. Existing sessions keep their
bound environment. Remote sessions receive filesystem tools and foreground
Shell only when the selected template grants them. An explicit `execution.none`
gets no filesystem, even when a template default is configured. Remote sessions
do not ingest project instructions, rules, skills, or source from the PVC.
Schedules, SkillDraft, background Shell, and delegated filesystem execution are
outside this deployment.

## Smoke-test the deployed image

Use this check after installing the derivative's generated revision and updating
`mecak8s` values. It exercises the deployed image, not repository fixtures.
Prerequisites are an authorized OIDC account, available template capacity, and
session capabilities that include files and foreground Shell. Configure an
operator permission rule to **Ask** for the utility command, following
[Permissions and posture](/features/security-and-execution/permissions-and-posture.md).
Choose a harmless, deterministic invocation of your added utility; the example
below assumes `reviewed-utility --version` reports a known version without
network access.

1. Connect to the authenticated deployment with
   [mecatui](/mecatui/remote-servers.md) or [Studio](/operating/studio.md).
   In the [TUI execution picker](/mecatui/using-the-tui.md#start-a-session-with-an-execution-choice)
   or [Studio Execution menu](/operating/studio.md#choose-execution-for-a-new-chat),
   select `coding` and the exact generated revision containing your derivative
   digest. Record that revision and the resulting session ID; a display name
   alone does not identify the image.
2. As the operator, identify the allocation by its `spec.bindingID` (the session
   ID) and verify its selected revision and readiness:

   ```sh
   kubectl --namespace <NAMESPACE> get executionenvironments \
     -o custom-columns='NAME:.metadata.name,SESSION:.spec.bindingID,TEMPLATE:.spec.templateID,REVISION:.spec.templateRevision,READY:.status.conditions[?(@.type=="Ready")].status'
   ```

   Expect the recorded session, `coding`, the exact revision, and `True`.
3. Send this prompt, using a filename unique to this smoke test:

   ```text
   Use Write to create smoke-<UNIQUE_ID>.txt containing "image smoke one\n".
   Read that file. Use Edit to replace exactly "image smoke one" with
   "image smoke two", then Read it again. Use foreground Shell to run
   reviewed-utility --version. Do not install anything or access credentials.
   ```

   Review the actual tool calls and results, not only the model's summary.
   Approve only the expected utility command when prompted. Expect Write and
   Edit success, final file content `image smoke two`, and exit code 0 with your
   utility's expected version. A denied permission is not an image failure;
   investigate the policy rather than bypassing it.
4. Wait for the run to finish, disconnect, and reopen **the same session** using
   the same account and server. With the TUI, use
   `mecatui connect <ADDRESS> --resume <SESSION_ID>` and the same TLS/login
   options as the initial connection. In Studio, reload and open that chat from
   history. Ask for another `Read` of the file and another approved utility
   invocation. Expect the edited contents and the same utility version. Read
   again before any subsequent Edit; reconnecting can discard read-version
   evidence without losing file contents.

This smoke test checks file operations, the added utility, approvals, and
same-session reattachment. It is not production qualification, a fencing proof,
or a test of node-loss recovery. Keep the test workspace until its evidence is
reviewed; session deletion does not dispose of the retained PVC. Use the
[lifecycle procedure](execution-provider-lifecycle.md#replace-or-retire-an-executor)
for deliberate retirement and storage deletion.

## Verify allocation and diagnose failures

Apply the updated values through your normal `mecak8s` Helm upgrade, then start
a new session using the pinned default or an eligible catalog template. Confirm
that its allocation reaches `Ready=True` before using filesystem or Shell tools.

### Provider startup fails on a class dependency

Check provider startup diagnostics for `profile preflight` and the reported
RuntimeClass or StorageClass name. `profile-resource-unavailable` is the
readiness reason for this check. Inspect the class and the provider's named
`get` permissions; a RuntimeClass with scheduling selectors or tolerations is
also rejected.

Restore the required class and its intended platform implementation, or restore
missing RBAC, then restart the failed provider process. Do not create a
same-named class backed by a less isolated runtime to bypass the check. If the
class is no longer supported, restore service long enough to finish the
supported retirement and storage-deletion workflow for affected allocations.
Keep their data if disposal is not authorized. For an unused revision, mark it
deprecated or revoked in `template-values.yaml`, select another eligible default
(or omit the default for a wholly ineligible ID), and update any explicit
`mecak8s` pin through a reviewed deployment. Use the quiesced lifecycle procedure
to apply configuration; retain the immutable recipe unless explicit pruning is
safe. A deprecated or revoked revision with an allocation still requires its
classes. There is no runtime fallback or per-allocation startup isolation.

### Allocation or image startup fails

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

If allocation is refused before a CR appears, check the ID's pooled
`maxEnvironments` usage and its retained reservation ledger. Increase the quota
only within reviewed namespace and cluster budgets, or retire and explicitly
delete storage for environments you no longer need. A full 1 MiB ledger needs
space reclaimed through that lifecycle; raising the quota cannot enlarge it.
Do not delete ledger entries or unrelated workspaces to clear a capacity error.

For an mTLS failure, check certificate validity, client URI SAN, EKUs, serving
DNS identity, and both trust bundles without printing credentials. A connection
timeout also requires checking client ingress selectors and client-side egress.
For denied run acquisition or renewal, check current client template scope and
revision revocation; replaying an existing claim cannot bypass withdrawn policy.
Reference release and authorized administrative cleanup remain available under
their own safety checks.

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
