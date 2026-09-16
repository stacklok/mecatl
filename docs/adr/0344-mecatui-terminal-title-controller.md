# ADR 0344 — Mecatui-owned terminal titles

- Status: Proposed
- Date: 2026-09-15
- Scope: `cmd/mecatui` terminal-title rendering, client settings, and status-template helpers
- Supersedes: none
- Superseded by: none

## Context

Mecatui currently places its composed title in `tea.View.WindowTitle`. Bubble Tea emits that value as OSC 2, which is a window-title update. Some terminals, notably iTerm2 under common profile settings, use the separate historical icon-name channel for their tab label. Mirroring OSC 2 at the output writer fixes that symptom but makes mecatui parse and rewrite a general terminal byte stream merely to own a title it already computes.

The title is also a presentation surface with a durable client configuration need. Mecatui already derives one bounded, display-safe `statusline.Input` snapshot for status templates, including the session title, session handle, model, activity, workspace, delegation, and clock. StatusML is not suitable for a terminal title: a title is plain text and must never accept markup or terminal controls. Command-backed status is a separate local-process boundary and must not acquire terminal-title authority.

Finally, session handles currently occupy the shipped header and terminal-title defaults. They should be available to deliberate customization but not consume default presentation space; `/session` is the explicit identity affordance and must be usable while a run is active.

## Decision

Mecatui owns terminal-title composition, change detection, OSC emission, and shutdown clearing; it no longer delegates title emission to Bubble Tea. The controller is UI-owned and synchronous with the renderer's output path. It has no independent goroutine or direct concurrent stdout writer.

The controller renders one plain-text Go template from the existing display-safe status input. Add user-global `$XDG_CONFIG_HOME/mecatui/settings.yaml` configuration:

```yaml
terminal_title:
  enabled: true
  template: '{{.Session.Title}} — {{.MainAgent.State}} · mecatui'
```

When absent, the feature is enabled and uses the shipped default. The default omits the session handle; `.Session.Handle` remains available to custom templates. Before OSC construction, the final rendered value strips C0, DEL, C1, and Unicode `Cc`/`Cf` controls; collapses all Unicode whitespace to one ASCII space; and applies an implementation-private rune bound. Invalid `terminal_title.template` values fail startup with an actionable error naming the configuration field and template failure. Templates expose no StatusML and no command output. Both status and title templates gain one display-width-aware helper, `elide WIDTH VALUE`: non-positive widths yield empty text; a fitting value is unchanged; a one-column truncation is `…`; and a wider truncation is the widest fitting prefix plus `…`. No generic trim helper is added. Numeric title and helper bounds are implementation tuning constants, not an ADR/API commitment; they may change while the bounded, display-safe behavior remains.

Enabled titles emit OSC 0, setting the icon name and window title to the same value. On clean exit, the controller emits empty OSC 0 only if it emitted a non-empty title during that process. Command-backed `status_customization` remains unable to produce or influence a terminal title.

Keep the established `--terminal-title` and `MECATUI_NO_TERMINAL_TITLE` compatibility controls. Track explicit flag presence: explicit `--terminal-title=off` or the environment opt-out disables all title writes; explicit `--terminal-title=on` enables the controller even when settings disable it. In the absence of an explicit control, `terminal_title.enabled: false` disables all title writes. Disabled mode does not clear or overwrite a shell/terminal-supplied title.

Apply the configured/default title to debug sessions too, with a `DEBUG` prefix but no mandatory handle. Permit the existing read-only `/session` details overlay while a session is active; it may own keyboard focus temporarily but must not interrupt, pause, or steer the run.

## Consequences

Mecatui controls a terminal-facing protocol explicitly and eliminates the OSC-output parser. The title lifecycle becomes testable at a single client seam and remains serialized with rendered output. OSC 0 is broadly compatible but terminals and multiplexers still own whether and where they display title metadata.

The user-global settings schema and template helper become a supported configuration contract. Template parsing is intentionally fail-fast, which makes configuration errors visible at startup rather than silently changing titles. The shared helper must remain bounded and terminal-display-width aware across status and title templates.

Custom title templates can deliberately include the session handle, but default chrome no longer exposes it. `/session` becomes the reliable explicit path to the full identifier while an agent is running.

## See also

- [Mecatui status-line acceptance plan](../acceptance/mecatui-status-line.md)
- [ADR 0247 — mecatui generated status lines](./0247-mecatui-status-line.md)
- [ADR 0289 — hardened status-command output and environment extension](./0289-hardened-status-command-boundary.md)
- [Issue #1460](https://github.com/stacklok/mecatl/issues/1460)
- [Issue #1606](https://github.com/stacklok/mecatl/issues/1606)
- [ADR 0002 — documentation lifecycle](./0002-documentation-lifecycle.md)
