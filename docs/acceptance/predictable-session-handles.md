# Predictable mecatui session handles — acceptance plan

**Phase:** capability — mecatui session discovery and debugger UX
**Status:** landed, 2026-09-01.
**Issue:** [stacklok/mecatl#922](https://github.com/stacklok/mecatl/issues/922).
**ADR:** [ADR-0280](../adr/0280-predictable-mecatui-session-handles.md) — one fixed client-side actionable short-handle contract, superseding ADR-0217's display-only digest decision.
**Related debugger boundaries:** [ADR-0254](../adr/0254-session-debugger-admin-transport.md), [ADR-0256](../adr/0256-session-debugger-evidence-and-reporting.md), and [ADR-0258](../adr/0258-cryptographic-session-incarnations.md).
**Accumulator branch:** `acc/predictable-session-handles` (off `main`).

The smallest set of work that makes the session handle shown by mecatui predictable,
terminal-safe, and usable to select that same stored session for debugging, while preserving
opaque exact IDs at every server boundary. It replaces the SHA-256 display identity with a
fixed escaped prefix; it does not add an alternate server identifier or a hash namespace.

The document is scenario-first: each scenario proves behavior a running embedded or connected
mecatui client can demonstrate, not a package-level implementation detail.

## Why these scope cuts

- **Ordinary presentation only.** This changes only mecatui's ordinary human-facing
  session-display projections: the normal header, `/sessions` rows, the debugger's target
  chrome and terminal title, and status-line input/templates. It does not change
  `InspectSession`, its related/history scope handles, its evidence/manifest digests, or the
  target- and incarnation-bound cryptographic handles defined by
  [ADR-0254](../adr/0254-session-debugger-admin-transport.md),
  [ADR-0256](../adr/0256-session-debugger-evidence-and-reporting.md), and
  [ADR-0258](../adr/0258-cryptographic-session-incarnations.md). Those remain distinct
  debugger evidence and authority contracts.
- **Client-only resolution.** The server continues to receive and authorize only exact opaque
  IDs. The complete caller-visible inventory is used only when the user invokes debug with a
  syntactically valid short handle, through the existing all-pages `ListSessions` helper in
  [`cmd/mecatui/client/sessions_list.go`](../../cmd/mecatui/client/sessions_list.go); no proto,
  server API, streaming scan, alternate-ID authorization surface, or paging abstraction is
  introduced.
- **Fixed escaped raw-ID prefixes, not digests.** [ADR-0280](../adr/0280-predictable-mecatui-session-handles.md)
  replaces only ADR-0217 decision 8's ordinary display-digest choice. The fixed projection keeps
  generated IDs recognizable and makes arbitrary valid-UTF-8 IDs safe without claiming that an
  escaped display token is a server ID. Invalid UTF-8 remains corrupt input: it emits no handle,
  is never repaired into a new identity, and cannot create a debugger session.
- **Status-line schema migration.** The external status-line `Input.Session.Digest` field becomes
  `Input.Session.Handle`; shipped StatusML templates and the documented template examples use
  `.Session.Handle`, with no duplicate digest alias. This is a protocol-schema change, so the
  existing `statusline.ProtocolVersion` advances from 1 to 2 and the status-line input reference
  documents the v2 contract.
- **Exact IDs remain authoritative.** `/session` retains its safely quoted, byte-exact copy
  path, as required by [ADR-0217](../adr/0217-session-discovery-continuation.md). The explicit
  `debug --exact SESSION_ID` form (and connected equivalent) bypasses inventory and is the
  fallback for an ambiguous handle, a short exact ID, or unavailable inventory. Existing long
  or syntactically non-handle exact IDs continue to bypass resolution automatically.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — One terminal-safe fixed handle renders without inventory

The client has one reusable handle projection available to ordinary presentation and debug
selection. For a non-empty valid-UTF-8 exact ID, percent-encode each UTF-8 byte outside
`[A-Za-z0-9._-]` as uppercase `%HH`, also encoding a leading `-` as `%2D`, then take the longest
prefix of complete literal or `%HH` atoms whose rendered width is at most twelve ASCII columns.
Hyphens after the first atom remain literal. The token is compared as a projection and is never
decoded. It has no hash, `id-` namespace, `#` marker, collision expansion, or rendering-time
inventory dependency. It can be rendered immediately without
loading inventory. This client concern leaves the full opaque ID as the server-owned identity
under [ADR-0217](../adr/0217-session-discovery-continuation.md), and leaves debugger
scope/evidence and cryptographic handle contracts unchanged under
[ADR-0254](../adr/0254-session-debugger-admin-transport.md),
[ADR-0256](../adr/0256-session-debugger-evidence-and-reporting.md), and
[ADR-0258](../adr/0258-cryptographic-session-incarnations.md).

**Acceptance:**
- AC1.1: A normal generated session ID has a handle containing its first twelve characters;
  header, `/sessions`, debugger target chrome, terminal title, and status input use that same
  literal rather than an ordinary display digest.
  - verify: `TestPredictableSessionHandles_Scenario1_SharedNormalHandle`
- AC1.2: Two IDs with the same fixed handle retain that same twelve-column projection; rendering
  neither expands either token nor loads the complete inventory, and pagination or ordering
  cannot change the displayed literal.
  - verify: `TestPredictableSessionHandles_Scenario1_FixedCollisionBehavior`
- AC1.3: An arbitrary non-empty valid-UTF-8 ID, including a long, shell-significant,
  leading-hyphen, or control-bearing legacy/custom value, produces the documented complete-atom,
  at-most-twelve ASCII-column literal. The literal contains only `[A-Za-z0-9._%-]`, never begins
  with `-`, and preserves non-leading hyphens literally; empty IDs remain corrupt/invalid for
  actionable handling and receive no fabricated handle.
  - verify: `TestPredictableSessionHandles_Scenario1_EscapedUTF8ControlsAndLeadingHyphen`
- AC1.4: Invalid UTF-8 is not repaired or percent-encoded into a new identity: ordinary
  projections emit no handle, and debug creation is not attempted. Existing corrupt-snapshot
  handling and protobuf-boundary UTF-8 behavior remain unchanged.
  - verify: `TestPredictableSessionHandles_Scenario1_InvalidUTF8HasNoHandleOrDebugCreate`
- AC1.5: Header and other normal chrome render the fixed handle without inventory. If positional
  debug cannot obtain a complete caller-visible inventory, it stops before create and directs the
  operator to `/session` plus `debug --exact`; that explicit form still sends each valid non-empty
  exact ID unchanged without inventory.
  - verify: `TestPredictableSessionHandles_Scenario1_InventoryFailureKeepsExplicitExactFallback`
- AC1.6: `SessionHandleWidth` is the sole exported ordinary-handle width constant; the obsolete
  `SessionIDDisplayWidth` compatibility alias is absent, with no change to debugger evidence or
  incarnation digest APIs.
  - verify: `TestPredictableSessionHandles_Scenario1_OnlyHandleWidthAPI`

---

### Scenario 2 — Debug resolves the displayed literal locally and sends only an exact ID

`CreateDebugSession` owns the one client-side resolver before it builds its existing create
request. A positional handle candidate is a non-empty ASCII token of at most twelve columns whose
first atom is `[A-Za-z0-9._]` or complete uppercase `%[0-9A-F]{2}` and whose later atoms may also
be `-`. Lowercase, malformed, truncated, leading-hyphen, longer, and other operands remain exact-ID
inputs and bypass inventory automatically. For a syntactically valid short candidate only, the
client uses the existing all-pages `ListSessions` helper to obtain the complete caller-visible
inventory, deduplicates exact IDs, gathers every distinct ID whose projection equals the token, and
requires exactly one match. Exact string equality does not take precedence over another projected
match. Zero matches, ambiguity, or inventory error fail before create with concrete `/session` and
`--exact` guidance. `mecatui debug --exact SESSION_ID` and
`mecatui connect ADDRESS debug --exact SESSION_ID` route through a small
`Client.CreateDebugSessionExact` sibling that shares the private create tail but bypasses inventory
and sends the supplied exact ID unchanged; `--exact` and the positional operand are mutually
exclusive. This preserves the debugger's ownership and
target binding in [ADR-0254](../adr/0254-session-debugger-admin-transport.md), its
`InspectSession` related/history evidence boundary in
[ADR-0256](../adr/0256-session-debugger-evidence-and-reporting.md), and its target+incarnation
cryptographic binding in [ADR-0258](../adr/0258-cryptographic-session-incarnations.md).

**Acceptance:**
- AC2.1: Only a syntactically valid positional short token invokes local resolution: it is
  non-empty ASCII, at most twelve columns, begins with `[A-Za-z0-9._]` or a complete uppercase
  `%[0-9A-F]{2}` atom, and thereafter consists of `[A-Za-z0-9._-]` literals or complete uppercase
  escapes. Lowercase, malformed, or truncated escapes, plus longer and other operands accepted
  positionally, remain exact IDs and bypass inventory. A leading-hyphen exact ID uses
  `--exact SESSION_ID` so it cannot be mistaken for a flag; the exact form also bypasses inventory
  and is mutually exclusive with the positional operand in embedded and connected forms.
  - verify: `TestPredictableSessionHandles_Scenario2_HandleGrammarAndExactEscapeHatch`
- AC2.2: For a syntactically valid short token, the complete caller-visible inventory is consulted
  before creation. Repeated rows for one exact ID count once; all distinct projected matches are
  gathered before selection, and multiple matches fail ambiguous even when one full ID exactly
  equals the token.
  - verify: `TestPredictableSessionHandles_Scenario2_AllProjectedMatchesPrecedeSelection`
- AC2.3: A literal short handle from the complete caller-visible inventory resolves uniquely before
  debug-session creation; the request carries the matched full ID, never the short handle. Explicit
  `--exact` sends its supplied valid ID unchanged and remains usable for short exact IDs and
  inventory outage.
  - verify: `TestPredictableSessionHandles_Scenario2_HandleAndExplicitExactPaths`
- AC2.4: Zero matches, distinct projected-ID ambiguity (including an exact-token collision), or an
  inventory error stops before `CreateSession`; each error gives concrete `/session` and `--exact`
  guidance without disclosing rows the caller cannot see.
  - verify: `TestPredictableSessionHandles_Scenario2_FailClosedWithExactGuidance`
- AC2.5: The literal emitted by the real rendered normal-session header passes unchanged through
  the real `Client.CreateDebugSession` path and binds the resulting request to that header's exact
  target, including an escaped control-bearing ID. This proof lives in composition-level
  `cmd/mecatui`, drives public UI update/View paths, and keeps `cmd/mecatui/ui` tests proto/gRPC-free
  without widening production APIs for test access.
  - verify: `TestPredictableSessionHandles_Scenario2_RenderedHeaderCreatesBoundDebugger`

---

### Scenario 3 — Help and operator documentation make the contract usable

The command index, command-specific help, TUI guide, operator usage guide, public mecatui
documentation, and status-line input reference describe one shared name: a short handle is the
displayed, client-resolved literal. The status-line schema renames `Session.Digest` to
`Session.Handle`; its built-in StatusML templates use `.Session.Handle` without `#`, and its
protocol version advances from 1 to 2 rather than retaining an alias. Examples may quote a handle
for shell safety, but no chrome punctuation must be removed before use. They retain the full ID
and `/session` copy flow. This is a user-visible client behavior, so the documentation lifecycle
in [`AGENTS.md`](../../AGENTS.md) requires both living docs and `user-docs/` coverage.

**Acceptance:**
- AC3.1: `mecatui debug` and `mecatui connect ADDRESS debug` help accurately distinguish a
  positional exact ID or displayed short handle from the explicit `--exact SESSION_ID` bypass,
  state their mutual exclusivity, direct ambiguous or inventory-failed input to `/session` plus
  `--exact`, explain that a leading-hyphen exact ID requires `--exact`, and show no leading `#`
  marker.
  - verify: `TestPredictableSessionHandles_Scenario3_CommandHelpAndExactBypass`
- AC3.2: `docs/tui.md`, `docs/usage.md`, the relevant `user-docs/` session/debug guides, and the
  status-line input reference use the handle term, explain leading-hyphen encoding and the fixed
  escaped-prefix projection, preserve `/session` exact-copy guidance, and document both embedded
  and connected `--exact` examples. They document the `Session.Digest` → `Session.Handle` schema
  rename and protocol v2, with no digest alias.
  - verify: inspection — `task docs` and `task site:build` validate the reviewed documentation paths
- AC3.3: Header, `/sessions`, debugger-target presentation, terminal title, status input, and
  shipped StatusML templates use the same handle grammar. The cross-boundary table proves its edge
  cases and that only ordinary presentation changes; `InspectSession` scope/history handles,
  evidence digests, and target+incarnation cryptographic handles remain unchanged. The relocated
  transport-spanning proof is composition-level and introduces no new proto/gRPC dependency into
  `cmd/mecatui/ui`.
  - verify: `TestPredictableSessionHandles_Scenario3_PresentationParitySafetyAndLayering`
- AC3.4: The landed session-continuity plan's AC4.2 and AC4.3 point directly to current ADR-0280
  scenario tests. Stale `TestADR_0108_DisplayDigestIsNotAnID`, digest-named compatibility aliases,
  and pre-ADR-0280 session-handle test names are absent; ordinary presentation is never described as a
  debugger evidence digest.
  - verify: `TestADR_0280_OrdinaryHandleDoesNotAlterDebuggerEvidenceHandles`
- AC3.5: ADR-0280 explicitly supersedes only ADR-0217's ordinary display-handle decision and
  ADR-0254's `DEBUG target #<digest>` presentation clause; both older ADRs carry scoped backlinks,
  while every debugger authority, evidence, and incarnation decision remains in force.
  - verify: inspection — `task docs` validates ADR metadata and links

**Cross-boundary oracle and boundary table**

| Boundary/input | Required result | Proof owner |
|---|---|---|
| exactly 12 safe literal columns | valid positional handle candidate; gather every distinct projected match before selecting | client resolver |
| `%HH` at the 12-column boundary | test both: a complete three-column atom exactly fitting is emitted/accepted; one that cannot completely fit is omitted/rejected, never sliced | projection + grammar table tests |
| leading `-` in exact ID | projected as leading `%2D`; later hyphens remain literal; `--exact` supplies the literal unchanged path | projection + CLI grammar tests |
| multibyte valid UTF-8 | byte-wise uppercase `%HH` atoms; no partial atom or terminal control reaches presentation | projection + presentation parity tests |
| invalid UTF-8 | no handle, no identity repair, no debug create; existing snapshot/protobuf behavior unchanged | invalid-UTF-8 regression |
| short exact ID colliding with another projection | ambiguous after gathering all distinct projected matches; use `/session` plus `--exact` | resolver table tests |
| unique, multiple, or zero projected-ID matches | unique sends the exact ID; multiple/zero stop before create with `/session` and `--exact` guidance | resolver table tests |
| explicit `--exact SESSION_ID` | bypass inventory and send the supplied valid ID unchanged; mutually exclusive with positional operand | embedded + connected CLI tests |
| normal header, `/sessions` row, debugger chrome, terminal title, status input/templates | same ordinary handle literal; no rendering inventory dependency | presentation parity + composition integration tests |
| relocated header-to-debug proof | introduces no proto or gRPC dependency into `cmd/mecatui/ui`; transport-spanning proof lives in `cmd/mecatui` | layering + integration tests |
| `InspectSession` related/history, evidence/manifest digest, target+incarnation handles | unchanged debugger-specific contracts | ADR-0254/0256/0258 regression test |

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| Server/proto alternate session-ID or short-handle API | never for this capability | [ADR-0280](../adr/0280-predictable-mecatui-session-handles.md) |
| Changing session ID generation, physical store naming, or ownership authorization | separate storage/identity work | [ADR-0104](../adr/0104-session-family-physical-naming.md), [ADR-0217](../adr/0217-session-discovery-continuation.md) |
| Changing `/session` full-ID display/copy semantics | not needed | [ADR-0217](../adr/0217-session-discovery-continuation.md) |
| `InspectSession` related/history handles, evidence/manifest digests, or target+incarnation cryptographic handles | explicitly preserved | [ADR-0254](../adr/0254-session-debugger-admin-transport.md), [ADR-0256](../adr/0256-session-debugger-evidence-and-reporting.md), [ADR-0258](../adr/0258-cryptographic-session-incarnations.md) |
| Collision-free short handles or inventory-dependent presentation | never for this capability | [ADR-0280](../adr/0280-predictable-mecatui-session-handles.md) |

## Sequencing recommendation

Land the resolver/CLI repair first: leading-hyphen projection, gather-all ambiguity semantics,
the explicit exact-ID bypass, and width-API cleanup. Then restore package layering by relocating
the transport-spanning proof, update traceability and ADR/docs identities, and reconcile living and
public documentation. No repair task should add a second resolver, collision expansion, a
server-side handle parser, or alter debugger evidence handles.

## Named tests landing in this plan

- `TestPredictableSessionHandles_Scenario1_*`
- `TestPredictableSessionHandles_Scenario2_*`
- `TestPredictableSessionHandles_Scenario3_*`
- `TestADR_0280_*`

## Definition of done

1. `task lint` and `task test` pass (both modules, `-race`).
2. `task docs` regenerates `llms.txt` and the matlatl strict link gate is green.
3. `task site:build` passes after the user-facing documentation changes.
4. `task ac-trace-strict` resolves every named proof when this plan is `landed`.
5. The composition-level real-header-to-real-`CreateDebugSession` test proves that an unchanged
   displayed literal produces an exact target ID on the create request for normal and escaped
   valid-UTF-8 IDs while `cmd/mecatui/ui` remains proto/gRPC-free.
6. `go run ./cmd/mecademo` still prints a full offline session.

## Deferred decisions and known risks

- **Debug-time inventory cost.** Rendering is immediate and inventory-free, but resolving a
  positional short handle requires a complete caller-visible inventory. A failed, incomplete,
  zero-match, or ambiguous resolution stops before create and directs the operator to `/session`
  plus the inventory-free `--exact` path.
- **Visible collisions.** Fixed handles intentionally do not expand. All distinct projected
  matches are gathered before selection, so even an exact-token row cannot mask a collision; the
  explicit full-ID path remains the predictable escape hatch.

## Exit criteria

When every point under *Definition of done* holds on the accumulator, this plan is satisfied.
