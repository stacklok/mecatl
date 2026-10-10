#!/bin/sh
set -eu

# Re-exec once with a deliberately small environment. In particular, no
# provider/API credential or ambient kube context can reach any subprocess.
if [ "${MECATL_EXECUTION_QUAL_CLEAN_ENV:-}" != 1 ]; then
  exec env -i \
    HOME="$HOME" USER="${USER:-user}" PATH="$PATH" XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}" \
    DBUS_SESSION_BUS_ADDRESS="${DBUS_SESSION_BUS_ADDRESS:-}" CONTAINER_HOST="${CONTAINER_HOST:-}" DOCKER_HOST="${DOCKER_HOST:-}" \
    CONTAINER_ENGINE="${CONTAINER_ENGINE:-}" MECATL_EXECUTION_QUAL_CI="${MECATL_EXECUTION_QUAL_CI:-}" MECATL_EXECUTION_QUAL_PROFILE="${MECATL_EXECUTION_QUAL_PROFILE:-}" \
    TOOLBOX_PATH="${TOOLBOX_PATH:-}" MECATL_EXECUTION_DEV_TOOLBOX="${MECATL_EXECUTION_DEV_TOOLBOX:-}" MECATL_EXECUTION_K8S_TOOLBOX="${MECATL_EXECUTION_K8S_TOOLBOX:-}" \
    MECATL_EXECUTION_QUAL_OUTPUT="${MECATL_EXECUTION_QUAL_OUTPUT:-}" MECATL_EXECUTION_QUAL_RESUME_STATE="${MECATL_EXECUTION_QUAL_RESUME_STATE:-}" \
    MECATL_EXECUTION_QUAL_CLEAN_ENV=1 "$0" "$@"
fi

