# Working in the terminal UI

Keep transport, host, and engine dependencies out of `ui`. The server can be
remote: use its capabilities and confirmed state. Sanitize untrusted terminal
text, and use theme-owned styles with meaningful non-color cues.

Read the relevant section of the [UI contributor guide](../../../docs/tui.md)
before editing; it owns shared invariants, presentation rules, and focused test
references.

| Task | Guide |
|---|---|
| Server events or session actions | [Client/server boundaries](../../../docs/tui.md#respect-clientserver-boundaries) |
| Overlay lifecycle or async input | [Surfaces](../../../docs/tui.md#add-or-change-a-surface) |
| Picker, browser, or form | [Lists and forms](../../../docs/tui.md#build-a-picker-browser-or-form) |
| Layout, sizing, wrapping, or pointer geometry | [Safe rendering](../../../docs/tui.md#size-and-render-content-safely) |
| Transcript, tool calls, or notices | [Conversation and tools](../../../docs/tui.md#change-conversation-or-tool-presentation) |
| Colors or terminal support | [Themes and capabilities](../../../docs/tui.md#apply-themes-and-terminal-capabilities) |
