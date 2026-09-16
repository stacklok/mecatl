# Mecatui-owned configurable terminal titles — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — replaces a third-party-owned terminal-output lifecycle and introduces a durable user-global title-template/configuration contract shared with the status-input surface.
**Decision record:** [ADR 0344](../adr/0344-mecatui-terminal-title-controller.md)
**Phase:** mecatui client presentation and session discoverability
**Status:** proposed, 2026-09-15. Decisions recorded with the directing operator.
**Delivery:** Split. The client configuration, terminal-control, compatibility, and live-run interaction contracts require separate human interface review before implementation.
**Expected tasks:** deferred to orchestration after the Plan / Interface PR is approved.
**Issue:** [stacklok/mecatl#1460](https://github.com/stacklok/mecatl/issues/1460); [stacklok/mecatl#1606](https://github.com/stacklok/mecatl/issues/1606).
**Plan PR:** absent until opened.
**Approved baseline:** absent until the Plan / Interface PR merges.

Mecatui will replace Bubble Tea's `tea.View.WindowTitle` output with a UI-owned, renderer-serialized title controller. It renders one plain-text title from the same display-safe `statusline.Input` facts used by status templates, sends OSC 0 only when the rendered title changes, and clears OSC 0 on clean shutdown. This removes output-stream parsing while populating both historical title channels for terminals such as iTerm2.

The default title and status header no longer show the session handle. Users may deliberately restore it through templates, and `/session` becomes the explicit full-ID affordance even while a run is active. The feature is client-local: it changes neither engine behavior nor remote/server configuration.

## Human decisions

- [x] Title controller ownership and lifecycle — Decision: a synchronous UI-owned controller writes only through the renderer-serialized output path; it owns deduplication and has no title goroutine. On clean exit, it clears with empty OSC 0 only after this process emitted a non-empty title; disabled mode writes no title sequence.
- [x] Settings schema and default — Decision: user-global `$XDG_CONFIG_HOME/mecatui/settings.yaml` gains `terminal_title.enabled` and `terminal_title.template`; absence enables the shipped no-handle default, while the no-title fallback is `mecatui`.
- [x] Template contract — Decision: titles use the existing display-safe status-input projection as plain text, with no StatusML or command output; `.Session.Handle` remains opt-in. Add `elide WIDTH VALUE` to both title and status templates: a non-positive width returns empty, a fitting value is unchanged, width one truncates to `…`, and wider truncation is the widest fitting prefix plus `…`. Do not add generic trim helpers.
- [x] Safety bounds — Decision: after template execution, titles strip C0, DEL, C1, and Unicode `Cc`/`Cf` controls; collapse all Unicode whitespace to one ASCII space; and remain rune-bounded before OSC construction. Numeric caps are implementation tuning constants, not a durable plan/ADR contract.
- [x] Terminal protocol — Decision: enabled dynamic titles emit OSC 0. Terminals/multiplexers retain presentation control.
- [x] Disablement and compatibility — Decision: explicit `--terminal-title=off` and `MECATUI_NO_TERMINAL_TITLE=1` disable all title writes; explicit `--terminal-title=on` overrides settings disablement; otherwise `terminal_title.enabled: false` disables the controller. Explicit flag presence must be distinguishable from its default.
- [x] Debug and session identity — Decision: default/debug titles omit the handle (debug adds a `DEBUG` prefix); `/session` may open its read-only overlay during an active run, temporarily taking input focus without interrupting, pausing, or steering it.
- [x] Command-status boundary — Decision: command-backed `status_customization` cannot set or influence terminal titles.

## Interface contract

- **gRPC / protobuf:** None — terminal-title configuration and the live `/session` overlay are local mecatui client behavior; no wire message or RPC changes.
- **Exported Go APIs / interfaces:** `cmd/mecatui/statusline` gains the shared `elide` template function in its private template projection; no `engine/` public API changes. The title controller remains a client-internal type.
- **Tool schemas:** None — `/session` is an existing local UI builtin, not an engine tool or model-visible tool schema.
- **CLI / config:** Preserve `--terminal-title=on|off|true|false|1|0` and `MECATUI_NO_TERMINAL_TITLE=1` semantics with explicit-source precedence. Add strict user-global `terminal_title:` settings with `enabled` and `template`; invalid YAML, unknown fields, or invalid template syntax/execution fail startup and identify the relevant field. The feature defaults enabled; title-off suppresses all OSC writes.
- **Events / persistence:** None — title values are derived client presentation state and are neither session-persisted nor emitted as protocol events. `/session` reads the client-bound existing session ID only.
- **Security / authority:** Templates receive only the existing display-safe status input. Rendered title text is terminal-control sanitized, single-line, and rune-bounded before OSC 0 construction. Command-backed status output and StatusML have no title channel. Settings remain user-global, never workspace/server/project-controlled.
- **Compatibility / migration:** Existing users retain enabled default terminal titles and their `--terminal-title`/environment opt-out. The default title/header stop showing a handle; custom templates may restore it. Add public migration/configuration documentation; no persisted-data migration.

## In scope — 4 scenarios, in implementation order

### Scenario 1 — Serialized OSC 0 title lifecycle

The client owns title emission rather than `tea.View.WindowTitle`. It derives a plain title from the current status facts, sends OSC 0 only when the final sanitized value changes, and clears with empty OSC 0 at clean exit. This preserves one serialized terminal-output authority and avoids parsing Bubble Tea's output stream, while making icon and window names equal as decided in [ADR 0344](../adr/0344-mecatui-terminal-title-controller.md).

**Acceptance:**
- AC1.1: An enabled normal or disconnected-recovery program emits one OSC 0 update for each changed final title and never emits Bubble Tea OSC 2 title output.
  - verify: `TestADR_0344_Scenario1_ControllerOwnsSerializedOSC0`
- AC1.2: Identical successive title inputs emit no duplicate sequence; a clean exit emits exactly one empty OSC 0 cleanup sequence only after the controller emitted a non-empty title, while disabled mode emits no OSC title sequence.
  - verify: `TestADR_0344_Scenario1_DeduplicatesConditionalCleanupAndDisables`
- AC1.3: After template execution and before OSC construction, rendered title content strips C0, DEL, C1, and Unicode `Cc`/`Cf` controls; collapses Unicode whitespace to one ASCII space; and applies a bounded final title, so data cannot inject an OSC terminator or another control sequence.
  - verify: `TestADR_0344_Scenario1_SanitizesRenderedTitle`

### Scenario 2 — User-global templates, defaults, and disablement

The strict mecatui client settings decoder accepts `terminal_title` independently of `status_customization`; no command source is involved. Both title and status templates receive the same display-safe projection and `elide` helper, while title output remains plain text rather than StatusML. This follows the client-local and command-boundary decisions in [ADR 0344](../adr/0344-mecatui-terminal-title-controller.md) and [ADR 0289](../adr/0289-hardened-status-command-boundary.md).

**Acceptance:**
- AC2.1: Absent settings use the shipped title template; it omits the session handle, falls back to `mecatui` before a session title exists, and the shipped status header no longer displays the handle.
  - verify: `TestADR_0344_Scenario2_DefaultPresentationOmitsHandle`
- AC2.2: A user-global `terminal_title.template` can compose the documented display-safe input values, including the opt-in handle, and shared title/status `elide WIDTH VALUE` behavior is display-width bounded: non-positive width returns empty, a fitting value is unchanged, width one produces `…`, and wider truncation returns the widest fitting prefix plus `…` without exceeding the requested width.
  - verify: `TestADR_0344_Scenario2_SharedTemplateProjectionAndElide`
- AC2.3: Unknown title settings fields, invalid YAML, and invalid template parse or execution failures reject startup with an error naming `terminal_title` or `terminal_title.template` and the actionable cause.
  - verify: `TestADR_0344_Scenario2_InvalidConfigurationFailsActionably`
- AC2.4: An explicitly supplied `--terminal-title=off` or environment opt-out suppresses every OSC title write regardless of a configured template; explicit `--terminal-title=on` enables configured/default rendering despite `terminal_title.enabled: false`; absent explicit controls honor settings disablement.
  - verify: `TestADR_0344_Scenario2_ExplicitDisablementPrecedence`
- AC2.5: A command-backed status configuration cannot set or influence terminal title output.
  - verify: `TestADR_0344_Scenario2_CommandStatusCannotControlTitle`

### Scenario 3 — Title presentation across session states

The title controller uses the status-derived session and activity facts without exposing a handle by default. Debug sessions retain their visible `DEBUG` distinction. This changes presentation only; session title ownership and durable server metadata remain as documented in [architecture](../architecture.md).

**Acceptance:**
- AC3.1: A session with a title renders the configured/default title with its human-readable activity; custom templates may explicitly include `.Session.Handle`.
  - verify: `TestADR_0344_Scenario3_TitleAndCustomHandle`
- AC3.2: A debug session applies the configured/default template with the `DEBUG` prefix and no mandatory session handle.
  - verify: `TestADR_0344_Scenario3_DebugTitle`
- AC3.3: Terminal-title configuration behaves identically for embedded and `mecatui connect` clients and never derives a title from a private workspace path.
  - verify: `TestADR_0344_Scenario3_LocalAndRemotePresentation`

### Scenario 4 — Explicit session identity during a live run

`/session` is an existing local builtin whose dispatch path already runs during an active agent turn; only its details overlay currently rejects non-idle phases. The implementation removes that presentation guard while preserving the no-session guard and the live-run lifecycle described in [architecture](../architecture.md).

**Acceptance:**
- AC4.1: During an active run with a bound session, `/session` opens the existing read-only details overlay and displays/copies that session's full ID.
  - verify: `TestADR_0344_Scenario4_SessionDetailsOpenWhileRunning`
- AC4.2: Opening and closing `/session` during a run does not cancel, pause, or steer the run; it temporarily captures overlay input and then returns to the live conversation.
  - verify: `TestADR_0344_Scenario4_SessionDetailsDoNotInterruptRun`
- AC4.3: With no bound session, `/session` retains its existing no-active-session response regardless of UI phase.
  - verify: `TestADR_0344_Scenario4_NoSessionGuardRemains`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Separate icon and window templates or user-selectable OSC 1/2 targets | a future terminal-compatibility proposal with evidence of a concrete need | v1 emits OSC 0 only |
| Status-command title output | no current proposal | command-backed status remains header/footer-only |
| Settings hot reload | later client-settings work | restart remains the configuration reload boundary |
| Server, engine, gRPC, HTTP, or SDK title configuration | no current proposal | terminal titles are client-local |
| Changing session-title generation or persistence | [session title generation plan](session-title-generation.md) | consume existing title metadata only |

## Definition of done

1. New and migrated offline tests cover every named acceptance proof and satisfy the test-isolation rule.
2. `task lint`, `task test`, `task api:check`, and `task docs` pass; `task site:build` passes after public documentation updates.
3. `go run ./cmd/mecademo` remains green.
4. User documentation covers settings location/schema, template input, `elide`, OSC 0 behavior, disablement precedence, terminal/multiplexer presentation limits, and `/session` during runs.
5. The implementation PR links this approved Plan / Interface PR and reports the unchanged gRPC/protobuf, engine API, tool-schema, event/persistence, and command-status boundaries.
6. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- Terminal emulators and multiplexers control their own presentation of OSC 0; mecatui can set metadata but cannot guarantee a tab label.
- `elide` is a public template affordance and needs focused Unicode/display-width tests before it is shared by title and status templates.
- The implementation must preserve a single serialized terminal-output owner; a background title writer would be contract drift and requires a new design review.
