---
sidebar_position: 10
title: Status line customization
description:
  Customize mecatui's local header and footer status surfaces with templates or
  a command.
---

# Status line customization

Customize the optional header and footer with responsive templates or one local
executable. Add `status_customization` to
`$XDG_CONFIG_HOME/mecatui/settings.yaml` (normally
`~/.config/mecatui/settings.yaml`). The setting is client-only and applies to
embedded and connected sessions. Project files, remote servers, prompts, and
sessions cannot change it.

With no `status_customization:` entry, `mecatui` uses its shipped responsive
templates. The header includes the active session title at every width and the
remote target in its full variant. Keyboard help, the header
posture/scroll/changed-file indicators, and the footer activity lane remain part
of the client interface; customization cannot remove them.

## Choose a source

`status_customization` must contain exactly one of `templates` or `command`. Its
optional `interval` must be at least one second. Without an interval, templates
refresh when their input changes and commands are event-driven.

|Source|Use it when|
|-|-|
|`templates`|The status line needs only data already provided by `mecatui`.|
|`command`|The status line needs local data from another program.|

```yaml
status_customization:
  interval: 1s
  templates:
    footer:
      full: >-
        <footer><text>{{.Clock.Now.Format "15:04:05"}} · ctx
        {{.Context.Percent}}%</text></footer>
      compact: >-
        <footer><text>{{.Clock.Now.Format "15:04"}}</text></footer>
      minimal: >-
        <footer><text>ctx</text></footer>
```

`header` and `footer` are optional. For each supplied surface, define `full`,
`compact`, and `minimal`. `mecatui` independently selects the richest variant
that fits each surface after reserving its required interface elements. An
omitted surface keeps the shipped template.

The interval also advances `.Clock.Now`, so the example above is a template-only
clock and creates no subprocess. Restart `mecatui` after editing settings; v1
does not hot-reload this file.

## Template input and escaping

Templates receive a display-safe projection of the status input. `mecatui`
removes terminal control characters and escapes every string for StatusML. A
substituted value therefore stays literal text and cannot create tags, terminal
sequences, or links. Use StatusML tags in the template itself.

Template fields have the same shape as the command JSON below. `Clock.Now` is a
time value and supports `{{.Clock.Now.Format "15:04"}}`. The `Human` members are
preformatted display values; use each `Raw` member when a template needs an
exact count.

### Common template functions

Status and terminal-title templates support `elide WIDTH VALUE`. A non-positive
width produces an empty value, a fitting value is unchanged, width `1` produces
`…`, and a wider value is shortened to the widest fitting prefix followed by
`…`.

They also support `lookup KEY match value ...`. It returns the value from the
first pair whose match exactly equals `KEY`; with no match, it returns an empty
string. For example, a title or status template can map an agent state to a
short label:

```gotemplate
{{lookup .MainAgent.State "idle" "ready" "thinking" "working" "failed" "error"}}
```

The helper is text-only and is available in both status and terminal-title
templates.

### Status-only context meter functions

Status templates provide three functions that render the current context use as
StatusML. They are not available to terminal-title templates. Use the function
that matches the status template variant:

- `contextMeter .Context` for `full`
- `contextMeterCompact .Context` for `compact`
- `contextMeterMinimal .Context` for `minimal`

For example, this footer uses the corresponding meter at each width:

```yaml
status_customization:
  templates:
    footer:
      full: '<footer>{{contextMeter .Context}}</footer>'
      compact: '<footer>{{contextMeterCompact .Context}}</footer>'
      minimal: '<footer>{{contextMeterMinimal .Context}}</footer>'
```

## Status input reference

Every source receives the same versioned, display-safe snapshot. A template gets
the escaped projection described above. A command gets the raw JSON encoding on
standard input. The JSON field names are shown here in their emitted Go
encoding. Unknown values use the normal zero value: strings are empty, counts
and dimensions are zero, and `Clock.Now` is the zero time until the source
refreshes it.

