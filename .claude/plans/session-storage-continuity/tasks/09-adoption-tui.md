---
id: 09-adoption-tui
title: Adopt-as-chat Sessions workflow
blocked_by: [03-progressive-sessions-tui, 08-legacy-adoption-server]
status: done
branch: "plan-session-storage-continuity/09-adoption-tui"
worktree: ""
issue: "594"
retries: 0
last_error: ""
accumulator: acc/session-storage-continuity
---

# Task brief

Add truthful legacy labels, capability-driven adopt affordance, explicit binding/review UI, stable cancel/error handling, and authoritative writable-target adoption through the real client/server path.

Follow ADR-0226, ADR-0217, ADR-0027, ADR-0104, AGENTS.md layering/security invariants, and existing repository conventions. Keep tests offline. Do not absorb later tasks or weaken fail-closed behavior. Update the living docs and user-docs affected by this task.

## Acceptance criteria

- AC8.1: An unknown row reads `Legacy session — inspect only`; `a: adopt as chat` appears only for server-advertised eligibility, while disabled rows show the authoritative reason.
  - verify: `TestSessionStorageContinuity_Scenario8_AdoptAffordanceTruth`
- AC8.2: Adoption review shows source, new-session semantics, target workspace/environment, provider/model, and future tool-write implications; cancel/error is stable, and success opens the authoritative new chat writable while the source remains inspect-only.
  - verify: `TestSessionStorageContinuity_Scenario8_AdoptionTUIFlow`
