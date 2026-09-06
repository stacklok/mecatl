---
id: 02-panel-repair
title: Repair double-Escape safety contract after panel review
blocked_by: [01-double-escape]
status: done
attempt: 1
branch: plan-mecatui-double-escape/02-panel-repair-attempt-1
worktree: .scratch/worker-mecatui-double-escape-02-panel-repair-attempt-1
issue: "605"
retries: 0
last_error: ""
accumulator: acc/mecatui-double-escape
---

# Repair double-Escape safety contract after panel review

Apply ADR 0302 to the landed implementation without widening its scope. Request Bubble
Tea `KeyboardEnhancements.ReportEventTypes` from `View()`, track
`KeyboardEnhancementsMsg.SupportsEventTypes`, and fail closed when event-type support
is absent. On supporting terminals, require non-repeat Escape press, Escape release,
and then non-repeat Escape press within the exact 500 ms generation-tagged expiry
before using the existing `clearPrompt` cleanup path.

## Corrected acceptance criteria

> AC1.1: `View()` requests `tea.KeyboardEnhancements.ReportEventTypes`, and the model
> tracks `tea.KeyboardEnhancementsMsg.SupportsEventTypes`.

> AC1.2: on a terminal that does not report event-type support, Escape never arms or
> clears the draft (fail closed).

> AC1.4: an Escape `tea.KeyReleaseMsg` establishes distinctness; only a second
> non-repeat Escape after that release and before matching expiry invokes the existing
> `clearPrompt` path and clears text, staged media, large pastes, and pending media;
> attachment-only content is cleared too.

> AC1.5: a held key or Bubble Tea repeat event never counts as the second deliberate
> press. Repeats and non-repeat presses before the release cannot clear the draft.

> AC1.7: expiry disarms without changing the draft, and an expiry from an older
> generation cannot clear or disarm a newer re-armed gesture. The test asserts the
> exact 500 ms command/message seam without `time.Sleep`.

> AC1.8: representative owner tests and an inventory-complete owner-suppression test
> prove that an owner dismissal leaves the next idle Escape as a fresh first press.

> AC2.1: live help names the two physical Escape presses, explicitly says enhanced
> key-event support is required, identifies the gesture as non-remappable, states that
> the first press is silent, and points users to the universal remappable ClearPrompt
> action (`ctrl+u` by default).

## Required work and verification

- Add deterministic tests for unsupported-terminal fail-closed behavior,
  press-release-press clearing, repeat-before-release suppression, the exact 500 ms
  expiry command/message, representative owners, and complete owner inventory
  suppression.
- Keep the first press silent; preserve attachment/paste/pending-media cleanup only
  through `clearPrompt`; do not make the gesture remappable.
- Update live help, `docs/tui.md`, and `user-docs/mecatui/keybindings.md` with the
  support requirement, silent first press, attachment/paste clearing, owner precedence,
  and `ctrl+u` alternative. Refresh only required help goldens.
- Run `task lint`, `task test`, `task docs`, `task site:build`, `task ac-trace`,
  `task ac-trace-strict`, and `go run ./cmd/mecademo`.
