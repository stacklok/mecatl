---
id: 05-delegation-derivation
title: Derive authority at all child and resume seams
blocked_by: [04-composition-root]
status: done
branch: "plan-authority-evaluator-port/05-delegation-derivation"
worktree: ""
issue: "371"
retries: 0
last_error: "task test baseline blockers: macOS /var symlink checks and skills lifecycle workspace test; task docs cannot fetch matlatl"
accumulator: acc/authority-evaluator-port
---

# Task brief

Consume the carried root or parent authority established by task 04 before
acquiring a child worktree, engine, environment, runner, or session. Derive all
Subagent variants, Parallel branches, the in-loop Team tool, and server-created
team members by narrowing that carried set, then consuming precisely one hop.
Use the `AgentOriginExplicit` managed-definition tier defined in task 04 for a
specialist ceiling: `Tools` minus `DisallowedTools`, plus resolved MCP tool
names. Implement per-call tightening and resume containment without reapplying
the ceiling or spending a second hop. Stamp owner and authority independently.
Do not alter root minting or adapter selection.

## Acceptance criteria

- AC4.1: A parent spawning a child that asks for more than the parent holds yields the intersection, on every seam and every Subagent variant including background, fork, structured-output, and per-call model override.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario4_ChildGetsIntersectionOnEverySeam`
- AC4.2: Derivation completes before any runtime resource is acquired; a refused delegation creates no worktree, engine, environment, runner, or child session.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario4_RefusalAcquiresNoRuntimeResource`
- AC4.3: The definition ceiling is the resolved `Tools` allowlist minus `DisallowedTools`, plus the expanded tool names of the definition's resolved `mcpServers:`, from an operator-managed definition tier only; a project-, user-, or driver-tier definition cannot establish a ceiling, and a lower-tier definition cannot occupy a higher-tier name.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario4_OnlyManagedTierSuppliesACeiling`
- AC4.4: A per-call request may only tighten; a call asking for a capability, delegate, or execution posture outside the derived set is refused with a reason naming which check refused it.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario4_CallTighteningCannotWiden`
- AC4.5: The child's owner and its capability set are stamped at the same seam and neither is inferred from the other.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario4_OwnerAndSetAreIndependentlyStamped`
- AC5.1: A resumed child whose persisted set is not contained by the caller's current set is refused; a child persisted before this feature is refused rather than upgraded.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario5_ResumedChildCannotExceedCurrentParent`
- AC5.2: A resume consumes no additional delegation hop and does not re-derive against the definition ceiling.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario5_ResumeSpendsNoAdditionalHop`
- AC5.3: The property holds across a process restart, on both the snapshot path and the event-fold path.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario5_NoWideningAcrossRestart`
- AC6.1: A session created through the ordinary composition path can spawn a default read-only subagent, a named managed specialist, a Parallel branch, and a Team, and each child receives a non-empty derived set.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario6_ComposedRootCanDelegateOnEverySeam`
