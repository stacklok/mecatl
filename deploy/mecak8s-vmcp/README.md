# Disposable Kind mecak8s + ToolHive vMCP integration fixture

This integration fixture exercises an OAuth-protected ToolHive vMCP endpoint in a disposable
Kind cluster. It deploys the local mecak8s chart, Keycloak, ToolHive operator, a
dedicated ToolHive Redis, Yardstick, and one `VirtualMCPServer`. Mecak8s is
present, but mecak8s is not in the outbound vMCP path: this does not implement a
credential broker, SPIFFE, sidecars, or mecatui remote login.

## Pinned runtimes

- ToolHive `v0.45.0`, commit `cc922a8b47652988ae4d057a957942385fc59270`
- Yardstick `v1.1.1`, commit `e5b8908ed6f53c1171ac805d82cf858d2982fa19e`,
  image `ghcr.io/stackloklabs/yardstick/yardstick-server:1.1.1`

`versions.yaml` records the pins. `task mecak8s-vmcp:vmcp-status` captures the actual
Pod image IDs; the mecatl ToolHive Go-module version is not runtime proof.

## Scope

```text
browser PKCE client -> loopback vMCP + embedded AS -> Yardstick /mcp
                                  |
                                  +-> Keycloak Alice or Bob
```

OAuth protects the vMCP resource. Yardstick is an internal deterministic,
read-only backend and receives no Keycloak credential. This proves ToolHive OAuth
sessions and fail-closed protected-resource handling, not external provider
grants or mecak8s outbound brokerage.

### Local-only Keycloak logins

The disposable fixture deliberately uses public test credentials so an operator
can complete the browser journeys without recovering a password hash:

| User | Password |
| --- | --- |
| `alice@example.com` | `Secret123` |
| `bob@example.com` | `Secret123` |

They are valid only for the ephemeral Keycloak realm imported by this fixture;
they are not Kubernetes Secret values, provider credentials, or reusable user
passwords.

## Lifecycle

```sh
task mecak8s-vmcp:vmcp-check
task mecak8s-vmcp:vmcp-setup
task mecak8s-vmcp:vmcp-status
kind delete cluster --name=mecatl-dev
```

All Kubernetes commands use `deploy/mecak8s-vmcp/kconfig.yaml` and
`kind-mecatl-dev`, never the ambient kubeconfig. The setup follows ToolHive's
local fixture pattern by writing the named cluster's config with
`kind get kubeconfig --name mecatl-dev > deploy/mecak8s-vmcp/kconfig.yaml`.

## Credential boundary

Setup generates Kubernetes Secrets `vmcp-redis-auth`, `vmcp-signing-key`, and
`vmcp-hmac` without printing values. Do not inspect Secret data, enable shell
tracing, or place bearer values, callback query values, authorization codes,
non-fixture credentials, raw responses, or session IDs in logs, reports, Git, or
model output. A journey client must retain those values only in memory.

Bind the Kind node-port mappings to loopback only. `kind-setup` creates these mappings:

| Host address | Kind node port | Service |
| --- | --- | --- |
| `https://keycloak.mecatl-vmcp.svc.cluster.local:8443` | `30843` | Keycloak |
| `https://mecak8s-mecak8s.mecatl-vmcp.svc.cluster.local:18081` | `30081` | mecak8s gRPC/HTTP |

The Keycloak and mecak8s host mappings need no `kubectl port-forward`. The vMCP
Service remains ClusterIP and still needs a loopback-only port-forward for the
browser resource endpoint.

For a temporary host journey, inspect the exact aliases, add them, and remove
them when finished. These tasks require `sudo`, never modify `/etc/hosts`
silently, and removal creates `/etc/hosts.bak`:

```sh
task mecak8s-vmcp:kind-hosts-show
task mecak8s-vmcp:kind-hosts-add
# use the browser/client journey
task mecak8s-vmcp:kind-hosts-remove
```

The exact entries managed by these tasks are:

```text
127.0.0.1 keycloak.mecatl-vmcp.svc.cluster.local
127.0.0.1 mecak8s-mecak8s.mecatl-vmcp.svc.cluster.local
```
## mecak8s TLS and caller identity

The Kind chart mounts the cert-manager-issued `mecak8s-tls` Secret as exact,
read-only `tls.crt` and `tls.key` items and starts both gRPC and HTTP with the
paired TLS flags. Its `/readyz`, `/healthz`, and `/drain` management endpoints
remain explicit and are probed over HTTPS; they are not authenticated API
requests. The chart mounts the public `tls.crt` item from `fixture-ca` and
passes its exact path through `--oidc-ca-cert-file`; it does not set the
process-wide `SSL_CERT_FILE`. The Kind profile enables
`--oidc-allow-private-https-issuer` only for the in-cluster Keycloak Service. The
scoped OIDC transport admits only the configured Keycloak host and its pinned
private addresses; HTTPS, CA and hostname validation, redirect refusal, and
per-dial DNS-pinned checks remain enforced. It validates the HTTPS Keycloak realm
issuer and the shared `http://127.0.0.1:18080/mcp` audience without the deprecated
combined HTTP/private `--oidc-insecure-allow-private-issuer` escape hatch.

