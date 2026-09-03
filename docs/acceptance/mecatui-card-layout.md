# Mecatui card layout — acceptance plan

**Phase:** mecatui rendering correctness
**Status:** in-progress, 2026-09-03. Derived from the card-width audit and the observed padding-before-wrap regression.
**Accumulator branch:** `acc/mecatui-card-layout` (off `main`).

The smallest set of work that makes every dynamically sized mecatui card render
within its usable viewport width without turning renderer-generated padding into
additional rows. The plan keeps each surface's existing interaction and content
semantics; it does not introduce a universal card-rendering abstraction.

The document is organized scenario-first because acceptance is about what the
running client renders, not which helpers exist on disk.

## Why these scope cuts

- [`docs/tui.md`](../tui.md) defines mecatui as a pure event-rendering gRPC
  client using themed Lipgloss cards for tool I/O and user prompts. This plan is
  limited to that client rendering boundary; it changes neither event payloads
  nor engine behaviour.
- [`AGENTS.md` — user-facing behaviour documentation](../../AGENTS.md) requires
  user-doc updates for a changed user-facing surface, while its minimal-change
  rule rejects a premature global abstraction.
- **Card-row invariant.** Every changed plain-text card region derives its
  final usable body width (including frame and prefix reservations), sanitizes
  untrusted terminal controls, removes display-only trailing padding, wraps raw
  rows, styles those completed rows, then applies the final frame. Tests use a
  long-row/short-row/whitespace-row fixture for each independently styled
  region; this is a rendering contract, not a new package-wide framework.
- Markdown and the input rail retain their separate rendering contracts: the
  former must preserve Markdown hard-break semantics and the latter deliberately
  paints fixed-width background rows.

## In scope — 4 scenarios, in implementation order

### Scenario 1 — bounded main-conversation tool cards

A user sees a Bash, Grep, Subagent, or file-edit tool card in the main
conversation. Tool output is server-derived and cards are themed Lipgloss
containers, as described by [`docs/tui.md`](../tui.md). The tool-card renderer
must derive the final body width before layout and never hard-wrap a multiline
value after Lipgloss has aligned it. This preserves the client-layer and
minimal-change rules in [`AGENTS.md`](../../AGENTS.md).

**Acceptance:**

- AC1.1: A collapsed tool result with one long row and shorter or whitespace-only
  subsequent source rows does not gain blank display rows from renderer padding,
  and its visible content fits the card body.
  - verify: `TestMecatuiCardLayout_Scenario1_ResultRowsWrapBeforeStyle`
- AC1.2: Collapsed Bash, Grep, Subagent, large-JSON, and typed-artifact results
  use their shared display-row budget without hiding meaningful rows behind
  padding-derived rows; Ctrl+t-expanded results retain the complete source
  content and intentional blank paragraphs.
  - verify: `TestMecatuiCardLayout_Scenario1_CollapsedResultRows`
- AC1.3: Tool headers, arguments, Subagent/Team/Parallel metadata, and Edit/Write
  diffs fit their derived card body width without an assembled styled-card
  hard-wrap; diff source whitespace and prefixes remain meaningful.
  - verify: `TestMecatuiCardLayout_Scenario1_ToolCardRegionsFitBodyWidth`
- AC1.4: Every rendered tool-card row, including expanded headers, arguments,
  diffs, result/artifact rows, and unbreakable tokens, fits its outer width at
  tiny, narrow, normal, and capped viewport geometries while retaining complete
  expanded source content.
  - verify: `TestMecatuiCardLayout_Scenario1_ExpandedToolCardWidthInvariant`
- AC1.5: The main tool-card renderer contains no width-affecting wrap over an
  assembled multiline styled card body; each independently styled region uses
  the card-row invariant before final framing.
  - verify: `TestMecatuiCardLayout_Scenario1_NoStyledBodyWrap`

---

### Scenario 2 — bounded delegation and approval cards

A user opens the Subagent, Parallel, Team, or permission-approval surfaces.
These are client-only presentations of the event stream, so their width handling
must remain within the client layers described by [`docs/tui.md`](../tui.md) and
must preserve their existing selection, scroll, and approval interactions,
consistent with the targeted rendering discipline in [`AGENTS.md`](../../AGENTS.md).

**Acceptance:**

- AC2.1: Delegation trace, roster, task, finding, and focus rows reserve their
  prefixes, trim display-only right padding, wrap raw text before styling, and
  fit the container's supplied body width.
  - verify: `TestMecatuiCardLayout_Scenario2_DelegationRowsFitBodyWidth`
- AC2.2: Approval reasons and non-diff arguments wrap before their fixed-width
  style is rendered, without whitespace-only continuation rows or changed
  approval hit-test/scroll behaviour.
  - verify: `TestMecatuiCardLayout_Scenario2_ApprovalRowsWrapBeforeStyle`
- AC2.3: Edit and Write approval diffs preserve semantically meaningful source
  whitespace while remaining within their viewport budget.
  - verify: `TestMecatuiCardLayout_Scenario2_ApprovalDiffsPreserveSourceWhitespace`

---

### Scenario 3 — bounded dynamic inventory and detail cards

