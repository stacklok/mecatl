#!/bin/sh
set -eu

# Explicit opt-in live qualification against one already-owned Kind cluster.
# Re-exec with only non-secret runtime selectors plus the credential FILE path.
if [ "${MECATL_EXECUTION_LIVE_CLEAN_ENV:-}" != 1 ]; then
  exec env -i HOME="$HOME" USER="${USER:-user}" PATH="$PATH" XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}" \
    DBUS_SESSION_BUS_ADDRESS="${DBUS_SESSION_BUS_ADDRESS:-}" CONTAINER_HOST="${CONTAINER_HOST:-}" DOCKER_HOST="${DOCKER_HOST:-}" \
    TOOLBOX_PATH="${TOOLBOX_PATH:-}" MECATL_EXECUTION_DEV_TOOLBOX="${MECATL_EXECUTION_DEV_TOOLBOX:-}" MECATL_EXECUTION_K8S_TOOLBOX="${MECATL_EXECUTION_K8S_TOOLBOX:-}" \
    MECATL_EXECUTION_CREDENTIAL_FILE="${MECATL_EXECUTION_CREDENTIAL_FILE:-}" MECATL_EXECUTION_QUAL_STATE="${MECATL_EXECUTION_QUAL_STATE:-}" \
    MECATL_EXECUTION_LIVE_CLEAN_ENV=1 "$0" "$@"
fi
[ "$#" -eq 0 ] || { echo "usage: MECATL_EXECUTION_CREDENTIAL_FILE=/absolute/file MECATL_EXECUTION_QUAL_STATE=/owned/state $0" >&2; exit 2; }
[ -n "$MECATL_EXECUTION_CREDENTIAL_FILE" ] || { echo "MECATL_EXECUTION_CREDENTIAL_FILE is required" >&2; exit 2; }
[ -n "$MECATL_EXECUTION_QUAL_STATE" ] || { echo "MECATL_EXECUTION_QUAL_STATE is required" >&2; exit 2; }

