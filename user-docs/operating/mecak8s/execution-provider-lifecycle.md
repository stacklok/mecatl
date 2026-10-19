---
title: Maintain the execution provider
description:
  Rotate authority and safely upgrade or retire Kubernetes execution state.
sidebar_position: 3
---

# Maintain the execution provider

Use these procedures for an existing [execution provider](native-execution.md).
Preserve retained authority and workspace state during every lifecycle
operation.

## Rotate execution-provider authority

Client authorization policy and TLS materials rotate independently. Publish a
higher `provider.securityManifest` generation for changed client URI, owner, or
template scope. A policy update is durable only after provider replicas agree on
the new generation; a lagging replica fails closed. For provider/client
certificates and CA bundles, use platform PKI and projected Secrets. Stage trust
overlap, renew identities, and withdraw old trust only after the new chain is
accepted. Valid TLS renewals need no policy-generation increment; expiry or
trust withdrawal aborts affected in-flight RPCs. Do not reuse a policy
generation for different policy bytes or reset the authority ConfigMap to
bypass a failed check. Verify readiness and a known authorized operation after
each change without logging private keys or credential values.

## Upgrade, uninstall, and reinstall the execution provider

The supported lifecycle keeps the **same Helm release name, namespace, resource
names, retained template definitions, network policy configuration, and security Secret
source mode**. Keep both values files in your operator configuration store:

- `execution-values.yaml` owns provider settings, the nonsecret client-policy
  manifest, network policy, and namespace resource budgets.
- `template-values.yaml` owns `templates`, including ID quotas, defaults,
  retained execution recipes, and revision policy.

Pass both files to every install or upgrade. Keep operator-owned Secrets and
TLS trust overlap independently; the chart neither owns nor reads Secret contents.

Finish and verify any external authority rotation before starting a chart
upgrade. Do not rotate the authority ConfigMap while Helm is writing chart
resources. Each Helm lifecycle command below initializes `HELM_APPLY_MODE` for
the current shell. Helm 4 selects client-side field ownership, while Helm 3.16
leaves the value empty. Continue to use reviewed current values and the
quiescence procedure below. Do not use `--force-conflicts` or `--take-ownership`
as a blanket takeover.

Before upgrading, verify that the retained templates ConfigMap contains a
nonempty `data["lifetime.json"]`, the policy authority and **per-template-ID**
capacity ledgers are intact, and the release still owns its retained executor
ServiceAccount. That account must have token automount disabled and no inherited
pull Secrets. Stop if any check fails; do not synthesize missing history or
reset authority. `templates.<ID>.maxEnvironments` and explicit namespace
`resourceGovernance` budgets can change with reviewed values without changing
recipe digests; shrinking an ID quota below current use prevents only new
reservations. Capacity reservations are retained authority: preserve each ID's
ledger and recover trusted state if it is missing or inconsistent. `slots.json`
must contain an array of unique allocation names valid as Kubernetes DNS
subdomains. An empty array (`[]`) is valid; `null`, non-array values, and invalid
or duplicate names block reservations, releases, and Helm rendering without
rewriting the ledger. Never reset or copy reservations to make an upgrade pass.

### Upgrade a compatible release

For a compatible provider upgrade:

1. Stop new client traffic and finish or explicitly fence active work. Stop all
   provider replicas before Helm reads the ledgers:

   ```sh
   kubectl --namespace <NAMESPACE> scale deployment/mecatl-execution --replicas=0
   kubectl --namespace <NAMESPACE> wait --for=delete pod \
     --selector app.kubernetes.io/name=mecatl-execution --timeout=2m
   ```

2. Apply the reviewed CRD schema **before** starting the upgraded provider. Helm
   does not upgrade existing CRDs:

   ```sh
   helm show crds oci://ghcr.io/stacklok/mecatl/charts/mecatl-execution \
     --version <VERSION> > execution-crds.yaml
   kubectl apply -f execution-crds.yaml
   kubectl wait --for=condition=Established \
     crd/executionenvironments.execution.mecatl.dev --timeout=60s
   ```

