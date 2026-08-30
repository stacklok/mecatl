---
id: 09-generator-foundation
title: UI-agnostic latest-wins status-line source foundation
blocked_by: [02-status-settings, 08-status-contract-corrections]
status: done
branch: plan-mecatui-status-line/09-generator-foundation
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/mecatui-status-line
---

# Task brief

Implement the ADR-0247 `Source` foundation before adding template or command sources. Compose it outside `ui`; `ui` submits raw `Input`, owns one Bubble Tea adapter listener, and stores only private `Result` runtime state. Implement `Submit(Input)`, capacity-one `Changed`, deep-copying `Latest() Result`, and `Close(context.Context)`. The source owns private stale-work generations, bounded result publication, autonomous interval clock updates, and clean cancellation/listener shutdown; it is UI/Bubble-Tea/ANSI/OSC-free. The UI computes and submits header/footer available widths; generated output contains semantic spans only and preserves renderer-owned chrome and final theme rendering.

Establish shipped default-source behavior and tests for latest-wins coalescing, listener re-arm, close unblocking, immutable result snapshots, resize/width submission, timer-only clock updates, and no raw terminal controls. Do not implement templates or commands in this task.

## Acceptance criteria

This foundation protects AC1.1, AC2.1, and AC4.1–AC4.3. Its tests must pin ADR-0247’s source port and lifecycle contract.
