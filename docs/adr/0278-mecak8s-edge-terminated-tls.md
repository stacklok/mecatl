# ADR 0278 — mecak8s edge-terminated TLS

- Status: Accepted
- Date: 2026-09-01
- Scope: mecak8s Helm real-provider transport gate
- Supersedes: [ADR 0240](./0240-mecak8s-credential-reload-and-chart-security.md) (provider-security gate scope only)

## Context

ADR 0240 required in-pod server TLS plus OIDC for every secure real-provider Helm
release. Kubernetes deployments commonly terminate public TLS at an operator-managed
gateway, however, and forcing a second certificate into the pod can complicate that
boundary without improving caller authentication.

## Decision

The chart has three explicit postures:

1. **In-pod TLS:** secure real-provider releases use `tls.enabled=true` and
   `oidc.enabled=true`.
2. **Edge-terminated TLS:** secure real-provider releases may use
   `security.tlsTerminatedUpstream=true`, `tls.enabled=false`,
   `oidc.enabled=true`, and `service.type=ClusterIP`. The backend is plaintext h2c.
   The operator owns gateway TLS, restricts backend reachability to the gateway or
   mesh, forwards the original `Authorization: Bearer` token, and must not replace it
   with forwarded-identity authentication.
3. **Unsafe bypass:** `security.allowUnsafeRealProvider=true` remains the explicit
   local/trusted-mesh bypass.

The chart stamps every secure real-provider upstream assertion with
`mecatl.stacklok.com/tls-terminated-upstream: "true"`, including valid in-pod TLS
re-encryption; it stamps the bypass with
`mecatl.stacklok.com/unsafe-real-provider: "true"`. These chart-owned annotations
cannot be overridden through `podAnnotations`. The upstream value and annotation are
attestations, not chart enforcement of the external boundary.

The chart deliberately creates no Gateway, Route, Certificate, or general
NetworkPolicy. The operator configures the chosen gateway and network isolation,
uses a `GRPCRoute` only, and must not public-route `/drain`, `/healthz`, or
`/readyz`. A Gateway API `BackendTLSPolicy` is the appropriate operator-owned
mechanism where gateway-to-pod re-encryption is required.

Changing an existing pod-TLS release to h2c changes its backend protocol. Use a
blue-green deployment or a maintenance cutover; do not assume a rolling update is
safe across that protocol boundary.

## Consequences

Secure real-provider deployments retain OIDC caller authentication while allowing a
clear, auditable TLS termination boundary. The chart cannot verify bearer forwarding,
gateway-only reachability, or gateway configuration, so those remain operator
responsibilities.

Edge mode is genuinely weaker than in-pod TLS, and the specific loss is worth naming:
on an h2c backend the caller's `Authorization: Bearer` token crosses the pod network in
cleartext. Any workload that can reach the Service ClusterIP can read it and then replay
it as that caller. The chart ships no NetworkPolicy, so by default every pod in the
cluster can reach it. Restricting that reachability — a NetworkPolicy admitting only the
gateway's pods, or an mTLS mesh — is therefore the load-bearing control in this posture,
not an optional hardening step. Where the pod network is not trusted for bearer tokens,
use in-pod TLS or a `BackendTLSPolicy`.

## See also

- [ADR 0240](./0240-mecak8s-credential-reload-and-chart-security.md)
- [ADR 0048](./0048-mecak8s.md)
- [Deployment hardening](../architecture/deployment-and-hardening.md)
