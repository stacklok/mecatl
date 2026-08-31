---
id: 04-rotation-evidence
title: Immutable rotation generations and overlap evidence
blocked_by: [03-issuer-host-custody]
status: pending
branch: ""
worktree: ""
issue: "478"
retries: 0
last_error: ""
accumulator: acc/identity-issuer-substrate
---

# Task brief

Complete I1’s manifest-driven prepublish/activate/retire validation and restart-safe rotation evidence. Do not add Redis coordinators, live Secret reload, B0/B1 state, vMCP, or logical-agent claims. Inventory any long-lived host resources in ADR 0027 as required.

## Acceptance criteria

- AC5.1: Prepublish starts old-key signing with old/new public verification keys and advances only when every ready replica reports the expected generation and identical bundle digest.
  - verify: `TestIdentityIssuerSubstrate_Scenario5_PrepublishEvidence`
- AC5.2: Activate starts new-key signing while independently cached verifiers accept both documented overlap keys; restart reconstructs the declared generation, phase, active key, and bundle sequence.
  - verify: `TestIdentityIssuerSubstrate_Scenario5_ActivateAndRestart`
- AC5.3: Retire refuses before last old issuance plus `T+S+R`; after that bound, old keys disappear and old-key JWT-SVIDs fail verification.
  - verify: `TestADR_0251_RetirementOverlapBound`
- AC5.4: New long-lived snapshots, listeners, refresh workers, or caches are inventoried in ADR 0027 before landing.
  - verify: inspection — ADR 0027 resource and fidelity inventory is reviewed alongside the implementation.
