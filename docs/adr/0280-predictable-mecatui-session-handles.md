# ADR 0280 — Predictable actionable mecatui session handles

- Status: Accepted
- Date: 2026-09-01
- Scope: mecatui client-side session-handle presentation and debug-target resolution
- Supersedes: ADR 0217 decision 8 (display handles) and ADR 0254 decision paragraph 3's `DEBUG target #<digest>` presentation clause only
- Superseded by: —

## Context

ADR 0217 correctly keeps the opaque full session ID as the only server API identity and
makes terminal display safe. Its display-only SHA-256 digest, however, diverged from the
separate raw-ID-prefix resolver used by `mecatui debug`: the prominent header and inventory
handle could not be used to select the session it named. The command help then incorrectly
called that undisplayed prefix the header ID.

Session IDs may be arbitrary valid UTF-8, including control-bearing legacy or custom values.
A raw byte prefix is neither a safe terminal representation nor a safe truncation boundary.
The server must not accept alternate identities: doing so would widen its authorization
surface and conflict with the opaque-ID contract. Hashing non-sensitive IDs to create a
separate display identity adds needless complexity without making the displayed token more
actionable.

## Decision

Replace the ordinary display digest with one client-owned, terminal-safe fixed short-handle
projection based on the exact session ID. For a non-empty valid-UTF-8 ID, percent-encode each
UTF-8 byte outside `[A-Za-z0-9._-]` as uppercase `%HH`, and also encode `-` as `%2D` when it
would be the first emitted atom. Then take the longest prefix of complete literal or `%HH` atoms
whose rendered width is at most twelve ASCII characters. A handle therefore never begins with
`-`, while later hyphens remain literal. The projection is never decoded: the client compares it
directly to projections from visible exact IDs. It has no hash, invented `id-` namespace, `#`
marker, or collision expansion. Normal generated 32-character hexadecimal IDs therefore display
their first twelve characters unchanged, while every emitted token uses the terminal- and
shell-safe alphabet `[A-Za-z0-9._%-]`.

This changes only ordinary mecatui presentation: the normal header, `/sessions` rows, debugger
target chrome and terminal title, and status-line input/templates. It does not change
`InspectSession`, its related/history scope handles, evidence or manifest digests, or the
target+incarnation-bound cryptographic handles defined by ADRs 0254, 0256, and 0258. Invalid
UTF-8 IDs remain corrupt: no handle is emitted, no replacement identity is synthesized, and no
debug-session create is attempted. Existing corrupt-snapshot and protobuf-boundary UTF-8 behavior
remains unchanged.

The client renders this same fixed literal without inventory in every ordinary presentation
surface. The external status-line schema renames `Input.Session.Digest` to `Input.Session.Handle`;
shipped StatusML templates use `.Session.Handle` with no `#` prefix, and no duplicate digest alias
is retained. This schema break advances the existing `statusline.ProtocolVersion` from 1 to 2 and
the status-line documentation must describe v2.

A positional debug operand is a handle candidate only when it is a non-empty ASCII token of at
most twelve columns whose first atom is `[A-Za-z0-9._]` or a complete uppercase
`%[0-9A-F]{2}` escape and whose later atoms may additionally be literal `-`. Lowercase,
malformed, or truncated escape candidates are not handles; longer and other operands accepted
positionally remain exact-ID inputs and bypass inventory automatically. A leading-hyphen exact ID
uses the explicit `--exact` form below so the command parser cannot mistake it for a flag. For a syntactically valid short candidate,
`mecatui debug` uses the existing all-pages `ListSessions` helper to obtain the complete
caller-visible inventory. It deduplicates repeated rows by exact ID, gathers every distinct ID
whose fixed projection equals the token, and requires exactly one projected match. Exact string
equality does not take precedence: if another ID projects to that same token, selection is
ambiguous even when one candidate's full ID equals the operand.

Both embedded and connected command forms provide the explicit exact-ID escape hatch
`mecatui debug --exact SESSION_ID` and
`mecatui connect ADDRESS debug --exact SESSION_ID`. The CLI routes that form through a small
`Client.CreateDebugSessionExact` sibling which shares the private request/create tail with
`CreateDebugSession` but skips inventory resolution; this explicit API exists for the product
behavior, not for test access. It bypasses inventory and sends the supplied valid exact ID
unchanged. This is the fallback for ambiguous short handles, short exact IDs,
leading-hyphen IDs, and inventory outage; `/session` remains the byte-exact source to copy from.
The positional operand and `--exact` are mutually exclusive and one is required. A zero projected
match, ambiguous projection, or inventory error fails before debug-session creation with concrete
`/session` and `--exact` guidance. Only the selected exact ID is put into
`CreateSessionRequest` or any other server API request. Thus neither a short token nor its
encoding crosses the server boundary, and no streaming scan or new paging abstraction is
introduced.

## Consequences

Headers and lists are immediate, stable, and inventory-independent. A short handle may collide;
that is an intentional trade-off for a predictable fixed twelve-column contract. The client pays
the existing caller-filtered all-pages inventory cost only for syntactically valid positional
short debug operands. All distinct projected matches are considered together; duplicate rows for
one exact ID do not create ambiguity, but exact equality cannot hide a collision. The explicit
`--exact` path and malformed/lowercase/truncated escapes, longer operands, and other non-handle
operands do not invoke inventory lookup.

The client keeps one small presentation/resolution utility rather than leaking a new ID type
through protobuf or server code. `SessionHandleWidth` is its only exported width constant;
the obsolete `SessionIDDisplayWidth` compatibility alias is removed. Existing full-ID inputs,
`/session`'s exact byte-preserving copy action, and server-side ownership/authorization remain
unchanged. Help examples use `--exact` for a literal unchanged path and may quote ordinary
handles for shell safety; they never instruct users to remove display punctuation, because no
such punctuation is rendered. The status-line protocol intentionally breaks from v1 `Digest` to
v2 `Handle`; templates and documentation move together, without a compatibility alias. The
already-landed session-continuity acceptance plan is updated to point its AC4.2/AC4.3 checks at
the ADR-0280 scenario proofs, after which stale ADR-0108 and digest-named compatibility test
aliases are removed; no frozen decision body is rewritten.

The rendered-header-to-transport integration proof belongs in the composition-level
`cmd/mecatui` package. It drives public UI update and `View` paths to obtain the real rendered
handle, then passes that literal through the real public `Client.CreateDebugSession` path. The
`cmd/mecatui/ui` package and its tests remain free of protobuf and gRPC imports; no production API
is widened solely for the test.

## See also

- [ADR 0217](./0217-session-discovery-continuation.md) — session metadata, authoritative inventory, and the superseded ordinary display-digest decision.
- [ADR 0247](./0247-mecatui-status-line.md) — versioned status-line input and StatusML templates.
- [ADR 0254](./0254-session-debugger-admin-transport.md) — dedicated debugger and `InspectSession` evidence boundary.
- [ADR 0256](./0256-session-debugger-evidence-and-reporting.md) — target-bound related/history scope handles and evidence digests.
- [ADR 0258](./0258-cryptographic-session-incarnations.md) — target+incarnation cryptographic handle binding.
- [ADR 0104](./0104-session-family-physical-naming.md) — opaque logical IDs and non-reversible physical store names.
- [Mecatui guide](../tui.md) — living client interaction behavior.
- [Issue #922](https://github.com/stacklok/mecatl/issues/922).
