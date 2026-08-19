# ToolHive-free local Kind profile

This directory owns the disposable **Phase-A** mecak8s qualification fixture. It
installs the local Helm chart with `deploy/helm/mecak8s/values-kind.yaml`, one
plaintext in-chart Redis fixture, and a locally loaded `ko.local/mecak8s:dev`
image. It does **not** install ToolHive, create vMCP resources, resolve a
release, or contact GitHub.

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
Production chart installs require a digest-pinned image, externally managed
Redis endpoint/CIDR, and a Secret reference for authenticated TLS Redis.
