---
id: 06-refresh-lifecycle
title: Status refresh, cancellation, and degradation
blocked_by: [03-ui-status-seam, 05-command-mode, 08-status-contract-corrections]
status: done
branch: plan-mecatui-status-line/06-refresh-lifecycle
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/mecatui-status-line
---

# Task brief

Implement the shared UI-agnostic `Source` lifecycle. It accepts the latest raw `Input` through non-blocking `Submit`, owns source selection/template evaluation/command debounce/cancellation/one-second timeout/process-tree cleanup/clock interval/default-last-good degradation, and publishes only bounded semantic `Result` header/footer spans. It never imports `ui`, Bubble Tea, or emits ANSI/OSC.

Its `Changed() <-chan struct{}` is a capacity-one wake-up edge, never a result queue. Store a private latest result under a mutex, allocate fresh span slices on every publish, and deep-copy them in `Latest()`. Private generations prevent stale command/template completion from publishing. The UI runs exactly one Bubble Tea adapter Cmd waiting on `Changed`, snapshots `Latest` into `statusLineChangedMsg`, applies it, then re-arms. The listener captures the TUI root context; `Close(context.Context)` cancels timer/process work, joins it, and closes `Changed` so the listener terminates. A source timer autonomously refreshes `clock.now` and may publish even when UI input is unchanged.

The UI submits the independently computed header/footer available widths in `Input`; the generator selects template variants and materializes renderer-owned context-meter semantic spans from those facts. Commands receive raw JSON plus those widths and emit one optional-surface StatusML document; templates receive a private automatically StatusML-escaped projection. Add this outlives-a-call resource to ADR 0027 inventory.

## Acceptance criteria

- AC4.1: Input changes coalesce to one latest command refresh; templates re-render without process creation, and an optional interval advances `clock.now` for template-only clocks.
  - verify: `TestStatusLine_Scenario4_DebounceTemplateRefreshAndClock`
- AC4.2: Interval refresh is optional; absent interval creates no periodic work, while a configured interval refreshes an idle command once for both surfaces.
  - verify: `TestStatusLine_Scenario4_OptionalInterval`
- AC4.3: Only one contained command tree runs; cancellation, timeout, and shutdown join the tree/readers, and stale completion cannot overwrite either current surface.
  - verify: `TestStatusLine_Scenario4_ProcessLifecycleAndGenerationGuard`
- AC4.4: Failed command refreshes preserve bounded stale surfaces or use their default templates, without leaking stream contents, command arguments, or token-shaped data to UI, conversation, or diagnostics.
  - verify: `TestStatusLine_Scenario4_FailureDegradesWithoutLeakage`
