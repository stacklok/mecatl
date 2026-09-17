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
kubeconfig="$state/kubeconfig"
mkdir -p "$root/.scratch/k8s-execution"
umask 077
mkdir "$state"
mkdir "$state/pki" "$state/images"
printf 'cluster=%s\ncontext=%s\nkubeconfig=%s\nnamespace=execution-qualification\nowner=%s\nruntime=%s\nprofile=%s\ncreated_at=%s\n' \
  "$cluster" "$context" "$kubeconfig" "${USER:-user}" "$runtime" "${MECATL_EXECUTION_QUAL_PROFILE:-development}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$state/ownership"

# Refuse collisions; never delete or reuse any cluster.
if kind get clusters 2>/dev/null | grep -Fx "$cluster" >/dev/null; then
  echo "qualification cluster already exists: $cluster" >&2
  exit 1
fi
printf '%s\n' "$state" >"$root/.scratch/k8s-execution/current"

if [ "$runtime" = podman ]; then
  export KIND_EXPERIMENTAL_PROVIDER=podman
else
  unset KIND_EXPERIMENTAL_PROVIDER
fi
if [ "${MECATL_EXECUTION_QUAL_CI:-}" = 1 ]; then
  cleanup_cluster() {
    status=$?
    artifact="$state/production-failure-artifact.txt"
    if [ "$status" -ne 0 ]; then
      kubectl --kubeconfig "$kubeconfig" --context "$context" get events -n execution-qualification \
        -o 'custom-columns=OBJECT:.involvedObject.name,REASON:.reason' --no-headers >"$artifact" 2>/dev/null || printf 'qualification_setup_failed\n' >"$artifact"
      head -c 1048576 "$artifact" >"$artifact.bounded"
      mv "$artifact.bounded" "$artifact"
    fi
    kind delete cluster --name "$cluster"
    return "$status"
  }
  trap cleanup_cluster EXIT
fi
export KUBECONFIG="$kubeconfig"
printf '{}\n' >"$state/registry-auth.json"
chmod 600 "$state/registry-auth.json"
export REGISTRY_AUTH_FILE="$state/registry-auth.json"

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
EOF
fi
if [ -n "$kind_config" ]; then
  kind create cluster --name "$cluster" --image kindest/node:v1.35.8@sha256:07b2536e30b803ed61d1677a79df6115f798ce64c80f9e22f6ed45afd09323c0 --config "$kind_config" --kubeconfig "$kubeconfig"
else
  kind create cluster --name "$cluster" --image kindest/node:v1.35.8@sha256:07b2536e30b803ed61d1677a79df6115f798ce64c80f9e22f6ed45afd09323c0 --kubeconfig "$kubeconfig"
fi

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

# Synthetic keys are generated by the fixture only and never printed. Resume
# keeps the original PKI so mounted trust and running identities cannot drift.
if [ ! -s "$state/pki/ca.crt" ]; then
  dev go run -tags kind_execution_e2e ./e2e/k8s_execution/fixture/pki "$state/pki"
fi

# Build the three static Go images in the development toolbox. ko emits a
# content tag; after loading, the fixture resolves the node's actual manifest
# digest and creates a matching digest alias for Kubernetes.
build_ko() {
  package=$1 repo=$2
  dev env KO_DOCKER_REPO="$repo" ko build --local --bare "$package" | tail -n 1
}
provider_tag=$(build_ko ./cmd/mecatl-execution-provider ko.local/mecatl-execution-provider)
agent_tag=$(build_ko ./cmd/mecak8s ko.local/mecak8s)
oidc_tag=$(dev env GOFLAGS=-tags=kind_execution_e2e KO_DOCKER_REPO=ko.local/mecatl-oidc-fixture ko build --local --bare ./e2e/k8s_execution/fixture/oidcissuer | tail -n 1)
netprobe_tag=$(dev env GOFLAGS=-tags=kind_execution_e2e KO_DOCKER_REPO=ko.local/mecatl-netprobe-fixture ko build --local --bare ./e2e/k8s_execution/fixture/netprobe | tail -n 1)

# Resolve the public Go base from the local OCI store, then feed its observed
# digest to the production workload recipe without weakening its pin check.
if [ "$runtime" = podman ]; then
  podman image exists docker.io/library/golang:1.27 || podman pull docker.io/library/golang:1.27 >/dev/null
  go_digest=$(podman image inspect docker.io/library/golang:1.27 --format '{{.Digest}}')
