# Predictable mecatui session handles — acceptance plan

**Phase:** capability — mecatui session discovery and debugger UX
**Status:** draft, 2026-09-01. Synthesized for issue #922 after the header/debug handle mismatch.
**Issue:** [stacklok/mecatl#922](https://github.com/stacklok/mecatl/issues/922).
**ADR:** [ADR-0278](../adr/0278-predictable-mecatui-session-handles.md) — one fixed client-side actionable short-handle contract, superseding ADR-0217's display-only digest decision.
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
- **Fixed escaped raw-ID prefixes, not digests.** [ADR-0278](../adr/0278-predictable-mecatui-session-handles.md)
  replaces only ADR-0217 decision 8's ordinary display-digest choice. The fixed projection keeps
  generated IDs recognizable and makes arbitrary valid-UTF-8 IDs safe without claiming that an
  escaped display token is a server ID. Invalid UTF-8 remains corrupt input: it emits no handle,
  is never repaired into a new identity, and cannot create a debugger session.
- **Status-line schema migration.** The external status-line `Input.Session.Digest` field becomes
  `Input.Session.Handle`; shipped StatusML templates and the documented template examples use
  `.Session.Handle`, with no duplicate digest alias. This is a protocol-schema change, so the
  existing `statusline.ProtocolVersion` advances from 1 to 2 and the status-line input reference
  documents the v2 contract.
- **Exact copy remains authoritative.** `/session` retains its safely quoted, byte-exact copy
  path, as required by [ADR-0217](../adr/0217-session-discovery-continuation.md); it is the
  fallback for an ambiguous handle, absent handle match, or unavailable inventory.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — One terminal-safe fixed handle renders without inventory

The client has one reusable handle projection available to ordinary presentation and debug
selection. For a non-empty valid-UTF-8 exact ID, percent-encode each UTF-8 byte outside
`[A-Za-z0-9._-]` as uppercase `%HH`, then take the longest prefix of complete literal or `%HH`
atoms whose rendered width is at most twelve ASCII columns. The token is compared as a
projection and is never decoded. It has no hash, `id-` namespace, `#` marker, collision
expansion, or rendering-time inventory dependency. It can be rendered immediately without
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
- AC1.3: An arbitrary non-empty valid-UTF-8 ID, including a long, shell-significant, or
  control-bearing legacy/custom value, produces the documented complete-atom, at-most-twelve
  ASCII-column literal. The literal contains only `[A-Za-z0-9._%-]`; empty IDs remain
  corrupt/invalid for actionable handling and receive no fabricated handle.
  - verify: `TestPredictableSessionHandles_Scenario1_EscapedUTF8AndControls`
- AC1.4: Invalid UTF-8 is not repaired or percent-encoded into a new identity: ordinary
  projections emit no handle, and debug creation is not attempted. Existing corrupt-snapshot
  handling and protobuf-boundary UTF-8 behavior remain unchanged.
  - verify: `TestPredictableSessionHandles_Scenario1_InvalidUTF8HasNoHandleOrDebugCreate`
- AC1.5: Header and other normal chrome render the fixed handle without inventory. If debug
  cannot obtain a complete caller-visible inventory, it stops before create and `/session` still
  safely renders and copies each valid non-empty exact ID unchanged.
  - verify: `TestPredictableSessionHandles_Scenario1_InventoryFailureKeepsExactCopyFallback`

---

### Scenario 2 — Debug resolves the displayed literal locally and sends only an exact ID

`CreateDebugSession` owns the one client-side resolver before it builds its existing create
request. A handle candidate is a non-empty ASCII token of at most twelve columns composed only
of safe literal atoms `[A-Za-z0-9._-]` and complete uppercase `%[0-9A-F]{2}` atoms. Lowercase, malformed,
or truncated escapes are not handles. Longer operands and every other operand remain exact-ID
inputs and bypass inventory resolution. For a syntactically valid short candidate only, the client
uses the existing all-pages `ListSessions` helper to obtain the complete caller-visible inventory,
first treats rows with the same exact ID as one candidate and checks exact equality, then compares
fixed projections for distinct exact IDs. Zero matches, ambiguity, or an inventory error fail
before create with concrete `/session` copy guidance. This preserves the debugger's ownership and
target binding in [ADR-0254](../adr/0254-session-debugger-admin-transport.md), its
`InspectSession` related/history evidence boundary in
[ADR-0256](../adr/0256-session-debugger-evidence-and-reporting.md), and its target+incarnation
cryptographic binding in [ADR-0258](../adr/0258-cryptographic-session-incarnations.md).

**Acceptance:**
- AC2.1: Only a syntactically valid short token invokes local resolution: it is non-empty ASCII,
  at most twelve columns, and consists of `[A-Za-z0-9._-]` literals and complete uppercase
  `%[0-9A-F]{2}` atoms. Lowercase, malformed, or truncated escape candidates, plus longer/other operands, remain
  exact-ID inputs and are sent unchanged without inventory lookup.
  - verify: `TestPredictableSessionHandles_Scenario2_HandleGrammar`
- AC2.2: For a syntactically valid short token, the complete caller-visible inventory is consulted
  before creation. Exact-ID equality wins before projection matching, and repeated rows for one
  exact ID count as one candidate.
  - verify: `TestPredictableSessionHandles_Scenario2_ExactIDPrecedesDistinctProjectionMatches`
- AC2.3: A literal short handle from the complete caller-visible inventory resolves uniquely before
  debug-session creation; the request carries the matched full ID, never the short handle.
  - verify: `TestPredictableSessionHandles_Scenario2_HandleResolvesToExactID`
- AC2.4: Zero matches, distinct projected-ID ambiguity, or an inventory error stops before
  `CreateSession`; each error gives concrete `/session` exact-copy guidance without disclosing
  rows the caller cannot see.
  - verify: `TestPredictableSessionHandles_Scenario2_FailClosedBeforeCreate`
- AC2.5: The literal emitted by the real rendered normal-session header passes unchanged through
  the real `CreateDebugSession` path and binds the resulting debugger request to that header's
  exact target; the same proof covers an escaped control-bearing valid-UTF-8 ID.
  - verify: `TestPredictableSessionHandles_Scenario2_RealHeaderHandleCreatesBoundDebugger`

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
- AC3.1: `mecatui debug` and `mecatui connect ADDRESS debug` help accurately say that they
  accept an exact session ID or a displayed short handle matching the literal grammar, direct
  ambiguous input to `/session` exact copy, and show no leading `#` marker.
  - verify: `TestPredictableSessionHandles_Scenario3_CommandHelp`
- AC3.2: `docs/tui.md`, `docs/usage.md`, the relevant `user-docs/` session/debug guides, and the
  status-line input reference use the handle term, explain the fixed escaped-prefix projection,
  preserve `/session` exact-copy guidance, and give shell-safe debug examples. They document the
  `Session.Digest` → `Session.Handle` schema rename and protocol v2, with no digest alias.
  - verify: inspection — `task docs` and `task site:build` validate the reviewed documentation paths
- AC3.3: Header, `/sessions`, debugger-target presentation, terminal title, status input, and
  shipped StatusML templates use the same handle grammar. The cross-boundary table below proves
  its edge cases and that only ordinary presentation changes; `InspectSession` scope/history
  handles, evidence digests, and target+incarnation cryptographic handles remain unchanged.
  - verify: `TestPredictableSessionHandles_Scenario3_PresentationParityAndSafety`
- AC3.4: Rename misleading legacy tests and summaries while landing the new contract: the
  unrelated `TestADR_0108_DisplayDigestIsNotAnID` becomes an ADR-0217/0278-named test, and the
  acceptance-plan/ADR README summaries say “handle”, never describe the ordinary projection as a
  debugger evidence digest.
  - verify: `TestADR_0278_OrdinaryHandleDoesNotAlterDebuggerEvidenceHandles`

**Cross-boundary oracle and boundary table**

| Boundary/input | Required result | Proof owner |
|---|---|---|
| exactly 12 safe literal columns | valid handle candidate; inventory exact-ID match precedes projections | client resolver |
| `%HH` at the 12-column boundary | test both: a complete three-column atom exactly fitting is emitted/accepted; one that cannot completely fit is omitted/rejected, never sliced | projection + grammar table tests |
| multibyte valid UTF-8 | byte-wise uppercase `%HH` atoms; no partial atom or terminal control reaches presentation | projection + presentation parity tests |
| invalid UTF-8 | no handle, no identity repair, no debug create; existing snapshot/protobuf behavior unchanged | invalid-UTF-8 regression |
| short exact ID colliding with a projection | inventory exact match wins; duplicate rows for that exact ID are one candidate | resolver table tests |
| unique, multiple, or zero projected-ID matches | unique sends the exact ID; multiple/zero stop before create with `/session` copy guidance | resolver table tests |
| normal header, `/sessions` row, debugger chrome, terminal title, status input/templates | same ordinary handle literal; no rendering inventory dependency | end-to-end presentation parity test |
| `InspectSession` related/history, evidence/manifest digest, target+incarnation handles | unchanged debugger-specific contracts | ADR-0254/0256/0258 regression test |

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| Server/proto alternate session-ID or short-handle API | never for this capability | [ADR-0278](../adr/0278-predictable-mecatui-session-handles.md) |
| Changing session ID generation, physical store naming, or ownership authorization | separate storage/identity work | [ADR-0104](../adr/0104-session-family-physical-naming.md), [ADR-0217](../adr/0217-session-discovery-continuation.md) |
| Changing `/session` full-ID display/copy semantics | not needed | [ADR-0217](../adr/0217-session-discovery-continuation.md) |
| `InspectSession` related/history handles, evidence/manifest digests, or target+incarnation cryptographic handles | explicitly preserved | [ADR-0254](../adr/0254-session-debugger-admin-transport.md), [ADR-0256](../adr/0256-session-debugger-evidence-and-reporting.md), [ADR-0258](../adr/0258-cryptographic-session-incarnations.md) |
| Collision-free short handles or inventory-dependent presentation | never for this capability | [ADR-0278](../adr/0278-predictable-mecatui-session-handles.md) |

## Sequencing recommendation

Land the fixed client handle projection first, including the control-bearing-ID, invalid-UTF-8,
and empty-ID contracts. Wire every ordinary presentation consumer without an inventory dependency,
including the status-line v2 `Session.Handle` migration and built-in templates. Then add the
complete-inventory resolver solely to debug and prove the real header-to-debug path and the
cross-boundary table. Update command wording and all living/public documentation only after the
shared contract is in place; no task should add a second resolver, collision expansion, a
server-side handle parser, or alter debugger evidence handles.

## Named tests landing in this plan

- `TestPredictableSessionHandles_Scenario1_*`
- `TestPredictableSessionHandles_Scenario2_*`
- `TestPredictableSessionHandles_Scenario3_*`
- `TestADR_0278_*`

## Definition of done

1. `task lint` and `task test` pass (both modules, `-race`).
2. `task docs` regenerates `llms.txt` and the matlatl strict link gate is green.
3. `task site:build` passes after the user-facing documentation changes.
4. `task ac-trace-strict` resolves every named proof when this plan is `landed`.
5. The real-header-to-real-`CreateDebugSession` test proves that an unchanged displayed literal
   produces an exact target ID on the create request for both normal and escaped valid-UTF-8 IDs.
6. `go run ./cmd/mecademo` still prints a full offline session.

## Deferred decisions and known risks

- **Debug-time inventory cost.** Rendering is immediate and inventory-free, but resolving a
  short handle requires a complete caller-visible inventory. A failed, incomplete, zero-match,
  or ambiguous resolution must stop before create and direct the user to `/session` exact copy.
- **Visible collisions.** Fixed handles intentionally do not expand. Ambiguity is detected only
  when debug is invoked; the exact full ID remains the predictable escape hatch.

## Exit criteria

When every point under *Definition of done* holds on the accumulator, this plan is satisfied.
