---
sidebar_position: 10
title: Status line customization
description: Customize mecatui's local header and footer status surfaces with templates or a command.
---

# Status line customization

Mecatui can generate the optional header and footer status surfaces from templates
or from one local executable. This is a **client-only** customization: configure it
only in `$XDG_CONFIG_HOME/mecatui/settings.yaml` (normally
`~/.config/mecatui/settings.yaml`). It applies to both embedded and `mecatui
connect` sessions. Project files, a remote server, prompts, and a session cannot
select or alter it.

With no `status_customization:` entry, mecatui uses its shipped responsive
templates. Keyboard help, the header posture/scroll/changed-file indicators, and
the footer activity lane remain mecatui-owned chrome; customization cannot remove
them.

## Source and result boundary

Mecatui keeps generation outside the Bubble Tea render model. The UI submits the
latest raw `Input`; the source publishes a coalesced wake-up edge and the UI takes
the newest `Result`. The result has independently optional `Header` and `Footer`
surfaces, each with one ordered semantic span list. A span contains display text, one
StatusML token, and optional validated link metadata—not ANSI, OSC, or final terminal
styling. Headers are renderer-left-aligned and footers renderer-right-aligned. The
active theme and the UI perform the final style, layout, clipping, and alignment work.

## Choose one source

`status_customization` must contain exactly one of `templates` or `command`. Its
optional `interval` must be at least one second. Without an interval, templates
refresh when their input changes and commands are event-driven.

```yaml
status_customization:
  interval: 1s
  templates:
    footer:
      full: >-
        <footer><text>{{.Clock.Now.Format "15:04:05"}} · ctx {{.Context.Percent}}%</text></footer>
      compact: >-
        <footer><text>{{.Clock.Now.Format "15:04"}}</text></footer>
      minimal: >-
        <footer><text>ctx</text></footer>
```

`header` and `footer` are independently optional. A supplied surface must provide
all three variants: `full`, `compact`, and `minimal`. Mecatui submits the remaining
columns for each surface after reserving its mandatory chrome, then selects the
richest variant that fits. Thus a compact header and a full footer may coexist; an
omitted surface retains the matching shipped template.

The interval also advances `.Clock.Now`, so the example above is a template-only
clock and creates no subprocess. Restart mecatui after editing settings; v1 does
not hot-reload this file.

## Template input and escaping

Templates receive a private projection of the status input. Every string is first
stripped of terminal control characters and then HTML-escaped for StatusML. A
substituted title such as `</accent><error>forged</error>` therefore stays literal
text; it cannot create tags, ANSI/OSC sequences, or links. Numeric fields remain
numeric. Use StatusML tags in the template itself, not in values from the input.

Template fields have the same shape as the command JSON below. `Clock.Now` is a
time value and supports `{{.Clock.Now.Format "15:04"}}`. The `Human` members are
preformatted display values; use each `Raw` member when a template needs an exact
count.

## Input reference

Every source receives the same versioned, display-safe snapshot. A template gets
the escaped projection described above. A command gets the raw JSON encoding on
standard input. The JSON field names are shown here in their emitted Go encoding.
Unknown values use the normal zero value: strings are empty, counts and dimensions
are zero, and `Clock.Now` is the zero time until the source refreshes it.

