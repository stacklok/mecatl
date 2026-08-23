# Disposable Kind mecak8s + ToolHive vMCP integration fixture

This integration fixture exercises an OAuth-protected ToolHive vMCP endpoint in a disposable
Kind cluster. It deploys the local mecak8s chart, Dex, ToolHive operator, a
dedicated ToolHive Redis, Yardstick, and one `VirtualMCPServer`. Mecak8s is
present, but mecak8s is not in the outbound vMCP path: this does not implement a
credential broker, SPIFFE, sidecars, or mecatui remote login.

## Pinned runtimes

- ToolHive `v0.44.0`, commit `b3df9689bdb7d55d0765565890ba9dc0c076dec0`
- Yardstick `v1.1.1`, commit `e5b8908ed6f53c1171ac805d82cf858d2982fa19e`,
  image `ghcr.io/stackloklabs/yardstick/yardstick-server:1.1.1`

`versions.yaml` records the pins. `task mecak8s:vmcp-status` captures the
actual Pod image IDs; the mecatl ToolHive Go-module version is not runtime proof.

## Scope

```text
browser PKCE client -> loopback vMCP + embedded AS -> Yardstick /mcp
                                  |
                                  +-> Dex Alice or Bob
```

OAuth protects the vMCP resource. Yardstick is an internal deterministic,
read-only backend and receives no Dex credential. This proves ToolHive OAuth
sessions and fail-closed protected-resource handling, not external provider
grants or mecak8s outbound brokerage.

### Local-only Dex logins

The disposable fixture deliberately uses public test credentials so an operator
can complete the browser journeys without recovering a password hash:

| User | Password |
| --- | --- |
| `alice@example.com` | `Secret123` |
| `bob@example.com` | `Secret123` |

They are valid only for the in-memory Dex deployment created by this fixture;
they are not Kubernetes Secret values, provider credentials, or reusable user
passwords.

## Lifecycle

```sh
task mecak8s:vmcp-check
task mecak8s:vmcp-setup
task mecak8s:vmcp-status
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
| `https://dex.mecatl-vmcp.svc.cluster.local:5556` | `30556` | Dex |
| `https://mecak8s-mecak8s.mecatl-vmcp.svc.cluster.local:18081` | `30081` | mecak8s gRPC/HTTP |

The Dex and mecak8s host mappings need no `kubectl port-forward`. The vMCP
Service remains ClusterIP and still needs a loopback-only port-forward for the
browser resource endpoint.

For a temporary host journey, inspect the exact aliases, add them, and remove
them when finished. These tasks require `sudo`, never modify `/etc/hosts`
silently, and removal creates `/etc/hosts.bak`:

```sh
task mecak8s:kind-hosts-show
task mecak8s:kind-hosts-add
# use the browser/client journey
task mecak8s:kind-hosts-remove
```

The exact entries managed by these tasks are:

```text
127.0.0.1 dex.mecatl-vmcp.svc.cluster.local
127.0.0.1 mecak8s-mecak8s.mecatl-vmcp.svc.cluster.local
```
## mecak8s TLS and caller identity

The Kind chart mounts the cert-manager-issued `mecak8s-tls` Secret as exact,
read-only `tls.crt` and `tls.key` items and starts both gRPC and HTTP with the
paired TLS flags. Its `/readyz`, `/healthz`, and `/drain` management endpoints
remain explicit and are probed over HTTPS; they are not authenticated API
requests. The chart mounts the public `tls.crt` item from `dex-fixture-ca` and
passes its exact path through `--oidc-ca-cert-file`; it does not set the
process-wide `SSL_CERT_FILE`. The Kind profile enables
`--oidc-allow-private-https-issuer` only for the in-cluster Dex Service. The
scoped OIDC transport admits only the configured Dex host and its pinned private
addresses; HTTPS, CA and hostname validation, redirect refusal, and per-dial
DNS-pinned checks remain enforced. It validates the HTTPS Dex issuer and
`mecatui-kind` audience without the deprecated combined
HTTP/private `--oidc-insecure-allow-private-issuer` escape hatch.

For a host-only TLS check, use the loopback-only mecak8s NodePort and export
the public fixture CA to `.scratch/` (the CA is public, but do not export or
print any private-key Secret item):

```sh
task mecak8s:kind-hosts-add
# connect to mecak8s-mecak8s.mecatl-vmcp.svc.cluster.local:18081
kubectl --kubeconfig=deploy/mecak8s-vmcp/kconfig.yaml --context=kind-mecatl-dev \
  --namespace=mecatl-vmcp get secret dex-fixture-ca -o jsonpath='{.data.tls\.crt}' | base64 --decode > .scratch/mecak8s-vmcp-ca.crt
```

A normal-terminal `mecatui connect` flow with custom-CA TLS and OIDC PKCE is
not implemented in this fixture, so the live client connection demonstration
is deferred. When that client support lands, it must use this loopback mapping,
the exported fixture CA, and a Dex-issued token; plaintext and an untrusted CA
must fail before an authenticated RPC is served. No token or credential is
stored by this fixture.

## Separate Dex public clients

`vmcp-browser` remains the embedded-vMCP browser client and uses
`http://127.0.0.1:18080/oauth/callback`. `mecatui-kind` is a separate public
client reserved for a normal-terminal loopback PKCE journey at
`http://127.0.0.1:18473/oauth/callback`. Neither client has a secret. The
fixture does not implement mecatui login, device flow, headless login, or
credential storage.

## Shared Dex HTTPS issuer

`kind-setup` installs the pinned cert-manager chart before it requests the
fixture-local CA and the `dex-tls` Certificate. cert-manager generates the CA
and leaf Secret values in the cluster; this repository contains neither a
private key nor a certificate value.

The sole shared issuer is
`https://dex.mecatl-vmcp.svc.cluster.local:5556`. Dex publishes discovery at
`https://dex.mecatl-vmcp.svc.cluster.local:5556/.well-known/openid-configuration`
and JWKS at `https://dex.mecatl-vmcp.svc.cluster.local:5556/keys`; tokens issued
there use that HTTPS URL as `iss`.

Pods use that Service-DNS name directly. For temporary host access, run
`task mecak8s:kind-hosts-add`; remove the exact aliases with
`task mecak8s:kind-hosts-remove` when finished. The host and pod therefore use
the same HTTPS issuer URL, with the Kind mapping bound to loopback only. The `dex-tls` certificate covers the Service DNS name, `localhost`, and `127.0.0.1`.
Mecak8s trusts the fixture CA through its direct `--oidc-ca-cert-file` flag and
admits only this private HTTPS Service issuer; it does not use the deprecated
HTTP/private issuer escape hatch; this is without OIDC insecure relaxation.

| Endpoint | Role |
| --- | --- |
| `https://dex.mecatl-vmcp.svc.cluster.local:5556` | Shared HTTPS Dex issuer for pod OIDC and host alias access. |
| `http://127.0.0.1:18080` | Separate loopback vMCP embedded authorization-server and browser callback baseline; it is not the Dex issuer. |

The fixture deliberately installs no `NetworkPolicy`: the prior Dex-only egress
policy blocked DNS and Redis, so it could not support the storage-free mecak8s
runtime. NetworkPolicy design and enforcement evidence are out of scope for this
local qualification; the default Kind CNI is not an enforcement proof.
