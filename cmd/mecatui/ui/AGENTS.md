# Working in the terminal UI

Read the [UI contributor guide](../../../docs/tui.md#start-here) for the task you
are changing. It owns cross-component conventions; this file is the entry point.

## Essential boundaries

- `ui` handles input and renders client messages. Keep generated contracts, gRPC,
  host packages, and `engine/` imports out of `ui`; transport belongs in `client`.
- The server may be remote. Render its capabilities and confirmed state rather
  than inferring authority or optimistically rebinding the active session.
- Sanitize untrusted display text through `internal/terminaltext` before terminal
  rendering. Expand tabs before measuring, wrapping, or clipping.
- Use theme-owned semantic styles and preserve meaningful cues without color.

## Find the contract before editing

Paths below are relative to `cmd/mecatui/`.

| Task | Guide | Reuse |
|---|---|---|
| Server events or session actions | [Client/server boundaries](../../../docs/tui.md#respect-clientserver-boundaries) | `client/`, `ui/update.go` |
| Add or change an overlay | [Surface ownership and lifecycle](../../../docs/tui.md#add-or-change-a-surface) | `ui/surface.go` |
| Picker, browser, or form | [Lists and forms](../../../docs/tui.md#build-a-picker-browser-or-form) | `ui/list_row.go`, `ui/internal/bounded/` |
| Layout, sizing, or pointer geometry | [Safe rendering](../../../docs/tui.md#size-and-render-content-safely) | `ui/layout.go`, `internal/terminaltext/` |
| Transcript, tool calls, or notices | [Conversation and tools](../../../docs/tui.md#change-conversation-or-tool-presentation) | `ui/conversation_view.go`, `internal/renderfmt/` |
| Theme or terminal support | [Themes and capabilities](../../../docs/tui.md#apply-themes-and-terminal-capabilities) | `theme/` |

Read the owning contract and a relevant implementation before adding a parallel
path. Keep surface state and async request validation on the surface; let the
parent own lifetime, placement, and root-state effects through narrow intents.
Keep shared frame geometry in `layout.go` and reading anchors in
`conversationView` rather than maintaining independent offsets.

## Verify the interaction

Use the guide's [test map](../../../docs/tui.md#verify-the-change) to choose focused
offline tests for the change and its integration boundary. In particular:

- Exercise late results after close/reopen and unrelated root updates while a
  modal is open or pending.
- Check narrow geometry, resize before input, hidden-action isolation, and
  keyboard-only operation for interactive surfaces.
- For rendering, check fully styled/framed widths and live updates off the tail;
  include light/dark and ANSI-stripped checks when cues or contrast change.

Refresh goldens only for intentional visual changes. Follow the repository-root
build/test gates and test-isolation rules; this guide does not replace them.

Keep detailed conventions in `docs/tui.md`, API mechanics beside the owning code,
and one-off regression cases in tests rather than expanding this entry point.