| JSON path | Type | Meaning |
| --- | --- | --- |
| `Version` | integer | Status input protocol version (currently `2`). |
| `Server.DisplayTarget` | string | Credential-free target shown by the client. |
| `Server.ConnectionMode` | string | `embedded`, `connect`, or empty while unknown. |
| `Session.Title` | string | Optional display title. |
| `Session.Handle` | string | Fixed 12-column ordinary session handle used by shipped headers: safe `[A-Za-z0-9._-]` bytes are literal except that a leading `-` is encoded as `%2D`; other UTF-8 bytes are uppercase `%HH`, and only complete atoms that fit are included. It has no leading `#` and replaces the v1 `Session.Digest` field in protocol v2; no digest alias is emitted. |
| `Session.Mode` | string | Active or pending permission mode used by the shipped header. |
| `Session.ReasoningEffort` | string | `low`, `medium`, `high`, `xhigh`, `max`, or empty. |
| `Model.ProviderID`, `Model.ID`, `Model.DisplayName`, `Model.Route` | strings | Provider/model routing identifiers, display label, and observed downstream route. |
| `Model.ContextWindow.{Raw,Human}` | integer, string | Resolved context capacity as exact and display-ready values. |
| `Usage.{Input,Output,CacheRead,CacheWrite}.{Raw,Human}` | integer, string | Cumulative exact and display-ready token atoms. `CacheRead` is a subset of input. |
| `Usage.CacheReadPercent` | integer | `CacheRead.Raw / Input.Raw` as an integer percentage, or `0` when input is zero. |
| `Context.{Used,Window}.{Raw,Human}` | integer, string | Current context use and capacity as exact and display-ready values. |
| `Context.Percent` | integer | `Used.Raw / Window.Raw` as an integer percentage, or `0` when unknown. |
| `Workspace.Location` | string | `local`, `remote`, or `unknown`. |
| `Workspace.Basename` | string | Provider-supplied local-session display label. It is not a directory basename or a usable path. |
| `Terminal.Rows`, `Terminal.Cols` | integers | Measured terminal dimensions. |
| `Terminal.HeaderAvailCols`, `Terminal.FooterAvailCols` | integers | Columns remaining after mecatui reserves mandatory header and footer lanes. |
| `MainAgent.State` | string | `connecting`, `idle`, `thinking`, `running_tool`, `awaiting_approval`, `completed`, `failed`, or `cancelled`. |
| `MainAgent.Activity` | string | Bounded display activity label. |
| `MainAgent.Approval` | string | `none` or `awaiting`. |
| `Delegation.{Total,DirectSubagent,TeamMember,ParallelBranch}` | object | Flat delegated-leaf counts by unit. Each object has `Running`, `AwaitingApproval`, `Completed`, `Failed`, `Cancelled`, and `Stopped` integer fields. `Total` is their component-wise sum. |
| `Delegation.Subagents`, `Delegation.Parallel` | object | Display summaries with `Running` and terminal-state-collapsed `Finished` counts. |
| `Delegation.Team` | object | Live-team display summary: `ID`, `Working`, and `Total`; all fields are zero/empty once the team is no longer live. |
| `Clock.Now` | RFC 3339 time | Source-owned current time; an interval refreshes it. |

The input deliberately excludes prompts, transcript and tool content, credentials,
authentication metadata, diagnostics, command output, and physical workspace roots.
The source privately uses the local launch directory as a command working-directory
fallback.

## StatusML

StatusML is semantic markup, not terminal output. A command emits one document
with optional `header` and `footer` surfaces; a template variant emits its own
surface fragment. The optional outer `<status>` wrapper is useful for a command
that supplies both surfaces:

```text
<status><header><accent>mecatui</accent><text> · GPT-5</text></header><footer><text>ctx 42%</text></footer></status>
```

Each surface contains its styled text and link nodes directly. Headers are
left-aligned by the renderer; footers are right-aligned. Text may be wrapped in
these semantic tokens:

`text`, `muted`, `primary`, `secondary`, `accent`, `success`, `warning`, `error`,
and `info`.

A link keeps its display text separate from its destination:

```text
<footer><link href="https://docs.example.test/status">status docs</link></footer>
```

### Escaping dynamic command output

Template substitutions are already escaped by mecatui. Do **not** escape a value
again inside a settings template.

A command instead receives raw JSON, so it must HTML-escape every dynamic value
that it places in a StatusML text node. Escape `&` first, then `<` and `>`;
quotes are also required when a value is placed in an attribute. StatusML decodes
those entities back to literal display text, so an escaped value cannot create a
tag or alter styling:

