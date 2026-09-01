# Logical-agent identity projection — acceptance plan

**Phase:** I2 logical-agent identity
**Status:** in-progress, 2026-08-31. Synthesized from the approved and adversarially reviewed I2 design session.
**Issues:** [stacklok/mecatl#367](https://github.com/stacklok/mecatl/issues/367), [#371](https://github.com/stacklok/mecatl/issues/371), [#375](https://github.com/stacklok/mecatl/issues/375), [#377](https://github.com/stacklok/mecatl/issues/377).
**ADR:** [ADR-0252](../adr/0252-logical-agent-identity-projection.md) — pins the logical subject, closed typed claim, one containment proof, and issue/verify boundary.
**Accumulator branch:** `acc/logical-agent-identity-projection` (off `acc/identity-issuer-substrate`).

The smallest set of work that lets the existing shell-less I1 host mint and independently verify a bounded logical-agent JWT-SVID whose exact tool authority cannot exceed a supplied mecatl `CapabilitySet`.

The plan deliberately stops before B4 spawn wiring and I3 user/actor exchange. Acceptance is about the signed value and its security boundary, not yet the distributed broker lifecycle that will consume it.

## Why these scope cuts

- [ADR-0252](../adr/0252-logical-agent-identity-projection.md) limits I2 to typed logical-definition identity, exact tool projection, constrained issuance, and independent verification.
- [ADR-0251](../adr/0251-identity-issuer-substrate.md) retains ES256 key custody, bundle verification, fixed audience/time policy, host isolation, and rotation.
- [ADR-0234](../adr/0234-authority-evaluator-port.md) and [`AGENTS.md` — authority cannot widen](../../AGENTS.md) keep runtime authority derivation and concrete request authorization outside token encoding.
- [`architecture.md` — dependency direction](../architecture.md) keeps `internal/identityissuer` dependent on the governance domain, never the reverse.

## In scope — 6 scenarios, in implementation order

### Scenario 1 — canonical logical-definition subject and closed claim

A trusted caller supplies one closed definition tier, its exact resolved name, and an optional occurrence label. I2 derives one deterministic local SPIFFE workload identity without lossy name collisions and validates the bounded v1 value before any signing work. This is the principal and schema fixed by [ADR-0252](../adr/0252-logical-agent-identity-projection.md).

**Acceptance:**

- AC1.1: Each of `system`, `managed`, `driver`, `user`, and `project` produces the versioned local subject `/mecatl/agent-definition/v1/<tier>/<slug>--<digest>`, where the digest golden is all SHA-256 bits encoded as RFC 4648 lowercase Base32 without padding over the ADR's byte-exact tuple.
  - verify: `TestADR_0252_CanonicalLogicalAgentSubject`
- AC1.2: Distinct exact names that share a display slug, the same name in different tiers, case variants, and canonically equivalent but byte-distinct Unicode names produce distinct subjects; rename is a principal change.
  - verify: `TestLogicalAgentIdentityProjection_Scenario1_DefinitionIdentityDoesNotCollapse`
- AC1.3: Empty, invalid-UTF-8, control-bearing, oversized, unknown-tier, malformed-path, percent-encoded, foreign-trust-domain, query/fragment, and over-2048-byte subjects fail without returning a partial identity.
  - verify: `TestADR_0252_SubjectAndDefinitionBoundsFailClosed`
- AC1.4: The v1 claim requires tier, exact name, and tools; optional instance is omitted rather than `null`; duplicate requested tools and every approved field/count/byte-bound violation fail validation before signing, while a canonical unique positive control succeeds.
  - verify: `TestLogicalAgentIdentityProjection_Scenario1_ClosedBoundedValue`

### Scenario 2 — exact no-wider authority projection

A caller requests an exact tool-name set from an already-derived source `governance.CapabilitySet`. I2 uses the existing predicate rather than introducing token-specific narrowing, as required by [ADR-0252](../adr/0252-logical-agent-identity-projection.md) and [ADR-0234](../adr/0234-authority-evaluator-port.md).

**Acceptance:**

- AC2.1: A requested tool set equal to or narrower than the source succeeds, including an empty request meaning no projected tools; reordered equivalent requests produce the same canonical sorted claim.
  - verify: `TestADR_0252_ContainedToolProjectionSucceeds`
- AC2.2: A request containing one exact tool absent from the source fails before signing and emits no compact token, while the same request succeeds when that tool is added to the positive-control source.
  - verify: `TestADR_0252_LogicalAgentProjectionNeverWidens`
- AC2.3: Tool names remain byte-exact: case, whitespace, Unicode, and MCP-shaped variants are neither trimmed, normalized, aliased, prefix-matched, nor granted through disclosure-only exceptions.
  - verify: `TestLogicalAgentIdentityProjection_Scenario2_ToolNamesStayExact`
- AC2.4: For every schema-valid unique requested set, the issuance decision matches `source.Contains(governance.CapabilitySet{Tools: requested})` across a deterministic table and fuzz-seed corpus covering empty, reordered, Unicode, whitespace, case, and MCP-shaped values.
  - verify: `TestADR_0252_ContainmentOracleCorpus`
- AC2.5: The production issuance path invokes `CapabilitySet.Contains` as its authority decision and contains no inlined or helper-local second subset policy; a structural sentinel fails if that call or dependency direction disappears.
  - verify: `TestADR_0252_IssuerUsesGovernanceContainment`

### Scenario 3 — constrained issuance and unpredictable token identity

The root-internal issuer host signs the canonical value while retaining ownership of JOSE, audience, time, key, and randomness. No caller can turn the operation into an arbitrary signing oracle. This extends [ADR-0251](../adr/0251-identity-issuer-substrate.md) without weakening its custody boundary.

**Acceptance:**

- AC3.1: A valid typed request produces one ES256 compact JWT-SVID with the configured local issuer, canonical subject, exact singleton I3 audience, fixed bounded TTL, current RFC-7638-derived `kid`, and the one v1 public claim.
  - verify: `TestLogicalAgentIdentityProjection_Scenario3_TypedIssue`
- AC3.2: Every mint receives exactly 16 cryptographically random bytes encoded as an unpadded base64url `jti`; repeated mints of the same identity differ in `jti`, times/signature as applicable, while deterministic injected-randomness tests remain offline.
  - verify: `TestADR_0252_FreshRandomJWTID`
- AC3.3: Randomness failure, claim validation failure, containment failure, or a post-sign token above 16 KiB returns no token and never falls back to a generated key, unsigned value, anonymous identity, or partial claim.
  - verify: `TestADR_0252_IssueFailsBeforeReturningCredential`
- AC3.4: The typed host operation exposes no caller control over algorithm, `kid`, issuer, audience, TTL, timestamps, `jti`, generic claim maps, signing bytes, or private key, and adds no listener or production mint RPC.
  - verify: `TestADR_0252_LogicalAgentIssuerIsNotArbitrarySigner`

### Scenario 4 — independent, single-pass typed verification

A retained verifier with only the compact token, configured trust domain/audience/schema limits, clock, and a fresh complete I1 bundle validates one security representation and returns a typed logical identity. It never verifies the envelope and then projects authority from a second unverified parse. This follows [ADR-0252](../adr/0252-logical-agent-identity-projection.md), [ADR-0251](../adr/0251-identity-issuer-substrate.md), and the fail-closed trust-boundary rules in [`AGENTS.md`](../../AGENTS.md).

**Acceptance:**

- AC4.1: An independently configured verifier accepts a valid I2 token and returns exactly trust domain, recomputed subject, tier, exact name, optional instance, a fresh sorted unique tool slice, JWT ID, and expiry—without raw compact JWT, generic claims, or a policy verdict.
  - verify: `TestLogicalAgentIdentityProjection_Scenario4_IndependentTypedVerification`
- AC4.2: Payload tampering, attacker-key signing with a copied `kid`, wrong algorithm/key/use/trust domain/audience/time, unknown key, unavailable/stale/incomplete/regressed bundle, missing/empty/malformed/padded/wrong-length/non-base64url `jti`, missing/malformed v1 claim, unknown tier/version/member, multiple logical-agent versions, non-canonical tools, or subject/profile disagreement fails with a zero typed result and no partial authority.
  - verify: `TestADR_0252_TypedVerifierFailsClosed`
- AC4.3: Duplicate protected-header, registered-claim, top-level profile, and every v1-object member fail even when a last-value-wins parser would see a valid final value; a canonical positive-control sibling verifies. The production verifier constructs its typed result from the signature-validated typed claims representation and does not decode the payload again through an unverified path.
  - verify: `TestADR_0252_TypedVerifierUsesOneSecurityRepresentation`
- AC4.4: A valid I1 canary token fails I2 verification, an I2 token remains rejected by I1's registered-claims-only canary verifier, and generic unrelated JWT claims never grant tools or select another validation profile.
  - verify: `TestADR_0252_ProfileConfusionMatrix`

### Scenario 5 — ephemeral remint across rotation without identity drift

I2 carries no durable token state. Using the same valid typed identity and same-or-narrower source authority after I1 activation produces a fresh credential under the current key while bounded overlap remains independently verifiable. This applies [ADR-0251](../adr/0251-identity-issuer-substrate.md) rotation and [ADR-0252](../adr/0252-logical-agent-identity-projection.md) remint semantics.

**Acceptance:**

- AC5.1: Before and after key activation, independently verified tokens have the same canonical subject/tier/name/instance and same-or-narrower tools, but fresh `jti`, signature, issuance time, and the active `kid`.
  - verify: `TestLogicalAgentIdentityProjection_Scenario5_RotationPreservesLogicalIdentity`
- AC5.2: During the documented overlap both unexpired tokens verify; after the ADR-0251 retirement bound the old-key token fails and the new-key token remains valid.
  - verify: `TestADR_0252_LogicalAgentRotationOverlap`
- AC5.3: A deterministic compact-token/signature canary is proven present at the successful issuance boundary, then absent from the typed verifier result, bounded issue/verify errors and diagnostics, and every persistence-capable domain value. Separate rejected fixtures place recognizable tool, instance, and unknown-claim canaries in claim-derived error paths and prove errors/diagnostics do not echo them; successful signed claims and typed results retain the approved tool/instance fields. No session, event, status, or model-visible type is widened to carry a compact token or parent token.
  - verify: `TestADR_0252_LogicalAgentCredentialCanariesNeverPersistOrLeak`

### Scenario 6 — vertical authority and compatibility proof

A reviewer definition receives a read-only projection, an independent verifier returns that exact per-token tool set, and the existing authority evaluator consumes that returned value. Read succeeds while deploy fails; a deploy-capable positive control follows the identical path and succeeds. This proves authentication remains separate from concrete authorization under [ADR-0234](../adr/0234-authority-evaluator-port.md) and [ADR-0252](../adr/0252-logical-agent-identity-projection.md).

**Acceptance:**

- AC6.1: The exact independently verified reviewer tools authorize `Read` and deny a deploy tool through the existing evaluator; the token, verifier result, and evaluated capability set agree byte-for-byte.
  - verify: `TestLogicalAgentIdentityProjection_Scenario6_ReviewerCannotDeploy`
- AC6.2: A legitimate deploy-capable source and token pass the same mint→verify→evaluate path and authorize deploy, proving the denial is caused by signed authority rather than an unrelated policy floor.
  - verify: `TestADR_0252_DeployPositiveControl`
- AC6.3: Adding deploy to the compact payload after signing fails verification before the evaluator is invoked; prior tokens, logical subject, instance, or unrelated claims are never unioned into current authority.
  - verify: `TestADR_0252_VerifierGrantsCurrentToolsOnly`
- AC6.4: I2 adds no production B4 lifecycle caller or dormant spawn hook; an architecture sentinel proves `internal/app`, server, and `engine/agent` remain outside I2 invocation, while existing session-snapshot and event golden bytes remain unchanged.
  - verify: `TestADR_0252_NoLifecycleWiringOrPersistenceDrift`

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| Root/Subagent/Parallel/Team/schedule binding and eager mint invocation | B4 | [ADR-0252](../adr/0252-logical-agent-identity-projection.md) |
| Durable five-tier definition binding and restart remint orchestration | B4/B0 | [ADR-0252](../adr/0252-logical-agent-identity-projection.md) |
| User subject, groups, RFC 8693 exchange, `act`, client/presenter authorization | I3 | [ADR-0252](../adr/0252-logical-agent-identity-projection.md) |
| Holder binding, replay consumption, vMCP token, route/resource/Cedar policy | I3/B4 | [ADR-0252](../adr/0252-logical-agent-identity-projection.md) |
| Redis fencing, failover, idempotency and broker ownership | B0/B1 | [ADR-0027](../adr/0027-cloud-native.md) |
| General resource-containment vocabulary, KMS/HSM and federation | later | [ADR-0252](../adr/0252-logical-agent-identity-projection.md) |
| Stage 3 MCP authorization interruption changes | separate track | [ADR-0251](../adr/0251-identity-issuer-substrate.md) |

## Cross-cutting deliverables

- `internal/identityissuer` remains the root-internal owner; `engine/governance` is reused inward-only and no engine package imports issuer code.
- The I1 canary verifier remains strict and byte-compatible; I2 receives a separate mutually exclusive typed validation profile.
- Errors and diagnostics expose bounded categories, never compact tokens, exact tool inventories, instance values, signing material, prompts, arguments, credentials, or attacker-controlled claim bodies.
- Golden subject vectors and malformed JWT corpora are deterministic and offline.
- Any outlives-a-call resource added contrary to the expected pure-operation design is inventoried in [ADR 0027](../adr/0027-cloud-native.md) before landing.

## Sequencing recommendation

Establish the pure definition/profile values and golden vectors first, then the containment-bound typed issuer, then the single-pass typed verifier, rotation/remint proofs, and finally the vertical authority/disabled-compatibility proof. Do not introduce B4 lifecycle wiring to make an I2 test pass; orchestration will decompose implementation tasks after this plan is accepted.

## Definition of done

1. `task lint` and `task test` pass for both Go modules with `-race`.
2. `task docs` passes with regenerated `llms.txt` and the strict matlatl link gate green.
3. `task api:check` passes; if an intentional exported engine change occurs despite the root-internal design, `task api:update` and the required `engine/CHANGELOG.md` compatibility note are included.
4. `task ac-trace-strict` passes after `/plan-orchestrate` changes this plan to `landed`.
5. Every named test above is green and grep-locatable by its identifier.
6. `go run ./cmd/mecademo` still prints the full offline turn → tool call → permission ask/approval → result session.
7. The I1 canary verifier and rotation suite remain green, and no test uses a live network, model, Kubernetes cluster, or external issuer.
8. Secret/privacy canaries are proven present at the successful issuance boundary and absent from forbidden claim fields, typed results, bounded errors/diagnostics, and persistence-capable domain values; no production lifecycle type or caller is widened for I2.

## Deferred decisions and known risks

- **Bearer replay.** I2 `jti` is correlation only; I3/B4 must authenticate the presenter and choose holder binding and any single-use/idempotent exchange rule.
- **Temporal authority.** An issued tool set remains usable until expiry even if runtime authority narrows; the fixed short TTL is I2's revocation bound.
- **Definition fidelity.** Current durable definition labels do not encode every resolved tier; B4 must carry a trusted typed winner rather than infer it.
- **Resource precision.** Tool presence does not grant every repository, path, tenant, route, credential, or argument behind that tool; I3/B4 performs resolved-target authorization.
- **Software key custody.** ADR-0251's broker-host, cluster-admin, node/kubelet, Secret-store, and bootstrap-root compromise limits remain; I4 may add KMS/HSM.

## Exit criteria

When every definition-of-done item holds on the accumulator, I2 supplies a bounded, independently verified logical-agent credential ready for B4 lifecycle binding and I3 actor exchange without widening mecatl authority.