3. Upgrade with the preserved lifetime configuration and current authority
   manifest. Helm reads the existing ConfigMaps and includes their actual ledger
   data, rather than empty bootstrap data, in the new release:

   ```sh
   HELM_APPLY_MODE=
   case "$(helm version --template '{{.Version}}')" in
     v4.*) HELM_APPLY_MODE=--server-side=false ;;
   esac
   helm upgrade mecatl-execution oci://ghcr.io/stacklok/mecatl/charts/mecatl-execution \
     --version <VERSION> \
     --namespace <NAMESPACE> --values execution-values.yaml \
     --values template-values.yaml \
     --wait --timeout=4m $HELM_APPLY_MODE
   ```

4. Verify readiness, exact environment/PVC UIDs, data, and network confinement
   before resuming traffic. Every environment requires schema version 2 in spec
   and status. Preserve rejected objects and their data while diagnosing a
   mismatch; do not reset or delete them to bypass validation. Run the same
   provider version on all replicas.

<span id="reuse-a-full-template-history"></span>

### Prune an unused retained revision

The selectable catalog holds at most 64 ID/revision entries; retained-only
historical definitions do not consume those slots. The combined ConfigMap data
limit, rather than an arbitrary history count, bounds retained definitions. To
publish beyond that physical limit, remove an unused historical definition in
the same upgrade that adds the next revision. Keep every definition still used
by an environment. The chart requires explicit `maintenance.pruneRevisions`
consent for each removed ID/revision pair; ordinary upgrades reject any
removal. The combined rendered `templates.yaml` and `lifetime.json` must also
fit within Kubernetes's 1 MiB ConfigMap data limit. This procedure requires
storage deletion for allocations of the revision being pruned. If that data
must remain, keep the definition instead.

1. While the provider is running, finish `RetireEnvironment` and
   `DeleteRetiredEnvironment` for every allocation of the **specific revision**
   being removed. Keep unrelated workspaces, including surviving revisions of
   the same ID. Stop client traffic and scale the provider to zero as in the
   upgrade procedure; keep it stopped through Helm apply. Lookup and apply do
   not form a transaction.
2. Verify that the existing Deployment has observed its zero-replica
   generation. For each affected template ID, inspect the retained per-ID
   `mecatl-exec-alloc-<first 32 hex of SHA-256 of ID>` ConfigMap. Its
   `slots.json` must be a canonical JSON array; each entry must resolve to an
   environment with a surviving revision. An unresolved pending reservation,
   missing ledger, orphan executor/PVC without an identifiable allocation, or
   reference to the removed revision blocks pruning. A Pod or PVC with execution
   labels or an `ExecutionEnvironment` owner reference must have a nonempty
   `execution.mecatl.dev/environment` label and verifiable allocation identity.
   Ordinary resources without execution evidence and verified allocations of
   other IDs do not block pruning. Inspect the live resources before continuing:

   ```sh
   kubectl --namespace <NAMESPACE> get deployment mecatl-execution \
     -o jsonpath='{.metadata.generation}{" "}{.status.observedGeneration}{" "}{.spec.replicas}{" "}{.status.replicas}{"\n"}'
   kubectl --namespace <NAMESPACE> get executionenvironments,pods,pvc
   kubectl --namespace <NAMESPACE> get configmap <AFFECTED_ID_LEDGER_NAME> \
     -o jsonpath='{.data.slots\.json}{"\n"}'
   ```

   If an affected check fails, finish supported lifecycle operations or recover
   trusted state; do not reset or delete retained resources.
3. Back up the existing `data["lifetime.json"]` and reviewed Helm values in
   your operator configuration store. Remove only the selected retired
   revision from `templates.<ID>.revisions` in `template-values.yaml`, add
   the next revision, and select an eligible default. Preserve the remaining
   recipes and network policy configuration. Validate the complete candidate
   template file before applying it:

   ```sh
   go run ./deploy/execution-templates -validate template-values.yaml
   ```

   For the single upgrade that removes it, pass the exact removed revision as
   consent:

   ```sh
   helm upgrade mecatl-execution oci://ghcr.io/stacklok/mecatl/charts/mecatl-execution \
     --version <VERSION> --namespace <NAMESPACE> --values execution-values.yaml \
     --values template-values.yaml \
     --set-json 'maintenance.pruneRevisions.<ID>=["<RETIRED_TEMPLATE_REVISION>"]' \
     --wait --timeout=4m $HELM_APPLY_MODE
   ```

   Set `HELM_APPLY_MODE` as in the compatible-upgrade procedure. Omit the
   prune setting on subsequent upgrades; it is valid only when the named
   definition exists in retained history and is absent from the candidate
   values. Verify the new `lifetime.json` and provider readiness before
   resuming traffic. If the upgrade fails, keep writers stopped and restore
   the trusted values/history or resolve the reported state mismatch before
   retrying. Do not use a rollback that would reintroduce a pruned definition
   in place of a subsequently published revision.

