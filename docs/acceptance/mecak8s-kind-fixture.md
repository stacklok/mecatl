# Local mecak8s Kind fixture — acceptance plan

**Phase:** local deployment fixture boundary
**Status:** in-progress, 2026-08-27. Synthesised from the mecak8s-vMCP preflight discussion.
**Accumulator branch:** `acc/mecak8s-kind-fixture` (off `main`).

The smallest set of work that makes the disposable local Kind deployment useful
without ToolHive or vMCP: mecak8s runs with its local Redis fixture and either
the canned mock provider or an explicitly supplied OpenRouter credential.
Keycloak is an independently enabled local caller-identity layer. The existing
vMCP delegation qualification remains a separate optional extension.

The document is organized scenario-first because acceptance is about what the
running deployment demonstrates, not which manifests happen to exist.

## Why these scope cuts

- [ADR-0048](../adr/0048-mecak8s.md) makes mecak8s a storage-free Kubernetes
  composition root backed by Redis and Kubernetes Leases; this plan changes no
  engine, port, or persistence behaviour.
- [ADR-0204](../adr/0204-caller-identity-threading.md) establishes optional
  verified caller identity and ownership isolation. Keycloak remains optional
  because enabling it is an identity cutover, not a prerequisite for the local
  mock demonstration.
- `e2e/k8s/` remains the automated cloud-native product proof. Its
  `values-kind.yaml` contract must remain independent of the operator-run
  fixture's Keycloak TLS and Secret material.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — ToolHive-free local mecak8s deployment

An operator starts a named disposable Kind cluster through
`mecak8s:kind-setup`. It builds and loads the local mecak8s image, deploys the
local Redis-backed chart profile, and can inspect or delete that fixture without
using the ambient kubeconfig. The fixture lives at `deploy/mecak8s-kind/`, while
`deploy/helm/mecak8s/` remains the product chart and `e2e/k8s/` remains the
automated deployment proof. This preserves ADR 0048's Redis-and-Lease topology
without introducing ToolHive as a dependency ([ADR-0048](../adr/0048-mecak8s.md)).

**Acceptance:**
- AC1.1: `mecak8s:kind-setup` is independently idempotent and creates the named
  Kind fixture with mecak8s and local Redis, but does not invoke or transitively
  depend on cert-manager, Keycloak, ToolHive, Yardstick, or any vMCP resource.
  - verify: `TestMecak8sKindFixture_Scenario1_ToolHiveFreeSetup`
- AC1.2: Setup, status, direct static loopback mappings, and destroy use
  the fixture's dedicated kubeconfig/context; destroy removes the named cluster,
  generated fixture kubeconfig, and local state without relying on the ambient
  kubeconfig.
  - verify: `TestMecak8sKindFixture_Scenario1_DedicatedKubeconfig`
- AC1.3: `values-kind.yaml` remains the exact e2e-safe profile: two replicas,
  `mockProvider: true`, local plaintext Redis at `redis:6379`, workspace `/tmp`,
  a ClusterIP Service, and no NodePort overlay or OIDC/TLS flags or Secret references. The
  `e2e/k8s/` suite continues to install that profile directly rather than any
  operator-fixture overlay.
  - verify: `TestMecak8sHelmChart_KindProfileAloneHasNoSecretDependency`
- AC1.4: The local fixture documentation distinguishes the operator-run Kind
  fixture from the production Helm chart and the `e2e/k8s/` suite, and does not
  claim production network isolation: it has no general NetworkPolicy and uses
  static Kind `extraPortMappings` for host access, all bound to loopback.
  - verify: `TestMecak8sKindFixture_Scenario1_DocumentationBoundaries`

---

### Scenario 2 — Explicit mock and real-provider modes

