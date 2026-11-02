#!/bin/sh

set -u

status=${1:?exit status is required}
[ "$status" -ne 0 ] || exit 0

cluster=mecatl-idbroker
state=.scratch/kind/mecatl-idbroker
setup_lock=.scratch/kind/mecatl-idbroker.setup-lock

if [ "${MECAK8S_KEEP_ON_FAILURE:-}" = 1 ]; then
	echo "setup failed; retaining $cluster cluster because MECAK8S_KEEP_ON_FAILURE=1"
	exit "$status"
fi

kind delete cluster --name="$cluster" --kubeconfig="$state/kconfig.yaml" 2>/dev/null || true
rm -rf "$state"
rm -f "$setup_lock"
exit "$status"
