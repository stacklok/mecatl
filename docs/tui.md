# Developing the `mecatui` terminal UI

This page is for contributors changing `cmd/mecatui`: the boundaries and shared
conventions that keep new interactions consistent. The
[public `mecatui` guides](../user-docs/mecatui/index.md) own how to start, use,
customize, and troubleshoot the client. Update this page only for a reusable design
rule or an architectural boundary.

## Client boundary

`mecatui` is always a gRPC client. By default it hosts its own server in-process:
`cmd/mecatui/embed` assembles the harness through `internal/app`, the composition
`mecated` uses, and serves it on a private UNIX socket. `mecatui connect ADDRESS`
dials an external server instead. The client code path is the same either way, so
nothing in the UI may assume the server is local.

`client` owns the transport and maps server events to Bubble Tea messages. `ui`
renders those messages and handles input; `theme` owns semantic styling. Only
`client`, `embed`, and the command entry point import generated contracts, gRPC, or
host packages. `ui` and `theme` import none of them and no `engine/` package; this
is a convention, not a lint rule, so check imports when you add a dependency.

Render the server's reported state; never infer authority, resolved models,
placement, or which actions are allowed:

- Feature affordances come from the server's advertised `client.Capabilities`.
  Hide or explain an unavailable feature instead of showing a dead control.
- Session inventory rows carry server-authored action capabilities. Copy,
  transcript view, fork, rename, and delete come from those bits, never from the
  shape of a session ID. The server revalidates every action, and a failed action
  must not rebind the prompt to another session.
- Clear and fork create server-side successors. Rebind the prompt only after the
  server confirms the successor.
- `mecatui debug TARGET` may resolve a short handle against the caller's inventory,
  but only the exact session ID crosses to the server, which decides access.
- Provider retry during a live run belongs to the server. The client never starts a
  second run after a terminal failure; `/retry` is the explicit action. See
  [observability](architecture/observability.md) for the recovery policy.

Some state is the client's own. Key events go through the client's action map
first, and unclaimed keys reach the prompt textarea, which keeps `ctrl+a`, `ctrl+e`,
and `ctrl+p` for line editing. Status-line customization is a client-side seam:
`customization.Source` receives display-safe snapshots and publishes semantic
spans, while the UI owns theme, clipping, and alignment. Its settings live only in
the client's settings file, so a remote server or a project can never select a
local executable.

## Layout and navigation

The frame is a stack of header, conversation, optional transient regions, prompt,
and footer. `cmd/mecatui/ui/layout.go` derives that stack for rendering, viewport
sizing, and mouse hit testing. Add regions there so a transient shrinks the
conversation instead of displacing the footer, and measure the rendered frame
rather than keeping a second set of row offsets.

Overlays that take over the conversation region own their interaction state and
size themselves from the geometry offered on each render; the parent owns placement
and pointer mapping. Follow the [`surface` contract](../cmd/mecatui/ui/surface.go)
and its [migration guidance](drafts/surface-migration-plan.md) when adding or
converting an overlay. A closed surface must not receive late results or leak
keyboard and wheel events into the conversation.

Selectable inventories use `presentListRow` for the cursor marker, status cells,
and selected-row styling. Let the surface own item semantics and activation.
Tool-call status uses `…` with `toolName`, `✓` with `toolOk`, and `✗` with
`toolErr`. Pending and failed calls retain readable labels; settled successful
single-line calls use the glyph and name without a redundant success label.

`cmd/mecatui/internal/renderfmt/toolcall.go` owns the single-line tool-call
vocabulary: `ToolIntent(name, arguments)` derives a sanitized call-side intent,
`ArgumentOrder(name)` orders known fields in the full-arguments inspector, and
`PresentToolLine(name, intent, state)` returns a bounded typed `ToolLine`; its
state-owned glyph, readable status label, and semantic style slot are exposed by
methods. Use `ToolRunning`, `ToolAwaitingResult`, `ToolFinalizing`,
`ToolFinalizingFailed`, `ToolDelegatedPending`, `ToolSucceeded`, or
`ToolFailed` rather than selecting those components in a surface. Settled successful rows suppress redundant status
text; failed rows consistently say `failed`. Extend its presentation table when
adding a tool; do not duplicate argument decoding or tool-specific summaries in a surface.
The conversation's settled rows, `/toolcalls` list and child inspector rows, and
expanded Subagent/Parallel/Team traces (including F6 Agents) use this API.
Surfaces own ANSI styles, clipping, wrapping, selection, and pointer geometry:
list rows stay single-line, child inspector summaries clip at the viewport width,
and expanded traces wrap at their lane width.