| Raw value | StatusML text-node value |
| --- | --- |
| `A & B` | `A &amp; B` |
| `<untrusted>` | `&lt;untrusted&gt;` |
| `</text><error>forged</error>` | `&lt;/text&gt;&lt;error&gt;forged&lt;/error&gt;` |

For example, a command written in Python can safely include a dynamic label:

```python
from html import escape

label = status["Workspace"]["Basename"]
print(f"<footer><text>{escape(label, quote=False)}</text></footer>")
```

Do not interpolate arbitrary data into `href`. A link destination must be a
trusted, bounded `http` or `https` URL without user information; use a fixed URL
or validate it with a URL parser before producing the StatusML document.

Only bounded `http` and `https` URLs without user information are retained. Until
terminal hyperlink support is added, a link is rendered as theme-styled underlined
text rather than an OSC 8 sequence. Malformed or unknown markup is literalized;
unsafe link destinations lose their destination but retain their display text.
Control characters, including ANSI, OSC, newline, tab, and Unicode line-separator
controls, are removed from markup text, link metadata, and theme data before rendering.
StatusML is always rendered as one terminal line.

The v1 token-to-palette mapping is a best effort, not a cross-widget compatibility
promise. [Issue #799](https://github.com/stacklok/mecatl/issues/799) owns the
stable semantic theme-token contract.

## Shipped template appendix

The following is a copy/paste equivalent of the shipped full, compact, and minimal
templates. The Go-template trim markers (`{{-` and `-}}`) remove source-line
whitespace, so each rendered value remains one StatusML line.

```yaml
status_customization:
  templates:
    header:
      full: |-
        <header><primary>mecatui · session {{.Session.Handle}} · {{if .Model.ProviderID}}{{.Model.ProviderID}}/{{end}}{{.Model.DisplayName}}{{if .Model.Route}}/{{.Model.Route}}{{end}}</primary>{{- if .Session.Mode -}}
        <warning> · mode {{.Session.Mode}}</warning>{{- end -}}
        {{- if .Server.DisplayTarget -}}
        <text> · {{.Server.DisplayTarget}}</text>{{- end -}}</header>
      compact: |-
        <header><primary>mecatui · {{.Session.Handle}} · {{.Model.DisplayName}}</primary>{{- if .Session.Mode -}}
        <warning> · {{.Session.Mode}}</warning>{{- end -}}</header>
      minimal: |-
        <header><primary>mecatui</primary></header>
    footer:
      full: |-
        <footer>{{- if or .Delegation.Parallel.Running .Delegation.Parallel.Finished -}}
        <accent>⑂ parallel {{.Delegation.Parallel.Running}}◐ {{.Delegation.Parallel.Finished}}✓</accent>{{"  "}}{{- end -}}
        {{- if or .Delegation.Subagents.Running .Delegation.Subagents.Finished -}}
        <accent>⛭ subagents {{.Delegation.Subagents.Running}}◐ {{.Delegation.Subagents.Finished}}✓</accent>{{"  "}}{{- end -}}
        {{- if .Delegation.Team.Total -}}
        <accent>⟳ team-{{.Delegation.Team.ID}} · {{.Delegation.Team.Working}}/{{.Delegation.Team.Total}} working</accent>{{"  "}}{{- end -}}
        {{- contextMeter .Context -}}
        <text> · ↑{{.Usage.Input.Human}} ↓{{.Usage.Output.Human}}{{- if .Usage.CacheWrite.Raw}} ⊕{{.Usage.CacheWrite.Human}}{{end}} cache {{.Usage.CacheReadPercent}}%</text></footer>
      compact: |-
        <footer>{{- if or .Delegation.Parallel.Running .Delegation.Parallel.Finished -}}
        <accent>⑂ {{.Delegation.Parallel.Running}}◐ {{.Delegation.Parallel.Finished}}✓</accent>{{"  "}}{{- end -}}
        {{- if or .Delegation.Subagents.Running .Delegation.Subagents.Finished -}}
        <accent>⛭ {{.Delegation.Subagents.Running}}◐ {{.Delegation.Subagents.Finished}}✓</accent>{{"  "}}{{- end -}}
        {{- if .Delegation.Team.Total -}}
        <accent>⟳ {{.Delegation.Team.Working}}/{{.Delegation.Team.Total}}</accent>{{"  "}}{{- end -}}
        {{- contextMeterCompact .Context -}}</footer>
      minimal: |-
        <footer>{{- contextMeterMinimal .Context -}}</footer>
```

## Direct executable command

Use `command` when local information needs a program. `executable` must be an
absolute path and `args` are literal arguments. The optional `passthrough_env` list
is the only environment extension: each name must match `[A-Za-z_][A-Za-z0-9_]*` and
must not be one of the reserved baseline or terminal-dimension names.
There are no shell, `source`, command-string, or CWD fields in the schema. `/bin/sh` is permitted
only by explicitly selecting it as `executable` and supplying its literal arguments;
it is not a shell mode or a default.

```yaml
status_customization:
  interval: 10s
  command:
    executable: /home/alice/.local/bin/mecatui-status
    args: [--format, statusml]
    passthrough_env: [TMUX] # optional: selected parent variables only
```

An inline shell command is therefore an explicit direct-executable opt-in, not a
separate configuration mode:

```yaml
status_customization:
  command:
    executable: /bin/sh
    args: [-c, 'read input; printf "<footer><text>local status</text></footer>"']
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

A command that interpolates string data into StatusML must escape that data itself;
commands do **not** receive the template escaping projection. They should emit only
one bounded StatusML document. Before StatusML parsing, mecatui trims only leading
and trailing ASCII space, tab, LF, CR, vertical tab, and form feed. This accepts the
trailing newline from the Python `print` example above while preserving whitespace
inside markup text. A supplied header or footer replaces that surface;
an omitted surface continues to use its shipped default.

Mecatui runs the executable in the eligible local root of the active session when
its opt-in local session-context service can resolve one. It refreshes that private
lookup after a session is created, adopted, cleared, forked, or switched, and
ignores an older response after a newer session becomes active. The root is used
only as the process CWD: it is not added to arguments, environment, command JSON,
template data, or the rendered UI. If context is unavailable or ineligible,
mecatui uses the local directory from which it was launched; a remote path is never
used as a local CWD. The process receives a fixed safe
baseline: `HOME`, `PATH`, `TERM`, `LANG`, `LC_ALL`, `COLUMNS`, and `LINES` when
available. `COLUMNS` and `LINES` come from the submitted terminal dimensions.

`passthrough_env` may add only explicitly named parent variables. Each name must
match `[A-Za-z_][A-Za-z0-9_]*`; reserved baseline and source-owned names are rejected
rather than silently ignored. Names are deduplicated, unset variables are omitted, Do not list secrets: no other parent
environment value is inherited, and mecatui never uses `os.Environ` for this command
boundary. For example, `[TMUX]` makes an existing `TMUX` value available for a local
tmux-aware integration.

Input changes are debounced for 250 ms. At most one contained command process tree
runs at a time; replacement, timeout, and shutdown cancel it. Each invocation has
a one-second deadline. Standard output and standard error share a 4 KiB streaming
limit. Malformed output, output overflow, terminal controls, or a failed command
never render raw output. A failed refresh preserves a bounded last-good supplied
surface with a stale marker when it fits; otherwise that surface falls back to its
shipped default. Command paths, arguments, input, output, and raw errors are not
shown in the status line or diagnostics.

For the lower-level client architecture and the complete source lifecycle, see the
[status-line section in `docs/tui.md`](https://github.com/stacklok/mecatl/blob/main/docs/tui.md#local-status-lines).
