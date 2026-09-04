# Acting-as-user exchange — acceptance plan

**Phase:** I3-C adapter-neutral acting-as-user exchange contract
**Status:** in-progress, 2026-09-03. Derived from the approved I3-D design.
**Issue:** [stacklok/mecatl#372](https://github.com/stacklok/mecatl/issues/372).
**ADR:** [ADR-0253](../adr/0253-acting-as-user-exchange.md) — live exchange contract and I3-C boundary.
**Accumulator branch:** `acc/acting-as-user-exchange` (off `acc/logical-agent-identity-projection`, the accepted I2 base).

The smallest set of work that proves an acting-as-user exchange cannot recombine independently valid user, actor, presenter, or target facts into broader authority. It creates an adapter-neutral, root-internal gate with deterministic offline evidence; it does not make a ToolHive production call.

The plan is scenario-first: every scenario demonstrates a bounded decision or secret boundary, not merely a new type or interface.

## Why these scope cuts

- [ADR-0253](../adr/0253-acting-as-user-exchange.md) keeps production ToolHive/vMCP, B1 workload verification, target route admission, credential lookup, Stage 3, persistent caching, and schedule consent outside I3-C.
- [ADR-0252](../adr/0252-logical-agent-identity-projection.md) fixes I2 as a definition-scoped JWT-SVID with exact tools; I3-C must not widen it with generic claims or infer resource scope from a tool name.
- [ADR-0234](../adr/0234-authority-evaluator-port.md) already establishes that external policy can only tighten carried authority.

## In scope — 6 scenarios, in implementation order

### Scenario 1 — Closed exchange inputs preserve distinct authority roles

A caller constructs a non-secret request from a durable issuer-qualified owner, a B1-verified presenter, and a composition-registered resource, operation, and canonical scope set. Credentials remain synchronous profile-specific values, so neither raw bearer nor actor JWT can be accidentally carried through a durable request. This follows the ownership-pair rule in [ADR-0204](../adr/0204-caller-identity-threading.md) and the inward dependency rule in [`AGENTS.md`](../../AGENTS.md).

**Acceptance:**
- AC1.1: An acting-access request accepts only bounded, canonical owner, presenter, resource, operation, and sorted unique scope values; empty, duplicate, control-bearing, or unknown values are refused.
  - verify: `TestActingAccess_Scenario1_RejectsNonCanonicalRequest`
- AC1.2: The public I3-C API exposes no generic claims map, arbitrary URL/audience, backend credential, authorization header, session store, event log, or ToolHive type.
  - verify: `TestInvariant_acting_access_closed_inputs`
- AC1.3: Subject assertion, I2 token, and output token are distinct non-serializable/redacted secret types and cannot be substituted for one another.
  - verify: `TestActingAccess_Scenario1_SeparatesCredentialProfiles`

---

### Scenario 2 — Verified user and I2 actor bind to the durable owner

The gate verifies a fresh subject assertion against the exact intended use and verifies the compact I2 JWT through the existing profile verifier; it never treats an exported parsed value as proof. The durable owner is an exact issuer/subject pair, while I2 remains independently constrained by [ADR-0252](../adr/0252-logical-agent-identity-projection.md).

**Acceptance:**
- AC2.1: A valid exchange-subject assertion whose issuer-qualified identity matches the durable owner reaches the policy gate; changing only issuer or subject refuses before issuance. Wrong/multiple audience, login-bearer or ID-token substitution, wrong `typ`/authorized party, missing/excessive-age temporal fields, duplicate registered claims, wrong algorithm, and oversized input also refuse.
  - verify: `TestADR_0253_ClosedSubjectAssertionProfile`
- AC2.2: A valid compact I2 JWT produces a copied verified logical actor, while wrong audience, expired, malformed, oversized, duplicate-claim, stale-bundle, and algorithm-confused actor tokens refuse without partial facts. Rotation accepts old/new keys only during ADR-0251 overlap and refuses retired or refresh-stale bundles.
  - verify: `TestADR_0253_ActorProfileVerificationAndRotation`
- AC2.3: A caller-constructed or post-verification-mutated logical-agent value cannot widen the tools used by the gate.
  - verify: `TestInvariant_acting_access_verified_actor_copy`

---

### Scenario 3 — Every authority ceiling independently contains the request

Given a registered request, the production gate permits only the conjunction of independently verified subject authority, consent, AS-authenticated presenter-to-actor association, exact I2 tool requirements, registered resource/operation/detail/scope ceilings, and target policy. Consent is not a policy override, and explicit deny in any source is terminal. It models target authority as a registered plan-level tuple, not an untrusted route string, as required by [ADR-0253](../adr/0253-acting-as-user-exchange.md) and [ADR-0234](../adr/0234-authority-evaluator-port.md).

**Acceptance:**
- AC3.1: Through the production exchange entrypoint, Alice, an associated AS-authenticated presenter, a reviewer with the exact registered read tool, an exact consent proof, and a permitted registered read target receive a permit trace containing every required gate.
  - verify: `TestActingAccess_Scenario3_AllCeilingsPermit`
- AC3.2: Six independent decision-source spies each deny the baseline alone; each denial prevents mechanism invocation and identifies its stable failure kind. Restoring that source permits. Changing one bound consent/presenter/resource/operation/detail/scope value likewise refuses before issuance.
  - verify: `TestADR_0253_IndependentCeilingRefusals`
- AC3.3: An explicit deny beats a matching permit in consent, association, or target policy, and neither consent nor a broader scope overrides it; unavailable/indeterminate fails closed without issuance.
  - verify: `TestADR_0253_DenyDominanceAndIndeterminacy`
- AC3.4: A logical actor subject with a valid signature but a narrower exact tool set cannot use a cached or subject-name-derived broader capability.
  - verify: `TestInvariant_acting_access_exact_actor_tools`
- AC3.5: An unknown resource/operation/detail, alias, omitted/default/extra scope, cross-resource scope reuse, or tool-name-only resource inference is refused.
  - verify: `TestActingAccess_Scenario3_RegisteredRequestOnly`
- AC3.6: Correlation changes are observable in a bounded trace but never change authorization; forged or duplicate caller correlation cannot merge audit records or become a lookup/policy input.
  - verify: `TestInvariant_acting_access_correlation_is_not_authority`

---

### Scenario 4 — An issued token is independently verified against the allowed plan

After all local checks permit, the issuance mechanism receives only an immutable validated plan and ephemeral credentials. A deterministic offline AS returns compact signed bytes, and a separate verifier accepts them only when the complete closed profile matches the plan. The verifier receives public test material and independently signed adversarial tokens, never an issuance fake’s parsed output or profile object. This preserves the independently verified single-representation discipline of [ADR-0252](../adr/0252-logical-agent-identity-projection.md).

**Acceptance:**
- AC4.1: Alice + reviewer/read yields a compact RFC 8693 response with required access-token fields and an independently verified output whose identity has collision-resistant issuer-qualified user `sub`, closed I2 logical `act`, AS-derived client attribution, exactly one registered audience, canonical scopes, and canonical operation detail.
  - verify: `TestActingAccess_Scenario4_VerifiesExactOutputProfile`
- AC4.2: Shortening each subject, actor, consent, association, target-policy, or configured lifetime bound independently shortens verified output expiry to that bound; a missing required bound fails closed, and no output includes a refresh token.
  - verify: `TestADR_0253_OutputLifetimeCeiling`
- AC4.3: Independent verification of compact, correctly signed adversarial tokens rejects wrong issuer/signature/user/actor/client attribution/audience/scope/detail, forbidden or nested `act`, `cnf`, missing or invalid response/temporal/header fields, duplicate security fields, multiple audiences, unsupported algorithm, and input-token-derived claims before returning usable access.
  - verify: `TestADR_0253_OutputProfileConfusionRefused`
- AC4.4: A reviewer requesting deploy is refused before issuance, while a deployer with the exact deploy tool, consent, and matching association is the positive control.
  - verify: `TestActingAccess_Scenario4_ReviewerReadDeployerWrite`

---

### Scenario 5 — Failures and secret boundaries fail closed

The gate distinguishes permanent authorization/profile failures from unavailable trusted infrastructure, returns only stable non-secret categories, and never creates a fallback identity. It protects the token-bearing values that RFC 8693 exchange would otherwise place at a high-risk boundary; [ADR-0253](../adr/0253-acting-as-user-exchange.md) forbids persistence and model-visible projection.

**Acceptance:**
- AC5.1: Invalid subject, owner mismatch, invalid actor, presenter denial, actor authority failure, target/scope denial, consent denial, unsupported profile, temporary unavailability, and invalid output have closed failure kinds with correct retryability.
  - verify: `TestActingAccess_Scenario5_FailureTaxonomy`
- AC5.2: Canary values placed in each secret wrapper are absent from every real I3-C error, diagnostic, decision trace, event, snapshot, tool result, JSON/text formatter, and persistence-capable value on success and every failure path; they appear only at the narrow mechanism call. The wrappers expose no `String`, `GoString`, marshal method, or exported raw-value accessor.
  - verify: `TestInvariant_acting_access_secret_sink_inventory`
- AC5.3: A denied or unavailable exchange never selects service, ownerless, ambient, broader actor, or provider credential authority. Retired/unknown subject, actor, or output verification keys and expired consent/association/policy facts fail closed after their bounded freshness window.
  - verify: `TestADR_0253_NoFallbackAuthorityAndStaleFacts`

---

### Scenario 6 — Offline proof resists cooperative fakes and preserves local work

The deterministic fake AS records only non-secret decision facts and can issue a compact token; it is not the authorization authority. Tests prove the gate predicates and cache/expiry posture rather than accepting a fake that special-cases definition names. This scenario follows the test isolation and adapter boundaries in [`AGENTS.md`](../../AGENTS.md).

**Acceptance:**
- AC6.1: Mutating/removing each production conjunction predicate makes a black-box entrypoint test with independent decision-source spies fail; the test never derives its decision from fixture labels or a trace helper.
  - verify: `TestInvariant_acting_access_predicate_mutation_resistance`
- AC6.2: The output-token mechanism is not called for any pre-issuance refusal; the independent verifier receives compact bytes and public test material rather than fake structs or generic claims.
  - verify: `TestActingAccess_Scenario6_RefusalPrecedesIssuance`
- AC6.3: I3-C contains no persistent output cache; repeated exchange cannot mint an output beyond the original minimum verified validity, and unavailable external exchange affects only that external request.
  - verify: `TestActingAccess_Scenario6_NoPersistentCacheAndReplayBound`
- AC6.4: Through real I3-C composition, one blocked/unavailable external exchange and one concurrent local-only work item prove isolation: local work completes within a bounded deadline with zero subject-verifier/mechanism calls, then the external request returns only `unavailable` with no token or fallback identity.
  - verify: `TestActingAccess_Scenario6_LocalOnlyOutageIsolation`

## ToolHive I3-T compatibility handoff

ToolHive source inspected at `288e466dcf6c496950be3ae9914cf45906f9fe77` (local `main`, no containing release tag); the currently consumed release is `v0.40.0`. No row below is I3-C implementation work. I3-T may start only after every blocker has an owner, a selected release, and the named black-box proof.

| Required behavior | Current ToolHive source / release | Status | I3-T blocker and black-box evidence |
|---|---|---|---|
| External subject issuer, owner qualification, and consent | `pkg/authserver/server/tokenexchange/factory.go` (`MultiIssuerTokenValidator`); `handler.go` external consent paths; main only | partial / unreleased | Configure the closed exchange-subject profile and prove wrong issuer/audience/consent tuple rejects. |
| I2 `actor_token` parsing and external trust | `pkg/authserver/server/tokenexchange/handler.go` self-issued actor validator; main only | upstream change | Accept external Mecatl bundle/JWKS with fixed I2 issuer/audience/algorithm/profile and bounded staleness; prove wrong bundle/key/profile rejects. |
| Presenter → logical definition association | `handler.go` requires actor `client_id ==` authenticated client; main only | upstream change | Replace equality with operator-owned presenter/definition/resource/operation/scope association; prove each one-value mismatch rejects. |
| Broker client authentication | existing client authentication; SPIFFE OAuth work tracked upstream | needs configuration/upstream | Select a released mTLS/SPIFFE client-auth profile; AS must derive client identity, never accept it from request input. |
| Scope/resource/detail intersection | `handler.go` client/subject scopes and one resource validation; main only | partial / unreleased | Add exact operation/RAR detail and I2-tool association intersection; prove omitted/default/extra scope and operation substitution reject. |
| Output `sub`, `act`, client attribution | `handler.go` qualified subject and `act` construction; main only | partial / unreleased | Prove exact I3 output profile, no actor/client swap or nested authority expansion. |
| Discovery, external bundle/JWKS staleness | auth-server discovery/trust configuration; main only | needs release/configuration | Pin released metadata/trust refresh behavior; prove rotation overlap, retired-key refusal, and bounded outage. |
| Redis restart and decision state | `pkg/authserver/storage/redis.go`; main only | needs black-box proof | Restart while issuing/validating and prove no widened association/consent/credential selection. |
| Post-admission credential lookup | vMCP route admission/credential path; main only | upstream/configuration | Prove lookup occurs only after exact target admission and never falls back to ambient credentials. |

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| ToolHive/vMCP production client and release integration | I3-T | [ADR-0253](../adr/0253-acting-as-user-exchange.md) |
| TokenReview, projected ServiceAccount token, and sidecar transport | B1 | [ADR-0253](../adr/0253-acting-as-user-exchange.md) |
| Provider credential lookup and resolved-route admission | I3-T | [ADR-0253](../adr/0253-acting-as-user-exchange.md) |
| Persistent output-token cache and distributed invalidation | broker/I3-T | [ADR-0253](../adr/0253-acting-as-user-exchange.md) |
| Stage 3 continuation | B4 | [ADR-0253](../adr/0253-acting-as-user-exchange.md) |
| Scheduled/offline consent | I3-S / #373 | [ADR-0253](../adr/0253-acting-as-user-exchange.md) |
| KMS, federation, and RFC 8705 deployment enforcement | I4 / I3-T | [ADR-0253](../adr/0253-acting-as-user-exchange.md) |

## Sequencing recommendation

Establish closed values and secret boundaries first, then subject/I2 verification, then the pure conjunction and decision trace, then compact output issuance/verification, and finally the adversarial sink and mutation proofs. Keep composition opt-in so disabled I3 leaves current behavior and stored bytes unchanged. `/plan-orchestrate` owns detailed task decomposition.

## Definition of done

1. `task lint` and `task test` pass.
2. `task docs` regenerates `llms.txt` and passes the strict documentation gate.
3. `task api:check` passes; any intentional engine exported API change includes `task api:update` and `engine/CHANGELOG.md`.
4. `task ac-trace-strict` passes after the plan is landed.
5. Every named `TestADR_0253_*`, `TestInvariant_acting_access_*`, and scenario test passes offline in the appropriate root-internal package.
6. `go run ./cmd/mecademo` still prints a full offline session.
7. Disabled I3 performs no exchange/verification and preserves current stored bytes.
8. A focused security review confirms AC3–AC6 invoke the production conjunction/output-verifier boundaries and identifies the independent fake source for each ceiling.

## Deferred decisions and known risks

- **ToolHive enforcement remains blocked.** An exact upstream association and external I2 actor-token trust are needed before I3-T; a main-branch feature is not a release dependency.
- **Bearer replay residual.** Output remains bearer until the broker/gateway implement and enforce RFC 8705 mTLS confirmation; expiry is the full replay window meanwhile.
- **Live consent profile.** The AS/enterprise IdP must select and test the authoritative subject-assertion/consent mechanism before deployment; I3-C hides it behind a typed verifier, not a generic claims map.
- **Schedule authority.** I3-S must define signed bounded consent separately; no live assertion is retained.

## Exit criteria

When every point under *Definition of done* holds on the accumulator, this plan is satisfied.
