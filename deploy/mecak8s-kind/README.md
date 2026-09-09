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
# Base fixture mappings expose gRPC at 127.0.0.1:18080 and HTTP at 127.0.0.1:18081.
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
`kind-mecatl-dev`; status, mapping, and Helm commands never select the
ambient kubeconfig. Setup is idempotent by recreating the named `mecatl-dev`
cluster and `.scratch/kind/mecatl-dev` state. Destroy removes that named
cluster, the generated fixture kubeconfig, and the local state.

Host access uses the Kind cluster's three static `extraPortMappings`, each bound
to `127.0.0.1` at cluster creation: NodePorts 30080/30081 map to host ports
18080/18081, and Keycloak NodePort 30443 maps to 8443. The shared
`values-kind.yaml` and bare chart defaults remain `ClusterIP`; only the
fixture's `kind-nodeports.yaml` overlay selects NodePort plumbing.

Only the *host* binding is loopback-only. Unlike the port-forward this
replaced, the NodePorts are also open on the Kind node container itself, so
anything that can route to that node's address -- other containers on the same
Docker network, and the Docker bridge on Linux -- reaches the base fixture,
which runs unauthenticated. That is accepted for a disposable local cluster;
it is not a production isolation claim.

## Optional Keycloak login journey

Run the identity layer only when caller identity needs exercising:

```sh
task mecak8s:kind-keycloak-setup
# Add the loopback-only issuer alias (requires sudo) before starting the browser login.
task mecak8s:kind-hosts-add
# The static Kind mappings expose the issuer and mecak8s API directly.
task mecak8s:kind-keycloak-demo
# Remove the alias when the local journey is complete.
task mecak8s:kind-hosts-remove
```

`kind-hosts-add` manages two entries in `/etc/hosts`:
`127.0.0.1 keycloak.mecatl.svc.cluster.local` and
`127.0.0.1 mecak8s-mecak8s.mecatl.svc.cluster.local`; `kind-hosts-remove` removes
only those exact entries and leaves an `/etc/hosts.bak` backup. The Keycloak
NodePort is mapped by Kind only to `127.0.0.1:8443` on the host. Keycloak's
alias preserves its configured issuer and certificate hostname while making the
local browser leg reachable. The mecak8s alias exists for a different, less
obvious reason -- see the footgun note below; it is not merely a second
convenience name.

### Direct remote-client quickstart

After `kind-keycloak-setup` and the explicit `kind-hosts-add` step above, run:

```sh
task mecak8s:kind-keycloak-demo
```

This task deliberately does **not** recreate the cluster or invoke `sudo`. It
waits for the three direct loopback mappings to accept connections, exports the
fixture CA to `.scratch/kind/mecatl-dev/fixture-ca.crt`, and prints the exact
`mecatui login` and `mecatui connect` commands. The task exits after readiness
checks; the mappings remain available while the cluster exists.

Or skip straight to a shell: `task mecak8s:kind-login` (adds the `/etc/hosts`
aliases, refreshes the CA, runs `mecatui login`) then `task
mecak8s:kind-connect` (same, then `mecatui connect`) — both bake in this
fixture's fixed issuer/client/audience/scopes, so there's nothing to copy from
the printed commands above.

> **The exported CA is only valid for the CURRENT cluster.** `kind-destroy` +
> recreate mints a brand-new self-signed CA; a `fixture-ca.crt` left over from
> a previous cluster fails TLS verification against the new one, and mecatui
> surfaces that as a bare `clientauth: OIDC discovery rejected` — nothing in
> that message hints that the cause is a stale CA rather than a real
> rejection. `kind-login`/`kind-connect` always refresh the CA before
> connecting, so this can't happen through them; if you invoke `mecatui`
> directly with a CA path from an earlier session, re-run
> `task mecak8s:kind-keycloak-demo` (or either shorthand task) first.