root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
cd "$root"
owned_root="$root/.scratch/k8s-execution"
case "${MECATL_EXECUTION_DEV_TOOLBOX:-}" in ''|dev) ;; *) echo "MECATL_EXECUTION_DEV_TOOLBOX must be empty or dev" >&2; exit 2 ;; esac
case "${MECATL_EXECUTION_K8S_TOOLBOX:-}" in ''|sre) ;; *) echo "MECATL_EXECUTION_K8S_TOOLBOX must be empty or sre" >&2; exit 2 ;; esac
state=$(CDPATH= cd -- "$MECATL_EXECUTION_QUAL_STATE" && pwd -P) || { echo "qualification state unavailable" >&2; exit 1; }
case "$state" in
  "$owned_root"/*) ;;
  *) echo "refusing qualification state outside owned root" >&2; exit 1 ;;
esac
ownership="$state/ownership"
[ -f "$ownership" ] && [ ! -L "$ownership" ] || { echo "owned qualification record unavailable" >&2; exit 1; }
owned_value() {
  awk -F= -v key="$1" '
    $1 == key { value=substr($0, length(key)+2); count++ }
    END { if (count != 1 || value == "") exit 1; print value }
  ' "$ownership"
}
optional_owned_value() {
  awk -F= -v key="$1" '
    $1 == key { value=substr($0, length(key)+2); count++ }
    END { if (count > 1) exit 1; if (count == 1) print value }
  ' "$ownership"
}
cluster=$(owned_value cluster) || { echo "owned cluster identity unavailable" >&2; exit 1; }
case "$cluster" in
  [a-z0-9]|[a-z0-9]*[a-z0-9]) ;;
  *) echo "owned cluster identity invalid" >&2; exit 1 ;;
esac
case "$cluster" in *[!a-z0-9-]*) echo "owned cluster identity invalid" >&2; exit 1 ;; esac
[ "${#cluster}" -le 63 ] || { echo "owned cluster identity invalid" >&2; exit 1; }
owned_namespace=$(owned_value namespace) || { echo "owned namespace unavailable" >&2; exit 1; }
[ "$owned_namespace" = execution-qualification ] || { echo "owned namespace mismatch" >&2; exit 1; }
recorded_owner=$(owned_value owner) || { echo "owned user identity unavailable" >&2; exit 1; }
[ "$recorded_owner" = "${USER:-user}" ] || { echo "owned user identity mismatch" >&2; exit 1; }
kubeconfig="$state/kubeconfig"
recorded_kubeconfig=$(owned_value kubeconfig) || { echo "owned kubeconfig identity unavailable" >&2; exit 1; }
[ "$recorded_kubeconfig" = "$kubeconfig" ] || { echo "owned kubeconfig mismatch" >&2; exit 1; }
[ -f "$kubeconfig" ] && [ ! -L "$kubeconfig" ] && [ "$(stat -c '%a' "$kubeconfig")" = 600 ] || { echo "owned kubeconfig must be a private regular file" >&2; exit 1; }
context=$(optional_owned_value context) || { echo "owned context identity invalid" >&2; exit 1; }
[ -n "$context" ] || context="kind-$cluster"
[ "$context" = "kind-$cluster" ] || { echo "owned context mismatch" >&2; exit 1; }
runtime=$(owned_value runtime) || { echo "owned container runtime unavailable" >&2; exit 1; }
[ "$runtime" = docker ] || [ "$runtime" = podman ] || { echo "owned container runtime invalid" >&2; exit 1; }
profile=$(owned_value profile) || { echo "owned qualification profile unavailable" >&2; exit 1; }
[ "$profile" = production ] || { echo "live qualification requires a completed production-state fixture" >&2; exit 1; }
label=$($runtime inspect "${cluster}-control-plane" --format '{{index .Config.Labels "io.x-k8s.kind.cluster"}}')
[ "$label" = "$cluster" ] || { echo "owned Kind container label mismatch" >&2; exit 1; }

if [ "$runtime" = podman ]; then export KIND_EXPERIMENTAL_PROVIDER=podman; else unset KIND_EXPERIMENTAL_PROVIDER; fi
export KUBECONFIG=$kubeconfig
export REGISTRY_AUTH_FILE="$state/registry-auth.json"
dev() {
  if [ -n "$MECATL_EXECUTION_DEV_TOOLBOX" ]; then toolbox run -c "$MECATL_EXECUTION_DEV_TOOLBOX" "$@"; else "$@"; fi
}
kube() {
  if [ -n "$MECATL_EXECUTION_K8S_TOOLBOX" ]; then toolbox run -c "$MECATL_EXECUTION_K8S_TOOLBOX" kubectl --kubeconfig "$kubeconfig" --context "$context" "$@"; else kubectl --kubeconfig "$kubeconfig" --context "$context" "$@"; fi
}
helm_kube() {
  if [ -n "$MECATL_EXECUTION_K8S_TOOLBOX" ]; then toolbox run -c "$MECATL_EXECUTION_K8S_TOOLBOX" helm --kubeconfig "$kubeconfig" --kube-context "$context" "$@"; else helm --kubeconfig "$kubeconfig" --kube-context "$context" "$@"; fi
}
[ "$(kube config current-context)" = "$context" ] || { echo "owned kube context mismatch" >&2; exit 1; }
build_ko() { package=$1 repo=$2; dev env KIND_EXPERIMENTAL_PROVIDER="${KIND_EXPERIMENTAL_PROVIDER:-}" KO_DOCKER_REPO="$repo" ko build --local --bare "$package" | tail -n 1; }
. "$root/deploy/mecatl-execution-kind/images.sh"
load_image() {
  tagged=$1
  archive="$state/images/live-$(printf '%s' "$tagged" | sha256sum | cut -c1-16).tar"
  $runtime save "$tagged" -o "$archive" >/dev/null
  kind load image-archive "$archive" --name "$cluster"
  pin_loaded "$tagged"
}

provider_tag=$(build_ko ./cmd/mecatl-execution-provider ko.local/mecatl-execution-provider)
agent_tag=$(build_ko ./cmd/mecak8s ko.local/mecak8s)
if [ "$runtime" = podman ]; then
  podman image exists docker.io/library/golang:1.27 || podman pull docker.io/library/golang:1.27 >/dev/null
  go_digest=$(podman image inspect docker.io/library/golang:1.27 --format '{{.Digest}}')
else
  docker image inspect docker.io/library/golang:1.27 >/dev/null 2>&1 || docker pull docker.io/library/golang:1.27 >/dev/null
  go_ref=$(docker image inspect docker.io/library/golang:1.27 --format '{{index .RepoDigests 0}}')
  go_digest=${go_ref#*@}
fi
go_image="docker.io/library/golang@${go_digest}"
workload_tag=localhost/mecatl-execution-workload:e2e
$runtime build --build-arg GO_IMAGE="$go_image" -f "$root/build/execution-workload/Dockerfile" -t "$workload_tag" "$root" >/dev/null
provider_image=$(load_image "$provider_tag")
agent_image=$(load_image "$agent_tag")
workload_image=$(load_image "$workload_tag")
printf 'provider=%s\nagent=%s\nworkload=%s\ngo_base=%s\n' "$provider_image" "$agent_image" "$workload_image" "$go_image" >"$state/images/live-proof"

# Consume the already-qualified storage and synthetic security state. Preserve
# Kind's local-path helper configuration; the non-root workload is not its helper.
# Live mode never regenerates or replaces the execution Secret/keyring, or reads it back.
# A real provider credential is staged only after this deterministic rerun passes.
kube -n execution-qualification create configmap execution-mock --from-file=mock-script.json="$root/deploy/mecatl-execution-kind/mock-script.json" --dry-run=client -o yaml | kube apply -f -
kube -n execution-qualification rollout status deployment/mecatl-execution --timeout=240s
helm_kube upgrade --install mecak8s "$root/deploy/helm/mecak8s" --namespace execution-qualification -f "$root/deploy/mecatl-execution-kind/mecak8s-values.yaml" \
  --set-string image.repository="${agent_image%@*}" --set-string image.tag= --set-string image.digest="${agent_image#*@}" --wait --timeout=5m
kube -n execution-qualification rollout restart deployment/mecak8s
kube -n execution-qualification rollout status deployment/mecak8s --timeout=240s
dev env -i HOME="$HOME" PATH="$PATH" KUBECONFIG="$kubeconfig" MECATL_KUBE_CONTEXT="$context" MECATL_EXECUTION_QUAL_STATE="$state" \
  go test -tags kind_execution_e2e -run '^TestKindExecutionQualification$' -count=1 -timeout=15m ./e2e/k8s_execution

secret="mecak8s-live-$(date -u +%Y%m%d%H%M%S)-$$"
receipt="$state/live-secret-$secret.receipt.json"
restore_needed=0
restore() {
  status=$?
  trap - EXIT INT TERM
  cleanup_failed=0
  mock_restored=0
  secret_cleaned=0
  if [ "$status" -ne 0 ] && [ "$restore_needed" -eq 1 ]; then
    # Ownership was verified before staging. Capture the real process before
    # Helm replaces it, independently of Secret cleanup and mock restoration.
    set -- sh "$root/deploy/mecatl-execution-kind/collect-failure.sh" "$kubeconfig" "$context" "$state/live-diagnostics.jsonl"
    if [ -n "${MECATL_EXECUTION_K8S_TOOLBOX:-}" ]; then
      set -- toolbox run -c "$MECATL_EXECUTION_K8S_TOOLBOX" "$@"
    fi
    if timeout --kill-after=5s 45s "$@"; then
      printf 'complete\n' > "$state/live-diagnostics.status" || echo 'warning: diagnostic status unavailable' >&2
    else
      printf 'incomplete\n' > "$state/live-diagnostics.status" || echo 'warning: diagnostic status unavailable' >&2
      echo 'warning: bounded pre-restoration diagnostics incomplete' >&2
    fi
  fi
  if [ "$restore_needed" -eq 1 ]; then
    if helm_kube upgrade --install mecak8s "$root/deploy/helm/mecak8s" --namespace execution-qualification -f "$root/deploy/mecatl-execution-kind/mecak8s-values.yaml" \
      --set-string image.repository="${agent_image%@*}" --set-string image.tag= --set-string image.digest="${agent_image#*@}" --wait --timeout=5m >/dev/null 2>&1; then
      mock_restored=1
    else
      echo "mock profile restoration failed; cluster retained for inspection" >&2
      cleanup_failed=1
    fi
  fi
  if [ -f "$receipt" ]; then
    if dev env -i HOME="$HOME" PATH="$PATH" KUBECONFIG="$kubeconfig" MECATL_KUBE_CONTEXT="$context" \
      go run -tags kind_execution_e2e ./e2e/k8s_execution/fixture/credentialloader delete "$kubeconfig" "$context" "$secret" "$receipt"; then
      secret_cleaned=1
    else
      echo "UID-pinned run-scoped provider Secret cleanup pending; cluster retained for inspection" >&2
      cleanup_failed=1
    fi
  else
    secret_cleaned=1
  fi
  if [ "$mock_restored" -eq 1 ] && [ "$secret_cleaned" -eq 1 ]; then
    echo "cleanup verification passed: mock harness restored; run-scoped Secret receipt cleared"
  fi
  if [ "$status" -eq 0 ] && [ "$cleanup_failed" -ne 0 ]; then
    status=1
  fi
  exit "$status"
}
trap restore EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

dev env -i HOME="$HOME" PATH="$PATH" KUBECONFIG="$kubeconfig" MECATL_KUBE_CONTEXT="$context" MECATL_EXECUTION_CREDENTIAL_FILE="$MECATL_EXECUTION_CREDENTIAL_FILE" \
  go run -tags kind_execution_e2e ./e2e/k8s_execution/fixture/credentialloader stage "$kubeconfig" "$context" "$secret" "$receipt"
restore_needed=1
helm_kube upgrade --install mecak8s "$root/deploy/helm/mecak8s" --namespace execution-qualification -f "$root/deploy/mecatl-execution-kind/mecak8s-values.yaml" \
  --set-string image.repository="${agent_image%@*}" --set-string image.tag= --set-string image.digest="${agent_image#*@}" \
  --set mockProvider=false --set security.allowUnsafeRealProvider=true --set defaultProvider=openrouter --set-string model=anthropic/claude-haiku-4.5 --set maxRunTokens=32000 \
  --set-json 'extraArgs=["--log-level=debug","--no-soul","--no-user-model","--permissions-conventional=false","--agents-conventional=false"]' \
  --set extraVolumes=null --set extraVolumeMounts=null \
  --set-string extraEnv[0].name=OPENROUTER_API_KEY --set-string extraEnv[0].valueFrom.secretKeyRef.name="$secret" --set-string extraEnv[0].valueFrom.secretKeyRef.key=OPENROUTER_API_KEY \
  --wait --timeout=5m
kube -n execution-qualification rollout status deployment/mecak8s --timeout=240s
dev env -i HOME="$HOME" PATH="$PATH" KUBECONFIG="$kubeconfig" MECATL_KUBE_CONTEXT="$context" MECATL_EXECUTION_QUAL_STATE="$state" MECATL_EXECUTION_LIVE=1 MECATL_EXECUTION_LIVE_SECRET="$secret" MECATL_EXECUTION_LIVE_CLUSTER="$cluster" \
  go test -tags kind_execution_e2e -run '^TestKindExecutionLiveQualification$' -count=1 -timeout=10m ./e2e/k8s_execution

echo "live qualification passed; sanitized summary: $state/live-summary.json"
echo "qualification cluster retained: $cluster"
