# Developing the `mecatui` terminal UI

**This page is for contributors designing and implementing `mecatui`.** It records
shared UI conventions and the boundaries that keep new interactions consistent.
It is not a command, keybinding, or feature reference for terminal users. The
[public `mecatui` guides](../user-docs/mecatui/index.md) own how to start, use,
customize, and troubleshoot the client.

When changing the TUI, update this page only for a reusable design rule or an
architectural boundary. Put user instructions in the existing public guide that
answers the user's task, but add them only when they help someone discover a
non-obvious action, avoid a consequential mistake, or recover from a problem.
Don't copy every overlay action or implementation detail into public docs. Keep
feature-specific behavior in its owning code and tests; keep decision rationale
in ADRs. Follow the documentation change review
when deciding whether either page needs an update.

## Client boundary

`mecatui` is a gRPC client, whether it connects to a separate server or hosts
one privately. The `client` package owns the transport and maps server events to
Bubble Tea messages. `ui` renders those messages and handles input; `theme` owns
semantic styling. Neither `ui` nor `theme` imports the engine, host application,
or generated contracts. Embedded-server composition stays in `cmd/mecatui/embed`
and the command entry point. Keep server decisions on the server and render its
reported state rather than inferring authority, resolved models, or placement in
the client.

Precommit provider recovery belongs to the server's active run, not a client-side
terminal retry loop. Keep failed-step retry an explicit `/retry` action; see
[provider resilience](architecture/observability.md#reliability--provider-resilience)
for the recovery policy and lifetime.

A new interaction should follow the same event and capability boundaries as an
existing one: do not show an unavailable control as if it were actionable, and
do not substitute a client guess for a server-confirmed result. See the
[API architecture](architecture/api-surface.md) for the wire boundary and
[permissions and posture](../user-docs/features/security-and-execution/permissions-and-posture.md)
for the operator-facing meaning of safety controls.

## Layout and navigation

The frame is a stack of header, conversation, optional transient regions,
prompt, and footer. `cmd/mecatui/ui/layout.go` derives that stack for rendering,
viewport sizing, and mouse hit testing. Add regions there so a transient shrinks
the conversation instead of displacing the footer. Measure the rendered frame
rather than maintaining a second set of row offsets.

Overlays that take over the conversation region own their interaction state and
size themselves from the geometry offered on each render. The parent owns their
placement and maps pointer coordinates into the rendered surface. Follow the
[`surface` contract](../cmd/mecatui/ui/surface.go) and its
[migration guidance](design/surface-migration-plan.md) when adding or converting
an overlay. A closed surface must not receive late results or leak keyboard and
wheel events into the conversation.

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
`ToolFailed` rather than selecting
those components in a surface. Settled successful rows suppress redundant status
text; failed rows consistently say `failed`. Extend its presentation table when
adding a tool;
do not duplicate argument decoding or tool-specific summaries in a surface.
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

For list-and-detail browsers, use a bounded list for stable item selection and
a separate bounded viewport for long detail. In wheel-enabled inventories,
wheel scrolling moves the list window independently of selection. Keyboard
navigation and clicking a row select and reveal it. Preserve the selected item
and list window when returning from detail; size both controls against the
current measured body instead of storing an independent row budget. Keep
detail scrolling within that viewport: follow appended result lines only while
its reader is at the bottom, and keep the reader's position when new content
arrives off-tail.
The saved-memory browser demonstrates list/detail ownership; Sessions demonstrates
the conversation-region fill placement for content that needs more room. Fill
surfaces use only the offered conversation region and keep the frame's header,
prompt, and footer.

Normal list and inspector cards cap their outer width at 128 cells or the
available width, whichever is smaller. Permission cards cap at 132 cells and
inline tool cards at 100. Derive content width from the actual styled frame;
check narrow terminals and wrapped rows, not just a wide golden. Budget
wrapped chrome and any overflow indicator before allocating viewport rows;
if no body row fits, use a close-only fallback.

Keep keyboard ownership visible. An open modal or transient handles its keys
before the prompt; a wheel event over a modal must not scroll the hidden
conversation. Provide a discoverable way to close a surface and show only
available actions in its hints. Keep common actions consistent with the
[public keybindings](../user-docs/mecatui/keybindings.md), rather than assigning
an existing chord a second meaning in the same focus context.

## Conversation and feedback

Preserve the reader's position while new output arrives. The conversation
follows the tail while at the bottom; when the reader scrolls up, subsequent
renders preserve a logical reading anchor until they return to the bottom. Use
`conversationView` rather than setting viewport offsets independently. Speaker
labels, hanging alignment, and spacing distinguish turns even without color.

Summarize large tool arguments and results in collapsed cards, and keep hidden
content inspectable. A preview must not flood scrollback or expose more than
the bounded, display-safe projection the client received. Keep safety and state
cues distinguishable without color: the prompt rail identifies the confirmed
session mode, while the server posture badge has separate prominence. The
high-risk posture badge uses a fixed, contrast-checked danger style rather than
a theme-controlled color.

Choose the lifetime of a notice deliberately. Put durable conversation facts in
scrollback; use the transient footer for advisories and activity whose outcome
belongs elsewhere. An error or failed action needs visible feedback, not a
silent reset or an optimistic success claim. Do not show raw transport or
credential material in a notice.

## Styling and terminal behavior

Use semantic slots from `theme.Palette` and derived styles so a surface works in
both light and dark themes. The fixed danger badge and contrast-calculated
selection foreground are intentional safety/legibility exceptions, not a model
for widget-local color constants. Preserve non-color cues for selection, status,
and warnings. Check ANSI-stripped rendering and compact geometry when a new
visual cue carries meaning.

Treat terminal features as capabilities, not assumptions. Provide a usable
keyboard path when mouse input is disabled and an appropriate fallback when a
terminal cannot report enhanced keys, color, or graphics. Terminal control
sequences and display text must go through the renderer's output and sanitizing
paths; never interpolate untrusted server text into terminal control sequences.

## Verification

Keep client and rendering tests offline. Test event-to-message behavior at the
client boundary, interaction and focus at the UI boundary, and layout at wide
and narrow terminal sizes. Exercise at least one light and one dark theme for
contrast-sensitive changes. For scrolling or overlays, test a live update while
the reader is off the tail or a modal is open, respectively, so a happy-path
snapshot cannot hide a navigation regression. Existing examples include
`cmd/mecatui/ui/scroll_test.go`, `toolcard_width_test.go`, and
`cmd/mecatui/theme/theme_test.go`.

For intentional visual changes, update the fixed-size `teatest` goldens with
`task test:golden`. Run focused tests while iterating and `task docs` for this
page. Follow the repository's full [verification gates](../AGENTS.md#build-and-test)
before a PR is ready.