For a host-only TLS check, use the loopback-only mecak8s NodePort and export
the public fixture CA to `.scratch/` (the CA is public, but do not export or
print any private-key Secret item):

```sh
task mecak8s-vmcp:kind-hosts-add
# connect to mecak8s-mecak8s.mecatl-vmcp.svc.cluster.local:18081
kubectl --kubeconfig=deploy/mecak8s-vmcp/kconfig.yaml --context=kind-mecatl-dev \
  --namespace=mecatl-vmcp get secret fixture-ca -o jsonpath='{.data.tls\.crt}' | base64 --decode > .scratch/mecak8s-vmcp-ca.crt
```

A normal-terminal `mecatui login` then `connect` flow with custom-CA TLS and OIDC
Authorization Code + PKCE is available. After `kind-hosts-add`, use the documented
Service-DNS host aliases and export the public fixture CA to `.scratch/`; then enroll
with:

```sh
mecatui login <mecak8s-host>:18081 --issuer <fixture-https-issuer> --client-id mecatui-kind --audience http://127.0.0.1:18080/mcp \
  --tls-ca <exported-fixture-ca> --scopes openid,profile,mcp:read,offline_access
```

Then connect with `mecatui connect <mecak8s-host>:18081 --tls --tls-ca <exported-fixture-ca>`.
The login uses Authorization Code + PKCE, not device flow. `offline_access` is optional on
`mecatui-kind`, and the fixture users hold Keycloak's `offline_access` realm role, so this
explicit request receives a refresh token. Mecatui stores it with the login and uses it to
refresh the access token; omit the scope when a refreshable login is not needed. For SSH or
another headless host, add
`--no-browser`, open the printed URL in a browser on the operator workstation, and
forward the fixed callback port back to the login host:

```sh
ssh -N -L 18473:127.0.0.1:18473 user@login-host
```

The callback remains `http://127.0.0.1:18473/oauth/callback`; do not substitute the
vMCP resource audience for the separate `mecatui-kind` client ID.

The security contract is fail-closed before any authenticated RPC: verified TLS
using the supplied custom fixture CA is required, and plaintext, unauthenticated,
or an untrusted CA must fail during transport/authentication setup. A successful
live client qualification remains confirmation-gated; offline tests do not claim
that live connection has been performed.

## Separate Keycloak public clients

`vmcp-browser` remains the embedded-vMCP browser client and uses
`http://127.0.0.1:18080/oauth/callback`. `mecatui-kind` is a separate public
client reserved for a normal-terminal loopback PKCE journey at
`http://127.0.0.1:18473/oauth/callback`. Neither client has a secret. After setup,
the host-side `mecatui login ADDRESS` flow may use `mecatui-kind` for the live remote
mecak8s qualification; the resulting credential is stored by mecatui, not by this
fixture. The fixture does not implement device flow or headless login, and the
setup/journey remains confirmation-gated.

## Shared Keycloak HTTPS issuer

`kind-setup` installs the pinned cert-manager chart before it requests the
fixture-local CA and the `keycloak-tls` Certificate. cert-manager generates the CA
and leaf Secret values in the cluster; this repository contains neither a
private key nor a certificate value.

The sole shared issuer is
`https://keycloak.mecatl-vmcp.svc.cluster.local:8443/realms/mecatl`. Keycloak publishes discovery at
`https://keycloak.mecatl-vmcp.svc.cluster.local:8443/realms/mecatl/.well-known/openid-configuration`
and JWKS at `https://keycloak.mecatl-vmcp.svc.cluster.local:8443/realms/mecatl/protocol/openid-connect/certs`; tokens issued
there use that HTTPS URL as `iss`.

Pods use that Service-DNS name directly. For temporary host access, run
`task mecak8s-vmcp:kind-hosts-add`; remove the exact aliases with
`task mecak8s-vmcp:kind-hosts-remove` when finished. The host and pod therefore use
the same HTTPS issuer URL, with the Kind mapping bound to loopback only. The `keycloak-tls` certificate covers the Service DNS name, `localhost`, and `127.0.0.1`.
Mecak8s trusts the fixture CA through its direct `--oidc-ca-cert-file` flag and
admits only this private HTTPS Service issuer; it does not use the deprecated
HTTP/private issuer escape hatch; this is without OIDC insecure relaxation.

