#!/bin/sh
set -eu
# Called only with the owned fixture's explicit kubeconfig/context. Each request
# is bounded; the caller also bounds the whole collector (including jq).
[ "$#" -eq 3 ] || exit 2
kubeconfig=$1
context=$2
output=$3
filters=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
umask 077
set -C
exec 3> "$output"
failed=0
# Collect logs first so a short termination grace still retains create progress.
# Drain every bounded response: no head/SIGPIPE and no raw log file, even locally.
# Fixed workloads in the already ownership-checked fixture namespace only.
for deployment in mecak8s mecatl-execution; do
  if ! {
    if ! kubectl --kubeconfig "$kubeconfig" --context "$context" --request-timeout=5s logs "deployment/$deployment" -n execution-qualification --all-containers=true --tail=1000 --limit-bytes=262144 2>/dev/null; then
      printf '\n{"collector_unavailable":true}\n'
    fi
  } | jq -Rnc --arg source "$deployment" -f "$filters/create-stages.jq" >&3 2>/dev/null; then
    failed=1
  fi
done
if ! {
  for resource in executionenvironments pods persistentvolumeclaims resourcequotas events; do
    if ! kubectl --kubeconfig "$kubeconfig" --context "$context" --request-timeout=5s get "$resource" -n execution-qualification -o json 2>/dev/null; then
      printf '{"unavailable":true}\n'
    fi
  done
} | jq -cs -f "$filters/failure-evidence.jq" >&3 2>/dev/null; then
  failed=1
  printf '{"kind":"collection","unavailable":true}\n' >&3
fi
exit "$failed"