[ "$#" -eq 0 ] || { echo "usage: $0" >&2; exit 2; }
case "${MECATL_EXECUTION_DEV_TOOLBOX:-}" in ''|dev) ;; *) echo "MECATL_EXECUTION_DEV_TOOLBOX must be empty or dev" >&2; exit 2 ;; esac
case "${MECATL_EXECUTION_K8S_TOOLBOX:-}" in ''|sre) ;; *) echo "MECATL_EXECUTION_K8S_TOOLBOX must be empty or sre" >&2; exit 2 ;; esac
runtime=${CONTAINER_ENGINE:-docker}
case "$runtime" in docker|podman) command -v "$runtime" >/dev/null 2>&1 ;; *) echo "CONTAINER_ENGINE must be docker or podman" >&2; exit 2 ;; esac
root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
cd "$root"
run_id=$(date -u +%Y%m%d%H%M%S)-$$
cluster="mecatl-execution-qual-$run_id"
context="kind-$cluster"
state="$root/.scratch/k8s-execution/$run_id"
resume=${MECATL_EXECUTION_QUAL_RESUME_STATE:-}
if [ -n "$resume" ]; then
  case "$resume" in "$root"/.scratch/k8s-execution/*) ;; *) echo "resume state outside owned fixture" >&2; exit 1 ;; esac
  [ -f "$resume/ownership" ] && [ ! -L "$resume/ownership" ] && [ -f "$resume/kubeconfig" ] || { echo "resume fixture ownership unavailable" >&2; exit 1; }
  state=$(CDPATH= cd -- "$resume" && pwd -P)
  case "$state" in "$root"/.scratch/k8s-execution/*) ;; *) echo "resolved resume state outside owned fixture" >&2; exit 1 ;; esac
  cluster=$(awk -F= '$1=="cluster" {print $2}' "$state/ownership")
  case "$cluster" in mecatl-execution-qual-*) ;; *) echo "resume fixture cluster mismatch" >&2; exit 1 ;; esac
  [ "$(awk -F= '$1=="runtime" {print $2}' "$state/ownership")" = "$runtime" ] &&
  [ "$(awk -F= '$1=="profile" {print $2}' "$state/ownership")" = "${MECATL_EXECUTION_QUAL_PROFILE:-development}" ] &&
  [ "$(awk -F= '$1=="kubeconfig" {print $2}' "$state/ownership")" = "$state/kubeconfig" ] &&
  [ "$(awk -F= '$1=="context" {print $2}' "$state/ownership")" = "kind-$cluster" ] || { echo "resume fixture record mismatch" >&2; exit 1; }
  [ "$(podman inspect "${cluster}-control-plane" --format '{{index .Config.Labels "io.x-k8s.kind.cluster"}}')" = "$cluster" ] || { echo "resume fixture container mismatch" >&2; exit 1; }
  context="kind-$cluster"
fi
kubeconfig="$state/kubeconfig"
if [ -z "$resume" ]; then
  mkdir -p "$root/.scratch/k8s-execution"
  umask 077
  mkdir "$state"
  mkdir "$state/pki" "$state/images"
  printf 'cluster=%s\ncontext=%s\nkubeconfig=%s\nnamespace=execution-qualification\nowner=%s\nruntime=%s\nprofile=%s\ncreated_at=%s\n' \
    "$cluster" "$context" "$kubeconfig" "${USER:-user}" "$runtime" "${MECATL_EXECUTION_QUAL_PROFILE:-development}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$state/ownership"
fi

# Refuse collisions; never delete or reuse any cluster.
if [ "$runtime" = podman ]; then
  export KIND_EXPERIMENTAL_PROVIDER=podman
else
  unset KIND_EXPERIMENTAL_PROVIDER
fi
clusters=$(kind get clusters) || { echo "qualification cluster discovery failed" >&2; exit 1; }
if [ -z "$resume" ] && printf '%s\n' "$clusters" | grep -Fx "$cluster" >/dev/null; then
  echo "qualification cluster already exists: $cluster" >&2
  exit 1
fi
# Publish the exact owned identity before creation can partially fail. Downstream
# CI steps consume these outputs, never the mutable local convenience pointer.
if [ -n "${MECATL_EXECUTION_QUAL_OUTPUT:-}" ]; then
  printf 'state=%s\ncluster=%s\n' "$state" "$cluster" >>"$MECATL_EXECUTION_QUAL_OUTPUT"
fi
printf '%s\n' "$state" >"$root/.scratch/k8s-execution/current"

test_build_pid=
cleanup_test_build() {
  if [ -n "$test_build_pid" ]; then
    kill "$test_build_pid" 2>/dev/null || :
    wait "$test_build_pid" || :
  fi
}
if [ "${MECATL_EXECUTION_QUAL_CI:-}" = 1 ]; then
  cleanup_cluster() {
    status=$?
    cleanup_test_build
    artifact="$state/production-failure-artifact.txt"
    if [ "$status" -ne 0 ]; then
      set -- sh "$root/deploy/mecatl-execution-kind/collect-failure.sh" "$kubeconfig" "$context" "$artifact"
      if [ -n "${MECATL_EXECUTION_K8S_TOOLBOX:-}" ]; then
        set -- toolbox run -c "$MECATL_EXECUTION_K8S_TOOLBOX" "$@"
      fi
      timeout --kill-after=5s 60s "$@" || printf 'qualification_diagnostics_incomplete\n' >&2
    fi
    kind delete cluster --name "$cluster"
    return "$status"
  }
  trap cleanup_cluster EXIT
else
  trap cleanup_test_build EXIT
fi
export KUBECONFIG="$kubeconfig"
printf '{}\n' >"$state/registry-auth.json"
chmod 600 "$state/registry-auth.json"
export REGISTRY_AUTH_FILE="$state/registry-auth.json"
phase_start=$(date +%s)
phase_done() {
  now=$(date +%s)
  printf 'qualification phase=%s elapsed=%ss\n' "$1" "$((now - phase_start))"
  phase_start=$now
}

kind_config=
if [ "${MECATL_EXECUTION_QUAL_PROFILE:-development}" = production ]; then
  kind_config="$state/kind.yaml"
  cat >"$kind_config" <<'EOF'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  disableDefaultCNI: true
  podSubnet: 192.168.0.0/16
  serviceSubnet: 10.96.0.0/12
nodes:
- role: control-plane
  kubeadmConfigPatches:
  - |
    apiVersion: kubelet.config.k8s.io/v1beta1
    kind: KubeletConfiguration
    syncFrequency: 5s
  - |
    # Kind's kubeadm patch matcher uses v1beta3 for the pinned node image.
    apiVersion: kubeadm.k8s.io/v1beta3
    kind: ClusterConfiguration
    controllerManager:
      extraArgs:
        resource-quota-sync-period: "10s"
EOF
fi
if [ -z "$resume" ]; then
  if [ -n "$kind_config" ]; then
    kind create cluster --name "$cluster" --image kindest/node:v1.35.8@sha256:07b2536e30b803ed61d1677a79df6115f798ce64c80f9e22f6ed45afd09323c0 --config "$kind_config" --kubeconfig "$kubeconfig"
  else
    kind create cluster --name "$cluster" --image kindest/node:v1.35.8@sha256:07b2536e30b803ed61d1677a79df6115f798ce64c80f9e22f6ed45afd09323c0 --kubeconfig "$kubeconfig"
  fi
fi
phase_done cluster

dev() {
  if [ -n "$MECATL_EXECUTION_DEV_TOOLBOX" ]; then
    (cd "$root" && toolbox run -c "$MECATL_EXECUTION_DEV_TOOLBOX" "$@")
  else
    (cd "$root" && "$@")
  fi
}
kube() {
  if [ -n "$MECATL_EXECUTION_K8S_TOOLBOX" ]; then
    toolbox run -c "$MECATL_EXECUTION_K8S_TOOLBOX" kubectl --kubeconfig "$kubeconfig" --context "$context" "$@"
  else
    kubectl --kubeconfig "$kubeconfig" --context "$context" "$@"
  fi
}
helm_kube() {
  if [ -n "$MECATL_EXECUTION_K8S_TOOLBOX" ]; then
    toolbox run -c "$MECATL_EXECUTION_K8S_TOOLBOX" helm --kubeconfig "$kubeconfig" --kube-context "$context" "$@"
  else
    helm --kubeconfig "$kubeconfig" --kube-context "$context" "$@"
  fi
}

if [ "${MECATL_EXECUTION_QUAL_PROFILE:-development}" = production ]; then
  cni_manifest="$state/calico-v3.32.2.yaml"
  curl --fail --location --proto '=https' --tlsv1.2 \
    https://raw.githubusercontent.com/projectcalico/calico/v3.32.2/manifests/calico.yaml \
    --output "$cni_manifest"
  printf '%s  %s\n' a8c828a06a87c629a282ebbc424895b77f3a030251993e41ea400a743675bb02 "$cni_manifest" | sha256sum -c -
  sed -i \
    -e 's|quay.io/calico/cni:v3.32.2|quay.io/calico/cni@sha256:0ef740bc587f25565905adf1d1f61a7faff0d571c449c6bdd789feed743d3ef7|g' \
    -e 's|quay.io/calico/kube-controllers:v3.32.2|quay.io/calico/kube-controllers@sha256:7870b67ebb13fabc3005252b44fe6e78b21635649bd3072b80afa1684b6565d0|g' \
    -e 's|quay.io/calico/node:v3.32.2|quay.io/calico/node@sha256:99b03fe91e8bfbcb153ae65ef4b701b24ce541ffdd74ff314eb041096008f7fd|g' \
    "$cni_manifest"
  kube apply -f "$cni_manifest"
  kube -n kube-system rollout status daemonset/calico-node --timeout=5m
  kube -n kube-system rollout status deployment/calico-kube-controllers --timeout=5m
  kube wait --for=condition=Ready node --all --timeout=3m
fi
phase_done network
image_step_start=$(date +%s)
image_step_done() {
  now=$(date +%s)
  printf 'qualification image_step=%s elapsed=%ss\n' "$1" "$((now - image_step_start))"
  image_step_start=$now
}

# Synthetic keys are generated by the fixture only and never printed. Resume
# keeps the original PKI so mounted trust and running identities cannot drift.
if [ ! -s "$state/pki/ca.crt" ]; then
  dev go run -tags kind_execution_e2e ./e2e/k8s_execution/fixture/pki "$state/pki"
fi
image_step_done pki

# Build the three static Go images in the development toolbox. ko emits a
# content tag; after loading, the fixture resolves the node's actual manifest
# digest and creates a matching digest alias for Kubernetes.
build_ko() {
  package=$1 repo=$2
  dev env KO_DOCKER_REPO="$repo" ko build --local --bare "$package" | tail -n 1
}
provider_tag=$(build_ko ./cmd/mecatl-execution-provider ko.local/mecatl-execution-provider)
image_step_done provider_build
agent_tag=$(build_ko ./cmd/mecak8s ko.local/mecak8s)
image_step_done agent_build
oidc_tag=$(dev env GOFLAGS=-tags=kind_execution_e2e KO_DOCKER_REPO=ko.local/mecatl-oidc-fixture ko build --local --bare ./e2e/k8s_execution/fixture/oidcissuer | tail -n 1)
image_step_done oidc_build
netprobe_tag=$(dev env GOFLAGS=-tags=kind_execution_e2e KO_DOCKER_REPO=ko.local/mecatl-netprobe-fixture ko build --local --bare ./e2e/k8s_execution/fixture/netprobe | tail -n 1)
image_step_done netprobe_build

# Resolve the public Go base from the local OCI store, then feed its observed
# digest to the production workload recipe without weakening its pin check.
if [ "$runtime" = podman ]; then
  podman image exists docker.io/library/golang:1.27.2 || podman pull docker.io/library/golang:1.27.2 >/dev/null
  go_digest=$(podman image inspect docker.io/library/golang:1.27.2 --format '{{.Digest}}')
else
  docker image inspect docker.io/library/golang:1.27.2 >/dev/null 2>&1 || docker pull docker.io/library/golang:1.27.2 >/dev/null
  go_ref=$(docker image inspect docker.io/library/golang:1.27.2 --format '{{index .RepoDigests 0}}')
  go_digest=${go_ref#*@}
fi
go_image="docker.io/library/golang@${go_digest}"
image_step_done base_image
workload_tag="localhost/mecatl-execution-workload:e2e"
"$runtime" build --build-arg GO_IMAGE="$go_image" -f "$root/build/execution-workload/Dockerfile" -t "$workload_tag" "$root" >/dev/null
image_step_done workload_build
# Give BuildKit a resolvable, run-unique local name; verify it still names the
# exact built image before using it as FROM (a raw config ID is not a FROM ref).
executor_base="localhost/mecatl-execution-base:qual-$run_id"
base_id=$("$runtime" image inspect "$workload_tag" --format '{{.Id}}')
printf '%s\n' "$base_id" | grep -Eq '^(sha256:)?[0-9a-f]{64}$' || { echo "executor base image ID unavailable" >&2; exit 1; }
"$runtime" tag "$workload_tag" "$executor_base"
[ "$("$runtime" image inspect "$executor_base" --format '{{.Id}}')" = "$base_id" ] || { echo "executor base image identity changed" >&2; exit 1; }
derivative_tag="localhost/mecatl-execution-operator-utility:e2e"
if [ "$runtime" = podman ]; then
  pull_policy=--pull=never
else
  pull_policy=--pull=false
fi
"$runtime" build "$pull_policy" --build-arg EXECUTOR_BASE="$executor_base" -f "$root/deploy/mecatl-execution-kind/fixture/derivative/Dockerfile" -t "$derivative_tag" "$root/deploy/mecatl-execution-kind/fixture/derivative" >/dev/null
image_step_done derivative_build
incompatible_tag="localhost/mecatl-execution-incompatible:e2e"
"$runtime" build "$pull_policy" --target incompatible --build-arg EXECUTOR_BASE="$executor_base" -f "$root/deploy/mecatl-execution-kind/fixture/derivative/Dockerfile" -t "$incompatible_tag" "$root/deploy/mecatl-execution-kind/fixture/derivative" >/dev/null
image_step_done incompatible_derivative_build

load_index=0
for item in "$provider_tag" "$agent_tag" "$oidc_tag" "$netprobe_tag" "$workload_tag" "$derivative_tag" "$incompatible_tag"; do
  archive="$state/images/$(printf '%s' "$item" | sha256sum | cut -c1-16).tar"
  "$runtime" save "$item" -o "$archive" >/dev/null
  kind load image-archive "$archive" --name "$cluster"
  load_index=$((load_index + 1))
  image_step_done "archive_load_$load_index"
done
. "$root/deploy/mecatl-execution-kind/images.sh"
provider_image=$(pin_loaded "$provider_tag")
agent_image=$(pin_loaded "$agent_tag")
oidc_image=$(pin_loaded "$oidc_tag")
netprobe_image=$(pin_loaded "$netprobe_tag")
workload_image=$(pin_loaded "$workload_tag")
derivative_image=$(pin_loaded "$derivative_tag")
incompatible_image=$(pin_loaded "$incompatible_tag")
printf 'provider=%s\nagent=%s\noidc=%s\nnetprobe=%s\nworkload=%s\nderivative=%s\nincompatible=%s\ngo_base=%s\n' "$provider_image" "$agent_image" "$oidc_image" "$netprobe_image" "$workload_image" "$derivative_image" "$incompatible_image" "$go_image" >"$state/images/proof"
image_step_done pin_loaded
phase_done images

# Compile the test binary while the already-built images are deployed. It is
# run only after the owned cluster is ready; no test starts in the background.
test_build_start=$(date +%s)
(
  compile_child=
  trap 'kill "$compile_child" 2>/dev/null || :; wait "$compile_child" 2>/dev/null || :; exit 143' TERM
  set -- env -i HOME="$HOME" PATH="$PATH" timeout --kill-after=5s 5m \
    go test -c -tags kind_execution_e2e -o "$state/qualification.test" ./e2e/k8s_execution
  if [ -n "$MECATL_EXECUTION_DEV_TOOLBOX" ]; then
    (cd "$root" && exec toolbox run -c "$MECATL_EXECUTION_DEV_TOOLBOX" "$@") &
  else
    (cd "$root" && exec "$@") &
  fi
  compile_child=$!
  wait "$compile_child"
  compile_child=
  trap - TERM
  printf 'qualification test_build=compiled elapsed=%ss\n' "$(( $(date +%s) - test_build_start))"
) &
test_build_pid=$!

kube create namespace execution-qualification --dry-run=client -o yaml | kube apply -f -
kube label namespace execution-qualification pod-security.kubernetes.io/enforce=restricted pod-security.kubernetes.io/audit=restricted pod-security.kubernetes.io/warn=restricted --overwrite

# Kind v0.33 installs its pinned local-path provisioner and default "standard"
# StorageClass as part of cluster creation. The fixture RuntimeClass names Kind's
# existing runc handler so provider startup can exercise the production preflight.
kube apply -f "$root/deploy/mecatl-execution-kind/runtimeclass.yaml"

if [ "${MECATL_EXECUTION_QUAL_PROFILE:-development}" = production ]; then
  cat >"$state/network-fixtures.yaml" <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: network-fixture
  namespace: execution-qualification
  labels: {app: network-fixture}
spec:
  automountServiceAccountToken: false
  restartPolicy: Always
  securityContext: {runAsNonRoot: true, runAsUser: 65532, runAsGroup: 65532, seccompProfile: {type: RuntimeDefault}}
  containers:
  - name: fixture
    image: $netprobe_image
    imagePullPolicy: IfNotPresent
    args: ["-listen=:8080"]
    securityContext: {allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: {drop: ["ALL"]}}
    resources: {requests: {cpu: 5m, memory: 8Mi}, limits: {cpu: 100m, memory: 32Mi}}
---
apiVersion: v1
kind: Pod
metadata:
  name: network-intruder
  namespace: execution-qualification
  labels: {app: network-intruder}
spec:
  automountServiceAccountToken: false
  restartPolicy: Always
  securityContext: {runAsNonRoot: true, runAsUser: 65532, runAsGroup: 65532, seccompProfile: {type: RuntimeDefault}}
  containers:
  - name: probe
    image: $netprobe_image
    imagePullPolicy: IfNotPresent
    command: ["/ko-app/netprobe"]
    args: ["-listen=:8080"]
    securityContext: {allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: {drop: ["ALL"]}}
    resources: {requests: {cpu: 5m, memory: 8Mi}, limits: {cpu: 100m, memory: 32Mi}}
EOF
  kube apply -f "$state/network-fixtures.yaml"
  kube -n execution-qualification wait --for=condition=Ready pod/network-fixture pod/network-intruder --timeout=2m
fi

# Compile against the current CRD only. Older stored objects must be refused
# without deleting any of their Pods or PVCs.

# The issuer is scoped to this qualification namespace; no cluster-wide
# certificate controller is part of the production execution chart.
helm_kube upgrade --install cert-manager oci://quay.io/jetstack/charts/cert-manager \
  --version=v1.17.2 --namespace=cert-manager --create-namespace \
  --set crds.enabled=true --wait --timeout=4m
kube -n execution-qualification create secret tls execution-fixture-ca \
  --cert="$state/pki/ca.crt" --key="$state/pki/ca.key" --dry-run=client -o yaml | kube apply -f -
kube apply -f "$root/deploy/mecatl-execution-kind/fixture-tls.yaml"
kube -n execution-qualification wait --for=condition=Ready \
  certificate/execution-provider-tls certificate/execution-client-tls \
  certificate/execution-intruder-tls certificate/execution-operations-tls \
  certificate/execution-wrong-scope-tls --timeout=3m
# Host-side test clients need the actual initially issued leaf identities; this
# snapshot is not used as a Kubernetes workload Secret or a renewal mechanism.
for name in provider:execution-security intruder:execution-intruder-tls mecak8s:execution-client-tls operations:execution-operations-tls wrong-scope:execution-wrong-scope-tls; do
  label=${name%%:*} secret=${name#*:}
  kube -n execution-qualification get secret "$secret" -o 'jsonpath={.data.tls\.crt}' | base64 -d >"$state/pki/$label.crt"
  kube -n execution-qualification get secret "$secret" -o 'jsonpath={.data.tls\.key}' | base64 -d >"$state/pki/$label.key"
done
kube -n execution-qualification create secret generic execution-trust \
  --from-file=clients.pem="$state/pki/ca.crt" --dry-run=client -o yaml | kube apply -f -
# cert-manager's client Secret includes ca.crt from this same issuer. The OIDC
# issuer is separately signed by the task-owned CA, so it remains trusted.

kube -n execution-qualification create secret generic oidc-fixture-identity \
  --from-file=tls.crt="$state/pki/oidc.crt" --from-file=tls.key="$state/pki/oidc.key" --from-file=jwt-key.pem="$state/pki/oidc-jwt-key.pem" --dry-run=client -o yaml | kube apply -f -

sed "s|OIDC_IMAGE|$oidc_image|g" "$root/deploy/mecatl-execution-kind/oidc.yaml" | kube apply -f -
kube -n execution-qualification rollout restart deployment/oidc-issuer
kube -n execution-qualification rollout status deployment/oidc-issuer --timeout=180s

runtime_values=
if [ "${MECATL_EXECUTION_QUAL_PROFILE:-development}" = production ]; then
  api_endpoint=$(kube -n default get endpoints kubernetes -o jsonpath='{.subsets[0].addresses[0].ip}')
  api_endpoint_port=$(kube -n default get endpoints kubernetes -o jsonpath='{.subsets[0].ports[0].port}')
  fixture_endpoint=$(kube -n execution-qualification get pod network-fixture -o jsonpath='{.status.podIP}')
  case "$api_endpoint:$fixture_endpoint:$api_endpoint_port" in
    *[!0-9.:]*) echo "invalid discovered network-policy endpoint" >&2; exit 1 ;;
    :*|*:) echo "missing discovered network-policy endpoint" >&2; exit 1 ;;
  esac
  runtime_values="$state/production-runtime-values.yaml"
  cat >"$runtime_values" <<EOF
provider:
  replicas: 2
  apiServerCIDRs:
    - 10.96.0.1/32
    - $api_endpoint/32
  dnsCIDRs:
    - 10.96.0.10/32
networkPolicy:
  apiServerPorts:
    - 443
    - $api_endpoint_port
  workloadProfiles:
    go:
      egress:
        - cidr: $fixture_endpoint/32
          ports:
            - protocol: TCP
              port: 8080
EOF
fi

# Render one operator-approved revision per fixture recipe using the provider's
# canonical encoding and validate the result with its real loader before install.
template_values="$state/templates-values-$run_id.yaml"
go_revision=$(dev go run -tags kind_execution_e2e ./deploy/mecatl-execution-kind/fixture/templates \
  "$root/deploy/mecatl-execution-kind/template-recipes.yaml" "$workload_image" "$derivative_image" "$incompatible_image" "$template_values")
case "$go_revision" in v1-*) ;; *) echo "missing generated go template revision" >&2; exit 1 ;; esac
printf '%s\n' "$go_revision" >"$state/go-template-revision"

# The chart requires a quiesced provider for every upgrade. On explicit
# task-owned resume, stop only this fixture's provider and retain Pods/PVCs
# belonging to existing execution environments.
if [ -n "$resume" ] && kube -n execution-qualification get deployment/mecatl-execution >/dev/null 2>&1; then
  kube -n execution-qualification scale deployment/mecatl-execution --replicas=0
  kube -n execution-qualification wait --for=delete pod -l app.kubernetes.io/name=mecatl-execution --timeout=4m
  # A previous interrupted qualification may have published a policy with
  # kubectl client-side apply. Return only this release-owned manifest's field
  # ownership to Helm before upgrading; preserve its data until Helm writes the
  # configured candidate and never touch the durable authority ConfigMap.
  kube -n execution-qualification get configmap/mecatl-execution-security-manifest -o yaml |
    kube -n execution-qualification apply --server-side --field-manager=helm --force-conflicts -f -
fi
set -- upgrade --install mecatl-execution "$root/deploy/helm/mecatl-execution" \
  --namespace execution-qualification \
  -f "$root/deploy/mecatl-execution-kind/execution-values.yaml" -f "$template_values" \
  -f "$root/deploy/mecatl-execution-kind/fixture-security-values.yaml"
if [ -n "$runtime_values" ]; then
  set -- "$@" -f "$runtime_values"
fi
helm_kube "$@" \
  --set fullnameOverride=mecatl-execution \
  --set-string provider.image="$provider_image" \
  --set provider.imagePullPolicy=IfNotPresent \
  --set-file provider.securityManifest="$state/pki/manifest.json" \
  --wait --timeout=4m
if [ -n "$resume" ]; then
  kube -n execution-qualification scale deployment/mecatl-execution --replicas=2
  kube -n execution-qualification rollout status deployment/mecatl-execution --timeout=4m
fi

kube -n execution-qualification create configmap execution-mock --from-file=mock-script.json="$root/deploy/mecatl-execution-kind/mock-script.json" --dry-run=client -o yaml | kube apply -f -
helm_kube upgrade --install mecak8s "$root/deploy/helm/mecak8s" \
  --namespace execution-qualification -f "$root/deploy/mecatl-execution-kind/mecak8s-values.yaml" \
  --set-string image.repository="${agent_image%@*}" --set-string image.digest="${agent_image#*@}" --set-string image.tag= \
  --set-string execution.templateRevision="$go_revision" \
  --wait --timeout=5m
kube -n execution-qualification rollout restart deployment/mecak8s
kube -n execution-qualification rollout status deployment/mecak8s --timeout=240s
phase_done deployment
printf 'qualification phase=tests starting\n'
test_build_wait_start=$(date +%s)
wait "$test_build_pid"
test_build_pid=
printf 'qualification test_build=wait elapsed=%ss\n' "$(( $(date +%s) - test_build_wait_start))"

dev env -i HOME="$HOME" PATH="$PATH" TOOLBOX_PATH="${TOOLBOX_PATH:-}" XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-}" DBUS_SESSION_BUS_ADDRESS="${DBUS_SESSION_BUS_ADDRESS:-}" \
  KUBECONFIG="$kubeconfig" MECATL_KUBE_CONTEXT="$context" MECATL_EXECUTION_K8S_TOOLBOX="${MECATL_EXECUTION_K8S_TOOLBOX:-}" MECATL_EXECUTION_TEMPLATE_REVISION="$go_revision" MECATL_EXECUTION_QUAL_STATE="$state" MECATL_EXECUTION_QUAL_PROFILE="${MECATL_EXECUTION_QUAL_PROFILE:-development}" \
  sh -c 'cd e2e/k8s_execution && exec "$@"' sh "$state/qualification.test" -test.v -test.count=1 -test.timeout=45m
phase_done tests

if [ "${MECATL_EXECUTION_QUAL_PROFILE:-development}" = production ]; then
  printf 'qualification phase=automatic_certificate_renewal starting (initial expiry plus two issuer renewals and in-flight transport reload)\n'
  dev env -i HOME="$HOME" PATH="$PATH" TOOLBOX_PATH="${TOOLBOX_PATH:-}" XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-}" DBUS_SESSION_BUS_ADDRESS="${DBUS_SESSION_BUS_ADDRESS:-}" \
    KUBECONFIG="$kubeconfig" MECATL_KUBE_CONTEXT="$context" MECATL_EXECUTION_K8S_TOOLBOX="${MECATL_EXECUTION_K8S_TOOLBOX:-}" MECATL_EXECUTION_TEMPLATE_REVISION="$go_revision" MECATL_EXECUTION_QUAL_STATE="$state" MECATL_EXECUTION_QUAL_PROFILE=production MECATL_EXECUTION_QUAL_RENEWAL=1 \
    sh -c 'cd e2e/k8s_execution && exec "$@"' sh "$state/qualification.test" -test.v -test.count=1 -test.run '^TestKindExecution(AutomaticCertificateRenewal|InFlightAutomaticRenewal)$' -test.timeout=105m
  phase_done automatic_certificate_renewal
fi

if [ "${MECATL_EXECUTION_QUAL_CI:-}" = 1 ]; then
  echo "qualification cluster is CI-owned and will be removed: $cluster"
else
  echo "qualification cluster retained: $cluster"
  echo "kubeconfig: $kubeconfig"
  echo "cleanup (requires human confirmation): CONTAINER_ENGINE=$runtime kind delete cluster --name $cluster"
fi