else
  docker image inspect docker.io/library/golang:1.27 >/dev/null 2>&1 || docker pull docker.io/library/golang:1.27 >/dev/null
  go_ref=$(docker image inspect docker.io/library/golang:1.27 --format '{{index .RepoDigests 0}}')
  go_digest=${go_ref#*@}
fi
go_image="docker.io/library/golang@${go_digest}"
workload_tag="localhost/mecatl-execution-workload:e2e"
"$runtime" build --build-arg GO_IMAGE="$go_image" -f "$root/build/execution-workload/Dockerfile" -t "$workload_tag" "$root" >/dev/null

for item in "$provider_tag" "$agent_tag" "$oidc_tag" "$netprobe_tag" "$workload_tag"; do
  archive="$state/images/$(printf '%s' "$item" | sha256sum | cut -c1-16).tar"
  "$runtime" save "$item" -o "$archive" >/dev/null
  kind load image-archive "$archive" --name "$cluster"
done
node="${cluster}-control-plane"
pin_loaded() {
  tagged=$1
  digest=$("$runtime" exec "$node" ctr -n k8s.io images ls | awk -v ref="$tagged" '$1 == ref {print $3; exit}')
  [ -n "$digest" ] || { echo "loaded image digest unavailable for $tagged" >&2; exit 1; }
  pinned="${tagged%:*}@${digest}"
  "$runtime" exec "$node" ctr -n k8s.io images tag "$tagged" "$pinned" >/dev/null
  printf '%s\n' "$pinned"
}
provider_image=$(pin_loaded "$provider_tag")
agent_image=$(pin_loaded "$agent_tag")
oidc_image=$(pin_loaded "$oidc_tag")
netprobe_image=$(pin_loaded "$netprobe_tag")
workload_image=$(pin_loaded "$workload_tag")
printf 'provider=%s\nagent=%s\noidc=%s\nnetprobe=%s\nworkload=%s\ngo_base=%s\n' "$provider_image" "$agent_image" "$oidc_image" "$netprobe_image" "$workload_image" "$go_image" >"$state/images/proof"

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

kube -n execution-qualification create secret generic execution-security \
  --from-file=grant-k1.pem="$state/pki/grant-key.pem" --from-file=tls.crt="$state/pki/provider.crt" \
  --from-file=tls.key="$state/pki/provider.key" --from-file=clients.pem="$state/pki/ca.crt" --dry-run=client -o yaml | kube apply -f -
kube -n execution-qualification create secret generic execution-client-tls \
  --from-file=ca.crt="$state/pki/ca.crt" --from-file=tls.crt="$state/pki/mecak8s.crt" --from-file=tls.key="$state/pki/mecak8s.key" --dry-run=client -o yaml | kube apply -f -
kube -n execution-qualification create secret generic execution-intruder-tls \
  --from-file=ca.crt="$state/pki/ca.crt" --from-file=tls.crt="$state/pki/intruder.crt" --from-file=tls.key="$state/pki/intruder.key" --dry-run=client -o yaml | kube apply -f -
kube -n execution-qualification create secret generic oidc-fixture-identity \
  --from-file=tls.crt="$state/pki/oidc.crt" --from-file=tls.key="$state/pki/oidc.key" --from-file=jwt-key.pem="$state/pki/oidc-jwt-key.pem" --dry-run=client -o yaml | kube apply -f -

sed "s|OIDC_IMAGE|$oidc_image|g" "$root/deploy/mecatl-execution-kind/oidc.yaml" | kube apply -f -
kube -n execution-qualification rollout restart deployment/oidc-issuer
kube -n execution-qualification rollout status deployment/oidc-issuer --timeout=180s

runtime_values=
if [ "${MECATL_EXECUTION_QUAL_PROFILE:-development}" = production ]; then
  api_endpoint=$(kube -n default get endpoints kubernetes -o jsonpath='{.subsets[0].addresses[0].ip}')
  fixture_endpoint=$(kube -n execution-qualification get pod network-fixture -o jsonpath='{.status.podIP}')
  case "$api_endpoint:$fixture_endpoint" in
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
  workloadProfiles:
    go:
      egress:
        - cidr: $fixture_endpoint/32
          ports:
            - protocol: TCP
              port: 8080
EOF
fi

set -- upgrade --install mecatl-execution "$root/deploy/helm/mecatl-execution" \
  --namespace execution-qualification \
  -f "$root/deploy/mecatl-execution-kind/execution-values.yaml"
if [ -n "$runtime_values" ]; then
  set -- "$@" -f "$runtime_values"
fi
helm_kube "$@" \
  --set fullnameOverride=mecatl-execution \
  --set-string provider.image="$provider_image" \
  --set provider.imagePullPolicy=IfNotPresent \
  --set provider.securitySecretName=execution-security \
  --set-file provider.securityManifest="$state/pki/manifest.json" \
  --set-string profiles.go.image="$workload_image" \
  --set-string profiles.quota-cas.image="$workload_image" \
  --set-string profiles.quota-kube.image="$workload_image" \
  --wait --timeout=4m

kube -n execution-qualification create configmap execution-mock --from-file=mock-script.json="$root/deploy/mecatl-execution-kind/mock-script.json" --dry-run=client -o yaml | kube apply -f -
helm_kube upgrade --install mecak8s "$root/deploy/helm/mecak8s" \
  --namespace execution-qualification -f "$root/deploy/mecatl-execution-kind/mecak8s-values.yaml" \
  --set-string image.repository="${agent_image%@*}" --set-string image.digest="${agent_image#*@}" \
  --wait --timeout=5m
kube -n execution-qualification rollout restart deployment/mecak8s
kube -n execution-qualification rollout status deployment/mecak8s --timeout=240s

dev env -i HOME="$HOME" PATH="$PATH" KUBECONFIG="$kubeconfig" MECATL_KUBE_CONTEXT="$context" MECATL_EXECUTION_QUAL_STATE="$state" MECATL_EXECUTION_QUAL_PROFILE="${MECATL_EXECUTION_QUAL_PROFILE:-development}" \
  go test -tags kind_execution_e2e -count=1 -timeout=45m ./e2e/k8s_execution

if [ "${MECATL_EXECUTION_QUAL_CI:-}" = 1 ]; then
  echo "qualification cluster is CI-owned and will be removed: $cluster"
else
  echo "qualification cluster retained: $cluster"
  echo "kubeconfig: $kubeconfig"
  echo "cleanup (requires human confirmation): CONTAINER_ENGINE=$runtime kind delete cluster --name $cluster"
fi
