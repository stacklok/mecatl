# ADR 0302 — Fail closed double-Escape clearing on enhanced key-event support

- Status: Accepted
- Date: 2026-09-06
- Scope: mecatui idle prompt draft-clear gesture and keybinding discoverability
- Supersedes: none
- Superseded by: none

## Context

The double-Escape draft-clear gesture is destructive: it clears both typed text and
all draft-local attachments, large pastes, and pending media through `clearPrompt`.
A terminal can emit repeated Escape presses while a key is held. Without enhanced
key-event event-type reporting, the client cannot distinguish such a repeat from a
later deliberate press. Treating either as confirmation would make a held key capable
of clearing a draft.

Bubble Tea can request event-type reporting from the terminal. When the terminal
supports it, a key-release event supplies a concrete boundary between physical presses.
When it does not, a best-effort timing heuristic cannot establish that boundary safely.
The existing remappable ClearPrompt action (`ctrl+u` by default) already provides a
universal alternative.

## Decision

`View()` requests Bubble Tea `KeyboardEnhancements.ReportEventTypes`, and the model
tracks `KeyboardEnhancementsMsg.SupportsEventTypes`.

Fail closed when the terminal does not report event-type support: do not arm or clear
through the double-Escape gesture. Keep ClearPrompt available everywhere.

On a supporting terminal, accept only this sequence for an idle, focused, non-empty
draft:

1. a first non-repeat Escape silently arms one generation-tagged expiry for exactly
   500 ms;
2. an Escape `KeyReleaseMsg` establishes that another physical press may confirm;
3. a second non-repeat Escape before the matching expiry invokes the existing
   `clearPrompt` path.

A repeat, or any press before the release, cannot clear. Existing selection, palette,
mention, approval, modal/overlay, and running-turn owners continue to consume or
disarm Escape before this idle gesture. A non-Escape key and expiry disarm it. The
first press changes no draft-local state and adds no status feedback.

Live help and published keybinding documentation must state the enhanced key-event
support requirement, silent first press, exact timing, release/repeat rule,
attachment/paste clearing, owner precedence, and the universal `ctrl+u` ClearPrompt
alternative. Help golden updates are allowed only where that text changes.

## Consequences

The gesture is safely unavailable on terminals that cannot prove distinct presses,
rather than being inconsistently available through timing guesses. Users retain the
existing remappable clear action on every terminal. The TUI gains narrowly scoped
keyboard-enhancement state and deterministic message tests, but no engine, API, proto,
or configuration surface.

## See also

- [ADR 0025 — UX discoverability](./0025-ux-discoverability.md)
- [ADR 0222 — mecatui ask-args view](./0222-mecatui-ask-args-view.md)
- [Mecatui double-Escape acceptance plan](../acceptance/mecatui-double-escape.md)
- [Mecatui guide](../tui.md#keys)