### Uninstall and reinstall with retained state

Default uninstall removes the provider but keeps runtime CRs, PVCs, surviving
executors, workload default-deny and profile NetworkPolicies, security authority
and per-template-ID capacity ledgers, and the template/security-manifest ConfigMaps. Uninstall is not
executor termination proof or storage disposal:

```sh
helm uninstall mecatl-execution --namespace <NAMESPACE>
```

Reinstall with the same identity and preserved values:

```sh
HELM_APPLY_MODE=
case "$(helm version --template '{{.Version}}')" in
  v4.*) HELM_APPLY_MODE=--server-side=false ;;
esac
helm install mecatl-execution oci://ghcr.io/stacklok/mecatl/charts/mecatl-execution \
  --version <VERSION> \
  --namespace <NAMESPACE> --values execution-values.yaml \
  --values template-values.yaml \
  --wait --timeout=4m $HELM_APPLY_MODE
```

Helm adopts retained resources only when their managed-by label and release-name
and release-namespace annotations match. The chart rejects missing or empty ledgers while allocations or capacity
reservations survive, and rejects unconsented changes to retained execution definitions.
Use the [pruning procedure](#prune-an-unused-retained-revision) for a
retired definition.
Template removal or egress edits require careful retention and quiescence:
retained allow policies are additive, so leaving an obsolete policy could widen
access. The provider rejects an older policy manifest against the retained
high-water generation; never reset that ledger to make readiness pass.

Use live Helm install/upgrade for this lifecycle. Offline `helm template` cannot
perform ownership or history lookups and is not an adoption mechanism. Keep
provider writers stopped for upgrades; lookup plus apply is not a cross-resource
transaction. Rendering rejects a nonzero existing provider Deployment or any
remaining provider Pod, including a terminating Pod. Changed release/namespace
adoption, chart rollback, `--force-conflicts`, `--take-ownership`, CRD or
namespace deletion with retained resources, and force-finalizer cleanup are
unsupported.

### Preserve state when decommissioning

Final infrastructure decommission is not automated by this provider. Supported
`RetireEnvironment` and `DeleteRetiredEnvironment` operations can terminate and
dispose of eligible individual environments; they do not remove the retained
NetworkPolicies, configuration, ledgers, or CRD. There is no supported final
infrastructure-cleanup procedure here. Manual destructive cleanup is outside the
supported lifecycle. Keep retention defaults and authority history, and never
reset ledgers or remove finalizers to bypass a failed safety check.

## Run an administrative lifecycle operation

Administrative RPCs require `administrator: true`. For a distinct operations
identity, set its `administratorFor` list to the canonical URI of each client
that created the environments it may administer, as in the
[setup values](native-execution.md). An absent or empty list permits only
self-administration. Keep `mayAttestOwner: false` for an operations-only
identity; administrative scope confers no filesystem, Shell, attach, run, or
reference access. See the
[scope constraints](/features/security-and-execution/execution-environments.md#production-security-material).

Publish administrator scope changes in a reviewed manifest with a higher
`generation`, preserve the authority high-water ConfigMap, and verify provider
readiness. All provider replicas must use the same version.

Run the RPC examples from an operator-managed administration Pod inside the
cluster, not from an executor Pod. In `execution-values.yaml`, include an ingress
selector for its namespace and Pod labels alongside the `mecak8s` selector:

```yaml
provider:
  clientIngressSelectors:
    - namespaceLabels: { kubernetes.io/metadata.name: <CLIENT_NAMESPACE> }
      podLabels: { app.kubernetes.io/name: mecak8s }
    - namespaceLabels: { kubernetes.io/metadata.name: <ADMIN_NAMESPACE> }
      podLabels: { app.kubernetes.io/name: execution-admin }
```

Provision the administration Pod with that label, `grpcurl`, version-matched
protocol source, and a read-only mount of its client mTLS Secret. Its namespace
must permit DNS resolution and egress to the provider Service on TCP 8443 (or
your configured `service.port`). Apply these selectors through the quiesced
upgrade procedure with both values files. A certificate alone does not grant
network access, and a network selector alone does not authorize an RPC. Keep
execution workloads default-deny; do not give them administration credentials.

The `kubectl` inventory commands can run from a separate authorized operator
workstation with access to the execution namespace. The in-cluster RPC examples
use Service DNS and need no Kubernetes API token.

Every administrative request still requires the original exact owner and
revision plus the operation's epoch, UID, or generation preconditions.
The scope names creators, not owners; it can include a creator whose login has
been removed. After maintenance, remove its scope entry and increase
`generation` again. Subsequent requests, including receipt retries on
established connections, are denied; already admitted lifecycle operations may
finish safe reconciliation.

An administrative `not_found` response deliberately does not distinguish a
missing environment from a wrong owner, revision, or creator scope. Check the
exact environment ID/revision and original owner against your authorized
operations record, then check that `administratorFor` names the original
creator's exact canonical client URI. If the scope is wrong, publish a reviewed
manifest at a higher `generation` and verify readiness before retrying. The RPC
will not disclose another creator's data to diagnose a scope mismatch.

### Capture the exact environment identity

First capture the private identity while the environment still has a reference:

```sh
kubectl --namespace <NAMESPACE> get executionenvironment <ENVIRONMENT_ID> \
  -o jsonpath='{.spec.revision}{"\n"}{.spec.ownerIssuer}{"\n"}{.spec.ownerSubject}{"\n"}{.status.epoch}{"\n"}{.status.pod.uid}{"\n"}{.status.pvc.uid}{"\n"}{.status.grantGeneration}{"\n"}'
```

Record those seven lines as `<REVISION>`, `<OWNER_ISSUER>`, `<OWNER_SUBJECT>`,
`<EPOCH>`, `<POD_UID>`, `<PVC_UID>`, and `<GRANT_GENERATION>`. The owner hash is
not reversible, so retain the bounded `spec.ownerIssuer` and `spec.ownerSubject`
attestation in your authorized operations record before removing the final
reference.

Obtain `contracts/proto` from the
[Mecatl source](https://github.com/stacklok/mecatl) at the same release tag as
the provider. Run the RPC examples from that checkout root with `grpcurl`
installed.

Set file references to trusted, mounted mTLS material. Keep private-key bytes
out of shell arguments and manifests:

```sh
EXECUTION_ENDPOINT=mecatl-execution.<NAMESPACE>.svc:8443
CA_FILE=/var/run/secrets/mecatl-admin/ca.crt
CERT_FILE=/var/run/secrets/mecatl-admin/tls.crt
KEY_FILE=/var/run/secrets/mecatl-admin/tls.key
PROTO=contracts/proto/mecatl/execution/v1/execution.proto
```

### Replace or retire an executor

Replace one executor by reusing the same operation ID for every retry:

```sh
grpcurl -cacert "$CA_FILE" -cert "$CERT_FILE" -key "$KEY_FILE" \
  -import-path contracts/proto -proto "$PROTO" \
  -d '{"environment":{"id":"<ENVIRONMENT_ID>","revision":"<REVISION>"},"owner":{"issuer":"<OWNER_ISSUER>","subject":"<OWNER_SUBJECT>"},"expectedExecutionEpoch":"<EPOCH>","expectedPodUid":"<POD_UID>","expectedPvcUid":"<PVC_UID>","operationId":"replace-<STABLE_UUID>"}' \
  "$EXECUTION_ENDPOINT" mecatl.execution.v1.ExecutionProviderService/ReplaceExecutor
```

To retire and then delete retained storage, call `RetireEnvironment` with the
same identity fields and a new stable operation ID. Poll until
`.status.conditions[?(@.type=="Retired")].status` is `True`, then call
`DeleteRetiredEnvironment` with the retained PVC UID and another stable
operation ID. The provider refuses either request while references, claims,
identity proof, or UID checks are incomplete.

```sh
grpcurl -cacert "$CA_FILE" -cert "$CERT_FILE" -key "$KEY_FILE" \
  -import-path contracts/proto -proto "$PROTO" \
  -d '{"environment":{"id":"<ENVIRONMENT_ID>","revision":"<REVISION>"},"owner":{"issuer":"<OWNER_ISSUER>","subject":"<OWNER_SUBJECT>"},"expectedExecutionEpoch":"<EPOCH>","expectedPodUid":"<POD_UID>","expectedPvcUid":"<PVC_UID>","operationId":"retire-<STABLE_UUID>"}' \
  "$EXECUTION_ENDPOINT" mecatl.execution.v1.ExecutionProviderService/RetireEnvironment
kubectl --namespace <NAMESPACE> wait executionenvironment/<ENVIRONMENT_ID> \
  --for='jsonpath={.status.conditions[?(@.type=="Retired")].status}=True' \
  --timeout=10m
grpcurl -cacert "$CA_FILE" -cert "$CERT_FILE" -key "$KEY_FILE" \
  -import-path contracts/proto -proto "$PROTO" \
  -d '{"environment":{"id":"<ENVIRONMENT_ID>","revision":"<REVISION>"},"owner":{"issuer":"<OWNER_ISSUER>","subject":"<OWNER_SUBJECT>"},"expectedPvcUid":"<PVC_UID>","operationId":"delete-<STABLE_UUID>"}' \
  "$EXECUTION_ENDPOINT" mecatl.execution.v1.ExecutionProviderService/DeleteRetiredEnvironment
```

### Revoke an environment

Revoke an environment using its durable `grantGeneration` fence (the wire name),
not a signed grant. Supply the current generation and a stable operation ID. The
response returns the new generation; retrying the identical request returns the
same receipt. Earlier operations cannot renew after this CAS succeeds.

```sh
grpcurl -cacert "$CA_FILE" -cert "$CERT_FILE" -key "$KEY_FILE" \
  -import-path contracts/proto -proto "$PROTO" \
  -d '{"environment":{"id":"<ENVIRONMENT_ID>","revision":"<REVISION>"},"owner":{"issuer":"<OWNER_ISSUER>","subject":"<OWNER_SUBJECT>"},"expectedGrantGeneration":"<GRANT_GENERATION>","operationId":"revoke-<STABLE_UUID>"}' \
  "$EXECUTION_ENDPOINT" mecatl.execution.v1.ExecutionProviderService/RevokeEnvironment
```

Foreground command cancellation is cooperative and bounded. The helper attempts
to terminate the command process group and reports a terminal receipt. If it
cannot prove termination, the environment is fenced instead of admitting more
work. There is no detached command status or cancellation API for this
deployment.

## Recover a fenced environment

Use the mTLS file references and version-matched protocol source configured
above.

The built-in recovery RPC requires the same exact identity and an independently
stable operation ID. It does not read a Secret and has no force or
acknowledgement field:

```sh
grpcurl -cacert "$CA_FILE" -cert "$CERT_FILE" -key "$KEY_FILE" \
  -import-path contracts/proto -proto "$PROTO" \
  -d '{"environment":{"id":"<ENVIRONMENT_ID>","revision":"<REVISION>"},"owner":{"issuer":"<OWNER_ISSUER>","subject":"<OWNER_SUBJECT>"},"expectedExecutionEpoch":"<EPOCH>","expectedPodUid":"<POD_UID>","expectedPvcUid":"<PVC_UID>","operationId":"recover-<STABLE_UUID>"}' \
  "$EXECUTION_ENDPOINT" mecatl.execution.v1.ExecutionProviderService/RecoverEnvironment
```

## Next steps

- [Observe and troubleshoot mecak8s](/operating/mecak8s/observe-and-troubleshoot.md).
- [Scale and recover the service](/operating/mecak8s/scale-recover-and-upgrade.md).
