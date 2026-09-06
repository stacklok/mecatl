---
id: 01-double-escape
title: Physical non-remappable double-Escape draft clearing
blocked_by: []
status: pending
attempt: 0
branch: ""
worktree: ""
issue: "605"
retries: 0
last_error: ""
accumulator: acc/mecatui-double-escape
---

# Physical non-remappable double-Escape draft clearing

Implement the focused mecatui UI behavior from issue #605 on the accumulator. Keep the gesture physical and non-remappable: two distinct Escape presses clear a non-empty idle, focused draft through the existing `clearPrompt` path. Preserve repeat filtering, generation-tagged expiry, owner-first precedence, all draft-local cleanup, live help, documentation, and only the goldens that actually change.

## Acceptance criteria

- AC1.1: the first distinct physical `Escape` is silently consumed as the first
  gesture half, leaves every draft-local value byte-for-byte unchanged, and arms
  exactly one 500 ms generation-tagged expiry.
  - verify: `TestADR_0025_DoubleEscape_Scenario1_FirstEscapeArmsWithoutMutation`
- AC1.2: a distinct second physical `Escape` before the matching expiry invokes the
  existing `clearPrompt` path and clears text, staged media, large pastes, and pending
  media; attachment-only content is cleared too.
  - verify: `TestADR_0025_DoubleEscape_Scenario1_SecondEscapeClearsThroughClearPrompt`
- AC1.3: a held key or Bubble Tea key-repeat event never counts as the second deliberate
  press and cannot clear the draft; if repeat metadata is unavailable, only a release
  (or equivalent distinct-press boundary) permits a later Escape to count.
  - verify: `TestADR_0025_DoubleEscape_Scenario1_HeldEscapeRepeatDoesNotArmOrClear`
- AC1.4: any intervening non-`Escape` key disarms the gesture and continues through
  existing normal routing, including ordinary prompt editing; it neither clears nor
  silently consumes that key.
  - verify: `TestADR_0025_DoubleEscape_Scenario1_InterveningKeyDisarmsAndRoutesNormally`
- AC1.5: expiry disarms without changing the draft, and an expiry from an older
  generation cannot clear or disarm a newer re-armed gesture.
  - verify: `TestADR_0025_DoubleEscape_Scenario1_GenerationGuardsExpiry`
- AC1.6: selection, slash palette, mention, approval, every overlay/modal owner, and
  running-turn cancellation consume or disarm Escape before idle gesture handling.
  Once an owner dismisses Escape, the next idle Escape is a fresh first press, never a
  destructive second press.
  - verify: `TestADR_0025_DoubleEscape_Scenario1_OwnersConsumeAndDisarm`
- AC1.7: existing ClearPrompt remains an accessible alternative through its current
  remappable action (`ctrl+u` by default); the physical two-Escape portability gesture
  is not added to or exposed as a remappable Cancel/ClearPrompt binding.
  - verify: `TestADR_0025_DoubleEscape_Scenario1_ClearPromptAlternativeRemainsAccessible`
- AC1.8: the first press remains silent and clearing adds no completion toast unless
  the existing `clearPrompt` path already emits one; no unrelated status UX is added.
  - verify: `TestADR_0025_DoubleEscape_Scenario1_FirstPressAndCompletionHaveNoNewStatus`
- AC1.9: tests use an injectable/deterministic timer-message seam or direct message
  delivery, not `time.Sleep`, wall-clock expiry, or a new production clock/API/config/
  proto seam; implementation remains local to the TUI.
  - verify: `TestADR_0025_DoubleEscape_Scenario1_IsDeterministicWithoutWallClock`
- AC2.1: live help names the two physical Escape presses, says the gesture is a
  non-remappable portability gesture, and points users to the remappable ClearPrompt
  action (`ctrl+u` by default) as the accessible alternative.
  - verify: `TestADR_0025_DoubleEscape_Scenario2_LiveHelpExplainsGestureAndAlternative`
- AC2.2: `docs/tui.md` and `user-docs/mecatui/keybindings.md` describe the same
  timing, silent-first-press behavior, draft-local non-empty definition, ownership
  precedence, repeat/held-key rule, and ClearPrompt alternative.
  - verify: `TestADR_0025_DoubleEscape_Scenario2_DocumentationNamesCompatibilityContract`
- AC2.3: update only help goldens that actually change; do not claim golden stability
  in advance. Unrelated view goldens remain unchanged.
  - verify: `TestADR_0025_DoubleEscape_Scenario2_HelpGoldenChangesAreScoped`

## Implementation boundaries

- Preserve selection-first and owner-first input routing. Every owner consumes or disarms Escape before the idle gesture; an owner dismissal cannot leave an armed destructive second press.
- Define non-empty from all draft-local content: text, staged media, large-paste content, and pending media. Use only `clearPrompt` to perform cleanup.
- Do not add or expose a keymap/config/remapping surface, production clock, engine/API/proto dependency, or broad golden refresh.
- Update live help, `docs/tui.md`, and `user-docs/mecatui/keybindings.md` consistently; inspect golden changes to prove they are limited to live help.

## Required verification

- `task lint`
- `task test`
- `task docs`
- `task site:build`
- `task ac-trace`
- `task ac-trace-strict`
- `go run ./cmd/mecademo`
