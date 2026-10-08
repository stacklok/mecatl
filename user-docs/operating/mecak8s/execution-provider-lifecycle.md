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

Give every changed material file a new, generation-specific basename. This
applies to signing keys, server certificates, server private keys, and client-CA
bundles. For example, a second bundle can use `grant-k2.pem`, `server-g2.crt`,
`server-g2.key`, and `clients-g2.pem`. Keep each name's bytes immutable.

1. Stage the new files before changing `provider.securityManifest`, retaining
   files referenced by the current manifest. With `securitySecretName`, add them
   to the single operator-managed Secret. With `securitySources`, first populate
   the new Secret keys, then add their destination mappings in a quiesced chart
   upgrade while the old manifest remains in place. Kubernetes requires every
   projected key to exist, even when the current manifest does not reference it.
2. Publish a higher-generation manifest whose existing `file`,
   `certificateFile`, `privateKeyFile`, and `clientCAFile` fields reference
   those names. For CA rotation, first publish a separately named overlap
   bundle, move clients and server trust, then publish another higher generation
   that removes old trust.
3. Verify that the authority ConfigMap has reached the intended generation and
   use a current claim to read known workspace content. Pod readiness alone can
   still reflect the previous generation. A replica whose snapshot lags the
   ledger rejects requests before dispatch with structured `not_ready` and
   `retryable=true`. Bound any read-only verification poll and stop on wrong
   content, a nonretryable error, or another error code.
4. Retain overlap files until no live or in-flight manifest references them.
   Retain verification keys through their grant windows before revoking them.
   Never reuse a retired name for different bytes.

Kubernetes projects Secret and ConfigMap updates independently. A manifest that
arrives before its new files fails closed until they arrive; staged files leave
an older manifest unchanged. Overwriting referenced TLS or CA files can instead
publish a mixed bundle's digest permanently at the new generation. If that has
happened, publish a complete, immutable bundle at a **higher** generation.
Repeated requests or a restart cannot repair same-generation digest drift;
preserve the authority ledger rather than resetting it. The provider confines
file access to the mounted security directory, but immutable publication remains
your secret-management procedure's responsibility.

## Upgrade, uninstall, and reinstall the execution provider

The supported lifecycle keeps the **same Helm release name, namespace, resource
names, profiles, network policy configuration, and security Secret name or
source mode**. Keep `execution-values.yaml` and the current nonsecret authority
manifest in your operator configuration store. Retain operator-owned Secrets and
their key history independently; the chart neither owns nor reads Secret
contents.

Finish and verify any external authority rotation before starting a chart
upgrade. Do not rotate the authority ConfigMap while Helm is writing chart
resources. Each Helm lifecycle command below initializes `HELM_APPLY_MODE` for
the current shell. Helm 4 selects client-side field ownership, while Helm 3.16
leaves the value empty. Continue to use reviewed current values and the
quiescence procedure below. Do not use `--force-conflicts` or `--take-ownership`
as a blanket takeover.

Before upgrading, verify that the retained profiles ConfigMap contains a
nonempty `data["lifetime.json"]`, the authority and capacity ledgers are intact,
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
and release-namespace annotations match. The chart rejects missing or empty
ledgers while allocations or capacity reservations survive, and rejects changes
to its retained lifetime configuration. Profile removal or egress edits are
deliberately outside this upgrade path: retained allow policies are additive, so
leaving an obsolete policy could widen access. The provider rejects an older
authority manifest against the retained high-water generation and key history;
never reset that ledger to make readiness pass.

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

### Revoke grants

Revoke grants with the current generation and a stable operation ID. The
response returns the new generation; retrying the identical request returns the
same receipt. An old grant is denied after this CAS succeeds.

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
