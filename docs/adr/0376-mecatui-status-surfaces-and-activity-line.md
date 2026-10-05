# ADR 0376 — mecatui status surfaces and activity line

- Status: Accepted
- Date: 2026-10-01
- Scope: `cmd/mecatui` local status-line generation, status-command boundary, and the
  placement of status chrome in the frame
- Supersedes: ADR 0289 (and, through it, ADR 0247)
- Superseded by: none

## Context

ADR 0247 established user-configurable header and footer status surfaces for
mecatui. ADR 0289 superseded it to harden the status-command boundary, but restated
only the command environment and output trimming. The rest of 0247's decision,
including where renderer-owned chrome sits, had no current record.

0247 placed the session activity signal ("thinking", running tools, approval state) in
the footer, below the input box. Operators finishing a prompt look above the input
for signs of progress, so the most important "is anything happening?" signal was easy
to miss ([issue #1325](https://github.com/stacklok/mecatl/issues/1325)). 0247 also
fixed alignment details of individual surfaces. Those details are presentation choices
that do not need an architecture record.

This ADR consolidates the decisions from 0247 and 0289 that remain in force and
records the new placement of status chrome.

## Decision

### Configuration and sources

Place `status_customization:` only in the user-global
`$XDG_CONFIG_HOME/mecatui/settings.yaml`, beside key bindings. It selects exactly one
source: responsive Go-template surfaces or a direct local executable at an absolute
`executable` path with literal `args`. Shell/source and command-string forms are not
configuration. The configuration may supply optional header and footer surfaces;
absent surfaces retain shipped defaults.

Compose one UI-agnostic `Source` outside `ui`. The UI submits the latest canonical raw
`Input` whenever display facts or the per-surface available widths change. The source
owns template-versus-command selection, template evaluation, command
debounce/cancellation/timeout/process cleanup, interval-driven clock refresh,
stale-completion suppression, and per-surface default/last-good degradation.

```go
type Source interface {
    Submit(Input)
    Changed() <-chan struct{}
    Latest() Result
    Close(context.Context) error
}
```

`Changed` is a capacity-one wake-up edge, never a result queue. The source stores its
latest result behind a mutex, allocates fresh bounded span slices on publish, and
`Latest` deep-copies them before return. Private generations prevent stale
command/template completions from publishing; no request token is exposed in input or
result. One Bubble Tea adapter command waits on `Changed`, snapshots `Latest`, posts a
message, and re-arms after each nonterminal notification. The listener captures the
TUI root context; source shutdown cancels owned work, joins it, and closes `Changed` so
the listener stops cleanly.

### Input and output boundary

Both source modes consume the same raw `Input`. Commands receive its JSON serialization
on stdin and never receive template interpolation. Templates receive a private
automatically StatusML-escaped projection; a direct executable must escape any dynamic
values that it interpolates into StatusML. `Input` contains only display-safe facts:
credential-free server target/connection mode, session/model facts, named usage/cache
atoms, context facts, main/delegation activity, session-workspace provenance, terminal
dimensions, per-surface available width, and clock. It excludes prompts,
transcript/tool content, credentials, authentication metadata, diagnostics, raw command
output, and the private local launch-workspace fallback.

Keep the status-command environment fixed to its safe baseline. Permit only additional
names listed in the user-global `status_customization.command.passthrough_env`
allowlist. Each name must match `[A-Za-z_][A-Za-z0-9_]*`; unset names are omitted, with
no fallback from `os.Environ`. Deduplicate names, and reject requested baseline or
source-owned names such as terminal `COLUMNS` and `LINES` during settings validation.

Immediately before StatusML parsing, trim only leading and trailing ASCII space, tab,
LF, CR, vertical tab, and form feed. Preserve all interior bytes; malformed StatusML
retains the safe fallback.

`Result` contains independently optional, width-selected header and footer semantic
spans. It contains no ANSI or OSC bytes. StatusML has optional `header` and `footer`
surfaces containing styled text/link nodes directly, with semantic style tokens and a
`link` node with separate display text and a bounded quoted `href`. Only `http` and
`https` links are accepted. The UI applies the active theme, renders validated links,
and performs final clipping and alignment.

### Placement of status chrome

Renderer-owned chrome cannot be replaced or removed by customization:

- The header row carries the header surface beside the header safety and navigation
  indicators.
- The activity line sits directly above the input box, below the conversation and any
  inline menus or queued follow-ups. It carries the session activity: idle and
  notice text, model thinking, running tools, approval state, and connection state.
- The footer sits below the input box. It carries the footer surface, followed by the
  keyboard help.

Status sources receive the width available to their surface after renderer-owned
chrome on the same row is reserved. The activity line no longer shares a row with the
footer surface.

## Consequences

Activity feedback sits where operators look while they wait, and custom footers gain
the width the activity text used to reserve. The frame spends more rows on chrome
below the conversation, so very short terminals trade padding around the activity
line before conversation rows.

Templates customize status without a process; local commands can run trusted
user-global integrations from a local session workspace when known, otherwise the local
launch workspace. A remote workspace never becomes a local command CWD. Commands
inherit only the explicit minimal environment plus operator-listed names and have
bounded output and lifetime. Operators must not list credentials in
`passthrough_env`.

The UI remains free of settings parsing, timers, subprocesses, and command policy while
receiving autonomous latest-wins updates. The source is an outlives-a-call resource
requiring lifecycle inventory, shutdown/leak, stale-result, and coalescing tests.
Generated semantic spans preserve theme control and terminal safety, at the cost of
maintaining a small StatusML parser.

## See also

- [ADR 0247 — mecatui generated status lines](./0247-mecatui-status-line.md)
- [ADR 0289 — Hardened status-command output and environment extension](./0289-hardened-status-command-boundary.md)
- [mecatui status-line acceptance plan](../acceptance/mecatui-status-line.md)
- [mecatui guide](../tui.md)
- The public `user-docs/mecatui/status-line.md` status-line customization guide
- [ADR 0027 — cloud-native lifecycle](./0027-cloud-native.md)
- [ADR 0002 — documentation lifecycle](./0002-documentation-lifecycle.md)
