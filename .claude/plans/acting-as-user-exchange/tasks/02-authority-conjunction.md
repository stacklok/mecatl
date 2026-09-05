---
id: 02-authority-conjunction
title: Exact authority conjunction and validated mechanism plan
blocked_by: [01-closed-inputs-and-verification]
status: done
branch: "plan-acting-as-user-exchange/02-authority-conjunction"
worktree: ""
issue: "372"
retries: 0
last_error: ""
accumulator: acc/acting-as-user-exchange
---

# Task brief

Build the concrete production entrypoint and exact authorization conjunction in `internal/actingaccess`. Add narrow consumer-owned decision ports for subject authority, consent, AS-authenticated presenter association, registry, and target policy; each decision carries permit/deny/unavailable and validity. Require the exact registered resource/operation/detail/scope tuple and exact copied I2 tools. Compute a validated immutable root-internal mechanism input with expiry ceiling. Do not introduce a generic policy pipeline, claims map, ToolHive types, caching, or a production client.

## Acceptance criteria

- AC3.1: Through the production exchange entrypoint, Alice, an associated AS-authenticated presenter, a reviewer with the exact registered read tool, an exact consent proof, and a permitted registered read target receive a permit trace containing every required gate.
  - verify: `TestActingAccess_Scenario3_AllCeilingsPermit`
- AC3.2: Six independent decision-source spies each deny the baseline alone; each denial prevents mechanism invocation and identifies its stable failure kind. Restoring that source permits. Changing one bound consent/presenter/resource/operation/detail/scope value likewise refuses before issuance.
  - verify: `TestADR_0302_IndependentCeilingRefusals`
- AC3.3: An explicit deny beats a matching permit in consent, association, or target policy, and neither consent nor a broader scope overrides it; unavailable/indeterminate fails closed without issuance.
  - verify: `TestADR_0302_DenyDominanceAndIndeterminacy`
- AC3.4: A logical actor subject with a valid signature but a narrower exact tool set cannot use a cached or subject-name-derived broader capability.
  - verify: `TestInvariant_acting_access_exact_actor_tools`
- AC3.5: An unknown resource/operation/detail, alias, omitted/default/extra scope, cross-resource scope reuse, or tool-name-only resource inference is refused.
  - verify: `TestActingAccess_Scenario3_RegisteredRequestOnly`
- AC3.6: Correlation changes are observable in a bounded trace but never change authorization; forged or duplicate caller correlation cannot merge audit records or become a lookup/policy input.
  - verify: `TestInvariant_acting_access_correlation_is_not_authority`
