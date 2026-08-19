# ToolHive-free local Kind mecak8s + Dex profile

This directory owns the disposable **Phase-A** mecak8s qualification fixture. It
installs the local Helm chart with `deploy/helm/mecak8s/values-kind.yaml`, one
plaintext in-chart Redis fixture, a locally loaded `ko.local/mecak8s:dev` image,
and an in-cluster Dex identity provider. It does **not** install ToolHive, create
vMCP resources, or resolve a ToolHive release.

```sh
task mecak8s:kind-setup
task mecak8s:kind-status
task mecak8s:kind-destroy
```

The `mecak8s` task namespace manages the named `mecatl-dev` cluster. Setup
recreates that named cluster and its local state; status uses only the dedicated
`.scratch/kind/mecatl-dev/kubeconfig` and `kind-mecatl-dev` context, never the
ambient kubeconfig.

The Kind values file is fixture-only: it permits a loaded `ko.local` image and
unauthenticated Redis solely because the cluster and its state are disposable.
Production chart installs require a signed release tag or digest, an externally managed
Redis endpoint/CIDR, and a Secret reference for authenticated TLS Redis.

## Local identities

`task mecak8s:kind-setup` also deploys Dex with in-memory state. The fixture has
two disposable users, `alice@example.com` and `bob@example.com`; both use the
password `password`. Dex is reachable in-cluster at
`http://dex.mecatl-vmcp.svc.cluster.local:5556` and is prepared for mecak8s to
validate its tokens once the OIDC settings are enabled in the following vMCP
integration patch.

For local inspection, port-forward the service using the fixture's dedicated
kubeconfig:

```sh
kubectl --kubeconfig=.scratch/kind/mecatl-dev/kubeconfig \
  --context=kind-mecatl-dev --namespace=mecatl-vmcp \
  port-forward service/dex 5556:5556
```

These credentials and the Dex client secret are intentionally public,
fixture-only values. They must not be reused in a deployed environment.
