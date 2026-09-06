# Mecatui double-Escape draft clearing — acceptance plan

**Phase:** focused capability — local mecatui prompt UX  
**Status:** landed, 2026-09-06
**Issue:** #605  
**Accumulator branch:** `acc/mecatui-double-escape` (off `main`)  
**Scope:** `cmd/mecatui/ui` input routing, live help/docs, and offline tests only

An idle mecatui with a focused, non-empty prompt offers a destructive draft-clear gesture
only when the terminal reports Bubble Tea enhanced key-event support. The UI requests
`tea.KeyboardEnhancements.ReportEventTypes` and records
`KeyboardEnhancementsMsg.SupportsEventTypes`; without that support it fails closed and
the double-Escape gesture is unavailable because repeat and a deliberate second press
are indistinguishable. The existing remappable ClearPrompt action (`ctrl+u` by default)
remains the universal accessible alternative.

On a supporting terminal, the first non-repeat physical Escape changes neither the
draft nor its staged media/paste and is silent; it arms one exact 500 ms
generation-tagged expiry. An Escape `KeyReleaseMsg` establishes the distinct-press
boundary. Only a subsequent non-repeat Escape after that release and before the
matching expiry calls the existing `clearPrompt`, clearing the draft and all staged
media/paste. Repeats and presses before a release cannot clear.

For this plan, **non-empty** means any draft-local content: text, staged media,
large-paste content, or pending media. Attachment-only drafts therefore clear through
the same existing `clearPrompt` path. Every existing owner (selection, palette,
mention, approval, overlays/modals, and running-turn cancellation) consumes or
disarms
Escape before the idle gesture. After an owner dismisses Escape, the next idle Escape
is a fresh first press; it must never become a destructive second press.

## Current contracts that constrain the change

- The TUI is a client presentation surface, not an engine/API surface: follow
  [`docs/architecture.md`](../architecture.md), the proto-free UI boundary, and the
  capability/help ownership decision in [ADR-0025](../adr/0025-ux-discoverability.md).
- Input and surface ownership are ordered. Existing selection-first behavior and
  modal/overlay routing remain authoritative; [ADR-0222](../adr/0222-mecatui-ask-args-view.md)
  is the current example that `esc` is consumed by the owning approval surface,
  not by the root model.
- The existing [`clearPrompt`](../../cmd/mecatui/ui/prompt_input.go) is the sole
  clear operation and already owns staged-media, large-paste, and pending-media
  cleanup. Do not duplicate that cleanup or widen the `ClearPrompt` remapping surface.
- The gesture is specifically two physical presses, not two Cancel actions and not a
  configurable key binding. Request `tea.KeyboardEnhancements.ReportEventTypes` and
  track `KeyboardEnhancementsMsg.SupportsEventTypes`. Without event-type support, do
  not arm or clear: a repeat cannot be distinguished safely from a deliberate press.
  With support, a first non-repeat press must be followed by `tea.KeyReleaseMsg` before
  a second non-repeat Escape can clear.