The authenticated mecak8s API is reached through the Kind host mappings: gRPC at
`18080`, HTTPS at `18081`. `localhost` and `127.0.0.1` are
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
empty`. A direct Kind mapping is loopback-bound on the host but never actually
co-located, so the heuristic is wrong for exactly this fixture's normal use.

The fix is the `mecak8s-mecak8s.mecatl.svc.cluster.local` alias
`kind-hosts-add` installs above: a DNS name is never treated as loopback by
`IsLoopbackHost`, so using it instead of `localhost` for `mecatui login` and
`mecatui connect` clears the client's workspace field as this deployment
requires, with no other change to the command. It resolves to the same loopback
address exposed by the Kind mapping, so nothing else about the connection
changes.

The normal client journey is **Authorization Code + PKCE** with the public
`mecatui-kind` client and the optional `mecak8s:access` and `offline_access`
scopes. Request `offline_access` deliberately when the client needs a refresh token;
it is not granted by default. The realm's fixture users and any password grant are a
narrowly scoped **test helper** for non-browser validation only; they are not the
normal login flow. Use the access token whose `aud` includes `mecak8s` as the bearer
credential. A missing, forged, wrong-issuer, or wrong-audience token is rejected
before API handling.

## Stored-credential qualification (manual)

Status: **not run**. These instructions are not live qualification evidence. Run on
macOS with explicit file storage and separately on genuinely headless Linux with
fresh default-auto storage. No automatic shared-cluster teardown is performed.

Run `task build` separately. `task mecak8s:kind-keycloak-setup` is **destructive**:
it deletes/recreates `mecatl-dev` and its `.scratch/kind/mecatl-dev` state. Only use
it for a new/disposable fixture. Reuse a healthy cluster with the idempotent
`task mecak8s:kind-keycloak-apply`, then `task mecak8s:kind-hosts-add` and
`task mecak8s:kind-keycloak-demo`. Use the mock provider (do not export
`OPENROUTER_API_KEY`), two mecak8s replicas, ephemeral local Redis, and one ephemeral
Keycloak `start-dev` pod. Keep the realm lifetime at **900 seconds**; `offline_access`
requests a refresh token. Wait about **870 seconds** for natural refresh demand;
do not edit stored tokens, clocks, or the realm lifetime.

Use a fresh isolated root, not your normal configuration. The explicit enrollment
below does not rely on discovery; keep every parameter:

```sh
install -d -m 0700 "$PWD/.scratch/kind/mecatl-dev/client-xdg"
XDG_CONFIG_HOME="$PWD/.scratch/kind/mecatl-dev/client-xdg" ./bin/mecatui login mecak8s-mecak8s.mecatl.svc.cluster.local:18080 --credential-store=file --issuer https://keycloak.mecatl.svc.cluster.local:8443/realms/mecatl --client-id mecatui-kind --audience mecak8s --tls-ca .scratch/kind/mecatl-dev/fixture-ca.crt --private-issuer --scopes openid,profile,mecak8s:access,offline_access
XDG_CONFIG_HOME="$PWD/.scratch/kind/mecatl-dev/client-xdg" ./bin/mecatui connect mecak8s-mecak8s.mecatl.svc.cluster.local:18080 sessions --tls --tls-ca .scratch/kind/mecatl-dev/fixture-ca.crt
```

The first login prints `Using file-backed credential storage (owner-only permissions).`
once before OAuth. After the original access token naturally enters the 30-second refresh
window (about 870 seconds after issue), run the authenticated `connect` command again.
Then start a new `mecatui` process and run it once more; both must authenticate without
another login or notice. Finally, log out and verify that an authenticated connect fails
with login required without silently starting OAuth:

```sh
XDG_CONFIG_HOME="$PWD/.scratch/kind/mecatl-dev/client-xdg" ./bin/mecatui logout mecak8s-mecak8s.mecatl.svc.cluster.local:18080
XDG_CONFIG_HOME="$PWD/.scratch/kind/mecatl-dev/client-xdg" ./bin/mecatui connect mecak8s-mecak8s.mecatl.svc.cluster.local:18080 sessions --tls --tls-ca .scratch/kind/mecatl-dev/fixture-ca.crt
```

This manual journey is intentionally weaker evidence: it has no field-by-field semantic
persistence oracle and makes no claim that helper checks remain. Automated hermetic tests
retain persistence, private-permission, and conflicting-selector coverage. Do not inspect
token files, export bearers, or place token material in shell history or evidence.

On an actual headless Linux host with no running session bus/Secret Service, create
a different root (merely unsetting `DBUS_SESSION_BUS_ADDRESS` is not qualification):

```sh
install -d -m 0700 "$PWD/.scratch/kind/mecatl-dev/client-xdg-linux"
XDG_CONFIG_HOME="$PWD/.scratch/kind/mecatl-dev/client-xdg-linux" ./bin/mecatui login mecak8s-mecak8s.mecatl.svc.cluster.local:18080 --no-browser --issuer https://keycloak.mecatl.svc.cluster.local:8443/realms/mecatl --client-id mecatui-kind --audience mecak8s --tls-ca .scratch/kind/mecatl-dev/fixture-ca.crt --private-issuer --scopes openid,profile,mecak8s:access,offline_access
```

Repeat the same login → connect → natural-refresh connect → new-process connect → logout
→ failed-connect journey using `client-xdg-linux`. This is Authorization Code + PKCE,
not device login: `--no-browser` still needs `http://127.0.0.1:18473/oauth/callback`.
Before login, arrange browser-side hostname resolution and callback connectivity; for SSH,
forward browser-side ports 8443 and 18473 to those ports on the Linux host and resolve the
Keycloak hostname to browser-side loopback. Without that prerequisite, record Linux
qualification blocked; do not substitute a password or device grant.

Always **logout before cleanup**, then `task mecak8s:kind-hosts-remove`. Preserve the
shared cluster by default. Run `task mecak8s:kind-destroy` only with explicit operator
intent to remove that disposable cluster. Record each platform's actual journey separately;
do not claim full E2E until both journeys pass.

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
