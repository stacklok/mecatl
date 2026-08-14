---
id: 07-startup-resume
title: Exact and latest startup resume without throwaway sessions
blocked_by: [05-sessions-overlay]
status: done
branch: "plan-session-continuity-ux/07-startup-resume"
worktree: ".scratch/worktrees/session-continuity-ux-task-07"
issue: "473"
retries: 0
last_error: ""
accumulator: acc/session-continuity-ux
---

# Task brief

Implement explicit `--resume <id>` and `--resume-latest` in embedded/connect modes using the shared authoritative transcript and capability contract. Do not preserve or edit the old `.scratch/worktrees/issue-473` implementation in place; consult it read-only if useful, then implement against the accumulator. Static adoption is pure; first prompt owns environment/engine/liveness/lease resolution. Seed prompt follows adoption exactly once.

## Acceptance criteria

- AC6.1: `--resume <id>` and `--resume-latest` are mutually exclusive, accepted in embedded and connect modes, and accurately described in CLI help.
  - verify: `TestSessionContinuityUX_Scenario6_FlagGrammar`
- AC6.2: Successful startup resume performs no `CreateSession`, loads the authoritative transcript, adopts metadata, then enables follow-up input.
  - verify: `TestSessionContinuityUX_Scenario6_NoThrowawaySession`
- AC6.3: `--resume-latest` deterministically selects the newest owned `main` row whose authoritative transcript is available; it excludes scheduled, child, unknown, awaiting, and currently-reported-live rows without resolving an Environment or rebuilding an engine during selection.
  - verify: `TestSessionContinuityUX_Scenario6_LatestSelection`
- AC6.4: Exact resume of a crash-orphaned `running` main may display its transcript, but the client never guesses staleness; the first prompt alone may repair it after the real run-entry lock/lease proves exclusivity.
  - verify: `TestSessionContinuityUX_Scenario6_StaleRunningDefersToRunEntry`
- AC6.5: Foreign/missing/pruned targets use generic NotFound guidance; inspect-only kind and unavailable transcript fail before interactive input with closed reason codes, while environment/profile/model attachment is deferred to first prompt.
  - verify: `TestADR_0108_StartupStaticValidation`
- AC6.6: The first prompt performs environment reattachment, engine rehydration, liveness, and lease checks through the ordinary run-entry funnel; any failure leaves the adopted transcript visible/read-only and offers retry/back without creating another session.
  - verify: `TestADR_0108_FirstPromptRevalidatesAtomically`
- AC6.7: A seed prompt is submitted exactly once only after successful transcript adoption; any adoption failure submits nothing.
  - verify: `TestSessionContinuityUX_Scenario6_SeedAfterAdoption`
- AC6.8: Bare `mecatui` remains new-session-by-default.
  - verify: `TestSessionContinuityUX_Scenario6_DefaultRemainsNew`
