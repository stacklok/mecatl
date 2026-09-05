---
id: 03-issuer-host-custody
title: Issuer-only combined-broker host and custody proof
blocked_by: [02-bundle-verifier]
status: pending
branch: ""
worktree: ""
issue: "478"
retries: 0
last_error: ""
accumulator: acc/identity-issuer-substrate
---

# Task brief

Add the issuer-only composition mode of the future combined broker. It must be agent-free and expose only bundle, liveness, readiness, and safe generation status. It must not create a parallel broker lifecycle, mint endpoint, ToolHive/vMCP/Redis state, or agent/tool surface. Add structural and runtime containment evidence before key loading.

## Acceptance criteria

- AC1.1: An identity-disabled host opens no keyring, starts no bundle listener, creates no identity resource, and leaves persisted session state unchanged.
  - verify: `TestIdentityIssuerSubstrate_Scenario1_DisabledHasNoSideEffects`
- AC4.1: The issuer host exposes only bundle, liveness, readiness, and safe generation status; it does not expose arbitrary signing, token minting, ToolHive, vMCP, Redis broker state, or an agent tool surface.
  - verify: `TestIdentityIssuerSubstrate_Scenario4_IssuerOnlyHostSurface`
- AC4.2: The issuer composition imports neither agent loop, provider/tool catalog, command runner, Bash tooling, nor `os/exec`; an unsafe signer-plus-execution composition refuses before any key open.
  - verify: `TestInvariant_identity_signer_outside_execution_process`
- AC4.3: Agent-side workloads lack the issuer Secret mount and Secret API read access, while the issuer-host positive control can complete a sign-and-independent-verify canary.
  - verify: `TestIdentityIssuerSubstrate_Scenario4_SecretContainment`
- AC4.4: Keys, compact JWTs, and bearer canaries never occur in bundle/status/diagnostic/error/event/snapshot/argv/environment projections.
  - verify: `TestADR_0300_SecretCanariesNeverLeak`
