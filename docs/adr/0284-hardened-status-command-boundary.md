# ADR 0284 — Hardened status-command output and environment extension

- Status: Accepted
- Date: 2026-09-01
- Scope: `cmd/mecatui` local status-command boundary
- Supersedes: ADR 0247
- Superseded by: none

## Context

ADR 0247 established a fixed environment for local status commands and required a
single StatusML document. Common local integrations need a small piece of terminal
context such as `TMUX`, while ordinary command output from `print` includes a
trailing newline that should not make an otherwise valid document fail parsing.

Inheriting the parent environment to accommodate either case would expose unrelated
and potentially secret values. Broad Unicode whitespace trimming would also change
content beyond the command-document boundary.

## Decision

Keep the status-command environment fixed to its existing safe baseline. Permit only
additional names listed in the user-global `status_customization.command.passthrough_env`
allowlist. Each name must match `[A-Za-z_][A-Za-z0-9_]*`; unset names are omitted,
including no fallback from `os.Environ`. Deduplicate names, and never let a requested
name replace baseline or source-owned values such as terminal `COLUMNS` and `LINES`.

Immediately before StatusML parsing, trim only leading and trailing ASCII space,
tab, LF, CR, vertical tab, and form feed. Preserve all interior bytes; malformed
StatusML retains the existing safe fallback.

## Consequences

A status command can opt into `TMUX` and similar context without gaining ambient
environment access. Operators must explicitly list every additional value and should
not list credentials. Python's ordinary `print` output parses correctly,
while interior spacing remains meaningful to StatusML.

The status command has a slightly broader, operator-selected input boundary. The
strict settings validator rejects malformed names without reflecting their values.

## See also

- [ADR 0247 — mecatui generated status lines](./0247-mecatui-status-line.md)
- [mecatui status-line acceptance plan](../acceptance/mecatui-status-line.md)
- [mecatui guide](../tui.md)
- The public `user-docs/mecatui/status-line.md` status-line customization guide
- [ADR 0002 — documentation lifecycle](./0002-documentation-lifecycle.md)
