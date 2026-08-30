---
id: 05-command-mode
title: Local command mode with shared input boundary
blocked_by: [01-status-protocol, 02-status-settings, 03-ui-status-seam, 04-template-overrides, 08-status-contract-corrections, 09-generator-foundation]
status: done
branch: plan-mecatui-status-line/05-command-mode
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/mecatui-status-line
---

# Task brief

Implement main-owned status command execution for direct executable args and a user-global trusted shell path plus source invoked directly. Send exactly the template `Input` as JSON stdin. Pick local session workspace CWD when known else launch workspace, never remote/unknown. Inherit only HOME, PATH, TERM, LANG, LC_ALL, COLUMNS, LINES; stream-bound combined stdout/stderr to 4 KiB; parse one StatusML document; never leak command source, args, input, errors, or streams into UI/diagnostics. Add an injectable command seam and offline tests. Unsupported process-tree containment must disable mode rather than silently kill only a direct child.

## Acceptance criteria

- AC1.1: The template renderer and configured command observe the same status values, including terminal dimensions, per-surface available columns, and `clock.now`; unknown values use their documented empty/zero representation.
  - verify: `TestStatusCustomization_Scenario1_TemplateAndCommandShareStatusInput`
- AC1.3: `workspace.launch` remains local. `workspace.session` declares its location explicitly and is blank/remote rather than mistaken for a local path in connect mode; command CWD uses a local session workspace when known, otherwise the local launch directory.
  - verify: `TestStatusCustomization_Scenario1_WorkspaceProvenanceAndCommandCWD`
- AC3.1: A user-global inline shell command can use the local checkout and the terminal dimensions to emit StatusML, without a separate script file.
  - verify: `TestStatusLine_Scenario3_InlineShellReceivesSharedInput`
- AC3.2: One command StatusML document can populate both surfaces; each supplied surface has the same themed spans/layout semantics as its template equivalent, and an omitted surface retains its default.
  - verify: `TestStatusLine_Scenario3_CommandAndTemplateShareSurfaces`
- AC3.3: The runner passes only the exact environment allowlist, never interpolates status input into shell source, and cannot be enabled or modified by project/server content.
  - verify: `TestStatusLine_Scenario3_CommandBoundaryIsLocalAndSecretFree`
- AC3.4: Stdout/stderr are bounded while read; overflow, malformed StatusML, and terminal controls fail safely without an unbounded allocation or rendered escape sequence.
  - verify: `TestStatusLine_Scenario3_BoundsAndSanitizesCommandOutput`
