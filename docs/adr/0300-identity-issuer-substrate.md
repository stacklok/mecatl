# ADR 0300 — Identity issuer substrate for the combined broker

- Status: Proposed
- Date: 2026-08-31
- Scope: I1 trust-domain, signing-key, JWT-SVID, bundle, verification, rotation, and broker-host custody substrate
- Supersedes: none
- Superseded by: none

## Context

Mecatl derives local authority for child sessions, but a downstream broker cannot verify that a subagent is the actor or that its authority was attenuated. The future combined identity + MCP broker needs a signing and verification substrate without putting signing material in the model-driven agent process.

The distributed-broker B0 contract reserves the combined broker Deployment for Redis-fenced broker correctness state, ToolHive OAuth state, route generations, and later credential custody. I1 must therefore not create a parallel broker platform. It must also leave the in-flight Stage 3 authorization interruption and continuation contract unchanged.

## Decision

I1 provides root-internal ES256 JWT-SVID issuer, bundle, and verifier modules to the future combined identity + MCP broker.

One explicit, effectively immutable SPIFFE trust domain is configured per deployment. I1 loads a strict manifest and PKCS#8 P-256 keys from immutable Kubernetes Secret generations, derives `kid` from the RFC 7638 public-JWK thumbprint, and exposes only the internal typed operation `IssueJWTSubject(subject, audience, ttl)`. The issuer owns JOSE headers and generated claims; callers cannot supply arbitrary claims or signing bytes. The only initial audience is a broker-verifier canary.

I1 publishes a canonical SPIFFE JWT bundle over an explicitly configured `https_web` bootstrap endpoint. The retained broker-oriented verifier validates fixed ES256, key ID, signature, configured trust domain, SPIFFE subject, single audience, and bounded time values. It uses a complete bundle snapshot only through cache bound `R`, then fails closed.

Rotation is operator-driven through immutable prepublish, activate, and retire Secret generations. The keyring manifest is the sole I1 rotation authority; no Redis rotation coordinator exists. I1 defaults to `T=5m`, `S=30s`, and `R=60s`; retirement is no earlier than last old issuance plus `T+S+R`.

An issuer-only mode may prove the future combined broker host’s agent-free custody boundary, but it is not a separate production broker Service or lifecycle. It contains no agent loop, command runner, Bash, ToolHive authserver, vMCP, Redis broker state, token exchange, or production mint endpoint.

## Consequences

I2 can project the already-derived child authority into typed logical-agent claims; I3/B4 can exchange it with user authority into a holder-bound vMCP token. The later broker authorizes its existing immutable route before credential lookup. I1 does not itself implement those claims, exchange, credential selection, Redis fencing, KMS/HSM, federation, revocation, or provider integration.

A compromised broker host, cluster administrator, node/kubelet, Secret store, or verifier bootstrap root remains outside I1’s protection claim. Software keys are an accepted first-release custody boundary.

## See also

- [ADR 0027 — cloud-native arc](./0027-cloud-native.md)
- [ADR 0048 — mecak8s](./0048-mecak8s.md)
- [ADR 0205 — bounded JWKS staleness](./0205-bounded-jwks-staleness.md)
- [Agent identity model](../agent-identity-model.md)
- [Agent identity outbound](../agent-identity-outbound.md)
