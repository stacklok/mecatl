# Identity issuer substrate — acceptance plan

**Phase:** I1 identity substrate
**Status:** draft, 2026-08-31. Synthesized from the approved I1 design session.
**Issue:** [stacklok/mecatl#478](https://github.com/stacklok/mecatl/issues/478).
**ADR:** [ADR-0300](../adr/0300-identity-issuer-substrate.md) — pins the I1 issuer substrate.
**Accumulator branch:** `acc/identity-issuer-substrate` (off `main`).

The smallest set of work that proves a shell-less combined-broker issuer can create and independently verify bounded ES256 JWT-SVIDs without becoming a parallel broker platform or changing Stage 3 MCP authorization.

## Why these scope cuts

- [ADR-0300](../adr/0300-identity-issuer-substrate.md) keeps I1 a signing, bundle, verifier, and custody substrate; logical-agent claims, user delegation, and vMCP enforcement follow in I2/I3/B4.
- [ADR 0027](../adr/0027-cloud-native.md) requires inventories for any new long-lived host resource; B0/B1 retain ownership of distributed broker state.
- [ADR 0048](../adr/0048-mecak8s.md) keeps agent pods storage-free and managed-service state external.

## In scope — 5 scenarios, in implementation order

### Scenario 1 — explicit disabled and configured identity posture

The broker-host configuration treats identity as disabled by default. When enabled it requires a valid explicit SPIFFE trust domain, bounded timing policy, registered audience, and bootstrap tuple; it never derives authority from requests or Kubernetes metadata. This follows [ADR-0300](../adr/0300-identity-issuer-substrate.md) and the [layering rule](../../AGENTS.md).

**Acceptance:**
- AC1.1: An identity-disabled host opens no keyring, starts no bundle listener, creates no identity resource, and leaves persisted session state unchanged.
  - verify: `TestIdentityIssuerSubstrate_Scenario1_DisabledHasNoSideEffects`
- AC1.2: An enabled host rejects absent/invalid trust domain, invalid `T`/`S`/`R`, unregistered audience, or incomplete HTTPS bootstrap before key loading.
  - verify: `TestADR_0300_IdentityConfigFailsClosed`
- AC1.3: A configured trust domain is validated by SPIFFE grammar and is never inferred from request host, callback origin, bundle URL, or Kubernetes metadata.
  - verify: `TestInvariant_identity_trust_domain_explicit`

### Scenario 2 — bounded signing from an immutable keyring generation

A complete immutable Secret generation produces one validated `crypto.Signer` snapshot. [ADR-0300](../adr/0300-identity-issuer-substrate.md) requires PKCS#8 P-256 material and issuer-owned envelope fields.

**Acceptance:**
- AC2.1: A strict manifest plus matching PKCS#8 P-256 key items produces a deterministic public-JWK-thumbprint `kid` and one active ES256 signer.
  - verify: `TestIdentityIssuerSubstrate_Scenario2_LoadImmutableKeyring`
- AC2.2: Malformed, oversized, partial, duplicate, unknown, non-P-256, mismatched, or no-active-signer keyrings fail without publishing partial state or generating an ephemeral replacement.
  - verify: `TestADR_0300_KeyringRejectsInvalidGeneration`
- AC2.3: `IssueJWTSubject` accepts only a local-trust-domain SPIFFE subject, one registered audience, and TTL within `T`; caller-controlled JOSE headers, claims, keys, timestamps, and arbitrary signing input are unavailable.
  - verify: `TestInvariant_identity_issuer_typed_envelope`

### Scenario 3 — canonical bundle and independent verification

The retained verifier fetches the canonical SPIFFE JWT bundle through the configured `https_web` bootstrap path and returns typed verified identity rather than authorization claims. [ADR-0300](../adr/0300-identity-issuer-substrate.md) and [ADR 0205](../adr/0205-bounded-jwks-staleness.md) pin bounded key freshness.

**Acceptance:**
- AC3.1: A separately configured verifier accepts an issuer JWT-SVID only for the configured trust domain, valid SPIFFE subject, one expected audience, active `kid`, ES256 signature, and valid bounded times.
  - verify: `TestIdentityIssuerSubstrate_Scenario3_IndependentBundleVerification`
- AC3.2: Algorithm confusion, unknown/duplicate `kid`, bundle substitution/regression, foreign subject, wrong or multiple audience, malformed/oversized token, expired/future/excessive-lifetime token all fail closed.
  - verify: `TestADR_0300_VerifierFailsClosed`
- AC3.3: A verifier retains a complete bundle only through `R`; failed refresh before then preserves it, while expiry of `R` denies verification and marks readiness unready.
  - verify: `TestInvariant_identity_bundle_freshness_bound`
- AC3.4: The bundle projects only public P-256 JWKs, monotonic sequence, and advisory refresh hint; no private material or compatibility JWKS endpoint is emitted.
  - verify: `TestADR_0300_CanonicalBundleHasNoPrivateMaterial`

### Scenario 4 — combined-broker host custody without parallel infrastructure

I1’s issuer-only mode is a composition mode of the future combined broker, not a new broker lifecycle. It preserves the root-internal layering and the Stage 3 boundary described by [ADR-0300](../adr/0300-identity-issuer-substrate.md) and [ADR 0048](../adr/0048-mecak8s.md).

**Acceptance:**
- AC4.1: The issuer host exposes only bundle, liveness, readiness, and safe generation status; it does not expose arbitrary signing, token minting, ToolHive, vMCP, Redis broker state, or an agent tool surface.
  - verify: `TestIdentityIssuerSubstrate_Scenario4_IssuerOnlyHostSurface`
- AC4.2: The issuer composition imports neither agent loop, provider/tool catalog, command runner, Bash tooling, nor `os/exec`; an unsafe signer-plus-execution composition refuses before any key open.
  - verify: `TestInvariant_identity_signer_outside_execution_process`
- AC4.3: Agent-side workloads lack the issuer Secret mount and Secret API read access, while the issuer-host positive control can complete a sign-and-independent-verify canary.
  - verify: `TestIdentityIssuerSubstrate_Scenario4_SecretContainment`
- AC4.4: Keys, compact JWTs, and bearer canaries never occur in bundle/status/diagnostic/error/event/snapshot/argv/environment projections.
  - verify: `TestADR_0300_SecretCanariesNeverLeak`

### Scenario 5 — restart-safe rotation with measurable overlap

The manifest generation is I1’s sole rotation record; B0/B1 Redis remains reserved for distributed broker correctness. [ADR-0300](../adr/0300-identity-issuer-substrate.md) and [ADR 0027](../adr/0027-cloud-native.md) require explicit lifecycle evidence.

**Acceptance:**
- AC5.1: Prepublish starts old-key signing with old/new public verification keys and advances only when every ready replica reports the expected generation and identical bundle digest.
  - verify: `TestIdentityIssuerSubstrate_Scenario5_PrepublishEvidence`
- AC5.2: Activate starts new-key signing while independently cached verifiers accept both documented overlap keys; restart reconstructs the declared generation, phase, active key, and bundle sequence.
  - verify: `TestIdentityIssuerSubstrate_Scenario5_ActivateAndRestart`
- AC5.3: Retire refuses before last old issuance plus `T+S+R`; after that bound, old keys disappear and old-key JWT-SVIDs fail verification.
  - verify: `TestADR_0300_RetirementOverlapBound`
- AC5.4: New long-lived snapshots, listeners, refresh workers, or caches are inventoried in ADR 0027 before landing.
  - verify: inspection — ADR 0027 resource and fidelity inventory is reviewed alongside the implementation.

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| Logical-agent claims and attenuation serialization | I2 | ADR-0300 |
| User-subject/agent-actor token exchange and holder binding | I3 | ADR-0300 |
| Broker Redis fences, callback/refresh recovery, replica ownership | B0/B1 | ADR-0300 |
| vMCP route admission and credential lookup | B4 | ADR-0300 |
| KMS/HSM, federation, revocation, compatibility JWKS, production mint API | later | ADR-0300 |

## Sequencing recommendation

Establish pure values/keyring/signing first, then bundle and independent verifier, then issuer-host custody proof, and finally rotation/Kind evidence. Keep all identity modules out of `engine/`; B0/B1 integration remains a compatibility boundary, not an I1 task.

## Definition of done

1. `task lint` and `task test` pass.
2. `task docs` passes with regenerated `llms.txt` and strict links.
3. `task api:check` passes, or intentional engine API changes include `task api:update` and compatibility documentation.
4. `task ac-trace-strict` passes when this plan is landed.
5. The named acceptance tests pass, including the independent verifier, secret-containment, and rotation proofs.
6. `go run ./cmd/mecademo` still completes its offline session.
7. The new ADR 0027 resource/fidelity inventory entries are reviewed and the B0/B1 compatibility boundary remains unchanged.

## Deferred decisions and known risks

- **Software-key custody.** Broker-host, cluster-admin, node/kubelet, Secret-store, and bootstrap-root compromise remain outside I1; I4 evaluates KMS/HSM.
- **Combined-broker implementation.** B0/B1 defines distributed state and B4 composes I1 with vMCP; I1 does not claim HA broker behavior.

## Exit criteria

When every definition-of-done item holds on the accumulator, I1 supplies an independently verified, rotation-safe, shell-isolated issuer substrate ready for I2/I3/B4.
