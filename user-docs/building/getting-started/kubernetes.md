---
sidebar_position: 3
title: Try Mecatl on Kubernetes
description: Run mecak8s in a local Kind cluster and connect with mecatui.
---

# Try Mecatl on Kubernetes

In this tutorial, you will run `mecak8s` in a local Kind cluster and connect to
it with `mecatui`. The cluster uses an offline mock model provider, Redis-backed
session state, and two `mecak8s` replicas.

The setup recreates a cluster named `mecatl-dev`. Use it only for local
exploration.

## Prerequisites

You need a clone of the Mecatl repository and these commands on `PATH`:

- `kind`, `kubectl`, and `helm`;
- [Task](https://taskfile.dev/);
- [ko](https://ko.build/); and
- Docker or Podman.

You also need `mecatui`. Install it with Homebrew or follow
[Install Mecatl](/install.md).

You do not need a model provider API key.

## Start the cluster

From the Mecatl repository root, create the cluster and check its status:

```sh
env -u ANTHROPIC_API_KEY \
  -u OPENAI_API_KEY \
  -u OPENROUTER_API_KEY \
  task mecak8s:kind-setup
task mecak8s:kind-status
```

The `env -u` options keep the setup on its offline mock provider if you have a
provider key exported in your shell.

Setup builds `mecak8s`, creates the cluster, installs Redis and the Helm chart,
and waits for the deployment. The status command should show the `mecak8s` pods
and Redis StatefulSet as ready.

The cluster exposes gRPC at `127.0.0.1:18080` and HTTP at
`http://127.0.0.1:18081`.

## Connect with mecatui

Open another terminal and connect the client:

```sh
mecatui connect 127.0.0.1:18080
```

The TUI header should show `127.0.0.1:18080`. Enter this message to create a
remote session:

```text
Hello from Kubernetes.
```

The mock provider returns:

```text
Mock provider: no real model is configured. Set OPENAI_API_KEY for live use.
```

The response is fixed. This tutorial tests the Kubernetes deployment rather
than model behavior. Redis stores the message and response as session state.

## Replace the session's pod

Enter `/session` in `mecatui`, then press `c` to copy the full session ID. Keep
the client open so its `mecak8s` replica continues to hold the session lease.

Open a third terminal in the Mecatl repository and set the copied value:

```sh
SESSION_ID='<SESSION_ID>'
```

Find the pod that holds the session lease. The lease holder starts with the pod
name and ends with a process ID and nonce.

```sh
HOLDER=$(
  kubectl --kubeconfig=deploy/mecak8s-kind/kconfig.yaml \
    --context=kind-mecatl-dev --namespace=mecatl \
    get leases -o go-template='{{range .items}}{{if eq (index .metadata.annotations "mecatl.stacklok.com/session-id") "'"$SESSION_ID"'"}}{{.spec.holderIdentity}}{{"\n"}}{{end}}{{end}}'
)

POD=
for NAME in $(
  kubectl --kubeconfig=deploy/mecak8s-kind/kconfig.yaml \
    --context=kind-mecatl-dev --namespace=mecatl \
    get pods \
    -l 'app.kubernetes.io/name=mecak8s,app.kubernetes.io/instance=mecak8s,app.kubernetes.io/component=agent' \
    -o name
); do
  NAME=${NAME#pod/}
  case "$HOLDER" in
    "$NAME"-*) POD=$NAME; break ;;
  esac
done

test -n "$POD"
echo "$POD"
```

Delete that pod and wait for its replacement:

```sh
kubectl --kubeconfig=deploy/mecak8s-kind/kconfig.yaml \
  --context=kind-mecatl-dev --namespace=mecatl \
  delete pod "$POD"
kubectl --kubeconfig=deploy/mecak8s-kind/kconfig.yaml \
  --context=kind-mecatl-dev --namespace=mecatl \
  rollout status deployment/mecak8s-mecak8s --timeout=240s
```

The open client loses its connection when the pod stops. Reconnect from the
third terminal and resume the same session:

```sh
mecatui connect 127.0.0.1:18080 --resume "$SESSION_ID"
```

The original message and response should appear in the transcript. Send another
message:

```text
Hello again after the pod replacement.
```

You should receive the same fixed response. This confirms that another replica
can continue the session. Redis preserved the session, and the Kubernetes Lease
transferred single-writer ownership to the replica that resumed it.

## Remove the cluster

Exit `mecatui` with `ctrl+c` twice or `/quit`, then remove the cluster and its
local state:

```sh
task mecak8s:kind-destroy
```

## Next steps

- [Operate mecak8s](/building/deployment/mecak8s.md) to configure a real model
  provider, authentication, Redis, and production Helm values.
- [Connect to a server](/mecatui/remote-servers.md) for TLS, bearer token, and
  OIDC client workflows.
- [Pick your deployment shape](./deployment-decision.md) to compare `mecated`,
  `mecak8s`, `mecatequi`, and an embedded engine.
