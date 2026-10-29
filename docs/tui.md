# Developing the `mecatui` terminal UI

Use this guide when changing `cmd/mecatui`, especially the `ui` package. It owns
cross-component conventions and the checks needed to preserve them. Agents working
in `ui` should start with the [scoped entry point](../cmd/mecatui/ui/AGENTS.md).
The [public `mecatui` guides](../user-docs/mecatui/index.md) own how to start, use,
customize, and troubleshoot the client.

Add guidance here when it changes how the next contributor should implement or
verify a class of UI changes. Keep precise API contracts and library mechanics
beside their code, and one-off regression details in tests.

## Start here

Read the client/server boundaries, then follow the section for your change.
Paths in the table are relative to `cmd/mecatui/`.

| Your change | Read | Main reuse points |
|---|---|---|
| Server events, capabilities, or session actions | [Client/server boundaries](#respect-clientserver-boundaries) | `client/`, `ui/update.go` |
| Overlay lifecycle or asynchronous input | [Surfaces](#add-or-change-a-surface) | `ui/surface.go` |
| Picker, browser, or form interaction | [Lists and forms](#build-a-picker-browser-or-form) | `ui/list_row.go`, `ui/internal/bounded/` |
| Frame layout, sizing, wrapping, or pointer geometry | [Safe rendering](#size-and-render-content-safely) | `ui/layout.go`, `internal/terminaltext/` |
| Transcript, tool calls, or notices | [Conversation and tools](#change-conversation-or-tool-presentation) | `ui/conversation_view.go`, `internal/renderfmt/` |
| Colors or terminal-dependent behavior | [Themes and capabilities](#apply-themes-and-terminal-capabilities) | `theme/` |

`client` owns transport and maps server events to Bubble Tea messages. `ui` renders
those messages and handles input; `theme` owns semantic styling. `embed` assembles
the in-process server through `internal/app`, the composition `mecated` uses.

## Respect client/server boundaries

`mecatui` is always a gRPC client. By default it hosts a server in-process on a
private UNIX socket; `mecatui connect ADDRESS` dials an external server instead.
The client path is the same either way. Nothing in the UI may assume the server
is local.

Only `client`, `embed`, and the command entry point import generated contracts,
gRPC, or host packages. `ui` and `theme` import none of them and no `engine/`
package. This is a convention rather than a lint rule, so check imports when
adding a dependency.

Render server-reported state rather than inferring authority, resolved models,
placement, or allowed actions:

- Gate feature affordances on `client.Capabilities`. Hide or explain unavailable
  features instead of showing dead controls.
- Use session rows' server-authored action capabilities for copy, transcript view,
  fork, rename, and delete. Session ID shape does not grant authority. The server
  revalidates actions; failures must not rebind the prompt to another session.
- Rebind after clear or fork only when the server confirms the successor session.
- Short-handle resolution for `mecatui debug TARGET` is local to the caller's
  inventory. Only the exact session ID crosses to the server, which decides access.
- Leave live provider retry to the server. A terminal failure must not make the
  client start a second run; `/retry` is the explicit action. See the
  [recovery policy](architecture/observability.md).

Key events go through the client's action map first. Unclaimed keys reach the
prompt textarea, which keeps `ctrl+a`, `ctrl+e`, and `ctrl+p` for line editing.

Status-line customization is client-owned: `customization.Source` receives
display-safe snapshots and publishes semantic spans; the UI owns theme, clipping,
and alignment. Its settings live only in the client's settings file. A remote
server or project must never select a local executable through this seam.

**Verify:** test event-to-message mapping at the client boundary and capability
gating or confirmed-state transitions in `ui`. For remote-observable changes,
follow the [wire API compatibility policy](architecture/api-surface.md#wire-api-compatibility).

## Add or change a surface

**Use:** the [`surface` contract](../cmd/mecatui/ui/surface.go) for overlays that
own the conversation region. Keep internal components narrow; introduce a shared
contract when comparable components already share behavior and it simplifies the
code, rather than for a speculative capability. Justify concrete type assertions
in generic UI flow with the component's genuinely unique lifecycle.

### Keep ownership explicit

| Surface owns | Parent owns |
|---|---|
| Interaction state and transitions | Modal lifetime and teardown routing |
| Async result validation and view caches | Placement and frame-scoped pointer mapping |
| Sizing from each render's geometry offer | Effects that change root-owned state |

Capture presentation and operating dependencies when the surface opens. Dynamic
theme or keybinding updates need a deliberate UI-wide design covering all affected
components. Emit a narrow intent for a root-owned effect instead of making the
parent inspect the surface's concrete type. If an intent replaces the surface,
tear down the outgoing instance and stop its close/refocus processing before it
can affect the replacement.

Correlate async results with both the surface instance and its active request.
Discard results the open surface does not own. Include read/write ordering:
a snapshot started before a mutation cannot replace its confirmed result.
Closing a view does not undo an operation already submitted to the server.
Widget command receipts, including private clipboard results, also carry surface
and focus ownership so delayed input cannot edit another field or reopened view.
Pass unrelated async messages to the root reducer, even while a form is pending
or compact.

### Route input against current geometry

Before keys, wheel events, paste, or marked async widget input, the parent prepares
the open surface's current `Render` offer. Only `View` publishes pointer hits.
Resize and undisplayed preparation invalidate both hits and placement metrics.
The surface sizes itself from each offer; the parent places it and maps pointer
coordinates.

An open modal or transient handles keys before the prompt. Wheel events over a
modal must not scroll the hidden conversation. A closed surface must not receive
late results, and modal keyboard and wheel input must not leak into the
conversation. Provide a discoverable close action and hints showing only
available actions. Keep chords unambiguous within a focus context and consistent
with the [public keybindings](../user-docs/mecatui/keybindings.md).

**Start from:** [`sessions_surface.go`](../cmd/mecatui/ui/sessions_surface.go) for
root-owned actions through intents, or
[`schedule_create.go`](../cmd/mecatui/ui/schedule_create.go) for widget receipt
ownership. Copy the relevant pattern rather than an entire component.

**Verify:** close/reopen with a late result, a read overtaken by a write, resize
before input, replacement without outgoing refocus, and unrelated root updates
while pending. See [verification](#verify-the-change) for representative tests.

## Build a picker, browser, or form

**Use:** [`presentListRow`](../cmd/mecatui/ui/list_row.go) for selectable inventory
rows and [`internal/bounded`](../cmd/mecatui/ui/internal/bounded) for bounded
navigation. Translate shared navigation keys to `bounded.Move` in `ui`; keep
`internal/bounded` independent of UI key-event and keymap dependencies.

### Lists and detail views

Follow `/models` for row presentation:

| Element | Convention |
|---|---|
| Selected rows | `spinner` accent style across all wrapped rows |
| Unselected rows | `muted` style |
| Cursor | `▶` on the selected item's first visible physical row only |
| Current/default status | Separate status cells, distinct from the cursor |
| Gutter and markers | Owned by `presentListRow`, not embedded in item text |

The surface owns item semantics and activation. Keep unavailable entries visible
when they explain the choices, but exclude them from selection and activation.

List-and-detail browsers use a bounded list and a separate bounded detail
viewport. Wheel scrolling moves the list window without changing selection.
Keyboard navigation and clicking an enabled row select and reveal it; selection
alone does not activate it. Consequential activation requires separate explicit
confirmation through keyboard, pointer, or both as appropriate to the widget.
Back returns one level and preserves the parent's selected identity and window.
Detail follows appended lines only while the reader is at the bottom.

Use fill placement for multi-level management browsers whose inventories and
history benefit from the conversation region. Use centered cards for focused
pickers and inspectors. Keep placement consistent across a browser's local views.
If configuration or warnings would crowd out history, keep a concise status
summary beside it and put full details in a separate scrollable view. Selection
keys and wheel input operate only on the visible browsing region.

Give lists a blank row above and below their rows and overflow indicators. Include
spacing in the geometry budget; reduce decoration before hiding useful content.
Inline search pickers should show several choices at once. A six-row window can
show multiple matches with both above/below indicators present.

### Forms

Keep persistent controls together in traversal order, with Cancel and explicit
submit directly after the last field. Put previews, explanatory text, and
validation feedback below the actions, separated by a blank row. Use normal theme
text for editable values, muted text for supporting information, and the error
style for failures. Focus needs a non-color marker.

At smaller sizes, scroll the field stack just enough to reveal the focused
control while keeping actions and feedback visible. Use a close-only fallback
when the focused control cannot fit.

**Start from:** [`models_surface.go`](../cmd/mecatui/ui/models_surface.go) for
inventory rows, [`usermodel.go`](../cmd/mecatui/ui/usermodel.go) for list/detail
ownership, and [`schedule_create.go`](../cmd/mecatui/ui/schedule_create.go) for form
traversal. Sessions demonstrates fill placement while retaining the header,
prompt, and footer.

**Verify:** selection versus activation, disabled rows, wheel/selection
independence, back-state preservation, focus visibility, and compact input
isolation. Include keyboard-only operation.

## Size and render content safely

**Use:** [`layout.go`](../cmd/mecatui/ui/layout.go) for the shared frame stack:
header, conversation, optional transient regions, prompt, and footer. Add regions
there so transients shrink the conversation instead of displacing the footer.
Rendering, viewport sizing, and mouse hit testing must derive from the rendered
frame rather than separate row offsets.

Card outer widths have these ceilings, further limited by available width:

| Card | Maximum cells |
|---|---|
| List or inspector | 128 |
| Permission | 132 |
| Inline tool | 100 |

These are ceilings, not targets. Small finite pickers prefer a content-sized
width measured from the complete inventory and fixed chrome, including gutters,
frame, warnings, and captured key hints. Large inventories and text inspectors
can use more available width when it improves readability.

Keep dimensions stable as selection or the visible window changes at a fixed
geometry offer. Measure all items, not only visible rows. Recalculate on resize;
wrap or clip when the offer is smaller than the preferred size. Budget wrapped
chrome before allocating viewport rows. If no body row fits, use a close-only
fallback that consumes input without activating hidden actions. Preserve browsing
state so a larger offer restores it.

Display text goes through
[`terminaltext`](../cmd/mecatui/internal/terminaltext). Never interpolate untrusted
server text into terminal control sequences. Sanitizing preserves tabs, so expand
them before measuring, wrapping, or clipping. Use `terminaltext.ExpandStyleTabs`
for content receiving a Lip Gloss style, or `terminaltext.ExpandTabs` with the
owning renderer's explicit tab width. The
[tab helpers](../cmd/mecatui/internal/terminaltext/tabs.go) own the Lip Gloss
default-resolution details.

Wrap against the content budget after subtracting indentation, rails, borders,
and padding. Measure the fully styled and framed rows passed to the viewport;
raw text width misses expansion and framing added later.

**Verify:** stable sizing while navigating, wide and narrow terminals, resize
before render/input, wrapped chrome, tabbed text, and fully framed row widths.
A wide golden alone does not establish correct geometry.

## Change conversation or tool presentation

**Use:** [`conversationView`](../cmd/mecatui/ui/conversation_view.go) for reading
position instead of setting viewport offsets directly. Follow the tail while the
reader is at the bottom; preserve a logical reading anchor after they scroll up.

Speaker labels, hanging alignment, and spacing distinguish turns without color.
Both views use accented, bold `▶ you`, `◀ mecatl`, and `◆ system` labels for user
prompts, model responses, and ordinary harness notices; bodies remain neutral.
Recovery and tool/hook outcome markers retain their own warning or lifecycle
meaning.

### Tool calls and delegated traces

Use [`renderfmt/toolcall.go`](../cmd/mecatui/internal/renderfmt/toolcall.go) for
the single-line vocabulary across settled conversation rows, `/toolcalls` lists
and child inspectors, and expanded Subagent/Parallel/Team traces (including F6
Agents):

| API | Responsibility |
|---|---|
| `ToolIntent(name, arguments)` | Sanitized call-side intent |
| `ArgumentOrder(name)` | Known-field ordering in the full-arguments inspector |
| `PresentToolLine(name, intent, state)` | Bounded typed `ToolLine` with state-owned glyph, status label, and semantic style slot |

Use the typed states `ToolRunning`, `ToolAwaitingResult`, `ToolFinalizing`,
`ToolFinalizingFailed`, `ToolDelegatedPending`, `ToolSucceeded`, and `ToolFailed`
instead of choosing presentation components in a surface. Extend the presentation
table when adding a tool; keep tool-specific decoding and summaries out of
surfaces.

| Lifecycle | Presentation |
|---|---|
| Pending | `…` with `toolName`; retain a readable status label |
| Settled success | `✓` with `toolOk`; glyph and name without redundant success text |
| Failure | `✗` with `toolErr`; readable `failed` label |

Surfaces own ANSI styles, clipping, wrapping, selection, and pointer geometry.
List rows stay single-line, child inspector summaries clip at viewport width,
and expanded traces wrap at their lane width.

Derive status from the observed call/result lifecycle, never output text.
Delegated events carry only bounded argument and result previews: derive and
retain intent at `tool.call`, then keep it when `tool.result` changes status or
the latest preview. Malformed or truncated JSON falls back to the tool name.
Never infer a path from a result body, including numbered Read output. Child
traces remain per-lane capped and cannot recover full arguments; parent inspector
arguments and results remain independently inspectable.

In the default view, render one aggregate for settled adjacent visible Tool,
Subagent, and Team calls, followed by active cards in their original relative
order. Presentation adjacency defines grouping, not dispatch or concurrency.
The detail view restores settled calls as one-line summaries in source order;
arguments and results belong in the Toolcalls inspector. Keep one blank row
between response blocks or groups and none between calls in a tool group.

### Transcript projection and feedback

Snapshot loads use the server's human-display projection. It omits known
background-work and no-progress control continuations only with explicit stored
harness provenance. Preserve user/legacy lookalikes, compaction summaries, and
scheduled deliveries. Stored model conversations and event replay are unchanged.

Collapse large tool arguments and results into cards while keeping hidden content
inspectable. Previews stay within the bounded, display-safe projection the client
received. The prompt rail shows the confirmed session mode; the server posture
badge is separate. Its high-risk variant uses a fixed, contrast-checked danger
style rather than a theme color.

Durable facts go in scrollback; advisories and transient activity go in the footer.
Failed actions need visible feedback, never a silent reset or optimistic success.
Notices must not show raw transport or credential material.

**Verify:** live updates while off the tail, source-order grouping, lifecycle
status independent of result text, bounded malformed previews, inspector access,
and visible failure feedback.

## Apply themes and terminal capabilities

**Use:** theme-owned semantic styles so surfaces work in light and dark themes.
Transcript actor glyph constants are separate from
`transcript{LLM,Tool,User,Harness}{Marker,Rail,Text}` styles. Marker styles own
only the actor symbol. Body and label text, reasoning, statistics, and outcome
indicators have independent styles. User prompts and LLM responses use their
actor's rail beside the body, within each message block. `transcriptRunRail`
remains separate; Markdown keeps its dedicated palette and syntax styles.

Built-in Aztec and Solar have separate dark/light transcript recipes. Custom
themes derive transcript defaults from existing palette slots, including when
overriding a built-in name. Startup background detection selects built-in Solar
only when no explicit theme is supplied on a real TTY. `switchTheme` replaces the
renderer and its caches so baked-in styles change together.

The fixed danger badge and contrast-calculated selection foreground are deliberate
exceptions, not patterns for widget-local colors. Terminal features are
capabilities: provide a keyboard path without mouse input and fallbacks without
enhanced keys, color, or graphics.

**Verify:** one light and one dark theme for contrast-sensitive changes, plus
ANSI-stripped rendering whenever a visual cue carries meaning.

## Verify the change

Keep tests offline and isolated from operator state. Choose focused tests for the
changed interaction and its direct integration boundary before running repository
gates. These are starting points, not a requirement to run every file:

| Change | Representative tests |
|---|---|
| Surface ownership and late results | [`sessions_surface_test.go`](../cmd/mecatui/ui/sessions_surface_test.go), [`models_request_token_test.go`](../cmd/mecatui/ui/models_request_token_test.go) |
| Geometry preparation and pointer lifetime | [`modal_lifecycle_test.go`](../cmd/mecatui/ui/modal_lifecycle_test.go), [`hit_regions_test.go`](../cmd/mecatui/ui/hit_regions_test.go) |
| Lists, navigation, and compact fallback | [`models_surface_test.go`](../cmd/mecatui/ui/models_surface_test.go), [`effort_surface_test.go`](../cmd/mecatui/ui/effort_surface_test.go) |
| Form focus and widget receipts | [`schedule_create_test.go`](../cmd/mecatui/ui/schedule_create_test.go) |
| Width, wrapping, and tabs | [`layout_test.go`](../cmd/mecatui/ui/layout_test.go), [`display_tabs_test.go`](../cmd/mecatui/ui/display_tabs_test.go) |
| Reading anchors and live updates | [`scroll_test.go`](../cmd/mecatui/ui/scroll_test.go) |
| Tool vocabulary and grouping | [`toolline_integration_test.go`](../cmd/mecatui/ui/toolline_integration_test.go), [`tool_groups_test.go`](../cmd/mecatui/ui/tool_groups_test.go) |
| Theme contrast and transcript styling | [`theme_test.go`](../cmd/mecatui/theme/theme_test.go), [`transcript_theme_test.go`](../cmd/mecatui/ui/transcript_theme_test.go) |

Test live updates while the reader is off the tail or a modal is open; snapshots
alone can hide navigation regressions. Refresh `teatest` goldens with
`task test:golden` only for intentional visual changes. Follow the repository-root
[build and test instructions](../AGENTS.md#build-and-test) for full gates.

## Related

- [API surface](architecture/api-surface.md)
- [Performance tracking](perf-tracking.md), for render-path benchmarks
- [`mecatui` user guides](../user-docs/mecatui/index.md)
- [Status line customization](../user-docs/mecatui/status-line.md)
