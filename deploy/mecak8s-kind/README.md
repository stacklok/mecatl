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

`kind-hosts-add` manages two entries in `/etc/hosts`:
`127.0.0.1 keycloak.mecatl.svc.cluster.local` and
`127.0.0.1 mecak8s-mecak8s.mecatl.svc.cluster.local`; `kind-hosts-remove` removes
only those exact entries and leaves an `/etc/hosts.bak` backup. The Keycloak
port-forward listens on `127.0.0.1:8443`; it is not a NodePort or external
Service. Keycloak's alias preserves its configured issuer and certificate
hostname while making the local browser leg reachable. The mecak8s alias exists
for a different, less obvious reason -- see the footgun note below; it is not
merely a second convenience name.

### Supervised remote-client quickstart

After `kind-keycloak-setup` and the explicit `kind-hosts-add` step above, run:

```sh
task mecak8s:kind-keycloak-demo
```

This task deliberately does **not** recreate the cluster or invoke `sudo`. It
supervises both loopback-only port-forwards, waits for them to accept connections,
exports the fixture CA to `.scratch/kind/mecatl-dev/fixture-ca.crt`, and prints the
exact `mecatui login` and `mecatui connect` commands. Leave it running while using
the client; `Ctrl-C` tears down both forwards. The existing individual forward tasks
remain available when you need to manage them separately.

The authenticated mecak8s API is still reached only through its loopback
port-forward: gRPC at `18080`, HTTP at `18081`. `localhost` and `127.0.0.1` are
both certificate-covered names, so a plain TLS client (`curl`, `openssl
s_client`) can verify the fixture CA and connect to either one directly for a
raw reachability check.

**Footgun: do not pass a `localhost`-named target to `mecatui login`/`connect`
against this fixture.** This deployment sets `--workspace` (see
`values-kind.yaml`), which makes mecak8s the sole authority over the session
workspace and rejects any client-supplied value. But mecatui's own client
treats an address whose hostname is literally `localhost` (or a loopback IP)
as a co-located, embedded-style server and defaults its workspace field to the
CALLER's own working directory instead of leaving it empty
(`client.IsLoopbackHost`, `configureWorkspaceForTransport`). The two
assumptions collide: `mecatui connect localhost:18080 ...` fails with
`rpc error: code = InvalidArgument desc = server: invalid argument: deployment
assigns the workspace; filesystem session requests must leave workspace
empty`. A `kubectl port-forward` target is loopback by construction but is
never actually co-located, so the heuristic is wrong for exactly this fixture's
normal use.

The fix is the `mecak8s-mecak8s.mecatl.svc.cluster.local` alias
`kind-hosts-add` installs above: a DNS name is never treated as loopback by
`IsLoopbackHost`, so using it instead of `localhost` for `mecatui login` and
`mecatui connect` clears the client's workspace field as this deployment
requires, with no other change to the command. It resolves to the same
loopback address the port-forward already binds, so nothing else about the
connection changes.

The normal client journey is **Authorization Code + PKCE** with the public
`mecatui-kind` client and the optional `mecak8s:access` and `offline_access`
scopes. Request `offline_access` deliberately when the client needs a refresh token;
it is not granted by default. The realm's fixture users and any password grant are a
narrowly scoped **test helper** for non-browser validation only; they are not the
normal login flow. Use the access token whose `aud` includes `mecak8s` as the bearer
credential. A missing, forged, wrong-issuer, or wrong-audience token is rejected
before API handling.

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
