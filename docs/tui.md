# Developing the `mecatui` terminal UI

Cross-component rules for contributors changing `cmd/mecatui`. Use the
[scoped agent entry point](../cmd/mecatui/ui/AGENTS.md) to find the section for your
task. The [public guides](../user-docs/mecatui/index.md) own client usage.

Keep shared invariants and presentation rules here, API mechanics beside their
code, and one-off regression cases in tests.

## Respect client/server boundaries

`client` owns transport and maps server events to Bubble Tea messages; `ui` owns
input and rendering; `theme` owns semantic styling. `mecatui` is a gRPC client
whether its server is embedded or remote, so UI code must not assume locality.
Keep generated contracts, gRPC, host packages, and `engine/` imports out of `ui`
and `theme`. This boundary is a convention, not a lint rule.

Render server-reported authority, resolved models, placement, and allowed actions:

- Gate features on `client.Capabilities`; hide or explain unavailable actions.
- Use session rows' action capabilities, never ID shape, for copy, transcript,
  fork, rename, and delete. Failed actions must not rebind the prompt.
- Clear and fork rebind only after server confirmation of the successor session.
- Leave live provider retry to the server. After terminal failure, only explicit
  `/retry` starts another run. See the [recovery policy](architecture/observability.md).

The action map handles keys before the prompt textarea. Preserve unclaimed
`ctrl+a`, `ctrl+e`, and `ctrl+p` for line editing.

[Status-line customization](../user-docs/mecatui/status-line.md) receives
safe snapshots and returns semantic spans; the UI owns styling and geometry.
Only client settings can select its local executable, never a server or project.

