#!/bin/sh

set -u

status=${1:?exit status is required}
[ "$status" -ne 0 ] || exit 0

cluster=mecatl-dev
state=.scratch/kind/mecatl-dev
kubeconfig=deploy/mecak8s-vmcp/kconfig.yaml
setup_lock=.scratch/kind/mecatl-dev.setup-lock

if [ "${MECAK8S_KEEP_ON_FAILURE:-}" = 1 ]; then
	echo "setup failed; retaining $cluster cluster because MECAK8S_KEEP_ON_FAILURE=1"
	exit "$status"
fi

kind delete cluster --name="$cluster" 2>/dev/null || true
rm -rf "$state"
rm -f "$kubeconfig" "$setup_lock"
exit "$status"
