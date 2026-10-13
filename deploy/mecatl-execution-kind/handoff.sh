#!/bin/sh

# CI step success is admission; this digest only detects same-run fixture drift.
handoff_ci_identity() {
  [ "${GITHUB_ACTIONS:-}" = true ] || return 1
  for number in "${GITHUB_RUN_ID:-}" "${GITHUB_RUN_ATTEMPT:-}"; do
    case "$number" in ''|*[!0-9]*) return 1 ;; esac
  done
  [ -n "${GITHUB_SHA:-}" ] && [ "${#GITHUB_SHA}" -eq 40 ] || return 1
  case "$GITHUB_SHA" in *[!0-9a-f]*) return 1 ;; esac
  [ "${MECATL_EXECUTION_HANDOFF_SHA:-}" = "$GITHUB_SHA" ] || return 1
  actual=$(git rev-parse HEAD) || return 1
  [ "$actual" = "$GITHUB_SHA" ] || return 1
  git diff --quiet HEAD -- || return 1
}

handoff_image() {
  value=$(awk -F= -v key="$1" '$1 == key {value=substr($0,length(key)+2); count++} END {if(count != 1 || value == "") exit 1; print value}' "$state/images/proof") || return 1
  digest=${value##*@}
  [ "$value" != "$digest" ] && [ "${#digest}" -eq 71 ] || return 1
  case "$digest" in sha256:*) ;; *) return 1 ;; esac
  case "${digest#sha256:}" in *[!0-9a-f]*) return 1 ;; esac
  printf '%s\n' "$value"
}

handoff_ready() {
  kube -n execution-qualification rollout status deployment/mecatl-execution --timeout=240s || return 1
  kube -n execution-qualification rollout status deployment/mecak8s --timeout=240s || return 1
  kube -n execution-qualification rollout status deployment/oidc-issuer --timeout=240s || return 1
  kube -n execution-qualification wait --for=condition=Ready pod -l app.kubernetes.io/name=oidc-issuer --timeout=240s || return 1
  kube -n execution-qualification wait --for=condition=Ready pod -l app.kubernetes.io/name=mecatl-execution --timeout=240s || return 1
  kube -n execution-qualification wait --for=condition=Ready pod -l app.kubernetes.io/name=mecak8s --timeout=240s || return 1
}

