# Predictable mecatui session handles — acceptance plan

**Phase:** capability — mecatui session discovery and debugger UX
**Status:** landed, 2026-09-02; traceability corrected 2026-09-21.
**Issue:** [stacklok/mecatl#922](https://github.com/stacklok/mecatl/issues/922).
**ADR:** [ADR-0350](../adr/0350-raw-readable-mecatui-session-handles.md) — raw readable
ordinary handles and non-syntax-gated debug resolution, superseding ADR-0285.
**Related debugger boundaries:** [ADR-0254](../adr/0254-session-debugger-admin-transport.md),
[ADR-0256](../adr/0256-session-debugger-evidence-and-reporting.md), and
[ADR-0258](../adr/0258-cryptographic-session-incarnations.md).
**Accumulator branch:** `acc/predictable-session-handles` (off `main`).

The landed client gives ordinary session presentation and debug selection one readable
projection while preserving opaque exact IDs at every server boundary. It does not add an
alternate server identifier, hash namespace, or server-side handle parser.

## Scope cuts

- **Ordinary presentation only.** The normal header, `/sessions` rows, debugger target
  chrome, and status input use the same handle. `InspectSession` scope/history handles,
  evidence and manifest digests, and target-and-incarnation cryptographic handles remain
  separate contracts under ADRs 0254, 0256, and 0258.
- **Client-only resolution.** The server receives and authorizes exact opaque IDs. The
  client consults the complete caller-visible inventory through
  [`cmd/mecatui/client/sessions_list.go`](../../cmd/mecatui/client/sessions_list.go) only to
  resolve a displayed handle.
- **Exact IDs remain authoritative.** `/session` retains its full-ID copy path. An exact
  inventory match wins. Ambiguity directs the operator to copy the full ID; an inventory
  error or no displayed-handle match passes `TARGET` unchanged to server authority.

## Landed behavior

### Scenario 1 — One raw readable handle renders without inventory

For a non-empty valid-UTF-8 ID, the client removes Unicode control (`Cc`) and format
(`Cf`) runes, preserves every other rune verbatim, then truncates the result at a grapheme
boundary to at most 12 display columns. Empty and invalid-UTF-8 IDs have no handle. The
projection does not encode or decode characters, require a safe-character alphabet, use a
marker, or depend on inventory.

**Acceptance:**

- AC1.1: Ordinary header, `/sessions`, debugger chrome, and status input use the same
  12-display-column handle.
  - verify: `TestPredictableSessionHandles_Scenario1_SharedNormalHandle`
- AC1.2: A handle preserves readable punctuation, leading hyphens, percent signs, and
  non-ASCII text; removes `Cc` and `Cf` runes; and does not split graphemes or exceed its
  display-column bound.
  - verify: `TestPredictableSessionHandles_Scenario1_RawUTF8AndDisplayColumns`
  - verify: `TestPredictableSessionHandles_Scenario1_OnlyHandleWidthAPI`
- AC1.3: Fixed handles do not expand for collisions or depend on inventory order.
  - verify: `TestPredictableSessionHandles_Scenario1_FixedCollisionBehavior`
- AC1.4: Invalid UTF-8 has no handle and cannot create a debug session.
  - verify: `TestPredictableSessionHandles_Scenario1_InvalidUTF8HasNoHandleOrDebugCreate`

### Scenario 2 — Debug resolution uses exact IDs or identical displayed handles

For every non-empty valid-UTF-8 `TARGET`, `CreateDebugSession` lists the complete
caller-visible inventory and deduplicates exact IDs. Exact equality takes priority.
Otherwise, one identical displayed handle resolves to that exact ID; multiple matches stop
before create with `/session` full-ID copy guidance. Inventory errors and zero matches pass
`TARGET` unchanged to the server's existing exact-ID path. Both embedded and connected
commands share the positional `TARGET` grammar; resolution has no syntax gate.

**Acceptance:**

- AC2.1: Embedded and connected debug commands pass one positional `TARGET`, including
  readable targets such as leading hyphens.
  - verify: `TestPredictableSessionHandles_Scenario2_UnifiedTargetGrammar`
- AC2.2: Exact equality wins; one displayed-handle match resolves; duplicate inventory
  rows do not create ambiguity; and ambiguous, zero-match, and inventory-error outcomes
  follow the documented paths.
  - verify: `TestCreateDebugSessionResolution`
- AC2.3: Raw readable handles for control-stripped, leading-hyphen, percent, slash, and
  multibyte IDs resolve to the full exact ID before debug-session creation.
  - verify: `TestCreateDebugSessionResolvesRenderedControlSafeHandle`
  - verify: `TestCreateDebugSessionResolvesAnyDisplayedUTF8Handle`
- AC2.4: A handle taken from the rendered header and a full final ID both reach the create
  request as the same exact target.
  - verify: `TestPredictableSessionHandles_Scenario2_RenderedHeaderCreatesBoundDebugger`

### Scenario 3 — Documentation and debugger evidence retain their boundaries

Help and current documentation describe the same `TARGET` flow and `/session` fallback.
Ordinary handles do not replace debugger evidence or exact server IDs.

**Acceptance:**

- AC3.1: Command help presents the one-`TARGET` flow and full-ID fallback.
  - verify: `TestPredictableSessionHandles_Scenario3_CommandHelpUsesOneTargetFlow`
- AC3.2: Ordinary-handle presentation does not alter debugger evidence handles or exact
  authoritative IDs.
  - verify: `TestADR_0350_OrdinaryHandleDoesNotAlterDebuggerEvidenceHandles`
- AC3.3: Living and public documentation name ADR-0350 and describe raw readable,
  control-stripped, 12-display-column handles with the `/session` exact-ID fallback.
  - verify: inspection — `task docs` and `task site:build`

**Cross-boundary oracle and boundary table**

| Boundary/input | Required result | Proof owner |
|---|---|---|
| empty or invalid UTF-8 ID | no displayed handle; invalid target is rejected | handle and debug validation tests |
| control or format runes | remove `Cc` and `Cf`; retain the other readable runes | raw UTF-8/display-column test |
| punctuation, leading hyphen, percent, slash, or non-ASCII text | retain verbatim, subject only to the display-column bound | raw UTF-8 and displayed-UTF-8 resolution tests |
| grapheme or wide-rune boundary | truncate at a grapheme boundary to at most 12 display columns | raw UTF-8/display-column test |
| exact full ID in inventory | exact equality wins before displayed-handle matching | resolver table test |
| one, multiple, or zero identical displayed handles | resolve one; direct multiple matches to `/session`; pass zero unchanged to server | resolver table test |
| inventory lookup failure | pass `TARGET` unchanged to server exact lookup | resolver table test |
| header literal and final full ID | both bind the same exact debug target | composition integration test |
| debugger evidence, scope/history, and incarnation handles | retain their dedicated contracts | ADR-0254/0256/0258 and evidence-boundary test |

## Out of scope

| Item | Decision |
|---|---|
| Server/proto alternate session-ID or short-handle API | No alternate identity for this capability. |
| Session ID generation, storage naming, and ownership authorization | Remain governed by [ADR-0104](../adr/0104-session-family-physical-naming.md) and [ADR-0217](../adr/0217-session-discovery-continuation.md). |
| `/session` full-ID copy semantics | Remain governed by [ADR-0217](../adr/0217-session-discovery-continuation.md). |
| Debugger evidence, scope/history, or cryptographic handles | Remain governed by [ADR-0254](../adr/0254-session-debugger-admin-transport.md), [ADR-0256](../adr/0256-session-debugger-evidence-and-reporting.md), and [ADR-0258](../adr/0258-cryptographic-session-incarnations.md). |
| Collision-free or inventory-dependent presentation | Not part of the fixed raw readable projection. |

## Sequencing record

The landed work established the shared projection, then client-side resolution, then
presentation and documentation. The traceability correction retains that implementation and
records its approved variance in ADR-0350; it does not add a second resolver, collision
expansion, server-side handle parser, or change to debugger evidence handles.

## Definition of done

1. `task lint` and `task test` pass.
2. `task docs` and `task site:build` pass after documentation changes.
3. `task ac-trace-strict` resolves every named proof for this landed plan.
4. The composition-level header-to-debug test proves that both a displayed handle and a
   full ID bind the exact target at the create request.
5. `go run ./cmd/mecademo` prints a full offline session.
