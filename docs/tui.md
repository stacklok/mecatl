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
and pointer mapping. Before keys, wheel events, paste, or marked asynchronous
widget input on an open surface, the parent prepares its current Render offer;
only View publishes pointer hits. Resize and
undisplayed preparation invalidate both hits and placement metrics. A surface
captures its presentation and operating dependencies when it opens. Introduce
dynamic theme or keybinding updates only with a deliberate UI-wide design that
covers every affected component.
Follow the [`surface` contract](../cmd/mecatui/ui/surface.go) when adding or
converting an overlay. A closed surface must not receive late results or leak
keyboard and wheel events into the conversation.

Keep surface-specific state, transitions, asynchronous result validation, and view
caches on the surface. The parent owns modal lifetime, placement, frame-scoped
pointer mapping, and effects that change root-owned state. A surface that needs a
root-owned effect emits a narrow intent; it does not require the parent to inspect
its concrete type. Correlate asynchronous results with the surface instance and
its active request, then discard results the open surface does not own. Request
correlation must also cover read/write ordering: a snapshot started before a
mutation cannot replace its confirmed result. Closing a view does not undo an
operation already submitted to the server. When an intent replaces a surface,
tear down the outgoing instance and stop its common close/refocus processing
before it can affect the replacement. Widget command receipts, including private
clipboard results, carry surface and focus ownership so delayed input cannot edit
a different field or reopened surface. Pass unrelated asynchronous messages to
the root reducer, including while a form is pending or compact.

The `ui` package is large, so keep its internal components narrow and their
contracts explicit. Prefer a shared contract when all comparable components use
the same behavior. Treat a concrete type assertion in generic UI flow as a smell:
justify the component's genuinely unique lifecycle before adding one. Do not add
an interface for a speculative future capability; introduce it when the shared
behavior exists and the contract can make the code simpler.

Selectable inventories use `presentListRow` for the cursor marker, status cells,
and selected-row styling. Follow `/models`: use the `spinner` accent style for
selected rows and `muted` for unselected rows. The `▶` cursor appears only on the
first visible physical row of the selected item; selected styling covers all its
wrapped rows. Optional current or default status cells remain distinct from the
cursor. `presentListRow` owns the gutter and cursor/status markers; surfaces must
not embed those markers or their padding in item text. Let the surface own item
semantics and activation.
Keep unavailable entries visible when that explains the available choices, but
exclude them from selection and activation. Translate shared navigation keys to
`bounded.Move` values in the `ui` package. Keep `internal/bounded` independent
of UI key-event and keymap dependencies.
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
selection. Keyboard navigation and clicking an enabled row select and reveal it;
selection alone does not activate it. Returning from detail preserves the selected
item and list window, and detail follows appended lines only while the reader is
at the bottom. Consequential activation requires a separate explicit confirmation
using keyboard, pointer, or both as appropriate to the widget. The saved-memory
browser shows list/detail ownership; Sessions shows a surface that fills the
conversation region while keeping the header, prompt, and footer.

Choose fill placement for multi-level management browsers whose inventories and
history benefit from the conversation region; use centered cards for focused
pickers and inspectors. Keep the placement consistent through a browser's local
views. Back returns one level and preserves the parent's selected identity and
window. If configuration or warnings would crowd history out of the viewport,
keep a concise status summary beside the history and put complete details in a
separate scrollable view. Selection keys and wheel input operate only on the
visible browsing region.

List and inspector cards cap their outer width at 128 cells, permission cards at
132, and inline tool cards at 100, or the available width if smaller. These are
ceilings, not target sizes. Small finite pickers should prefer a content-sized
width measured from the complete inventory and fixed chrome, including gutters,
frame, warnings, and captured key hints. Large inventories and text inspectors
can use more of the available width when it improves readability.

Keep card dimensions stable while selection or the visible window changes at a
fixed geometry offer. Measure all items when choosing a content-sized width,
not just the visible rows. Recalculate for terminal resize; wrap or clip content
when the offer is smaller than its preferred size. Budget wrapped chrome before
allocating viewport rows, fall back to close-only when no body row fits, and
check narrow terminals as well as a wide golden. A compact fallback must consume
input without activating hidden actions; preserve browsing state so a larger
geometry offer can restore it.

