# Mecatui double-Escape draft clearing — acceptance plan

**Phase:** focused capability — local mecatui prompt UX  
**Status:** landed, 2026-09-06
**Issue:** #605  
**Accumulator branch:** `acc/mecatui-double-escape` (off `main`)  
**Scope:** `cmd/mecatui/ui` input routing, live help/docs, and offline tests only

An idle mecatui with a focused, non-empty prompt treats **two distinct physical
Escape key presses** as a quiet draft-clear gesture. This is a portability gesture,
not the remappable Cancel action: it is deliberately not configurable or remappable.
The existing remappable ClearPrompt action (`ctrl+u` by default) remains the accessible
alternative. The first physical press changes neither the draft nor its staged
media/paste and is silent; it arms a 500 ms generation-tagged chord. A distinct second
press before expiry calls the existing `clearPrompt`, clearing the draft and all staged
media/paste. Key-repeat/held-Escape events do not count as the second deliberate press:
use Bubble Tea repeat metadata when available, otherwise use a release/distinct-press
safe mechanism.

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
  configurable key binding. Held/repeated key events must be filtered as repeats;
  when Bubble Tea cannot provide repeat metadata, require a release or equivalent
  distinct-press boundary before accepting another Escape.
- The existing key reference in [`docs/tui.md`](../tui.md#keys), public
  `user-docs/mecatui/keybindings.md`, and live help must describe the gesture, its
  silent first press, timing, portability/non-remappability, ownership precedence,
  and `ctrl+u`/ClearPrompt alternative. Timer messages are injected deterministically
  and matched by generation; no wall-clock behavior belongs in acceptance tests.

### Scenario 1 — focused idle prompt, two-Escape gesture

Given an idle model whose prompt is focused and whose draft-local content is non-empty
(text, staged media, large-paste content, or pending media), while preserving the
selection-first and owner-first TUI boundary in [ADR-0222](../adr/0222-mecatui-ask-args-view.md):

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

### Scenario 2 — discoverability and compatibility

Given the live help view and the published key references, following
[ADR-0025](../adr/0025-ux-discoverability.md)'s discoverability contract:

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

- Update live help to make the gesture discoverable, identify it as non-remappable,
  and explicitly point to the existing ClearPrompt action (`ctrl+u` by default).
- Update [`docs/tui.md`](../tui.md#keys) with two distinct physical Escape presses,
  500 ms window, silent first press, repeat filtering, draft-local non-empty content,
  ownership precedence, and the ClearPrompt alternative.
- Update `user-docs/mecatui/keybindings.md` with the same user-facing compatibility
  contract, including staged attachments and large-paste/pending-media content.
- Add or update only the help golden(s) required by the live-help text; inspect the
  diff to prove no unrelated golden changed.
- Keep implementation/test changes confined to the mecatui UI surface; do not add a
  new public API, proto/config field, engine dependency, or golden-wide refresh.

## Definition of done

1. The named scenarios and ADR proofs pass offline, including the exact held/repeat
   regression proof `TestADR_0025_DoubleEscape_Scenario1_HeldEscapeRepeatDoesNotArmOrClear`,
   with no wall-clock sleeps.
2. Live help, `docs/tui.md`, and public keybinding docs consistently explain the
   non-remappable physical gesture and the remappable ClearPrompt alternative.
3. Any golden changes are limited to the live-help output and are explicitly reviewed.
4. `task lint` and `task test` pass.
5. `task docs` passes its generated-reference and strict-link checks.
6. `task site:build` passes after the public keybindings update.
7. `task ac-trace` passes (and `task ac-trace-strict` after this plan is landed).
8. `go run ./cmd/mecademo` still prints the complete offline session.
9. This draft is refined only; no implementation or commit is made as part of drafting.

## Implementation task

Implement one small reducer/timer-message seam in `cmd/mecatui/ui` that preserves
current dispatch ordering and owner consumption, tags expiry with the active
generation, filters held/repeat Escape events, and invokes `clearPrompt` only for a
matching second distinct physical Escape. Add the named deterministic proofs, update
live help and the two keybinding documents, refresh only necessary help goldens, then
run the gates above. Do not implement or commit as part of drafting this plan.
