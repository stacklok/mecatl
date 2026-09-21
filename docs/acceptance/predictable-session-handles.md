# Predictable mecatui session handles — acceptance plan

**Phase:** capability — mecatui session discovery and debugger UX
**Status:** landed, 2026-09-02.
**Issue:** [stacklok/mecatl#922](https://github.com/stacklok/mecatl/issues/922).
**ADR:** [ADR-0285](../adr/0285-predictable-mecatui-session-handles.md) — one fixed client-side actionable short-handle contract, superseding ADR-0217's display-only digest decision.
**Related debugger boundaries:** [ADR-0254](../adr/0254-session-debugger-admin-transport.md), [ADR-0256](../adr/0256-session-debugger-evidence-and-reporting.md), and [ADR-0258](../adr/0258-cryptographic-session-incarnations.md).
**Accumulator branch:** `acc/predictable-session-handles` (off `main`).

The landed client gives ordinary session presentation and debug selection one terminal-safe,
fixed escaped prefix while preserving opaque exact IDs at every server boundary. It does not add an
alternate server identifier, hash namespace, or server-side handle parser.

## Scope cuts

- **Ordinary presentation only.** The normal header, `/sessions` rows, debugger target
  chrome, and status input use the same handle. A custom terminal-title template may include it;
  the default title does not require it. `InspectSession` scope/history handles,
  evidence and manifest digests, and target-and-incarnation cryptographic handles remain separate
  contracts under ADRs 0254, 0256, and 0258.
- **Client-only resolution.** The server receives and authorizes exact opaque IDs. The client
  consults the complete caller-visible inventory through
  [`cmd/mecatui/client/sessions_list.go`](../../cmd/mecatui/client/sessions_list.go) only for a
  syntactically valid short handle.
- **Exact IDs remain authoritative.** `/session` retains its full-ID copy path. Exact inventory
  equality wins. Ambiguity directs the operator to copy the full ID; an inventory error or no
  projected-handle match passes `TARGET` unchanged to server authority. Long and non-handle inputs
  bypass local resolution.

## Landed behavior

### Scenario 1 — One escaped fixed handle renders without inventory

For a non-empty valid-UTF-8 ID, the client percent-encodes every UTF-8 byte outside
`[A-Za-z0-9._-]` as uppercase `%HH`, and encodes a leading hyphen as `%2D`. It takes only complete
literal or `%HH` atoms fitting 12 ASCII columns. Empty and invalid-UTF-8 IDs have no handle; the
projection is never decoded.

**Acceptance:**

- AC1.1: Ordinary header, `/sessions`, debugger chrome, and status input use the same fixed
  12-column handle.
  - verify: `TestPredictableSessionHandles_Scenario1_SharedNormalHandle`
- AC1.2: The handle escapes non-unreserved UTF-8 bytes, preserves non-leading hyphens, never
  begins with `-`, and includes only complete atoms fitting the ASCII bound.
  - verify: `TestPredictableSessionHandles_Scenario1_EscapedUTF8ControlsAndLeadingHyphen`
  - verify: `TestPredictableSessionHandles_Scenario1_OnlyHandleWidthAPI`
- AC1.3: Fixed handles do not expand for collisions or depend on inventory order.
  - verify: `TestPredictableSessionHandles_Scenario1_FixedCollisionBehavior`
- AC1.4: Invalid UTF-8 has no handle and cannot create a debug session.
  - verify: `TestPredictableSessionHandles_Scenario1_InvalidUTF8HasNoHandleOrDebugCreate`

### Scenario 2 — Debug resolution is syntax-gated and sends only exact IDs

`CreateDebugSession` queries the complete caller-visible inventory only for a non-empty ASCII
candidate of at most 12 columns whose first atom is `[A-Za-z0-9._]` or complete uppercase
`%[0-9A-F]{2}` and whose later atoms may also be `-`. Exact equality takes priority. Otherwise,
one matching projection resolves to its full exact ID; multiple matches stop before create with
`/session` full-ID copy guidance. Inventory errors and zero matches pass `TARGET` unchanged to the
server's existing exact-ID path. Long, malformed, lowercase-escape, leading-hyphen, non-ASCII, and
other non-handle inputs bypass inventory.

**Acceptance:**

- AC2.1: Only a canonical short handle candidate invokes local resolution; embedded and connected
  commands share one positional `TARGET` grammar.
  - verify: `TestPredictableSessionHandles_Scenario2_HandleGrammarAndUnifiedTarget`
- AC2.2: Exact equality wins; one projected-handle match resolves; duplicate inventory rows do not
  create ambiguity; and ambiguous, zero-match, and inventory-error outcomes follow the documented
  paths.
  - verify: `TestCreateDebugSessionResolution`
- AC2.3: A handle from the rendered header and a full final ID both reach the create request as the
  same exact target.
  - verify: `TestPredictableSessionHandles_Scenario2_RenderedHeaderCreatesBoundDebugger`

### Scenario 3 — Documentation and debugger evidence retain their boundaries

Help and documentation use the one `TARGET` flow and preserve `/session` as the exact-ID fallback.
Ordinary handles do not replace debugger evidence or exact server IDs.

**Acceptance:**

- AC3.1: Command help presents the one-`TARGET` flow and full-ID fallback.
  - verify: `TestPredictableSessionHandles_Scenario3_CommandHelpUsesOneTargetFlow`
- AC3.2: Ordinary-handle presentation does not alter debugger evidence handles or exact
  authoritative IDs.
  - verify: `TestADR_0285_OrdinaryHandleDoesNotAlterDebuggerEvidenceHandles`
- AC3.3: Living and public documentation name ADR-0285, preserve `/session` as the full exact-ID
  fallback, and describe the same debug-target behavior.
  - verify: inspection — `task docs` and `task site:build`

**Cross-boundary oracle and boundary table**

| Boundary/input | Required result | Proof owner |
|---|---|---|
| empty or invalid UTF-8 ID | no displayed handle; invalid target is rejected | handle and debug validation tests |
| `%HH` at the 12-column boundary | complete atom emitted/accepted only when it fits; never sliced | projection and grammar tests |
| leading `-` in exact ID | projected as `%2D`; later hyphens remain literal | projection and grammar tests |
| multibyte valid UTF-8 | byte-wise uppercase `%HH` atoms | projection test |
| exact full ID in inventory | exact equality wins before projected-handle matching | resolver table test |
| one, multiple, or zero projected handles | resolve one; direct multiple matches to `/session`; pass zero unchanged to server | resolver table test |
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
| Collision-free or inventory-dependent presentation | Not part of the fixed escaped projection. |

## Definition of done

1. `task lint` and `task test` pass.
2. `task docs` and `task site:build` pass after documentation changes.
3. `task ac-trace-strict` resolves every named proof for this landed plan.
4. The composition-level header-to-debug test proves that both a displayed handle and a full ID bind the exact target at the create request.
5. `go run ./cmd/mecademo` prints a full offline session.
