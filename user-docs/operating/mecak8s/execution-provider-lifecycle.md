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
source mode**. Keep `execution-values.yaml` and the current nonsecret client-policy
manifest in your operator configuration store. Keep operator-owned Secrets and TLS trust overlap independently; the chart neither owns nor reads Secret
contents.

Finish and verify any external authority rotation before starting a chart
upgrade. Do not rotate the authority ConfigMap while Helm is writing chart
resources. Each Helm lifecycle command below initializes `HELM_APPLY_MODE` for
the current shell. Helm 4 selects client-side field ownership, while Helm 3.16
leaves the value empty. Continue to use reviewed current values and the
quiescence procedure below. Do not use `--force-conflicts` or `--take-ownership`
as a blanket takeover.

Before upgrading, verify that the retained templates ConfigMap contains a
nonempty `data["lifetime.json"]`, the policy authority and capacity ledgers are intact,
and the release still owns its retained executor ServiceAccount. That account
must have token automount disabled and no inherited pull Secrets. Stop if any of
these checks fails; do not synthesize missing history or reset authority.

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
     --wait --timeout=4m $HELM_APPLY_MODE
   ```

4. Verify readiness, exact environment/PVC UIDs, data, and network confinement
   before resuming traffic. Only execution-environment schema version 2 is
   supported; old objects are not automatically upgraded, reset, or deleted.
   Mixed-version provider operation is unsupported.

### Reuse a full template history

A template ID holds at most 32 revisions, and the entire release holds at most
64. To publish beyond either limit, remove a retired historical definition in
the same upgrade that adds the next revision. Keep every definition still used
by an environment. The chart requires explicit `maintenance.pruneRevisions`
consent for each removed ID/revision pair; ordinary upgrades reject any
removal.

1. While the provider is running, complete `RetireEnvironment` and
   `DeleteRetiredEnvironment` for **every** environment in the release. Wait
   for deletion to finish, including PVC deletion and capacity release. Stop
   client traffic and scale the provider to zero as in the upgrade procedure.
   Keep it stopped through the Helm upgrade; Helm lookup and apply do not form
   a transaction.
2. Verify that the existing Deployment has observed its zero-replica
   generation and that the namespace contains no Pods or PVCs. Confirm there
   are no `ExecutionEnvironment` CRs, including terminating CRs. Inspect the
   retained `mecatl-execution-profile-allocations` ConfigMap: its data must be
   empty or contain only `profile-<32 lowercase hex>.json` entries with the
   exact value `[]`. A reservation can exist before a CR is created. For
   example, inspect the live resources before continuing:

   ```sh
   kubectl --namespace <NAMESPACE> get deployment mecatl-execution \
     -o jsonpath='{.metadata.generation}{" "}{.status.observedGeneration}{" "}{.spec.replicas}{" "}{.status.replicas}{"\n"}'
   kubectl --namespace <NAMESPACE> get executionenvironments,pods,pvc
   kubectl --namespace <NAMESPACE> get configmap mecatl-execution-profile-allocations \
     -o jsonpath='{.data}{"\n"}'
   ```

   If any check fails, finish the supported lifecycle operations or recover
   the trusted ledger; do not reset or delete retained resources.
3. Back up the existing `data["lifetime.json"]` and reviewed Helm values in
   your operator configuration store. Remove only the selected retired
   revision from `templates.<ID>.revisions` in `execution-values.yaml`, add
   the next revision, and select an eligible default. Preserve the remaining
   recipes and network policy configuration. For the single upgrade that
   removes it, pass the exact removed revision as consent:

   ```sh
   helm upgrade mecatl-execution oci://ghcr.io/stacklok/mecatl/charts/mecatl-execution \
     --version <VERSION> --namespace <NAMESPACE> --values execution-values.yaml \
     --set-json 'maintenance.pruneRevisions.<ID>=["<RETIRED_TEMPLATE_REVISION>"]' \
     --wait --timeout=4m $HELM_APPLY_MODE
   ```

   Set `HELM_APPLY_MODE` as in the compatible-upgrade procedure. Omit the
   prune setting on subsequent upgrades; it is valid only when the named
   definition exists in the previous retained history and is absent from the
   candidate. Verify the new `lifetime.json` and provider readiness before
   resuming traffic. If the upgrade fails, keep writers stopped and restore
   the trusted values/history or resolve the reported state mismatch before
   retrying. Do not use a rollback that would reintroduce a pruned definition
   in place of a subsequently published revision.

### Uninstall and reinstall with retained state

Default uninstall removes the provider but keeps runtime CRs, PVCs, surviving
executors, workload default-deny and profile NetworkPolicies, both authority and
capacity ledgers, and the profile/security-manifest ConfigMaps. Uninstall is not
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
  --wait --timeout=4m $HELM_APPLY_MODE
```

Helm adopts retained resources only when their managed-by label and release-name
and release-namespace annotations match. The chart rejects missing or empty ledgers while allocations or capacity
reservations survive, and rejects unconsented changes to retained execution definitions.
Use the [full-history procedure](#reuse-a-full-template-history) for a
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

Before first publishing `administratorFor`, quiesce client traffic and upgrade
all provider replicas to a version that understands the field. Older strict
manifest decoders reject it; mixed-version rolling operation is unsupported.
Preserve the authority high-water ConfigMap throughout the upgrade. Publish the
reviewed manifest with a higher `generation`, verify provider readiness, then
resume client traffic.

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
