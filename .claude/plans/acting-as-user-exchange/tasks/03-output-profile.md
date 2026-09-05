---
id: 03-output-profile
title: Deterministic issuance and independent output verification
blocked_by: [02-authority-conjunction]
status: done
branch: "plan-acting-as-user-exchange/03-output-profile"
worktree: ""
issue: "372"
retries: 0
last_error: ""
accumulator: acc/acting-as-user-exchange
---

# Task brief

Add a deterministic offline issuance mechanism and an independent compact output-token verifier for the closed acting-access profile. The verifier must parse independently signed adversarial tokens using public test material, not fake parsed structures. Enforce exact user/actor/client/resource/scope/detail output contract, response metadata, temporal ceiling, closed JOSE/JWT profile and no forbidden `cnf` or refresh token. Keep it root-internal and do not add production OAuth/ToolHive integration.

## Acceptance criteria

- AC4.1: Alice + reviewer/read yields a compact RFC 8693 response with required access-token fields and an independently verified output whose identity has collision-resistant issuer-qualified user `sub`, closed I2 logical `act`, AS-derived client attribution, exactly one registered audience, canonical scopes, and canonical operation detail.
  - verify: `TestActingAccess_Scenario4_VerifiesExactOutputProfile`
- AC4.2: Shortening each subject, actor, consent, association, target-policy, or configured lifetime bound independently shortens verified output expiry to that bound; a missing required bound fails closed, and no output includes a refresh token.
  - verify: `TestADR_0302_OutputLifetimeCeiling`
- AC4.3: Independent verification of compact, correctly signed adversarial tokens rejects wrong issuer/signature/user/actor/client attribution/audience/scope/detail, forbidden or nested `act`, `cnf`, missing or invalid response/temporal/header fields, duplicate security fields, multiple audiences, unsupported algorithm, and input-token-derived claims before returning usable access.
  - verify: `TestADR_0302_OutputProfileConfusionRefused`
- AC4.4: A reviewer requesting deploy is refused before issuance, while a deployer with the exact deploy tool, consent, and matching association is the positive control.
  - verify: `TestActingAccess_Scenario4_ReviewerReadDeployerWrite`
