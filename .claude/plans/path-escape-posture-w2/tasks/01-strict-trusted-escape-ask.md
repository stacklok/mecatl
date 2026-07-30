---
id: 01-strict-trusted-escape-ask
title: strict/trusted resolve out-of-root escapes to Ask (Scenario 4)
blocked_by: []
status: done
branch: "plan-path-escape-posture-w2/01-strict-trusted-escape-ask"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/path-escape-posture-w2
---

# Task brief

Implement Scenario 4: at `strict` and `trusted`, an out-of-root read or write
escape resolves **Ask** rather than today's fall-through to the inner decision
(which hard-denies with `ErrPathEscape` and just pushes the model to an opaque
Bash `cat /path`). Moving the ask onto the FS tool makes it legible and keeps
the FS tools' invariants in play.

Read `.claude/agents/tdd-worker.md` first. Wave-1 code is on `main`:
`internal/app/escapepolicy.go` `Evaluate` (the posture fold). Today rule 6
("everything else → inner decision verbatim") leaves strict/trusted escapes to
the inner policy's deny. Extend the fold so a strict/trusted escape resolves
Ask. Constraints:

- The wrapper already delegates to the inner policy FIRST (deny-dominance +
  configured-Ask floor). Preserve that: a configured Deny and a configured Ask
  still win over any escape decision; the plan-mode hard-deny inside
  `EvaluateWith` must precede the escape decision (AC4.4).
- The escape Ask must NOT be `ConfiguredAsk`/`FlooredConfiguredAllow` (it must
  surface to a human; A2/floored-allow key off those bits). v1 asks are
  allow-once (the existing Learn guard already suppresses learning escapes).
- The Ask rides the EXISTING `surfaceAsk` spine (mint askID → PauseForApproval →
  EvPermissionAsk) — no new ask channel. The escape reason names the path and
  that it lies outside the workspace.
- Plan mode: a write escape is hard-denied BEFORE any escape Ask (plan-mode
  deny wins first); a read escape in plan mode follows the read row (allow).
- Headless: a main-engine escape ask at strict/trusted must NOT hang — with no
  approver wired the await ends on cancellation (the existing headless main-ask
  behaviour); never a fabricated "denied by user".
- Children never relax (Wave-1 guard holds): a child escape is still hard-deny
  regardless of this change — do not regress the Scenario 5 tests.

## Acceptance criteria

- AC4.1: at `strict`, a `Read` escape surfaces an `EvPermissionAsk`; on allow
  the read executes.
  - verify: `TestPathEscapePosture_Scenario4_StrictReadEscapeAsks`
- AC4.2: at `trusted`, a `Write` escape surfaces an `EvPermissionAsk`; on deny a
  deny result is recorded and nothing is written.
  - verify: `TestPathEscapePosture_Scenario4_TrustedWriteEscapeAsks`
- AC4.3: a headless main-engine escape Ask at `strict`/`trusted` does NOT block
  forever: with no approver wired the run surfaces an actionable stop (the
  existing headless main-ask behaviour — the await ends on cancellation), never
  a silent hang and never a fabricated "denied by user".
  - verify: `TestPathEscapePosture_Scenario4_HeadlessEscapeAskDoesNotHang`
- AC4.4: in plan mode a write escape is hard-denied before any escape Ask is
  surfaced — the wrapper consults the inner policy first, so the plan-mode deny
  inside `EvaluateWith` precedes the escape decision (plan-mode precedence and
  deny-dominance both hold).
  - verify: `TestPathEscapePosture_Scenario4_PlanModeWriteEscapeDenied`