Status comes from the observed call/result lifecycle, never from output text.
Delegated events carry only bounded argument and result previews: derive and
retain intent at `tool.call` from the received bounded arguments, then keep it
when a `tool.result` changes status or the latest preview. Malformed or
truncated JSON falls back to the tool name; never infer a path from a result
body (including numbered Read output). Child traces remain per-lane capped and
cannot recover full arguments; parent inspector arguments and results remain
independently inspectable.

List-and-detail browsers use a bounded list for selection and a separate bounded
viewport for detail. Wheel scrolling moves the list window without changing
selection. Returning from detail preserves the selected item and list window, and
detail follows appended lines only while the reader is at the bottom. The
saved-memory browser shows list/detail ownership; Sessions shows a surface that
fills the conversation region while keeping the header, prompt, and footer.

List and inspector cards cap their outer width at 128 cells, permission cards at
132, and inline tool cards at 100, or the available width if smaller. Budget
wrapped chrome before allocating viewport rows, fall back to close-only when no
body row fits, and check narrow terminals, not just a wide golden.

An open modal or transient handles its keys before the prompt, and a wheel event
over a modal must not scroll the hidden conversation. Every surface needs a
discoverable way to close, and its hints show only available actions. Don't give an
existing chord a second meaning in the same focus context; stay consistent with the
[public keybindings](../user-docs/mecatui/keybindings.md).

## Conversation and feedback

The conversation follows the tail while the reader is at the bottom; once they
scroll up, renders keep a logical reading anchor until they return. Use
`conversationView` rather than setting viewport offsets directly. Speaker labels,
hanging alignment, and spacing distinguish turns without color.

Collapse large tool arguments and results into cards and keep hidden content
inspectable. A preview must not flood scrollback or show more than the bounded,
display-safe projection the client received. The prompt rail shows the confirmed
session mode; the server posture badge is separate, and its high-risk variant uses
a fixed, contrast-checked danger style rather than a theme color.

Durable facts go in scrollback; advisories and transient activity go in the footer.
A failed action needs visible feedback, never a silent reset or an optimistic
success claim, and a notice never shows raw transport or credential material.

## Styling and terminal behavior

Use semantic slots from `theme.Palette` so a surface works in light and dark
themes. The fixed danger badge and the contrast-calculated selection foreground are
deliberate exceptions, not a model for widget-local colors. Check ANSI-stripped
rendering when a visual cue carries meaning.

Treat terminal features as capabilities: provide a keyboard path when mouse input
is off and a fallback when a terminal lacks enhanced keys, color, or graphics.
Display text goes through `cmd/mecatui/internal/terminaltext` sanitizing; never interpolate
untrusted server text into terminal control sequences.

## Verification

Keep tests offline. Test event-to-message mapping at the client boundary,
interaction and focus in `ui`, layout at wide and narrow sizes, and one light and
one dark theme for contrast-sensitive changes. For scrolling or overlays, test a
live update while the reader is off the tail or a modal is open, so a happy-path
snapshot cannot hide a navigation regression. Examples include
`cmd/mecatui/ui/scroll_test.go` and `cmd/mecatui/theme/theme_test.go`. Refresh the
`teatest` goldens with `task test:golden` only for intentional visual changes.

## Related

- [API surface](architecture/api-surface.md)
- [Performance tracking](perf-tracking.md), for the render-path benchmarks
- [`mecatui` user guides](../user-docs/mecatui/index.md)
- [Status line customization](../user-docs/mecatui/status-line.md)
