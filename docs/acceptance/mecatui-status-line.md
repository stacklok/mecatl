# Extensible mecatui status line — acceptance plan

**Phase:** capability — local TUI presentation extension
**Status:** in-progress, 2026-08-27. Updated after the template, shared-markup, and mecatui-settings decisions.
**ADR:** [ADR-0247](../adr/0247-mecatui-status-line.md) — user-global status-line source with a UI-agnostic boundary.
**Accumulator branch:** `acc/mecatui-status-line` (off `main`).

The smallest useful capability makes both the current mecatui header and status/usage row shipped default templates. Operators may replace either surface through the user-global mecatui settings document with responsive templates or one local command. Both paths consume the same status input and produce the same safe, theme-aware `StatusML` document. The keyboard-help row remains mecatui chrome, and the renderer preserves mandatory header safety/navigation indicators outside template control.

The feature is local to mecatui: it adds no engine, provider, gRPC, or server configuration surface. It works identically in embedded and `mecatui connect` modes.

## Why these scope cuts

- **Mecatui settings only.** `status_customization:` belongs in the user-global `$XDG_CONFIG_HOME/mecatui/settings.yaml`, alongside key bindings. A project checkout or remote server cannot configure local execution. This preserves the client boundary in [`architecture.md`](../architecture.md) and the trust decision in [ADR-0247](../adr/0247-mecatui-status-line.md).
- **One input, generated result.** Templates receive an automatically StatusML-escaped projection of canonical raw `Input`; commands receive its raw JSON serialization on stdin. One UI-agnostic source returns a latest width-selected semantic `Result` with optional `header` and `footer` surfaces, so the UI never learns which source produced it.
- **Theme tokens, not terminal escapes.** `StatusML` has a small semantic token vocabulary mapped through the active theme. It uses the current palette as the v1 best effort; [issue #799](https://github.com/stacklok/mecatl/issues/799) owns the stable cross-widget theme-token contract and may require a breaking adjustment.
- **Template first; command when dynamic data is needed.** Templates make the current status information customizable without a process. A user-global direct executable with literal arguments supports Git/clock/task integrations; snapshot data is delivered only as JSON on stdin.
- **Go retains responsive layout and safety.** The UI calculates mandatory header/footer reservations and submits their available widths. The source selects template variants, evaluates StatusML, manages refresh/degradation, and returns semantic spans; the UI maps those spans through the active theme, joins renderer-owned chrome, and performs final clipping/alignment.

## In scope — 5 scenarios, in implementation order

### Scenario 1 — Shared status input and mecatui settings

The UI derives and submits a complete versioned raw `Input` whenever display facts or its independently reserved header/footer widths change. A single UI-agnostic source owns template-versus-command execution and publishes latest-wins `Result` semantic spans. Templates receive a private StatusML-escaped projection; commands receive raw JSON. The Bubble Tea model listens for source changes and installs only the newest generated result; it neither reads settings nor starts processes, preserving its dependency boundary in [`architecture.md`](../architecture.md) and the layout’s single-source rule in [`cmd/mecatui/ui/layout.go`](../../cmd/mecatui/ui/layout.go).

The input contains session title and resolved model facts; a credential-free server display target/connection mode; named cumulative usage/cache atoms; raw plus humanized context atoms and percentage; main-agent activity; flat delegated-leaf state counts; session workspace `location`/path/basename; terminal dimensions; independently reserved header/footer available columns; and `clock.now`. It excludes prompts, transcript/tool content, credentials, authentication metadata, diagnostics, raw command output, and the private local launch workspace. The session workspace path is populated only when local; the source privately uses the launch workspace as command-CWD fallback without exposing it in `Input`.

**Acceptance:**
- AC1.1: The template renderer and configured command observe the same status values, including terminal dimensions, per-surface available columns, and `clock.now`; unknown values use their documented empty/zero representation.
  - verify: `TestStatusCustomization_Scenario1_TemplateAndCommandShareStatusInput`
- AC1.2: The status input excludes prompts, transcript/tool content, credentials, authentication metadata, and diagnostics; the server identity is display-only and never carries authentication data.
  - verify: `TestStatusCustomization_Scenario1_StatusInputExcludesSensitiveContent`
- AC1.3: `workspace.launch` remains local. `workspace.session` declares its location explicitly and is blank/remote rather than mistaken for a local path in connect mode; command CWD uses a local session workspace when known, otherwise the local launch directory.
  - verify: `TestStatusCustomization_Scenario1_WorkspaceProvenanceAndCommandCWD`
- AC1.4: `status_customization:` is parsed only from the user-global mecatui settings document, beside key bindings; absent settings select the shipped default template.
  - verify: `TestStatusCustomization_Scenario1_UserSettingsOwnCustomization`

---

### Scenario 2 — Theme-integrated StatusML and default templates

`StatusML` is the command output grammar. One command document may contain optional `header` and `footer` surfaces; each is a single-line fragment containing styled text/link nodes directly. Headers are renderer-left-aligned and footers renderer-right-aligned; the semantic tags are `text`, `muted`, `primary`, `secondary`, `accent`, `success`, `warning`, `error`, and `info`:

```text
<status><header><accent>mecatl</accent></header><footer><text>ctx 42%</text></footer></status>
```

Settings templates are intentionally more readable surface fragments: their `header.full` / `footer.compact` strings omit the enclosing command-document tags. Template substitutions are escaped as text before parsing. A command emits the full StatusML document. Unknown/malformed tags are rendered as safe text or rejected to that surface’s default template—never interpreted as terminal control sequences.

#### Configuration examples

**Keep the shipped header and footer.** Omit `status_customization:` entirely. Both default template sets render current chrome.

**Override only the footer.** `header:` is absent, so the shipped header templates and their renderer-owned posture/scroll/changed-file lanes remain active. The left footer activity lane (`ready`, `thinking…`, approval state, or tool progress) is likewise renderer-owned; a footer customization is right-justified in the remaining width. The optional interval advances `clock.now`, making this a template-only clock:

```yaml
status_customization:
  interval: 1s
  templates:
    footer:
      full: >-
        <footer><text>{{.Clock.Now.Format "15:04:05"}} · {{.Context.Percent}}%</text></footer>
      compact: >-
        <footer><text>{{.Clock.Now.Format "15:04"}}</text></footer>
      minimal: >-
        <footer><text>ctx</text></footer>
```

**Override only the header.** `footer:` is absent and therefore keeps the shipped context, usage, and delegation summaries:

```yaml
status_customization:
  templates:
    header:
      full: >-
        <header><accent>mecatl</accent> <text>{{.Model.DisplayName}}</text></header>
      compact: >-
        <header><text>{{.Model.ID}}</text></header>
      minimal: >-
        <header><text>mecatl</text></header>
```

A command may likewise emit only `<footer>` or `<header>` inside its `<status>` document; the omitted surface uses the corresponding default templates.

The shipped `full`, `compact`, and `minimal` templates reproduce both present surfaces: header identity and status/usage/delegation/context content. The renderer independently chooses the richest fitting variant for each surface, preserving the responsive footer contract in [`cmd/mecatui/ui/view.go`](../../cmd/mecatui/ui/view.go). It retains the header’s posture badge plus scroll/changed-file indicators in renderer-owned lanes, so a custom template cannot hide safety or navigation state. This template/render split is the presentation decision in [ADR-0247](../adr/0247-mecatui-status-line.md). The active theme resolves StatusML tokens using the current palette; #799 will formalize and harmonize that vocabulary across mecatui.

**Acceptance:**
- AC2.1: With no status-line override, shipped templates reproduce the current header and status/usage row while keyboard help remains unchanged.
  - verify: `TestStatusLine_Scenario2_DefaultTemplatesPreserveChrome`
- AC2.2: A template can compose status input values and StatusML tags; substituted values cannot forge layout/style markup or terminal controls.
  - verify: `TestStatusLine_Scenario2_TemplateEscapesValues`
- AC2.3: Full, compact, and minimal variants are measured and selected independently per surface; custom header content gets only columns remaining after safety lanes, and custom footer content is right-justified after the renderer-owned activity lane.
  - verify: `TestStatusCustomization_Scenario2_ReservedLanesAndResponsiveSelection`
- AC2.4: Header posture, scroll, and changed-file indicators remain renderer-owned and visible when applicable, regardless of a custom header template.
  - verify: `TestStatusLine_Scenario2_HeaderSystemIndicatorsSurviveOverride`
- AC2.5: Each StatusML semantic token resolves through the active theme, including custom palettes; no raw ANSI/OSC sequence reaches the terminal.
  - verify: `TestStatusLine_Scenario2_StatusMLUsesThemeAndSanitizesTerminal`
- AC2.6: An override of only `header` or only `footer` replaces only that surface; the omitted surface renders the matching shipped default templates.
  - verify: `TestStatusLine_Scenario2_PartialSurfaceOverrideKeepsDefault`

---

### Scenario 3 — Local command mode with the same protocol

A settings override may select a template set or a command, never both:

```yaml
status_customization:
  command:
    executable: /usr/local/bin/mecatui-status
    args: [--format, statusml]
  interval: 10s # optional; omitted means event-driven
```

The executable must be an absolute path and receives only literal arguments; shell and source forms are rejected. One command invocation emits one StatusML document and may populate both surfaces; an omitted surface retains its shipped default. Commands receive raw `Input` JSON on stdin, including the actual `Terminal.HeaderAvailCols` and `Terminal.FooterAvailCols` after renderer reservations, then choose their own compact representation. Their CWD is the local session workspace when the client knows it is local, otherwise the local launch directory; a remote session path is never used as CWD.

The environment is an exact allowlist: `HOME`, `PATH`, `TERM`, `LANG`, `LC_ALL`, `COLUMNS`, and `LINES`; unset values are omitted and no other parent environment entry is inherited. Stdout/stderr are bounded streaming readers with a combined 4 KiB limit. The process tree is contained and cancelled on timeout, replacement, or shutdown. These constraints follow [ADR-0247](../adr/0247-mecatui-status-line.md) and the secret-scrubbing invariant in [`AGENTS.md`](../../AGENTS.md).

**Acceptance:**
- AC3.1: A user-global command invokes only an absolute executable with literal arguments, receives raw JSON `Input` on stdin, and can use the local checkout and terminal dimensions to emit StatusML.
  - verify: `TestStatusLine_Scenario3_DirectExecutableReceivesSharedInput`
- AC3.2: One command StatusML document can populate both surfaces; each supplied surface has the same themed spans/layout semantics as its template equivalent, and an omitted surface retains its default.
  - verify: `TestStatusLine_Scenario3_CommandAndTemplateShareSurfaces`
- AC3.3: The runner passes only the exact environment allowlist, passes raw input only on stdin, and cannot be enabled or modified by project/server content.
  - verify: `TestStatusLine_Scenario3_CommandBoundaryIsLocalAndSecretFree`
- AC3.4: Stdout/stderr are bounded while read; overflow, malformed StatusML, and terminal controls fail safely without an unbounded allocation or rendered escape sequence.
  - verify: `TestStatusLine_Scenario3_BoundsAndSanitizesCommandOutput`

---

### Scenario 4 — Refresh, cancellation, and degradation

Templates re-render when `Input` changes. Commands run after initialization and meaningful input changes with a 250 ms debounce; an optional interval is at least one second. The same interval advances `clock.now` and re-renders templates, so a template-only clock needs no subprocess. At most one command process tree runs; one result generation updates both surfaces. A new request cancels an older invocation, every invocation has a one-second deadline, and generation correlation prevents stale completion from replacing newer status.

A command failure leaves each last successful supplied surface with a renderer-owned stale marker: append ` [stale]`, prepend `[stale] ` if that is the only fitting placement, then replace the customization with `[stale]` if neither fits. An initial failure or omitted surface falls back to that surface’s shipped default template. Diagnostics carry only a fixed failure category, numeric exit status when available, and duration—never command path/arguments, input JSON, raw errors, or captured streams. This follows the diagnostic rule in [`AGENTS.md`](../../AGENTS.md).

**Acceptance:**
- AC4.1: Input changes coalesce to one latest command refresh; templates re-render without process creation, and an optional interval advances `clock.now` for template-only clocks.
  - verify: `TestStatusLine_Scenario4_DebounceTemplateRefreshAndClock`
- AC4.2: Interval refresh is optional; absent interval creates no periodic work, while a configured interval refreshes an idle command once for both surfaces.
  - verify: `TestStatusLine_Scenario4_OptionalInterval`
- AC4.3: Only one contained command tree runs; cancellation, timeout, and shutdown join the tree/readers, and stale completion cannot overwrite either current surface.
  - verify: `TestStatusLine_Scenario4_ProcessLifecycleAndGenerationGuard`
- AC4.4: Failed command refreshes preserve bounded stale surfaces or use their default templates, without leaking stream contents, command arguments, or token-shaped data to UI, conversation, or diagnostics.
  - verify: `TestStatusLine_Scenario4_FailureDegradesWithoutLeakage`

---

### Scenario 5 — Documentation and theme-contract handoff

The operator guide documents the mecatui settings schema, shared header/footer `StatusML` document, `Input` fields (including `clock.now`), semantic tokens, per-surface template variants, direct executable command mode, refresh policy, output/process limits, and local-user authority. It gives copyable template-clock and executable examples. It names #799 as the owner of the stable semantic theme contract rather than claiming the v1 token mapping is immutable.

This is a user-visible configuration surface, so [`AGENTS.md`](../../AGENTS.md) requires corresponding `user-docs/` coverage. The live architecture documents the local status seam and the updated render-input boundary.

**Acceptance:**
- AC5.1: `docs/tui.md` and `user-docs/` document the settings location/schema, shared input, StatusML, templates, command mode, and safety limits with copyable examples.
  - verify: inspection — user/operator documentation is updated with the shipped schema
- AC5.2: The documentation distinguishes the best-effort v1 StatusML token mapping from the stable cross-widget token contract owned by #799.
  - verify: inspection — #799 is linked from theme/status documentation
- AC5.3: Existing behavior without settings and the default-template behavior stay covered by offline golden tests.
  - verify: `TestStatusLine_Scenario5_DefaultCompatibility`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Project-local or server-provided status configuration | not planned without a distinct trust design | user-global mecatui settings only |
| Raw ANSI/OSC, hyperlinks, images, or interactive controls | future design | StatusML owns styling and layout |
| A stable theme-token compatibility promise across every widget | [#799](https://github.com/stacklok/mecatl/issues/799) | status line uses a best-effort v1 mapping |
| Prompt, transcript, tool-result, monetary-cost, rate-limit, or remote API data | later, separately scoped | `Input` stays local and bounded |
| Hot reload of settings/template files | follow-up | restart is the v1 reload boundary |
| A `/statusline` assistant command | follow-up | it would author local executable/template configuration |

## Cross-cutting deliverables

- [ADR-0247](../adr/0247-mecatui-status-line.md) is accepted with implementation.
- Update [`docs/tui.md`](../tui.md), [`docs/architecture.md`](../architecture.md), and the relevant `user-docs/` page. Run `task docs` and `task site:build`; do not hand-edit generated `llms.txt`.
- Add the command runner/timer to the cloud-native resource inventory if it outlives one invocation, as required by [`AGENTS.md`](../../AGENTS.md).

## Sequencing recommendation

First define `Input`, shared header/footer StatusML parsing/rendering, and the default responsive template sets with preserved system indicators. Next add user settings parsing and per-surface template overrides. Command mode follows over the same input/output seams, then the shared refresh lifecycle (including template clocks) and documentation. #799 is independent and must not block this capability, but the status implementation must use its eventual token contract when that issue lands.

## Named tests landing in this plan

`TestStatusCustomization_Scenario1_TemplateAndCommandShareStatusInput`, `TestStatusCustomization_Scenario1_StatusInputExcludesSensitiveContent`, `TestStatusCustomization_Scenario1_WorkspaceProvenanceAndCommandCWD`, `TestStatusCustomization_Scenario1_UserSettingsOwnCustomization`, `TestStatusCustomization_Scenario2_DefaultTemplatesPreserveChrome`, `TestStatusCustomization_Scenario2_TemplateEscapesValues`, `TestStatusCustomization_Scenario2_ReservedLanesAndResponsiveSelection`, `TestStatusCustomization_Scenario2_HeaderSystemIndicatorsSurviveOverride`, `TestStatusCustomization_Scenario2_StatusMLUsesThemeAndSanitizesTerminal`, `TestStatusCustomization_Scenario2_PartialSurfaceOverrideKeepsDefault`, `TestStatusLine_Scenario3_DirectExecutableReceivesSharedInput`, `TestStatusCustomization_Scenario3_CommandAndTemplateShareSurfaces`, `TestStatusCustomization_Scenario3_CommandBoundaryIsLocalAndSecretFree`, `TestStatusCustomization_Scenario3_BoundsAndSanitizesCommandOutput`, `TestStatusCustomization_Scenario4_DebounceTemplateRefreshAndClock`, `TestStatusCustomization_Scenario4_OptionalInterval`, `TestStatusCustomization_Scenario4_ProcessLifecycleAndGenerationGuard`, `TestStatusCustomization_Scenario4_FailureDegradesWithoutLeakage`, `TestStatusCustomization_Scenario5_DefaultCompatibility`.

## Definition of done

1. `task lint` and `task test` pass (both modules, with `-race`).
2. `task docs` regenerates `llms.txt` and passes the strict documentation gate.
3. `task site:build` passes after public documentation updates.
4. `task api:check` passes without an engine API change.
5. `task ac-trace-strict` passes after the plan is marked `landed`.
6. Named tests run offline; no test calls a live service.
7. `go run ./cmd/mecademo` still prints a full offline session.

## Deferred decisions and known risks

- **The v1 token mapping may change when #799 lands.** The template/StatusML shape is the status feature’s contract; exact palette-to-token mapping is intentionally not frozen ahead of the theme work.
- **A one-second command deadline favors responsiveness over slow integrations.** Evidence from use should justify any configurable cap or cache protocol.
- **Process-tree containment is platform-specific.** Unsupported targets must disable command mode rather than silently killing only the direct child.

## Exit criteria

When every point under *Definition of done* holds on the accumulator, this plan is satisfied.
