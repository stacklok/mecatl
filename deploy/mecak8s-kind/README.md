# Local mecak8s Kind fixture

This is an **operator-run**, disposable Kind baseline for `mecak8s`. It is not
the production Helm deployment in `deploy/helm/mecak8s/`, and it is not the
`e2e/k8s/` automated topology proof. The fixture builds and loads the local
`ko.local/mecak8s:dev` image, installs the chart's `values-kind.yaml` profile,
and uses that profile's local Redis StatefulSet.

It intentionally installs no optional identity or integration stack. By default it is
a local mock-provider baseline, so setup makes no provider network request and spends
no provider tokens.

## Provider modes

`task mecak8s:kind-setup` remains offline unless the operator explicitly exports
`OPENROUTER_API_KEY`. With that variable set, setup creates the fixture-owned
`mecak8s-openrouter` Secret from standard input, applies the real-provider overlay,
and disables `--mock`. The credential is projected only as the container's
`OPENROUTER_API_KEY` environment variable; it is never a Helm value or command-line
argument. Running setup later without the variable returns to mock mode and deletes
that fixture-owned Secret.

A real-provider smoke call is a **separate, explicit billable operator action** after
setup. It is not part of fixture setup or default tests; inspect the deployment and
choose an intentional client request only when provider spending is desired.

## Lifecycle

```sh
task mecak8s:kind-setup
task mecak8s:kind-status
# forwards gRPC to 127.0.0.1:18080 and HTTP to 127.0.0.1:18081
task mecak8s:kind-port-forward
task mecak8s:kind-destroy
```

Kind must talk to Podman directly on a Podman host. This avoids the Docker CLI
compatibility shim, whose cgroup detection does not match the real runtime:

```sh
KIND_EXPERIMENTAL_PROVIDER=podman task mecak8s:kind-setup
```

The same variable must be present for later `kind-status` and `kind-destroy`
commands. The image-loading task sees it and exports the locally built mecak8s
image through Podman before loading it into the Kind node.

The fixture always uses `deploy/mecak8s-kind/kconfig.yaml` and
`kind-mecatl-dev`; status, forwarding, and Helm commands never select the
ambient kubeconfig. Setup is idempotent by recreating the named `mecatl-dev`
cluster and `.scratch/kind/mecatl-dev` state. Destroy removes that named
cluster, the generated fixture kubeconfig, and the local state.

Host access is only through the explicit `kubectl port-forward` command. It
binds both ports to `127.0.0.1`; the chart Service remains `ClusterIP` and the
fixture creates no ingress, NodePort, LoadBalancer, or wildcard host binding.

## Optional Keycloak login journey

Run the identity layer only when caller identity needs exercising:

```sh
task mecak8s:kind-keycloak-setup
# Add the loopback-only issuer alias (requires sudo) before starting the browser login.
task mecak8s:kind-hosts-add
# In separate terminals, forward the issuer and mecak8s API.
task mecak8s:kind-keycloak-port-forward
task mecak8s:kind-port-forward
# Remove the alias when the local journey is complete.
task mecak8s:kind-hosts-remove
```

`kind-hosts-add` manages only `127.0.0.1 keycloak.mecatl.svc.cluster.local` in
`/etc/hosts`; `kind-hosts-remove` removes only that exact entry and leaves an
`/etc/hosts.bak` backup. The Keycloak port-forward listens on
`127.0.0.1:8443`; it is not a NodePort or external Service. This preserves
Keycloak's configured issuer and its certificate hostname while making the
local browser leg reachable.

The authenticated mecak8s API is still reached only through its loopback
port-forward. Connect to `https://localhost:18081` (and gRPC at
`localhost:18080`): `localhost` and `127.0.0.1` are certificate-covered names,
so clients must verify the fixture CA and hostname rather than disable TLS
verification.

The normal client journey is **Authorization Code + PKCE** with the public
`mecatui-kind` client and the optional `mecak8s:access` scope. The realm's
fixture users and any password grant are a narrowly scoped **test helper** for
non-browser validation only; they are not the normal login flow. Use the
access token whose `aud` includes `mecak8s` as the bearer credential. A missing,
forged, wrong-issuer, or wrong-audience token is rejected before API handling.

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