Test event-to-message mapping at the client boundary and capability gating and
confirmed-state transitions in `ui`. Remote-observable changes require the
[wire API compatibility assessment](architecture/api-surface.md#wire-api-compatibility).

## Add or change a surface

Use the [`surface` contract](../cmd/mecatui/ui/surface.go) for conversation-region
overlays:

| Surface owns | Parent owns |
|---|---|
| Interaction state, transitions, async validation, and view caches | Lifetime, placement, and frame-scoped pointer mapping |
| Sizing from each render offer | Root-state effects, requested through narrow intents |

Capture dependencies at open. Dynamic theme or keybinding updates require a
UI-wide design, not a component-local exception. Root effects must not require
the parent to inspect a concrete surface type. When an intent replaces a surface,
tear down the outgoing instance and stop its close/refocus processing before it
can affect the replacement.

Correlate async results with the surface instance and active request; reject stale
results, including snapshots started before a confirmed mutation. Closing a view
does not undo submitted server operations. Widget receipts, including private
clipboard results, also need focus ownership so delayed input cannot edit another
field or reopened view. Pass unrelated messages to the root reducer even while
pending or compact.

Before keys, wheel, paste, or marked async widget input, the parent prepares the
current `Render` offer. Only `View` publishes pointer hits; resize and undisplayed
preparation invalidate hits and placement metrics. Surfaces size, parents place
and map pointer coordinates.

Modals and transients handle keys before the prompt. Modal wheel input must not
scroll the hidden conversation; modal input must not activate underlying controls.
Reject late results after close. Provide a discoverable close action and advertise
only available actions. Keep chords unambiguous within a focus context and
consistent with [public keybindings](../user-docs/mecatui/keybindings.md).

For implementation patterns, see
[`sessions_surface.go`](../cmd/mecatui/ui/sessions_surface.go) for root intents and
[`schedule_create.go`](../cmd/mecatui/ui/schedule_create.go) for widget receipts.
Exercise close/reopen with late results, read/write ordering, replacement without
outgoing refocus, and unrelated root updates while pending. Examples:
[`sessions_surface_test.go`](../cmd/mecatui/ui/sessions_surface_test.go) and
[`modal_lifecycle_test.go`](../cmd/mecatui/ui/modal_lifecycle_test.go).

## Build a picker, browser, or form

Use [`presentListRow`](../cmd/mecatui/ui/list_row.go) for inventory rows and
[`internal/bounded`](../cmd/mecatui/ui/internal/bounded) for navigation. Translate
keys to `bounded.Move` in `ui`; keep key-event and keymap dependencies out of
`internal/bounded`.

### Lists and detail views

Follow [`models_surface.go`](../cmd/mecatui/ui/models_surface.go) for shared row
presentation:

| Element | Convention |
|---|---|
| Selected rows | `spinner` accent across all wrapped rows |
| Unselected rows | `muted` style |
| Cursor | `▶` on the selected item's first visible physical row only |
| Current/default status | Separate cells, distinct from the cursor |
| Gutter and markers | Owned by `presentListRow`, not embedded in item text |

Surfaces own item semantics and activation. Keep unavailable entries visible when
they explain choices, but exclude them from selection and activation.

Use separate bounded list and detail viewports. Wheel scroll changes the list
window without selecting; keyboard navigation and enabled-row clicks select and
reveal without activating. Consequential actions need separate explicit
confirmation. Back preserves the parent's selected identity and window. Detail
follows appended lines only while the reader is at the bottom. See
[`usermodel.go`](../cmd/mecatui/ui/usermodel.go) for list/detail ownership.

Use fill placement for multi-level management browsers and centered cards for
focused pickers and inspectors; keep placement consistent across local views.
If warnings or configuration crowd out history, keep a concise summary beside it
and put full details in a scrollable view. Navigation acts only on the visible
browsing region.

Give lists a blank row above and below rows and overflow indicators. Budget that
spacing, but reduce decoration before useful content in small terminals. Inline
search pickers need several visible choices plus overflow indicators; a six-row
window keeps multiple matches visible with both indicators present.

### Forms

Keep fields together in traversal order, followed directly by Cancel and explicit
submit. Put previews, explanations, and validation feedback below the actions,
separated by a blank row. Use normal theme text for editable values, muted text
for supporting information, error styling for failures, and a non-color focus
marker.

Scroll the field stack enough to reveal the focused control while keeping actions
and feedback visible. Fall back to close-only when the focused control cannot fit.
See [`schedule_create.go`](../cmd/mecatui/ui/schedule_create.go) and
[`schedule_create_test.go`](../cmd/mecatui/ui/schedule_create_test.go) for traversal
and focus. Check keyboard-only operation and list selection/back-state behavior;
[`models_surface_test.go`](../cmd/mecatui/ui/models_surface_test.go) covers list
interaction.

## Size and render content safely

[`layout.go`](../cmd/mecatui/ui/layout.go) owns the frame stack: header,
conversation, transients, prompt, and footer. Add regions there so transients
shrink the conversation rather than displace the footer. Rendering, viewport
sizing, and hit testing must derive from the same rendered frame, not independent
row offsets.

Card outer widths are capped at the available width and these ceilings:

| Card | Maximum cells |
|---|---|
| List or inspector | 128 |
| Permission | 132 |
| Inline tool | 100 |

Ceilings are not targets. Small finite pickers use content-sized widths measured
from the complete inventory and chrome, including gutters, frames, warnings, and
captured key hints. Large inventories and text inspectors can use more width for
readability.

At a fixed geometry offer, keep card dimensions stable across selection and window
changes. Recalculate on resize; wrap or clip smaller offers. Budget wrapped chrome
before viewport rows. When no body row fits, use close-only rendering that consumes
input without activating hidden actions. Preserve browsing state for restoration
at larger sizes.

Sanitize display text through
[`terminaltext`](../cmd/mecatui/internal/terminaltext); never interpolate untrusted
server text into terminal control sequences. Sanitizing preserves tabs: expand
them before measuring, wrapping, or clipping, using `ExpandStyleTabs` for styled
content or `ExpandTabs` with the renderer's explicit width. The
[tab helpers](../cmd/mecatui/internal/terminaltext/tabs.go) own default resolution.

Subtract indentation, rails, borders, and padding before wrapping. Test fully
styled/framed row widths, including tabs, narrow terminals, and resize before
input. Check compact input isolation and restored browsing state, not only wide
goldens. Refresh `teatest` goldens with `task test:golden` only for intentional
visual changes. Examples: [`display_tabs_test.go`](../cmd/mecatui/ui/display_tabs_test.go)
and [`effort_surface_test.go`](../cmd/mecatui/ui/effort_surface_test.go).

## Change conversation or tool presentation

Use [`conversationView`](../cmd/mecatui/ui/conversation_view.go) rather than direct
viewport offsets. Follow the tail at the bottom; preserve a logical reading
anchor after scrolling up. Test live updates off the tail and under an open modal,
not just snapshots; see [`scroll_test.go`](../cmd/mecatui/ui/scroll_test.go).

Use speaker labels, hanging alignment, and spacing to distinguish turns without
color. Both views use accented, bold `▶ you`, `◀ mecatl`, and `◆ system` labels
with neutral bodies. Recovery and tool/hook markers retain their own lifecycle
or warning meaning.

### Tool calls and delegated traces

Use `ToolIntent` and `PresentToolLine` from
[`renderfmt/toolcall.go`](../cmd/mecatui/internal/renderfmt/toolcall.go) for shared
tool vocabulary across conversation rows, inspectors, and expanded delegated
traces. Use its typed lifecycle states and extend its presentation table for new
tools instead of duplicating argument decoding or summaries in surfaces.

| Lifecycle | Presentation |
|---|---|
| Pending | `…` with `toolName` and a readable status label |
| Settled success | `✓` with `toolOk`; glyph and name without redundant success text |
| Failure | `✗` with `toolErr` and readable `failed` label |

Surfaces own styling, wrapping/clipping, selection, and pointer geometry. List rows
stay single-line, child inspector summaries clip at viewport width, and expanded
traces wrap at lane width.

Derive status from call/result lifecycle, never output text. Delegated previews
are bounded: derive intent at `tool.call` and retain it through `tool.result`
updates. Malformed/truncated JSON falls back to the tool name; never infer a path
from result text, including numbered Read output. Child traces stay per-lane
capped and cannot recover full arguments; parent inspector arguments and results
remain independently inspectable.

Default view aggregates settled adjacent visible Tool, Subagent, and Team calls,
then shows active cards in their original relative order. Presentation adjacency,
not dispatch or concurrency, determines groups. Detail view restores settled
one-line summaries in source order; arguments/results belong in the Toolcalls
inspector. Keep one blank row between response blocks/groups and none between
calls within a group. See
[`toolline_integration_test.go`](../cmd/mecatui/ui/toolline_integration_test.go)
and [`tool_groups_test.go`](../cmd/mecatui/ui/tool_groups_test.go).

### Transcript projection and feedback

Use the server's human-display projection for snapshots. Omit background-work
and no-progress continuations only with explicit stored harness provenance;
preserve user/legacy lookalikes, compaction summaries, and scheduled deliveries.
Stored model conversation and event replay remain unchanged.

Collapse large arguments/results into cards, retaining inspector access and staying
within received display-safe bounds. The prompt rail shows confirmed session mode;
the separate server posture badge uses fixed, contrast-checked danger styling for
its high-risk variant.

Durable facts go in scrollback; advisories and transient activity go in the footer.
Failed actions need visible feedback, not silent resets or optimistic success.
Notices must not expose raw transport or credential material.

## Apply themes and terminal capabilities

Use theme-owned semantic styles. Actor marker styles affect only glyphs; labels,
bodies, reasoning, statistics, and outcomes have independent styles. Actor rails
stay within their message blocks; run rails remain separate. Markdown retains
its dedicated palette and syntax styling. Custom themes derive transcript defaults
from existing palette slots, including when overriding a built-in name. Theme
changes must replace the renderer and its caches together; see
[`switchTheme`](../cmd/mecatui/ui/update.go).

Fixed danger badges and contrast-calculated selection foregrounds are deliberate
exceptions, not precedents for widget-local colors. Preserve keyboard operation
without a mouse and fallbacks without enhanced keys, color, or graphics.

For contrast-sensitive changes, check light and dark themes and ANSI-stripped
meaningful cues. Examples: [`theme_test.go`](../cmd/mecatui/theme/theme_test.go)
and [`transcript_theme_test.go`](../cmd/mecatui/ui/transcript_theme_test.go).

## Related

- [Performance tracking](perf-tracking.md), for render-path benchmarks
