---
id: 01-kind-base
title: "ToolHive-free Kind fixture boundary"
blocked_by: []
status: done
branch: "plan-mecak8s-kind-fixture/01-kind-base"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/mecak8s-kind-fixture
---

# ToolHive-free Kind fixture boundary

Move the operator-run baseline lifecycle and its documentation from
`deploy/mecak8s-vmcp/` into `deploy/mecak8s-kind/`. Establish canonical
`mecak8s:kind-setup`, `kind-status`, `kind-destroy`, and explicit loopback
port-forwarding targets. The base must install only Kind, namespace, local
mecak8s image/chart, and local Redis—never Keycloak, cert-manager, ToolHive,
Yardstick, or vMCP. Keep `deploy/helm/mecak8s/values-kind.yaml` and the
`e2e/k8s/` topology byte-compatible in semantics.

## Acceptance criteria

- AC1.1: `mecak8s:kind-setup` is independently idempotent and creates the named
  Kind fixture with mecak8s and local Redis, but does not invoke or transitively
  depend on cert-manager, Keycloak, ToolHive, Yardstick, or any vMCP resource.
  - verify: `TestMecak8sKindFixture_Scenario1_ToolHiveFreeSetup`
- AC1.2: Setup, status, explicit loopback-only port forwarding, and destroy use
  the fixture's dedicated kubeconfig/context; destroy removes the named cluster,
  generated fixture kubeconfig, and local state without relying on the ambient
  kubeconfig.
  - verify: `TestMecak8sKindFixture_Scenario1_DedicatedKubeconfig`
- AC1.3: `values-kind.yaml` remains the exact e2e-safe profile: two replicas,
  `mockProvider: true`, local plaintext Redis at `redis:6379`, workspace `/tmp`,
  a ClusterIP Service, and no OIDC/TLS flags or Secret references. The
  `e2e/k8s/` suite continues to install that profile directly rather than any
  operator-fixture overlay.
  - verify: `TestMecak8sHelmChart_KindProfileAloneHasNoSecretDependency`
- AC1.4: The local fixture documentation distinguishes the operator-run Kind
  fixture from the production Helm chart and the `e2e/k8s/` suite, and does not
  claim production network isolation: it has no general NetworkPolicy and uses
  explicit loopback-only forwarding for host access.
  - verify: none — fixture boundary claims in documentation are reviewed by humans; `task docs` checks links and structure