- The existing key reference in [`docs/tui.md`](../tui.md#keys), public
  `user-docs/mecatui/keybindings.md`, and live help must state the enhanced key-event
  support requirement, silent first press, exact 500 ms window, attachment/paste
  clearing, owner precedence, and universal `ctrl+u`/ClearPrompt alternative. Timer
  messages are injected deterministically and matched by generation; no wall-clock
  behavior belongs in acceptance tests.

### Scenario 1 — focused idle prompt, two-Escape gesture

Given an idle model whose prompt is focused and whose draft-local content is non-empty
(text, staged media, large-paste content, or pending media), while preserving the
selection-first and owner-first TUI boundary in [ADR-0222](../adr/0222-mecatui-ask-args-view.md):

- AC1.1: `View()` requests `tea.KeyboardEnhancements.ReportEventTypes`, and the model
  tracks `tea.KeyboardEnhancementsMsg.SupportsEventTypes`.
  - verify: `TestADR_0302_DoubleEscape_Scenario1_ViewRequestsEventTypesAndTracksSupport`
- AC1.2: on a terminal that does not report event-type support, Escape never arms or
  clears the draft (fail closed).
  - verify: `TestADR_0302_DoubleEscape_Scenario1_UnsupportedTerminalFailsClosed`
- AC1.3: on a supporting terminal, the first non-repeat physical `Escape` is silently
  consumed as the first gesture half, leaves every draft-local value byte-for-byte
  unchanged, and arms exactly one 500 ms generation-tagged expiry command/message.
  - verify: `TestADR_0302_DoubleEscape_Scenario1_FirstPressArmsExactExpiry`
- AC1.4: an Escape `tea.KeyReleaseMsg` establishes distinctness; only a second
  non-repeat Escape after that release and before matching expiry invokes the existing
  `clearPrompt` path and clears text, staged media, large pastes, and pending media;
  attachment-only content is cleared too.
  - verify: `TestADR_0302_DoubleEscape_Scenario1_PressReleasePressClearsThroughClearPrompt`
- AC1.5: a held key or Bubble Tea repeat event never counts as the second deliberate
  press. Repeats and non-repeat presses before the release cannot clear the draft.
  - verify: `TestADR_0302_DoubleEscape_Scenario1_RepeatBeforeReleaseCannotClear`
- AC1.6: any intervening non-`Escape` key disarms the gesture and continues through
  existing normal routing, including ordinary prompt editing; it neither clears nor
  silently consumes that key.
  - verify: `TestADR_0302_DoubleEscape_Scenario1_InterveningKeyDisarmsAndRoutesNormally`
- AC1.7: expiry disarms without changing the draft, and an expiry from an older
  generation cannot clear or disarm a newer re-armed gesture. The test asserts the
  exact 500 ms command/message seam without `time.Sleep`.
  - verify: `TestADR_0302_DoubleEscape_Scenario1_ExactExpiryAndGenerationGuard`
- AC1.8: selection, slash palette, mention, approval, every overlay/modal owner, and
  running-turn cancellation consume or disarm Escape before idle gesture handling.
  Representative owner tests and an inventory-complete owner-suppression test prove
  that an owner dismissal leaves the next idle Escape as a fresh first press.
  - verify: `TestADR_0302_DoubleEscape_Scenario1_OwnersConsumeAndDisarm`, `TestADR_0302_DoubleEscape_Scenario1_AllEscapeOwnersSuppressGesture`
- AC1.9: existing ClearPrompt remains an accessible universal alternative through its
  current remappable action (`ctrl+u` by default); the physical two-Escape gesture is
  not added to or exposed as a remappable Cancel/ClearPrompt binding.
  - verify: `TestADR_0302_DoubleEscape_Scenario1_ClearPromptAlternativeRemainsAccessible`
- AC1.10: the first press remains silent and clearing adds no completion toast unless
  the existing `clearPrompt` path already emits one; no unrelated status UX is added.
  - verify: `TestADR_0302_DoubleEscape_Scenario1_FirstPressAndCompletionHaveNoNewStatus`

### Scenario 2 — discoverability and compatibility

Given the live help view and the published key references, following
[ADR-0025](../adr/0025-ux-discoverability.md)'s discoverability contract:

- AC2.1: live help names the two physical Escape presses, explicitly says enhanced
  key-event support is required, identifies the gesture as non-remappable, states that
  the first press is silent, and points users to the universal remappable ClearPrompt
  action (`ctrl+u` by default).
  - verify: `TestADR_0302_DoubleEscape_Scenario2_LiveHelpExplainsRequirementAndAlternative`
- AC2.2: `docs/tui.md` and `user-docs/mecatui/keybindings.md` describe the same
  support requirement, 500 ms timing, silent-first-press behavior, attachment/paste
  clearing, ownership precedence, release-and-repeat rule, and ClearPrompt alternative.
  - verify: `TestADR_0302_DoubleEscape_Scenario2_DocumentationNamesSafetyContract`
- AC2.3: update only help goldens that actually change; do not claim golden stability
  in advance. Unrelated view goldens remain unchanged.
  - verify: `TestADR_0302_DoubleEscape_Scenario2_HelpGoldenChangesAreScoped`

## Out of scope

| Item | Decision |
|---|---|
| Running-turn cancellation semantics | unchanged; running cancel precedes the idle gesture |
| Selection, palette, mention, approval, modal, and overlay dispatch | unchanged; owners consume/disarm before root prompt handling |
| Physical gesture remapping | out of scope; two physical Escape presses are a non-remappable portability gesture |
| Cancel action semantics | unchanged; this is not the remappable Cancel action |
| ClearPrompt keymap/config surface | unchanged; the existing remappable ClearPrompt/`ctrl+u` remains the accessible alternative |
| Engine, gRPC, proto, config, public API, or #555 dependency | explicitly out of scope |
| Completion toast/status redesign | out of scope; preserve existing `clearPrompt` behavior |
| Wall-clock-dependent tests | prohibited; use deterministic messages/generations |
| Broad golden refresh | prohibited; permit only necessary live-help golden updates |

## Cross-cutting deliverables

- Update live help to state that enhanced key-event support is required, the first
  physical Escape is silent, the exact 500 ms release-qualified gesture clears staged
  attachments/pastes, and `ctrl+u`/ClearPrompt is universal.
- Update [`docs/tui.md`](../tui.md#keys) with the same support requirement, two
  distinct physical Escape presses, 500 ms window, silent first press, release/repeat
  rule, attachment/paste clearing, ownership precedence, and ClearPrompt alternative.
- Update `user-docs/mecatui/keybindings.md` with the same user-facing safety contract.
- Add deterministic support, press-release-press, repeat-before-release, exact-expiry,
  representative-owner, and inventory-complete owner-suppression proofs. Update only
  the help golden(s) required by the live-help text; inspect the diff to prove no
  unrelated golden changed.
- Keep implementation/test changes confined to the mecatui UI surface; do not add a
  new public API, proto/config field, engine dependency, or golden-wide refresh.

## Definition of done

1. The named scenarios and ADR-0302 proofs pass offline, including fail-closed
   unsupported-terminal, press-release-press, repeat-before-release, exact-500-ms,
   and inventory-complete owner-suppression coverage, with no wall-clock sleeps.
2. Live help, `docs/tui.md`, and public keybinding docs consistently explain enhanced
   key-event support, the silent release-qualified physical gesture, attachment/paste
   clearing, ownership precedence, and the universal remappable ClearPrompt alternative.
3. Any golden changes are limited to the live-help output and are explicitly reviewed.
4. `task lint` and `task test` pass.
5. `task docs` passes its generated-reference and strict-link checks.
6. `task site:build` passes after the public keybindings update.
7. `task ac-trace` and `task ac-trace-strict` pass when the corrected plan lands.
8. `go run ./cmd/mecademo` still prints the complete offline session.

## Implementation task

Implement the corrected reducer/message seam in `cmd/mecatui/ui`: request and track
Bubble Tea event-type support; fail closed without it; require non-repeat
press-release-press within the exact 500 ms generation-tagged expiry; preserve current
dispatch ordering and owner consumption; and invoke `clearPrompt` only on the matching
second press. Add the named deterministic proofs, update live help and the two
keybinding documents, refresh only necessary help goldens, then run the gates above.