handoff_digest() (
  set -eu
  handoff_ci_identity || exit 1
  canonical=$(CDPATH= cd -- "$state" && pwd -P) || exit 1
  [ "$canonical" = "$state" ] || exit 1
  [ ! -L "$state" ] && [ -f "$state/ownership" ] && [ ! -L "$state/ownership" ] || exit 1
  [ -f "$kubeconfig" ] && [ ! -L "$kubeconfig" ] || exit 1
  mode=$(stat -c '%a' "$kubeconfig") || exit 1
  [ "$mode" = 600 ] || exit 1
  current_context=$(kube config current-context) || exit 1
  [ "$current_context" = "$context" ] || exit 1
  label=$("$runtime" inspect "${cluster}-control-plane" --format '{{index .Config.Labels "io.x-k8s.kind.cluster"}}') || exit 1
  [ "$label" = "$cluster" ] || exit 1
  tmp=$(mktemp -d) || exit 1
  trap 'rm -f -- "$tmp/input" "$tmp/object"; rmdir -- "$tmp"' EXIT
  {
    printf '%s\n' "$GITHUB_SHA" "$GITHUB_RUN_ID" "$GITHUB_RUN_ATTEMPT" "$state" "$cluster" "$context" "$kubeconfig"
    stat -c '%u:%g:%a:%i' "$state/ownership" "$kubeconfig" || exit 1
    sha256sum "$state/ownership" "$state/images/proof" "$state/go-template-revision" "$state/pki/ca.crt" || exit 1
    "$runtime" inspect "${cluster}-control-plane" --format '{{.Id}} {{.Image}}' || exit 1
    # Only public connection identity, never --raw kubeconfig or key material.
    kube config view --minify -o json >"$tmp/object" || exit 1
    jq -ceS 'select((.users | length) == 1 and (.users[0].user | keys) == ["client-certificate-data","client-key-data"])' "$tmp/object" || exit 1
    images=$("$runtime" exec "${cluster}-control-plane" ctr -n k8s.io images ls) || exit 1
    for key in provider agent oidc netprobe workload derivative incompatible; do
      image=$(handoff_image "$key") || exit 1
      descriptor=$(printf '%s\n' "$images" | awk -v ref="$image" '$1 == ref {print $2 " " $3; count++} END {if(count != 1) exit 1}') || exit 1
      [ "${descriptor##* }" = "${image##*@}" ] || exit 1
      printf '%s=%s %s\n' "$key" "$image" "$descriptor"
    done
    # Installed identity/spec/data, not Helm's original values or status clocks.
    for object in namespace/execution-qualification deployment/mecatl-execution deployment/mecak8s deployment/oidc-issuer configmap/execution-mock configmap/mecatl-execution-templates configmap/mecatl-execution-security-manifest configmap/mecatl-execution-security-authority issuer/execution-fixture-ca certificate/execution-provider-tls certificate/execution-client-tls; do
      kube -n execution-qualification get "$object" -o json >"$tmp/object" || exit 1
      jq -ceS '{kind, name:.metadata.name, uid:.metadata.uid, labels:.metadata.labels, spec, data} | select(.uid != null and .uid != "")' "$tmp/object" || exit 1
      case "$object" in
        deployment/mecatl-execution) key=provider ;;
        deployment/mecak8s) key=agent ;;
        deployment/oidc-issuer) key=oidc ;;
        *) continue ;;
      esac
      image=$(handoff_image "$key") || exit 1
      jq -e --arg image "$image" '.spec.template.spec.containers | any(.image == $image)' "$tmp/object" >/dev/null || exit 1
    done
    # Policy and service wiring are also public; exclude mutable metadata/status.
    for resource in networkpolicy service serviceaccount role rolebinding; do
      kube -n execution-qualification get "$resource" -o json >"$tmp/object" || exit 1
      jq -ceS '[.items[] | {kind, name:.metadata.name, uid:.metadata.uid, spec, rules, roleRef, subjects, automountServiceAccountToken}] | sort_by(.kind,.name)' "$tmp/object" || exit 1
    done
    # Request only Secret metadata and public trust anchors. Leaf renewal is free
    # to change certificate bytes/resourceVersion without invalidating the proof.
    for name in execution-security execution-client-tls execution-trust execution-fixture-ca oidc-fixture-identity; do
      identity=$(kube -n execution-qualification get secret "$name" -o 'jsonpath={.metadata.uid}{" "}{.metadata.ownerReferences}') || exit 1
      [ -n "${identity%% *}" ] || exit 1
      printf '%s=%s\n' "$name" "$identity"
    done
    for name in execution-trust execution-client-tls execution-fixture-ca; do
      case "$name" in
        execution-trust) projection='{.data.clients\.pem}' ;;
        execution-client-tls) projection='{.data.ca\.crt}' ;;
        execution-fixture-ca) projection='{.data.tls\.crt}' ;;
      esac
      trust=$(kube -n execution-qualification get secret "$name" -o "jsonpath=$projection") || exit 1
      [ -n "$trust" ] || exit 1
      printf '%s=%s\n' "$name" "$trust"
    done
  } >"$tmp/input"
  hash=$(sha256sum "$tmp/input") || exit 1
  printf '%s\n' "${hash%% *}"
)

handoff_consume() {
  handoff_ci_identity || return 1
  [ "${MECATL_EXECUTION_HANDOFF_COMPLETE:-}" = 1 ] || return 1
  [ "${MECATL_EXECUTION_HANDOFF_RUN_ID:-}" = "$GITHUB_RUN_ID" ] &&
  [ "${MECATL_EXECUTION_HANDOFF_RUN_ATTEMPT:-}" = "$GITHUB_RUN_ATTEMPT" ] || return 1
  [ "${MECATL_EXECUTION_HANDOFF_CLUSTER:-}" = "$cluster" ] &&
  [ "${MECATL_EXECUTION_QUAL_STATE:-}" = "$state" ] || return 1
  [ -z "${MECATL_EXECUTION_LIVE_SESSION:-}" ] || return 1
  expected=${MECATL_EXECUTION_HANDOFF_DIGEST:-}
  [ "${#expected}" -eq 64 ] || return 1
  case "$expected" in *[!0-9a-f]*) return 1 ;; esac
  handoff_ready || return 1
  current=$(handoff_digest) || return 1
  [ "$current" = "$expected" ] || return 1
}
