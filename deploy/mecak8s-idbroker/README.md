# Local mecak8s identity broker + hosted AS-1 Kind profile

This is an **operator-run**, disposable Kind profile for developing the
identity broker. One command creates the `mecatl-idbroker` cluster with
`mecak8s` hosting AS-1 (the identity broker's sign-in server), Dex as AS-1's
upstream IdP, cert-manager with a fixture CA, and local Redis. It is not the
production chart profile and not the `e2e/k8s/` proof; the hosted AS-1 e2e
journey uses the same Dex shape.

It uses its own cluster, namespace (`mecatl-idbroker`), kubeconfig, state
(`.scratch/kind/mecatl-idbroker`), image tag (`ko.local/mecak8s:idbroker`), and
host ports, so it runs alongside the `mecatl-dev` fixtures. The ambient
kubeconfig is never modified.

## Prerequisites

`kind`, `kubectl`, `helm`, `ko`, `openssl`, `nc`, and Docker (or Podman with
`KIND_EXPERIMENTAL_PROVIDER=podman`). Host ports `127.0.0.1:18090`, `18091`,
and `5557` must be free. Setup pulls cert-manager and Dex images.

## Use it

```sh
task mecak8s-idbroker:kind-setup   # recreate the cluster; prints these next steps
task mecak8s-idbroker:hosts-add    # once; sudo; loopback aliases in /etc/hosts
task build                         # ./bin/mecatui
task mecak8s-idbroker:login
task mecak8s-idbroker:connect
```

`login` and `connect` run `hosts-add` (no `sudo` prompt when the aliases are
present) and re-export the current fixture CA to
`.scratch/kind/mecatl-idbroker/fixture-ca.crt` first; every `kind-setup` mints a
new CA. `login` runs:

```sh
./bin/mecatui login mecak8s-mecak8s.mecatl-idbroker.svc.cluster.local:18090 \
  --issuer https://mecak8s-mecak8s.mecatl-idbroker.svc.cluster.local:18091/as1 \
  --client-id mecatui \
  --audience https://mecak8s-mecak8s.mecatl-idbroker.svc.cluster.local:18091/api \
  --tls-ca .scratch/kind/mecatl-idbroker/fixture-ca.crt --private-issuer \
  --scopes openid,profile,offline_access
```

The browser opens AS-1's consent page, then Dex's login form. Both are served
with the fixture CA, which the browser does not trust: accept the warning or
trust that CA file. Sign in as the dev-only Dex user `dev@example.com` with
password `password`. It exists only in this Dex's in-memory configuration.
Sessions are owned by the Dex identity in the access token's `sub_id`.

Export `OPENROUTER_API_KEY` before `kind-setup` for a real provider; without
it, `mecak8s` runs the offline mock provider. The key reaches the cluster only
as the `mecak8s-openrouter` Secret, from standard input.

## Names and ports

`hosts-add` aliases two names to `127.0.0.1`, marked `# mecatl-idbroker`;
`hosts-remove` deletes only those lines (backup `/etc/hosts.bak`) and
`hosts-status` shows them. The names are the in-cluster Service names, and each
Service port equals its host port, so the issuer URLs are the same for the
browser, `mecatui`, and AS-1 inside the cluster:

| URL | NodePort | Service |
| --- | --- | --- |
| `mecak8s-mecak8s.mecatl-idbroker.svc.cluster.local:18090` | `30090` | `mecak8s` gRPC |
| `https://mecak8s-mecak8s.mecatl-idbroker.svc.cluster.local:18091` | `30091` | `mecak8s` HTTPS; AS-1 under `/as1` |
| `https://dex.mecatl-idbroker.svc.cluster.local:5557` | `30557` | Dex |

Do not use `localhost` as the `mecatui` target: `mecatui` then sends its own
working directory as the session workspace, which this deployment refuses.

Values: `deploy/helm/mecak8s/values-kind.yaml` plus
`deploy/helm/mecak8s/values-kind-idbroker.yaml`. Setup generates the
`as1-signing-key` (EC P-256) and `dex-as1-client` Secrets, keeping existing ones;
cert-manager issues `fixture-ca`, `mecak8s-tls`, and `dex-tls`.

## Poke it

Without the aliases, pin the names with `curl --resolve`:

```sh
ca=.scratch/kind/mecatl-idbroker/fixture-ca.crt
h=mecak8s-mecak8s.mecatl-idbroker.svc.cluster.local
curl --resolve $h:18091:127.0.0.1 --cacert $ca https://$h:18091/.well-known/oauth-protected-resource
curl --resolve $h:18091:127.0.0.1 --cacert $ca https://$h:18091/as1/.well-known/openid-configuration
d=dex.mecatl-idbroker.svc.cluster.local
curl --resolve $d:5557:127.0.0.1 --cacert $ca https://$d:5557/.well-known/openid-configuration
k='kubectl --kubeconfig=.scratch/kind/mecatl-idbroker/kconfig.yaml --context=kind-mecatl-idbroker -n mecatl-idbroker'
$k logs deployment/mecak8s-mecak8s
$k logs deployment/dex
task mecak8s-idbroker:status
```

## Iterate and restart

`task mecak8s-idbroker:image-refresh` rebuilds the image from the current tree,
loads it, and restarts `mecak8s` without resetting the cluster. AS-1 keeps its
users, sign-ins, and refresh tokens in memory, so any restart signs everyone out
of AS-1: run `login` again. AS-1's signing key is in the `as1-signing-key`
Secret, so access tokens issued before the restart keep verifying until they
expire. Sessions persist in Redis.

`task mecak8s-idbroker:destroy` deletes only the `mecatl-idbroker` cluster and
its state. Remove the aliases separately with `hosts-remove`. A failed
`kind-setup` deletes the half-built cluster unless `MECAK8S_KEEP_ON_FAILURE=1`.
