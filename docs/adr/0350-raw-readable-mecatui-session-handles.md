# ADR 0350 — Raw readable mecatui session handles

- Status: Accepted
- Date: 2026-09-21
- Scope: mecatui client-side session-handle presentation and debug-target resolution
- Supersedes: ADR 0285
- Superseded by: none

## Context

ADR 0285 selected percent-encoded, syntax-gated handles. The landed client deliberately
uses a simpler raw readable projection instead. It removes terminal controls and Unicode
format characters, retains the rest of a valid UTF-8 session ID verbatim, and clips at a
grapheme boundary by display width. Debug resolution likewise accepts a displayed handle
without requiring it to match a separate handle grammar.

The server remains the authority for opaque session IDs. The client may use its visible
inventory only to turn an unambiguous display projection back into an exact ID before it
creates a debug session. This correction records the operator-approved variance rather
than changing the shipped behavior to fit ADR 0285.

## Decision

Use one raw readable ordinary session-handle projection. Only a non-empty valid-UTF-8 ID is
eligible for a handle. Remove Unicode control (`Cc`) and format (`Cf`) runes, retain all other
readable characters verbatim, then truncate the remaining text at a grapheme boundary to at
most 12 display columns. Do not percent-encode, decode, impose a safe-character alphabet, or
add a handle marker.

Use the projection in ordinary mecatui session presentation. It is not an alternate server
identity. `/session` remains the exact full-ID copy path.

For every non-empty valid-UTF-8 debug `TARGET`, list the complete caller-visible inventory.
Deduplicate by exact ID. Prefer an exact full-ID match. Otherwise, resolve exactly one ID
whose displayed handle is identical to `TARGET`; reject multiple matches with full-ID copy
guidance. On inventory failure or no displayed-handle match, pass `TARGET` unchanged to the
existing server authority. Do not syntax-gate local resolution.

Both `mecatui debug TARGET` and `mecatui connect ADDRESS debug TARGET` use this one path.
Only an exact ID reaches the debug-session create request. Debugger evidence, scope, and
incarnation handles remain separate contracts.

## Consequences

Displayed handles remain readable for arbitrary valid UTF-8 IDs, including punctuation,
leading hyphens, percent signs, and non-ASCII characters. Removing controls and format
characters keeps ordinary terminal chrome safe, while grapheme-aware display-width
truncation avoids splitting visible characters. Distinct IDs can share a handle; users
resolve ambiguity by copying the full ID from `/session`.

Debug creation lists inventory for every valid target, including exact IDs, rather than
bypassing it based on spelling. Exact equality still wins, and the server still receives
only an exact ID or, on client fallthrough, the unchanged target. This retains server-side
authorization and not-found behavior without introducing a server-side handle namespace.

## See also

- [ADR 0217](./0217-session-discovery-continuation.md) — authoritative session inventory and full IDs.
- [ADR 0247](./0247-mecatui-status-line.md) — status-line session-handle presentation.
- [ADR 0254](./0254-session-debugger-admin-transport.md) — debugger target authority.
- [ADR 0256](./0256-session-debugger-evidence-and-reporting.md) — debugger evidence handles.
- [ADR 0258](./0258-cryptographic-session-incarnations.md) — target and incarnation handles.
- [Mecatui guide](../tui.md) — living client behavior.
- [Predictable session handles acceptance plan](../acceptance/predictable-session-handles.md).
- [ADR 0002](./0002-documentation-lifecycle.md) — frozen ADR lifecycle.
