#!/bin/sh
# Passive checks against an operator-provisioned local Kind broker fixture.
set -eu
if [ "$#" -ne 4 ]; then
  echo 'usage: broker-session-check.sh KUBECONFIG CONTEXT NAMESPACE RELEASE' >&2
  exit 2
fi
kubeconfig=$1
context=$2
namespace=$3
release=$4
case "$context" in kind-*) ;; *) echo 'an explicit Kind context is required' >&2; exit 2 ;; esac
command -v kubectl >/dev/null
command -v jq >/dev/null
kube() { kubectl --kubeconfig="$kubeconfig" --context="$context" --namespace="$namespace" "$@"; }
kube get configmap "$release-mecak8s-broker-config" -o json | jq -e '
  .data["broker.json"] | fromjson |
  .session_api.mode == "OWNERLESS" and
  (.session_api.deployment | test("^[A-Za-z0-9._-]{1,128}$")) and
  (.transport | keys == ["execute_deadline", "rpc_deadline"]) and
  (.protected_storage.redis.address | length > 0)
' >/dev/null
kube get configmap "$release-mecak8s-mcp" -o json | jq -e '
  .data["settings.yaml"] | test("^mcp:\n  mode: broker\n?$")
' >/dev/null
kube rollout status "deployment/$release-mecak8s-broker" --timeout=120s
kube rollout status "deployment/$release-mecak8s" --timeout=120s
kube exec "deployment/$release-mecak8s-broker" -- /ko-app/mecabroker ready
if [ -n "${HOST_URL:-}" ]; then
  : "${SESSION_ID:?provide a test-owned enrolled session ID}"
  : "${CURL_CONFIG:?provide a private curl config with host CA and caller authentication}"
  case "$SESSION_ID" in *[!A-Za-z0-9_-]*|'') echo 'invalid session ID' >&2; exit 2 ;; esac
  curl --config "$CURL_CONFIG" --silent --show-error --fail --max-time 15 \
    "$HOST_URL/v1/sessions/$SESSION_ID/mcp/connectors" | jq -e '.availability == "available" and .total_connectors > 0' >/dev/null
fi
printf '%s\n' 'Kind broker configuration and readiness checks passed; invocation/replacement checks remain manual.'
