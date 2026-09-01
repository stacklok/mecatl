---
id: 03-command-help-and-docs
title: Handle command help and living/public documentation
blocked_by: [01-handle-projection-presentation, 02-debug-handle-resolution]
status: in-progress
branch: ""
worktree: ".scratch/worktrees/issue-922-task03"
issue: "922"
retries: 0
last_error: ""
accumulator: acc/predictable-session-handles
---

# Task brief

Update command index and command-specific help for both embedded and `connect ADDRESS debug` forms to describe the shared displayed short handle, its literal grammar, ambiguity fallback to `/session` exact copy, and the absence of a leading `#` marker. Update the living mecatui material in `docs/tui.md` and `docs/usage.md`, relevant architecture/implementation notes where the ordinary digest wording is now stale, and the existing public mecatui/session documentation under `user-docs/`. Update the status-line input reference for `Session.Handle`, protocol v2, and no digest alias. Rename misleading ordinary-display legacy tests/summaries to ADR-0217/0278 terminology without changing debugger evidence contracts. Do not edit the acceptance plan, frozen ADRs, generated `llms.txt`, or product code; aggregate generated-doc reconciliation remains the orchestrator's responsibility.

Add/adjust focused command-help and regression tests. Document shell-safe debug examples and retain `/session`'s byte-exact copy path. The task follows the implementation tasks so documentation describes the final shared projection and resolver behavior.

## Acceptance criteria

- AC3.1: `mecatui debug` and `mecatui connect ADDRESS debug` help accurately say that they
  accept an exact session ID or a displayed short handle matching the literal grammar, direct
  ambiguous input to `/session` exact copy, and show no leading `#` marker.
  - verify: `TestPredictableSessionHandles_Scenario3_CommandHelp`
- AC3.2: `docs/tui.md`, `docs/usage.md`, the relevant `user-docs/` session/debug guides, and the
  status-line input reference use the handle term, explain the fixed escaped-prefix projection,
  preserve `/session` exact-copy guidance, and give shell-safe debug examples. They document the
  `Session.Digest` → `Session.Handle` schema rename and protocol v2, with no digest alias.
  - verify: inspection — `task docs` and `task site:build` validate the reviewed documentation paths
- AC3.4: Rename misleading legacy tests and summaries while landing the new contract: the
  unrelated `TestADR_0108_DisplayDigestIsNotAnID` becomes an ADR-0217/0278-named test, and the
  acceptance-plan/ADR README summaries say “handle”, never describe the ordinary projection as a
  debugger evidence digest.
  - verify: `TestADR_0278_OrdinaryHandleDoesNotAlterDebuggerEvidenceHandles`
