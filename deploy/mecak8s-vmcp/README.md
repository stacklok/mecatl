# Historical mecak8s ToolHive vMCP material

The manifests and pinned runtime notes in this directory are historical
reference material for the ToolHive vMCP qualification work. They are not an
operator lifecycle and do not own the local mecak8s Kind fixture.

For the supported ToolHive-free, operator-run Kind baseline, use
[`deploy/mecak8s-kind/`](../mecak8s-kind/README.md):

```sh
task mecak8s:kind-setup
task mecak8s:kind-status
task mecak8s:kind-port-forward
task mecak8s:kind-destroy
```

The local baseline installs only the mecak8s chart's `values-kind.yaml` profile
and local Redis. Its explicit loopback port-forward is the only host-access
path. It does not install this directory's optional integration components.

The supported optional identity journey belongs to
[`deploy/mecak8s-kind/`](../mecak8s-kind/README.md), not this historical vMCP
material: use Authorization Code + PKCE through the public Keycloak client.
A password grant, if used in fixture validation, is a test helper and never the
normal client flow.

The vMCP delegation design and qualification record remains in
[`docs/design/mecak8s-vmcp-delegation-contract.md`](../../docs/design/mecak8s-vmcp-delegation-contract.md).
