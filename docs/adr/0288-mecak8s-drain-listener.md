# ADR 0288 — Separate the mecak8s drain listener from API traffic

- Status: Accepted
- Date: 2026-09-02
- Scope: `cmd/mecak8s` listener topology and its Helm deployment
- Supersedes: —
- Superseded by: —

## Context

The Kubernetes preStop hook needs an unauthenticated `GET /drain` endpoint so it
can reject new work and allow endpoint removal before SIGTERM. Previously that
endpoint shared the normal HTTP/SSE listener with the authenticated API. Any
Service or gateway that reached the API listener could therefore also trigger a
credential-free lifecycle operation.

## Decision

Bind a separate plaintext drain-only listener at `0.0.0.0:8082` by default,
configurable with `--drain-addr`. It serves only `GET /drain`; the normal
HTTP/SSE listener serves health/readiness outside authentication and the API
behind authentication, but never `/drain`. The Helm chart exposes the drain
port only on the Pod and directs its preStop HTTP request to that named port;
the Service continues to expose only gRPC and HTTP.

## Consequences

Normal Service and gateway API traffic cannot invoke the drain endpoint,
including when the API listener uses TLS. The lifecycle endpoint remains
plaintext because kubelet preStop calls it directly and it carries no
credentials.

A direct Pod-IP connection to port 8082 can still drain a Pod. Network isolation
for that port is an operator-enforced residual: the chart deliberately does not
claim that its omitted Service port is a network boundary. Operators must use a
NetworkPolicy, mesh policy, or equivalent controls to restrict Pod-IP access.

## See also

- [Architecture](../architecture.md)
- [Deployment and hardening](../architecture/deployment-and-hardening.md)
- [mecak8s usage](../usage/mecak8s.md)
- [Cloud-native resource inventory](./0027-cloud-native.md)
- [ADR 0048](./0048-mecak8s.md)
- [ADR 0002](./0002-documentation-lifecycle.md)
