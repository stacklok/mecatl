# ADR 0247 — mecatui generated status lines

- Status: Accepted
- Date: 2026-08-27
- Scope: `cmd/mecatui` local status-line generation and rendering
- Supersedes: none
- Superseded by: ADR 0289

## Context

Mecatui already has responsive header and status/usage rows, but operators cannot customize them or add local signals without changing the TUI. Simple customizations should not require a process; local Git, clock, task, and window-name integrations sometimes do. Mecatui may embed a server or connect remotely, so project or remote-server content must never configure local execution.

The `ui` package is deliberately pure: it renders client facts and owns frame geometry, but does not read settings or start processes. Status generation can change without a UI fact changing—a clock interval, command completion, stale fallback, or cancellation can publish a new result—so polling from Bubble Tea would either delay updates or add needless periodic work.

Themes must remain the sole source of terminal styling. Raw ANSI, OSC, and terminal hyperlinks would bypass that boundary. StatusML therefore needs a small semantic vocabulary, validated URL metadata, and a renderer that can later add terminal hyperlink support without accepting arbitrary control bytes.

## Decision

Place `status_customization:` only in the user-global `$XDG_CONFIG_HOME/mecatui/settings.yaml`, beside key bindings. It selects exactly one source: responsive Go-template surfaces or a direct local executable at an absolute `executable` path with literal `args`. Shell/source and command-string forms are not configuration. The configuration may supply optional header and footer surfaces; absent surfaces retain shipped defaults. Keyboard help, header safety/navigation indicators, and the footer activity lane remain renderer-owned chrome.

Compose one UI-agnostic `Source` outside `ui`. The UI submits the latest canonical raw `Input` whenever display facts or independently reserved header/footer widths change. The source owns template-versus-command selection, template evaluation, command debounce/cancellation/timeout/process cleanup, interval-driven clock refresh, stale-completion suppression, and per-surface default/last-good degradation.

```go
type Source interface {
    Submit(Input)
    Changed() <-chan struct{}
    Latest() Result
    Close(context.Context) error
}
```

`Changed` is a capacity-one wake-up edge, never a result queue. The source stores its latest result behind a mutex, allocates fresh bounded span slices on publish, and `Latest` deep-copies them before return. Private generations prevent stale command/template completions from publishing; no request token is exposed in input or result.

`Result` contains independently optional, width-selected header and footer semantic spans. It is generated—not terminal-rendered: it contains no ANSI or OSC bytes and carries semantic token/link metadata. The UI runs one Bubble Tea adapter command that waits on `Changed`, snapshots `Latest`, posts a message, and re-arms after each nonterminal notification. The listener captures the TUI root context; source shutdown cancels owned work, joins it, and closes `Changed` so the listener stops cleanly. The UI applies the active theme, joins the generated surfaces beside mandatory chrome, and performs final clipping/alignment.

Both source modes consume the same raw `Input`. Commands receive its JSON serialization on stdin and never receive template interpolation. Templates receive a private automatically StatusML-escaped projection; a direct executable must escape any dynamic values that it interpolates into StatusML. `Input` contains only display-safe facts: credential-free server target/connection mode, session/model facts, named usage/cache atoms, context facts, main/delegation activity, session-workspace provenance, terminal dimensions, per-surface available width, and clock. It excludes prompts, transcript/tool content, credentials, authentication metadata, diagnostics, raw command output, and the private local launch-workspace fallback.

StatusML has optional `header` and `footer` surfaces containing styled text/link nodes directly. The renderer left-aligns headers and right-aligns footers. It supports semantic style tokens and a `link` node with separate display text and a bounded quoted `href`. Only `http` and `https` links are accepted. Until terminal hyperlink support is added, links render as theme-styled underlined text; a future UI renderer may emit validated OSC 8 sequences.

## Consequences

Templates customize status without a process; local commands can run trusted user-global integrations from a local session workspace when known, otherwise the local launch workspace. Commands inherit only the explicit minimal environment and have bounded output and lifetime. A remote workspace never becomes a local command CWD.

The UI remains free of settings parsing, timers, subprocesses, and command policy while receiving autonomous latest-wins updates. The source becomes an outlives-a-call resource requiring lifecycle inventory, shutdown/leak, stale-result, and coalescing tests. Generated semantic spans preserve theme control and terminal safety, at the cost of maintaining a small StatusML parser rather than passing arbitrary terminal output through.

## See also

- [Extensible mecatui status-line acceptance plan](../acceptance/mecatui-status-line.md)
- [ADR 0027 — cloud-native lifecycle](./0027-cloud-native.md)
- [ADR 0002 — documentation lifecycle](./0002-documentation-lifecycle.md)
- [Issue #799 — stable semantic theme-token contract](https://github.com/stacklok/mecatl/issues/799)
