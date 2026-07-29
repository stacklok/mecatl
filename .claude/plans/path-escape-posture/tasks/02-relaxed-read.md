---
id: 02-relaxed-read
title: yolo/auto allow out-of-root reads (+ pseudo-fs deny, containment, rehydrate)
blocked_by: [01-escape-classifier]
status: done
branch: "plan-path-escape-posture/02-relaxed-read"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/path-escape-posture
---

# Task brief

Wire the relaxed-READ path: at posture `yolo` and `auto` an out-of-root
absolute-path `Read`/`Stat` succeeds through the FS tool instead of failing with
`ErrPathEscape`. This is the honesty fix — at those postures Bash already reads
the same bytes. The relax flows through the ordinary `authorize → preHook +
execute` tail so audit (`ToolCallRecorder`), `EvToolResult`, and PostToolUse
hooks fire identically to an in-root read.

Read `.claude/agents/tdd-worker.md` first. Key constraints:
- The decision lives in a root-aware wrapping `port.PermissionPolicy` (a
  permpolicy sibling closing over the session root + the task-01 classifier). The
  wrapper DELEGATES TO THE INNER POLICY FIRST and only relaxes a non-deny — never
  overrides an inner Deny (deny-dominance).
- osfs `Read`/`Stat` serve a canonicalized out-of-root absolute path only under
  an explicit relaxed-read construction option (DEFAULT OFF — the zero-value
  workspace stays deny). Serving opens a FRESH `*os.Root` on the target's parent
  and serves the leaf through it (never a bare `os.Open`), so a symlink inside
  the target dir that escapes further is refused by that root's containment.
- Pseudo-fs paths (`/proc`, `/sys`, `/dev`) are NEVER served — `Read
  /proc/self/environ` must NOT return the raw server env (Bash reads the
  envscrub-scrubbed child env; an in-process read would leak server secrets).
- The relaxed option is wired into the MAIN session's workspace construction
  ONLY, derived from posture, never into `newForkWorkspace` or any child engine.
- Rehydration: a relaxed session restarted must rehydrate the SAME relaxed
  workspace via `Service.rehydrateSession` (persisted labels), not the default
  deny workspace.

Plan mode still permits the read (reads are not mutations).

## Acceptance criteria

- AC2.1: at posture `yolo`, `Read` of an out-of-root absolute path returns the
  file's contents (no `ErrPathEscape`).
  - verify: `TestPathEscapePosture_Scenario2_YoloReadEscapeAllowed`
- AC2.2: at posture `auto` with no escape guardrail knob, `Read` of an
  out-of-root absolute path succeeds (Bash parity).
  - verify: `TestPathEscapePosture_Scenario2_AutoReadEscapeAllowed`
- AC2.3: an allowed read escape records the call verbatim in the
  `ToolCallRecorder` and emits `EvToolResult`, exactly as an in-root read.
  - verify: `TestPathEscapePosture_Scenario2_ReadEscapeAuditParity`
- AC2.4: at posture `strict`, a `Read` escape does NOT silently succeed in this
  wave (the ask lands in Scenario 4) — behaviour is unchanged from today.
  - verify: `TestPathEscapePosture_Scenario2_StrictReadUnchanged`
- AC2.5: at `yolo`, `Read /proc/self/environ` does NOT return the raw server
  environment (pseudo-fs hard-deny) — no `*_API_KEY`/`*_TOKEN`/`*_SECRET`
  substring reaches the result.
  - verify: `TestPathEscapePosture_Scenario2_ProcEnvironNotExposed`
- AC2.6: a symlink inside an allowed out-of-root target dir whose target escapes
  further is refused by the serving `*os.Root` (containment survives the relax).
  - verify: `TestPathEscapePosture_Scenario2_NestedSymlinkEscapeRejected`
- AC2.7: a session that ran relaxed and is restarted rehydrates the SAME relaxed
  workspace (via `Service.rehydrateSession`), so a resumed out-of-root `Read`
  still succeeds rather than dead-ending on `ErrPathEscape`.
  - verify: `TestPathEscapePosture_Scenario2_RestartRehydratesRelaxedWorkspace`