|JSON path|Type|Meaning|
|-|-|-|
|`Version`|integer|Status input protocol version (currently `3`).|
|`Server.DisplayTarget`|string|Credential-free target shown by the client.|
|`Server.ConnectionMode`|string|`embedded`, `connect`, or empty while unknown.|
|`Session.Title`|string|Optional display title.|
|`Session.Handle`|string|Short displayed session ID, available to custom status and terminal-title templates. Use `/session` to copy the full ID.|
|`Session.Mode`|string|Active or pending permission mode used by the shipped header.|
|`Session.ReasoningEffort`|string|`low`, `medium`, `high`, `xhigh`, `max`, or empty.|
|`Model.ProviderID`, `Model.ID`, `Model.DisplayName`, `Model.Route`|strings|Provider/model routing identifiers, display label, and observed downstream route.|
|`Model.ContextWindow.{Raw,Human}`|integer, string|Resolved context capacity as exact and display-ready values.|
|`Usage.{Input,Output,CacheRead,CacheWrite}.{Raw,Human}`|integer, string|Cumulative exact and display-ready token atoms. `CacheRead` is a subset of input.|
|`Usage.CacheReadPercent`|integer|`CacheRead.Raw / Input.Raw` as an integer percentage, or `0` when input is zero.|
|`Context.{Used,Window}.{Raw,Human}`|integer, string|Current context use and capacity as exact and display-ready values.|
|`Context.Percent`|integer|`Used.Raw / Window.Raw` as an integer percentage, or `0` when unknown.|
|`Workspace.Location`|string|`local`, `remote`, or `unknown`.|
|`Workspace.Name`|string|Provider-supplied workspace display metadata. It is not a directory basename or a usable path.|
|`Workspace.Path`|string|Exact local root returned by the privileged local-context RPC. It is available to templates through their escaped projection and to a configured direct local status command. It is empty for remote, untrusted, no-FS, unavailable, and otherwise ineligible sessions.|
|`Terminal.Rows`, `Terminal.Cols`|integers|Measured terminal dimensions.|
|`Terminal.HeaderAvailCols`, `Terminal.FooterAvailCols`|integers|Columns remaining after the client reserves mandatory header and footer lanes.|
|`MainAgent.State`|string|`connecting`, `idle`, `thinking`, `running_tool`, `awaiting_approval`, `completed`, `failed`, or `cancelled`.|
|`MainAgent.Activity`|string|Bounded display activity label.|
|`MainAgent.Approval`|string|`none` or `awaiting`.|
|`Delegation.{Total,DirectSubagent,TeamMember,ParallelBranch}`|object|Flat delegated-leaf counts by unit. Each object has `Running`, `AwaitingApproval`, `Completed`, `Failed`, `Cancelled`, and `Stopped` integer fields. `Total` is their component-wise sum.|
|`Delegation.Subagents`, `Delegation.Parallel`|object|Display summaries with `Running` and terminal-state-collapsed `Finished` counts.|
|`Delegation.Team`|object|Live-team display summary: `ID`, `Working`, and `Total`; all fields are zero/empty once the team is no longer live.|
|`Clock.Now`|RFC 3339 time|Source-owned current time; an interval refreshes it.|

The input excludes prompts, transcript and tool content, credentials,
authentication metadata, diagnostics, and command output. `Workspace.Path` is
the only privileged value. It is available only when the embedded client can
resolve an eligible local session. Templates receive an escaped value; a local
status command receives the raw path and uses it as its working directory.

## StatusML

StatusML is semantic markup rather than terminal output. A command emits one
document with optional `header` and `footer` surfaces; a template variant emits
its own surface fragment. The optional outer `<status>` wrapper is useful for a
command that supplies both surfaces:

```text
<status><header><accent>mecatui</accent><text> · GPT-5</text></header><footer><text>ctx 42%</text></footer></status>
```

Each surface contains its styled text and link nodes directly. Headers are
left-aligned by the renderer; footers are right-aligned. Text may be wrapped in
these semantic tokens:

`text`, `muted`, `primary`, `secondary`, `accent`, `success`, `warning`,
`error`, and `info`.

A link keeps its display text separate from its destination. Use the StatusML
`<link>` element instead of the HTML `<a>` element. It must be a direct child of
`<header>` or `<footer>` and its contents must be plain text; semantic-token and
nested-link children are not supported:

```text
<footer><link href="https://docs.example.test/status">status docs</link></footer>
```

These forms are invalid:

```text
<footer><a href="https://docs.example.test/status">status docs</a></footer>
<footer><link href="https://docs.example.test/status"><text>status docs</text></link></footer>
```

### Escaping dynamic command output

Template substitutions are already escaped by `mecatui`. Do not escape a value
again inside a settings template.

A command instead receives raw JSON, so it must HTML-escape every dynamic value
that it places in a StatusML text node. Escape `&` first, then `<` and `>`;
quotes are also required when a value is placed in an attribute. StatusML
decodes those entities back to literal display text, so an escaped value cannot
create a tag or alter styling:

|Raw value|StatusML text-node value|
|-|-|
|`A & B`|`A &amp; B`|
|`<untrusted>`|`&lt;untrusted&gt;`|
|`</text><error>forged</error>`|`&lt;/text&gt;&lt;error&gt;forged&lt;/error&gt;`|

For example, a command written in Python can safely include a dynamic label:

```python
from html import escape

label = status["Workspace"]["Name"]
print(f"<footer><text>{escape(label, quote=False)}</text></footer>")
```

Do not interpolate arbitrary data into `href`. A link destination must be a
trusted, bounded `http` or `https` URL without user information; use a fixed URL
or validate it with a URL parser before producing the StatusML document.

Only bounded `http` and `https` URLs without user information are retained.
Until terminal hyperlink support is added, a link is rendered as theme-styled
underlined text rather than an OSC 8 sequence. Unsafe link destinations lose
their destination but retain their display text. Control characters, including
ANSI, OSC, newline, tab, and Unicode line-separator controls, are removed from
markup text, link metadata, and theme data before rendering. StatusML is always
rendered as one terminal line.

### Handle command failures

A status command has a one-second invocation deadline and a combined 4 KiB
`stdout`/`stderr` limit. Its complete combined output must be one valid StatusML
document. In particular, do not write logs, warnings, tracebacks, or progress
output to `stderr`: it is combined with `stdout`, so it makes the document
invalid.

If the executable cannot start, exits unsuccessfully, times out, exceeds the
output limit, or emits malformed StatusML, `mecatui` keeps the most recently
successful custom surface and marks it `[stale]`. If it has no successful custom
surface yet, it instead uses the shipped default surface. The command does not
receive a failure result or retry signal. `/diagnostics` reports safe current
command state: each header/footer is `default`, `custom`, or `stale`; `error` is
`none`, `unsupported`, `timeout`, `output_limit`, `invalid_statusml`, `exit`, or
`failed`. It never reports captured output, arguments, paths, or raw failure
text. `[stale]` remains the only in-client failure indication; it is not a
machine-readable feedback channel for the command.

Test a command with representative JSON before configuring it. It must write
exactly one StatusML document to standard output. Send command-specific
diagnostics somewhere other than standard error. Malformed StatusML in a
template renders as literal text; malformed command output triggers the fallback
behavior above.

