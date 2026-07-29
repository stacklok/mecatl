---
id: 03-relaxed-write
title: yolo allow writes; auto ask on writes (os.Root-served, Edit-ledger)
blocked_by: [02-relaxed-read]
status: done
branch: "plan-path-escape-posture/03-relaxed-write"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/path-escape-posture
---

# Task brief

Wire the relaxed-WRITE path: under `yolo` an out-of-root `Write`/`Edit`
succeeds; under `auto` (no guardrail knob) a write escape resolves **Ask** —
never a silent un-asked mutation below `yolo`. Writes route through the
mutate-serial path unchanged (the read-parallel / mutate-serial invariant
holds). Edit's three invariants apply to an out-of-root target identically.

Read `.claude/agents/tdd-worker.md` first. Key constraints:
- The wrapping policy (from task 02) resolves a write escape Allow at `yolo`,
  Ask at `auto`/`strict`/`trusted`. It DELEGATES TO THE INNER POLICY FIRST and
  never overrides an inner Deny — a configured Deny (or configured Ask) on the
  tool still wins over an escape Allow (deny-dominance, AGENTS.md permission-fold
  invariants).
- osfs `Write` (and the Edit mutation path) serve a canonicalized out-of-root
  absolute path under the relaxed-write construction option, THROUGH A FRESH
  `*os.Root` on the target's parent — never a direct `os.WriteFile` — so the
  symlink-escape containment (ADR-0047) survives.
- The Edit read-ledger (`RecordRead`/`WasReadUnchanged`) keys out-of-root paths
  by their canonical ABSOLUTE form (out-of-root paths have no root-relative
  form) without regressing in-root cross-form matching.
- The escape Ask surfaces through the ordinary `surfaceAsk` spine (mint askID →
  PauseForApproval → EvPermissionAsk) — no new ask channel. v1 asks are
  allow-once only (no learned out-of-root rule).
- Relaxed-write option wired into the MAIN session's workspace only, never a
  child engine.

## Acceptance criteria

- AC3.1: at `yolo`, `Write` to an out-of-root absolute path creates/replaces the
  file.
  - verify: `TestPathEscapePosture_Scenario3_YoloWriteEscapeAllowed`
- AC3.2: at `auto` (no guardrail knob), a `Write` escape surfaces an
  `EvPermissionAsk` and executes only on an allow verdict.
  - verify: `TestPathEscapePosture_Scenario3_AutoWriteEscapeAsks`
- AC3.3: an out-of-root `Edit` enforces read-before-edit-and-unchanged: editing
  a path not first read (or changed since) is rejected, keyed on the canonical
  path (a path with `..` components normalizing to the same canonical form
  matches).
  - verify: `TestPathEscapePosture_Scenario3_EditLedgerOutOfRoot`
- AC3.4: two concurrent write escapes never run in parallel (mutate-serial
  preserved).
  - verify: `TestPathEscapePosture_Scenario3_WriteEscapeMutateSerial`
- AC3.5: an out-of-root `Write`/`Edit` flows through an `*os.Root` on the
  target's parent (not a direct `os` call): a symlinked parent component that
  escapes is refused, exactly as an in-root write.
  - verify: `TestPathEscapePosture_Scenario3_WriteEscapeServedThroughOsRoot`
- AC3.6: a configured Deny on the tool still wins over a posture-relaxed escape
  Allow (deny-dominance); a configured Ask is never suppressed by the relax.
  - verify: `TestPathEscapePosture_Scenario3_ConfiguredDenyWinsOverEscapeAllow`
