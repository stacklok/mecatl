# ADR 0280 — Automatic light theme selection in mecatui

- Status: Accepted
- Date: 2026-08-26
- Scope: `cmd/mecatui`

## Context

mecatui already ships the light-oriented `solar` theme, but defaults to the
dark-oriented `aztec` theme. Users of light terminal profiles therefore need
to know about `--theme solar` before the interface renders well.

Bubble Tea v2 can asynchronously query the terminal background with
`tea.RequestBackgroundColor` and reports the result as a
`tea.BackgroundColorMsg`. A synchronous Lip Gloss query would block startup
while waiting for terminals or multiplexers that never answer.

The query must not be written to redirected output, and automatic selection
must not override `--theme` or `MECATUI_THEME`.

## Decision

When no theme was explicitly selected and stdout is a terminal, mecatui asks
Bubble Tea for the terminal background color during `ui.Model.Init`.

- A light response switches to the existing built-in `solar` theme.
- A dark response, no response, or an unsolicited duplicate leaves the active
  theme unchanged.
- Any explicit `--theme` or `MECATUI_THEME` value disables detection.
- Redirected stdout disables detection so the OSC query cannot leak into a
  pipe or file.

The response handler updates the model theme, rebuilds the theme-dependent
renderer caches, updates the spinner style, and refreshes the current view.
The request wraps the shared startup command once, so every startup path gets
the same behavior.

No separate opt-out is added: explicitly selecting `aztec` already preserves
the former behavior.

## Consequences

Light terminals that support the OSC background query select a suitable theme
without configuration. Unsupported terminals retain the existing default. The
query is asynchronous, so the first frame may briefly use `aztec` before a
light response arrives.

Future components that cache theme-derived styles must also be refreshed by
`switchTheme`.

## See also

- [TUI guide](../tui.md)
- [`Solar` in the theme registry](../../cmd/mecatui/theme/registry.go)
- [`onBackgroundColor` and `switchTheme` in the UI reducer](../../cmd/mecatui/ui/update.go)
