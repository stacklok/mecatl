---
id: 08-status-contract-corrections
title: Correct Input and independent status surfaces
blocked_by: [01-status-protocol, 03-ui-status-seam]
status: done
branch: plan-mecatui-status-line/08-status-contract-corrections
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/mecatui-status-line
---

# Task brief

Correct the status-line foundation before customization controllers are implemented.

Move canonical raw `Input` and its input-only value objects to `cmd/mecatui/statusline/input.go`; keep StatusML grammar/parser/AST separate. Replace the underspecified input with: server display target/transport (no auth), optional session title and reasoning effort, named usage values for input/output/cache read/cache write plus cache-read percentage, context used/window raw+humanized values plus percent and renderer-owned bar, session workspace `{location,path,basename}` (no launch workspace in input), terminal dimensions plus independent header/footer available widths, main-agent state/activity/approval facts, and flattened leaf delegation state counts by direct subagent/team member/parallel branch and totals (including awaiting approval). Remove the unused compact boolean.

Replace the opaque single UI status-line state with independent header and footer surface state/variants so each has its own selection, default, stale/degradation state, and available-width handling. A single command document remains one generation and may atomically update either or both surfaces; this does not create two commands.

Add StatusML `link` nodes with a quoted `href`, separate display text/destination, theme link/underline rendering, and bounded `http`/`https` validation. No OSC 8 emission yet.

Preserve raw canonical input for commands. Template execution later must receive an automatic private StatusML-escaped projection of textual fields; command output does not pass through Go templates and must escape dynamic values itself. Add focused tests that pin the corrected contract and terminal/markup safety. Do not modify plan/ADR/task-state documentation in this worker.

## Acceptance criteria

This correction protects AC1.1–AC1.3, AC2.1–AC2.5, AC3.1–AC3.4, and AC4.1–AC4.4. Its pinning tests must cover the revised input allowlist/provenance, per-surface layout and state, cache/context atoms, main/delegation state (including child approval), StatusML links, and parser safety.