An open modal or transient handles its keys before the prompt, and a wheel event
over a modal must not scroll the hidden conversation. Every surface needs a
discoverable way to close, and its hints show only available actions. Don't give an
existing chord a second meaning in the same focus context; stay consistent with the
[public keybindings](../user-docs/mecatui/keybindings.md).

## Conversation and feedback

The conversation follows the tail while the reader is at the bottom; once they
scroll up, renders keep a logical reading anchor until they return. Use
`conversationView` rather than setting viewport offsets directly. Speaker labels,
hanging alignment, and spacing distinguish turns without color. User prompts,
model responses, and ordinary harness notices use accented, bold `▶ you`,
`◀ mecatl`, and `◆ system` labels in both views; their bodies remain neutral.
Recovery and tool or hook outcome markers retain their own warning or lifecycle
meaning.

In the default view, render one aggregate for settled adjacent visible
Tool, Subagent, and Team calls, followed by any active cards in their original
relative order. Presentation adjacency, rather than dispatch or concurrency,
defines the group. The detail view restores settled calls as one-line summaries
in source order; keep arguments and results in the Toolcalls inspector. Preserve
one blank row between response blocks or groups, with none between calls in a
tool group.

Snapshot transcript loads use the server's human-display projection, which omits
known background-work and no-progress control continuations only with explicit
stored harness provenance. Preserve user/legacy lookalikes, compaction summaries
and scheduled deliveries; the stored model conversation and event replay remain
unchanged.

Collapse large tool arguments and results into cards and keep hidden content
inspectable. A preview must not flood scrollback or show more than the bounded,
display-safe projection the client received. The prompt rail shows the confirmed
session mode; the server posture badge is separate, and its high-risk variant uses
a fixed, contrast-checked danger style rather than a theme color.

Durable facts go in scrollback; advisories and transient activity go in the footer.
A failed action needs visible feedback, never a silent reset or an optimistic
success claim, and a notice never shows raw transport or credential material.

## Styling and terminal behavior

Use theme-owned semantic styles so a surface works in light and dark themes.
Transcript actor glyph constants are separate from
`transcript{LLM,Tool,User,Harness}{Marker,Rail,Text}` styles. Marker styles own
only the actor symbol; body and label text, reasoning, statistics, and outcome
indicators have independent styles. User prompts and LLM responses use their
actor's rail style beside the body; rails stay within each message block.
`transcriptRunRail` remains separate. Markdown keeps its dedicated palette and
syntax styles.

Built-in Aztec and Solar have separate dark/light transcript recipes. Custom
themes derive transcript defaults from existing palette slots, including when
they override a built-in name. Startup background detection selects built-in
Solar only when no explicit theme is supplied on a real TTY; `switchTheme`
replaces the renderer and its caches so baked-in styles change together.

The fixed danger badge and the contrast-calculated selection foreground are
deliberate exceptions, not a model for widget-local colors. Check ANSI-stripped
rendering when a visual cue carries meaning.

Treat terminal features as capabilities: provide a keyboard path when mouse input
is off and a fallback when a terminal lacks enhanced keys, color, or graphics.
Display text goes through `cmd/mecatui/internal/terminaltext` sanitizing; never interpolate
untrusted server text into terminal control sequences.

Expand literal tabs to spaces before measuring, wrapping, or clipping display text.
Sanitizing preserves tabs, and ANSI width and wrapping helpers do not account for
Lip Gloss's later tab expansion (four spaces by default). Expanding tabs only in
`Style.Render` can widen an already-wrapped line; a surrounding rail or card can
then pad every row to that oversized width. Use `terminaltext.ExpandStyleTabs`
for content that will receive a Lip Gloss style, or `terminaltext.ExpandTabs`
with the owning renderer's explicit tab width. These helpers live in
`cmd/mecatui/internal/terminaltext/tabs.go`, separate from sanitization and
Markdown processing. The style helper resolves the implicit default as well as
explicit tab settings; the raw `GetTabWidth()` getter does not distinguish an
unset value from an explicit zero.

Wrap against the content budget after subtracting outer indentation, rails,
borders, and padding. Verify widths on the fully styled and framed rows passed to
the viewport, including tabbed text and terminal resizes. Measuring only the raw
text misses expansion and framing added later in the render path.

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
