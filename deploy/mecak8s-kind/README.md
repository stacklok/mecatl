# Local mecak8s Kind fixture

This is an **operator-run**, disposable Kind baseline for `mecak8s`. It is not
the production Helm deployment in `deploy/helm/mecak8s/`, and it is not the
`e2e/k8s/` automated topology proof. The fixture builds and loads the local
`ko.local/mecak8s:dev` image, installs the chart's `values-kind.yaml` profile,
and uses that profile's local Redis StatefulSet.

It intentionally installs no optional identity or integration stack. It is a
local mock-provider baseline only.

## Lifecycle

```sh
task mecak8s:kind-setup
task mecak8s:kind-status
# forwards gRPC to 127.0.0.1:18080 and HTTP to 127.0.0.1:18081
task mecak8s:kind-port-forward
task mecak8s:kind-destroy
```

The fixture always uses `deploy/mecak8s-kind/kconfig.yaml` and
`kind-mecatl-dev`; status, forwarding, and Helm commands never select the
ambient kubeconfig. Setup is idempotent by recreating the named `mecatl-dev`
cluster and `.scratch/kind/mecatl-dev` state. Destroy removes that named
cluster, the generated fixture kubeconfig, and the local state.

Host access is only through the explicit `kubectl port-forward` command. It
binds both ports to `127.0.0.1`; the chart Service remains `ClusterIP` and the
fixture creates no ingress or node-port exposure.

## Boundary

`values-kind.yaml` is intentionally the exact profile installed directly by
`e2e/k8s/`. Do not add fixture-specific overlays or Secret dependencies to it:
the automated suite creates only its namespace before chart installation. For
production settings, use `deploy/helm/mecak8s/` with an externally managed
Redis endpoint and its required credentials.

This convenience fixture makes no production network-isolation claim. It has
no general NetworkPolicy; the default Kind network is not enforcement evidence.
Use production authentication and network controls when exposing a service
outside the local loopback workflow.