| Endpoint | Role |
| --- | --- |
| `https://keycloak.mecatl-vmcp.svc.cluster.local:8443/realms/mecatl` | Shared HTTPS Keycloak realm issuer for pod OIDC and host alias access. |
| `http://127.0.0.1:18080` | Separate loopback vMCP embedded authorization-server and browser callback baseline; it is not the Keycloak issuer. |

The fixture deliberately installs no `NetworkPolicy`: an earlier fixture egress
policy blocked DNS and Redis, so it could not support the storage-free mecak8s
runtime. NetworkPolicy design and enforcement evidence are out of scope for this
local qualification; the default Kind CNI is not an enforcement proof.

## RFC 8693 delegation

The fixture proves **external-issuer** token exchange: a caller authenticates at
Keycloak, and a confidential delegate client exchanges that Keycloak token for a
short-lived, scope-attenuated vMCP token.

```text
alice -> Keycloak -> access token (scope: mcp:read, aud: vMCP resource)
      -> RFC 8693 exchange at ToolHive's embedded AS as mecak8s-delegate
      -> T2 (act.sub=mecak8s-delegate, act.act.sub=mecatui-kind, scp=[mcp:read])
      -> vMCP -> Yardstick
```

Keycloak replaced Dex because Dex cannot issue a usable subject token: its JWT
carries no `scope`/`scp` claim, and its id_token carries `at_hash`, which ToolHive
rejects outright. See [the delegation contract report](../../docs/design/mecak8s-vmcp-delegation-contract.md) for the
contract, and `keycloak-realm-generate.sh` for how the realm JSON is produced
(do not hand-edit it -- a hand-written `clientScopes` array silently replaces
Keycloak's built-ins and drops the `sub` claim).

### Known gaps

- `incomingAuth.authzConfig` is absent. A Cedar authorizer alongside an embedded
  auth server makes the operator derive `primaryUpstreamProvider`, after which
  Cedar resolves claims from the upstream IdP token -- which a delegated token
  never has. Every tool is then filtered before policy evaluation. See
  [stacklok/toolhive#6424](https://github.com/stacklok/toolhive/issues/6424).
  The fixture therefore demonstrates authentication only, not authorization.
- `SSL_CERT_FILE` is still set on the vMCP pod because `TrustedIssuerConfig` has
  no CA-bundle field ([#6429](https://github.com/stacklok/toolhive/issues/6429)).
- mecak8s is not yet in the token path, so `values-kind-vmcp.yaml`'s `audience`
  is an unexercised placeholder.

### Getting a delegated token

Requires the loopback aliases from `task mecak8s-vmcp:kind-hosts-add` (Keycloak is
reached by its in-cluster name, so one URL works from host and pod alike), plus a
port-forward for vMCP:

```sh
kubectl -n mecatl-vmcp port-forward svc/vmcp-vmcp 18080:4483
```

The normal client login is Authorization Code + PKCE, as used by the browser
PKCE client in [Scope](#scope). The password grant call below is a narrow,
non-browser test helper for scripting this walkthrough, not the normal login
flow.

Then two calls. First, the caller authenticates at Keycloak:

```sh
KC=https://keycloak.mecatl-vmcp.svc.cluster.local:8443/realms/mecatl
curl -s --cacert .scratch/kind/mecatl-dev/fixture-ca.crt \
  -X POST "$KC/protocol/openid-connect/token" \
  -d grant_type=password -d client_id=mecatui-kind \
  -d username=alice -d password=Secret123 \
  -d 'scope=openid email mcp:read' | jq -r .access_token
```

Then exchange it. Read the delegate secret as RAW BYTES -- `$(...)` strips the
trailing newline the Secret actually stores, which fails as `invalid_client`:

```sh
SEC=$(kubectl -n mecatl-vmcp get secret vmcp-delegate-auth \
       -o jsonpath='{.data.client-secret}' | base64 -d | jq -sRr @uri)
curl -s -X POST http://127.0.0.1:18080/oauth/token \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  --data-binary "grant_type=urn:ietf:params:oauth:grant-type:token-exchange\
&subject_token=$T1&subject_token_type=urn:ietf:params:oauth:token-type:access_token\
&scope=mcp%3Aread&resource=http%3A%2F%2F127.0.0.1%3A18080%2Fmcp\
&client_id=mecak8s-delegate&client_secret=$SEC"
```

The result is the delegated token: `act.sub=mecak8s-delegate`,
`act.act.sub=mecatui-kind`, `scp=[mcp:read]`, audience bound to the vMCP
resource, expiry clamped to the subject token's.

The upstream browser leg (`upstreamProviders`) is configured because the CRD
requires at least one, but nothing in this flow exercises it.