The v1 token-to-palette mapping is a best effort, not a cross-widget
compatibility promise.
[Issue #799](https://github.com/stacklok/mecatl/issues/799) tracks the stable
semantic theme-token contract.

## Use a direct executable

Use `command` when the status line needs local information from a program.
`executable` must be an absolute path and `args` are literal arguments. The
optional `passthrough_env` list is the only environment extension: each name
must match `[A-Za-z_][A-Za-z0-9_]*` and must not be one of the reserved baseline
or terminal-dimension names. The schema has no shell, `source`, command-string,
or working-directory fields. To use `/bin/sh`, select it as `executable` and
supply its literal arguments.

```yaml
status_customization:
  interval: 10s
  command:
    executable: /home/alice/.local/bin/mecatui-status
    args: [--format, statusml]
    passthrough_env: [TMUX] # optional: selected parent variables only
```

To run a shell command, select the shell as the executable and pass literal
arguments:

```yaml
status_customization:
  command:
    executable: /bin/sh
    args:
      [-c, 'read input; printf "<footer><text>local status</text></footer>"']
```

The program is invoked directly and receives the raw `Input` JSON only on stdin.
For example, this executable uses the context percentage while emitting fixed,
safe StatusML:

```python
#!/usr/bin/env python3
import json
import sys

status = json.load(sys.stdin)
percent = status["Context"]["Percent"]
print(f"<status><footer><text>ctx {percent}%</text></footer></status>")
```

Install it at the absolute path named above and make it executable:

```sh
chmod 0755 ~/.local/bin/mecatui-status
```

A command that interpolates string data into StatusML must escape that data
itself; commands do **not** receive the template escaping projection. They
should emit only one bounded StatusML document. Before StatusML parsing,
`mecatui` trims only leading and trailing ASCII space, tab, LF, CR, vertical
tab, and form feed. This accepts the trailing newline from the Python `print`
example above while preserving whitespace inside markup text. A supplied header
or footer replaces that surface; an omitted surface continues to use its shipped
default.

For an eligible local session, `mecatui` runs the executable in the workspace
root and includes that root as `Workspace.Path`. A remote path is never used as
a local working directory. When no eligible root exists, `Workspace.Path` is
empty and the executable runs from the configured executable's parent directory,
or from the launch directory when the parent cannot be determined.

The process receives `HOME`, `PATH`, `TERM`, `LANG`, `LC_ALL`, `COLUMNS`, and
`LINES` when available. `COLUMNS` and `LINES` reflect the submitted terminal
dimensions.

`passthrough_env` may add only explicitly named parent variables. Each name must
match `[A-Za-z_][A-Za-z0-9_]*`; reserved baseline and source-owned names are
rejected. Names are deduplicated, and unset variables are omitted. Do not list
secrets. No other parent environment values are inherited. For example, `[TMUX]`
makes an existing `TMUX` value available to a local tmux-aware integration.

Input changes are debounced for 250 ms, and only one command process tree runs
at a time. Replacement, timeout, and shutdown cancel it. Each invocation has a
one-second deadline, and standard output and standard error share a 4 KiB limit.
Failures never render raw output. A failed refresh keeps the last successful
surface with a stale marker when it fits, or falls back to the shipped default.

For the lower-level client architecture and the complete source lifecycle, see
the
[status-line section in `docs/tui.md`](https://github.com/stacklok/mecatl/blob/main/docs/tui.md#local-status-lines).

## Customize the terminal title

`mecatui` updates the terminal title when its rendered value changes and clears
it on a clean exit. Title templates produce plain text and are independent of
StatusML and status commands.

Configure the title in the client-owned
`$XDG_CONFIG_HOME/mecatui/settings.yaml` file (normally
`~/.config/mecatui/settings.yaml`):

```yaml
terminal_title:
  enabled: true
```

Set `template` to replace the shipped title template.

The setting applies to embedded and connected clients. The shipped title is
state-aware: it prefixes a titled session with a state label and elides the
title to 40 display columns. Without a title, it shows the state label with
`mecatui` when a session handle is available, otherwise just `mecatui`. It does
not include the session handle itself; add `.Session.Handle` when you want one.
Title templates use the shared template input described in [Status input
reference](#status-input-reference), including `Workspace.Path`, and support
the common `elide` and `lookup` functions.

Title writes follow this precedence:

1. `--terminal-title=off` disables the controller, while
   `--terminal-title=on` enables it even when settings disable it. Both forms
   accept `true`, `false`, `1`, and `0`.
2. When the flag is absent, `MECATUI_NO_TERMINAL_TITLE=1` or `true` disables
   title writes.
3. When neither explicit control applies, `terminal_title.enabled` controls
   the feature. The default is enabled.

A terminal emulator or multiplexer decides whether and where to show the title,
so a tab or pane label can remain unchanged. Disable titles when the terminal
environment owns title presentation. Invalid YAML, unknown fields, and invalid
title templates stop startup with an error that identifies `terminal_title` or
`terminal_title.template`. Restart `mecatui` after changing this file; settings
are not hot-reloaded.

## Next steps

- [Choose a theme](./themes.md) for the rest of the interface.
- [Find diagnostics](./troubleshooting.md#find-diagnostics) when a status
  command stays stale.