The base fixture is offline and cost-free by default. When an operator supplies
`OPENROUTER_API_KEY`, the fixture creates or updates a Kubernetes Secret without
placing the credential in process arguments, projects that Secret through the
chart's `extraEnv`, and disables the mock provider. Fixture setup, render tests,
and `task test` never make a provider call. This does not change the separately
opt-in `e2e:k8s` live variant. The fixture remains an ADR 0048 disposable
Redis-backed deployment, rather than a new provider runtime
([ADR-0048](../adr/0048-mecak8s.md)). The chart's raw `EnvVar` projection is the
existing deployment seam; credential values are never chart values or command
arguments ([`user-docs/building/deployment/mecak8s.md`](https://mecatl.dev/docs/building/deployment/mecak8s)).

**Acceptance:**
- AC2.1: Without `OPENROUTER_API_KEY`, `mecak8s:kind-setup` renders the canned
  mock provider and makes no provider network request.
  - verify: `TestMecak8sKindFixture_Scenario2_MockDefault`
- AC2.2: With `OPENROUTER_API_KEY`, the rendered deployment omits `--mock` and
  includes exactly one `OPENROUTER_API_KEY` environment projection from the
  fixture-owned Secret.
  - verify: `TestMecak8sHelmChart_KindFixtureRealProviderDisablesMock`
- AC2.3: The fixture passes an OpenRouter credential to `kubectl` via standard
  input and `--from-file`; no fixture command, rendered manifest, pod argument,
  or diagnostic path prints the value or uses
  `--from-literal=OPENROUTER_API_KEY`.
  - verify: `TestInvariant_credential_not_process_argument`
- AC2.4: Switching the fixture from real-provider mode back to mock mode stops
  the Secret projection and deletes the fixture-owned provider Secret; a stale
  credential is never mounted by the mock deployment.
  - verify: `TestMecak8sKindFixture_Scenario2_ResetToMock`
- AC2.5: A real-provider smoke call is neither a setup action nor a default-test
  side effect; fixture instructions name it as a separate, explicit billable
  operator action.
  - verify: `TestMecak8sKindFixture_Scenario2_LiveSmokeIsExplicit`

---

### Scenario 3 — Optional Keycloak caller identity for mecak8s

An operator can layer Keycloak onto an already-running base fixture with
`mecak8s:kind-keycloak-apply`, or choose `mecak8s:kind-keycloak-setup` for the
combined path. `kind-keycloak-apply` is independently idempotent: it installs
cert-manager, issues the fixture CA and leaf certificates, starts Keycloak, and
upgrades the Helm release to its OIDC/TLS overlay. The public `mecatui-kind`
client uses authorization-code + PKCE S256 and requests optional
`mecak8s:access`; that scope's access-token-only audience mapper adds
`mecak8s`. Mecak8s is the resource server, not a Keycloak OAuth client. The
TLS/issuer overlay configures mecak8s to accept that precise audience, not the
optional vMCP resource URL. This follows the chart's optional OIDC
caller-identity cutover ([ADR-0204](../adr/0204-caller-identity-threading.md))
and the bounded signing-key availability policy
([ADR-0205](../adr/0205-bounded-jwks-staleness.md)).

**Acceptance:**
- AC3.1: `mecak8s:kind-setup` neither invokes nor transitively depends on
  cert-manager, certificates, Keycloak, or OIDC/TLS Helm flags; the separately
  idempotent `kind-keycloak-apply` and composed `kind-keycloak-setup` own those
  effects, and a failed apply does not report a ready authenticated fixture.
  - verify: `TestMecak8sKindFixture_Scenario3_KeycloakIsOptIn`
- AC3.2: The Keycloak overlay projects exact TLS and OIDC-CA Secret keys and
  configures the exact HTTPS issuer and `oidc.audience: mecak8s`. It uses
  `--oidc-ca-cert-file` and the disposable-only
  `--oidc-allow-private-https-issuer`, never `SSL_CERT_FILE` or the deprecated
  HTTP/private-issuer relaxation.
  - verify: `TestMecak8sKindFixture_Scenario3_KeycloakOIDCOverlay`
- AC3.3: `mecatui-kind` remains a public authorization-code + PKCE S256 client
  with no client secret, implicit flow, direct grants, or service accounts. Its
  optional `mecak8s:access` scope adds `mecak8s` only to the requested access
  token's audience, never to an ID token or an access token that omits the
  scope.
  - verify: `TestMecak8sKindFixture_Scenario3_ResourceAudience`
- AC3.4: A local client reaches the authenticated mecak8s service through the static
  loopback-only Kind mappings and certificate-covered hostname; the fixture
  NodePort overlay is separate from the shared ClusterIP values and there is no
  externally reachable LoadBalancer, Ingress, or wildcard host binding.
  - verify: `TestMecak8sKindFixture_Scenario3_LoopbackReachability`
- AC3.5: mecak8s rejects an absent, forged, wrong-issuer, wrong-audience,
  wrong-hostname, or untrusted-CA bearer request before authenticated API
  handling, and accepts a valid Keycloak access token whose audience contains
  `mecak8s`.
  - verify: `TestMecak8sKindFixture_Scenario3_AuthenticatedRequest`
- AC3.6: An OIDC-enabled deployment fails closed when its initial issuer/JWKS
  fetch is unavailable; after a successful fetch, an outage beyond the
  configured JWKS staleness bound yields retryable 503 rather than an
  unauthenticated fallback or a 401 token classification.
  - verify: `TestADR_0205_InitialJWKSOutagePreventsValidatorStartup`
    (`go test ./authn/oidc -run '^TestADR_0205_InitialJWKSOutagePreventsValidatorStartup$'`;
    `authn/oidc/fixture_test.go`),
    `TestCallerIdentity_Scenario1_JWKSDownIsTransientNotUnauthorized`
    (`go test ./internal/adapter/server -run '^TestCallerIdentity_Scenario1_JWKSDownIsTransientNotUnauthorized$'`;
    `internal/adapter/server/caller_identity_test.go`)
- AC3.7: Fixture instructions document authorization-code + PKCE as the normal
  login journey; any fixture password grant is identified as a narrowly scoped
  test helper and is not presented as the normal client flow.
  - verify: `TestMecak8sKindFixture_Scenario3_LoginDocumentation`

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| ToolHive operator, vMCP, Yardstick, RFC 8693 exchange, and their CA workaround | Follow-up vMCP extension plan after the ToolHive worktree fix is independently validated | Existing [`mecak8s-vmcp` delegation contract](../design/mecak8s-vmcp-delegation-contract.md) |
| A live OpenRouter smoke request | Explicit operator-invoked task only | No automatic provider spending |
| mecakui remote login or credential storage | Existing remote-login work | mecak8s remains a headless server |
| Scope-based operation authorization | Separate authorization capability | ADR 0204 supplies identity/ownership, not authority |
| Changes to `e2e/k8s/` topology or its Dex proof | Existing product e2e | ADR 0048 |

## Cross-cutting deliverables

- Move baseline Kind assets and documentation from `deploy/mecak8s-vmcp/` to
  `deploy/mecak8s-kind/`; leave vMCP-specific manifests and the historical
  delegation report under their existing names.
- Remove the old `mecak8s:vmcp-*` ownership of baseline lifecycle targets: this
  is a clean break, with no compatibility aliases.
- Update `user-docs/building/deployment/mecak8s.md`, `deploy/README.md`, and
  `user-docs/building/deployment/mecak8s.md` with links and scope distinctions;
  regenerate the configuration reference through `task docs`.
- Keep all new code/configuration tests offline. A Kind journey is an opt-in
  deployment check, not part of `task test`.

## Sequencing recommendation

First establish the renamed ToolHive-free fixture and pin that the existing
Kind e2e overlay remains unchanged. Next make the mock/real-provider selection
truthful in the fixture. Add Keycloak only after its own overlay and realm have
an independent base fixture. Then update all documentation references in one
pass. The eventual vMCP extension should consume the base fixture but is not
part of this accumulator.

## Named tests landing in this plan

- `TestMecak8sKindFixture_Scenario1_ToolHiveFreeSetup`
- `TestMecak8sKindFixture_Scenario1_DedicatedKubeconfig`
- `TestMecak8sKindFixture_Scenario1_DocumentationBoundaries`
- `TestMecak8sKindFixture_Scenario2_MockDefault`
- `TestMecak8sHelmChart_KindFixtureRealProviderDisablesMock`
- `TestInvariant_credential_not_process_argument`
- `TestMecak8sKindFixture_Scenario2_ResetToMock`
- `TestMecak8sKindFixture_Scenario2_LiveSmokeIsExplicit`
- `TestMecak8sKindFixture_Scenario3_KeycloakIsOptIn`
- `TestMecak8sKindFixture_Scenario3_KeycloakOIDCOverlay`
- `TestMecak8sKindFixture_Scenario3_ResourceAudience`
- `TestMecak8sKindFixture_Scenario3_LoopbackReachability`
- `TestMecak8sKindFixture_Scenario3_AuthenticatedRequest`
- `TestADR_0205_InitialJWKSOutagePreventsValidatorStartup`
- `TestCallerIdentity_Scenario1_JWKSDownIsTransientNotUnauthorized`
- `TestMecak8sKindFixture_Scenario3_LoginDocumentation`

## Definition of done

1. `task lint` and `task test` pass (both modules, `-race`).
2. `task docs` regenerates the configuration reference and passes the matlatl strict link gate;
   `task site:build` validates the required user-facing deployment documentation.
3. `task api:check` passes without an API update; this plan changes no engine
   exported surface.
4. `task ac-trace-strict` resolves every acceptance proof when the plan becomes
   `landed`.
5. The named tests and the existing
   `TestMecak8sHelmChart_KindProfileAloneHasNoSecretDependency` pass.
6. An explicit local Kind demonstration proves the mock base, the
   Secret-backed real-provider render, and the Keycloak audience acceptance and
   rejection cases without recording credentials.
7. `go run ./cmd/mecademo` still prints a full offline session.

## Deferred decisions and known risks

- **ToolHive CA configuration.** The current `SSL_CERT_FILE` workaround and the
  external ToolHive worktree fix are deliberately not changed here; validate
  the upstream fix before a vMCP extension consumes it.
- **Local Keycloak authentication client.** The fixture may retain a
  password-grant-only test helper, but the normal terminal/browser PKCE journey
  remains dependent on the remote-login work.
- **Local service exposure.** The fixture must remain loopback-only. A broader
  ingress, NodePort, or TLS-edge design is not implied by this convenience
  fixture.

## Exit criteria

When every point under *Definition of done* holds on the accumulator, this plan
is satisfied.