A user opens any dynamic mecatui inventory or detail card: Models, Sessions,
Worktrees, MCP (including resource/prompt subviews), Skills and learned details,
Agents, Connect, Session Details, Dream, Reflections, Soul, User Model, or
Diagnostics. The UI remains a pure client and therefore sanitizes and lays out
those values before final card framing, consistent with
[`AGENTS.md`](../../AGENTS.md) and the mecatui client boundary. This list is the
exhaustive Scenario 3 surface inventory; a surface added later must either meet
the card-row invariant or be explicitly documented as an exception.

**Acceptance:**

- AC3.1: MCP (including resource/prompt subviews), Models, Sessions, Worktrees,
  Diagnostics, and Agents/Team/Parallel inventory surfaces derive one effective
  body width at their container boundary and every dynamic row fits it without
  padding-derived blank rows.
  - verify: `TestMecatuiCardLayout_Scenario3_InventoryAndMCPFitWidth`
- AC3.2: Skills and learned details, agent definitions, Connect, Session Details,
  Dream, Reflections, Soul, and User Model dynamic details wrap sanitized raw
  rows before style and frame application.
  - verify: `TestMecatuiCardLayout_Scenario3_DynamicDetailsFitWidth`
- AC3.3: Focused and scrollable views preserve their existing selection,
  truncation, and height-window behaviour while their individual visible rows
  stay inside the effective width.
  - verify: `TestMecatuiCardLayout_Scenario3_FocusAndScrollableViewsRemainUsable`

---

### Scenario 4 — rendering-contract regression coverage and documentation

The client has an executable layout contract across card families. The contract
is local to card renderers rather than a package-wide style framework, following
[`AGENTS.md` — minimum code and existing-convention discipline](../../AGENTS.md).

**Acceptance:**

- AC4.1: Each changed card family has an isolated renderer test that includes a
  long row followed by short and whitespace-only rows, proving that only source
  content—not style alignment padding—can determine visual rows.
  - verify: `TestMecatuiCardLayout_Scenario4_NoPaddingBeforeWrapRegression`
- AC4.2: Plain dynamic text is terminal-sanitized, has display-only trailing
  whitespace normalized before wrapping, and is styled only after wrapping;
  Markdown and the fixed-background input rail retain their documented separate
  handling.
  - verify: `TestMecatuiCardLayout_Scenario4_RenderingExceptionsRemainIntact`
- AC4.3: Representative tool, approval, inventory, and detail fields containing
  terminal controls or bidi-format bytes render no unsafe control sequence while
  permitted layout newlines/tabs and width bounds remain intact; Markdown stays
  on its separate Glamour sanitization path.
  - verify: `TestMecatuiCardLayout_Scenario4_DynamicCardTextIsTerminalSafe`
- AC4.4: Public mecatui documentation explains that collapsed tool cards are
  width-bounded previews and Ctrl+t exposes complete tool output.
  - verify: inspection — documentation is user-facing prose; the key behaviour is covered by Scenario 1 renderer tests

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Engine event, tool-result, provider, or protocol changes | future issue only if a payload contract changes | This is a mecatui-only layout correction. |
| A package-wide generic card/layout framework | future repetition with a demonstrated common semantic contract | Existing card families have distinct Markdown, diff, selection, and scroll contracts. |
| Altering Markdown source whitespace or input-rail background padding | future rendering-specific work | Those are intentional semantics, not display-only tool/card padding. |
| New configuration, keybindings, or API surface | not planned | Existing Ctrl+t expansion behaviour remains authoritative. |

## Sequencing recommendation

First establish the card-layout test vocabulary and finish the main tool-card
pipeline, including removal of the final assembled styled-card wrapper. Then
carry the body-width contract through the shared delegation and approval
renderers. Inventory and detail overlays can proceed in parallel once their
container-width seams are identified. Finish with cross-family regression
coverage and concise user documentation.

## Named tests landing in this plan

- `TestMecatuiCardLayout_Scenario1_ExpandedToolCardWidthInvariant`
- `TestMecatuiCardLayout_Scenario1_NoStyledBodyWrap`
- `TestMecatuiCardLayout_Scenario2_DelegationRowsFitBodyWidth`
- `TestMecatuiCardLayout_Scenario2_ApprovalRowsWrapBeforeStyle`
- `TestMecatuiCardLayout_Scenario3_InventoryAndMCPFitWidth`
- `TestMecatuiCardLayout_Scenario3_DynamicDetailsFitWidth`
- `TestMecatuiCardLayout_Scenario4_NoPaddingBeforeWrapRegression`
- `TestMecatuiCardLayout_Scenario4_DynamicCardTextIsTerminalSafe`

## Definition of done

1. `task lint` and `task test` pass.
2. `task docs` regenerates `llms.txt` and passes the strict link gate.
3. `task ac-trace-strict` passes after this plan is `landed`.
4. The named scenario tests are green and grep-locatable.
5. `task site:build` passes after the mecatui user documentation update.
6. `go run ./cmd/mecademo` prints a full offline session.

## Deferred decisions and known risks

- **Card-local helpers remain preferred.** Extract a shared helper only after at
  least three surfaces need the same raw-row semantics; different handling for
  Markdown, diffs, and interactive scroll regions must remain explicit.
- **Unicode width.** Plain-text row preparation must retain the existing
  wide-character/emoji-width handling before it is generalized to additional
  surfaces.
- **Existing exploratory tool-card changes.** Treat them only as a hypothesis:
  Scenario 1's named regressions are the authority for retaining, revising, or
  discarding any pre-accumulator implementation.

## Exit criteria

When every point under *Definition of done* holds on the accumulator, this plan
is satisfied.
