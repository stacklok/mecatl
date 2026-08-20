---
id: 04-composition-root
title: Establish managed definition tier and mint authority roots
blocked_by: [03-execution-evaluator]
status: done
branch: "plan-authority-evaluator-port/04-composition-root"
worktree: ""
issue: "371"
retries: 0
last_error: "task test baseline blockers: macOS /var symlink checks and skills lifecycle workspace test; task docs cannot fetch matlatl"
accumulator: acc/authority-evaluator-port
---

# Task brief

Establish the authority source before any child derivation. Define the existing
`tool.AgentOriginExplicit` tier as the sole managed-definition tier for local
authority ceilings; project, user, and driver definitions must remain unable to
establish one. Mint and stamp a complete root authority set from the composed
catalog at every root seam: ordinary app-created sessions, direct server-created
teams, scheduled fires, and peer forks. A peer fork copies its source authority
and provenance without re-derivation. Wire explicit noop/local evaluator
selection and the build-once posture line. This task supplies a carried parent
set to later delegation work; it must not derive children or implement Cedar.

## Acceptance criteria

- AC3.4: An absent evaluator is a deliberate deployment mode selected by an explicit flag and reported in the build-once posture line; it is never a silent default.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario3_AbsentEvaluatorIsExplicitAndAnnounced`
- AC6.2: Every field of a minted root set is populated explicitly at the mint site, and the minted root can consume one delegation hop.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario6_MintPopulatesEveryFieldExplicitly`, `TestADR_0233_AuthorityEvaluator_Scenario6_MintedRootCanDescend`
- AC6.3: A server-created team, a peer fork, and a scheduled fire each receive a set whose provenance is recorded and whose derivation point is documented; a fork copies its source's set and safe provenance without re-deriving.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario6_NonSpawnDerivationPointsAreExplicit`
- AC6.4: The composition posture line reports which evaluator adapter is active and whether enforcement is on.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario6_PostureLineReportsEvaluator`
