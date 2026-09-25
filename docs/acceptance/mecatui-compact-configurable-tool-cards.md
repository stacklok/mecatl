# Mecatui compact configurable tool cards — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — changes the client-local tool-card presentation and adds one strict user-global mecatui setting, without changing wire, engine, persistence, authority, or public Go contracts.
**Decision record:** None — compact-card presentation, its local configuration key, and compatibility treatment are bounded mecatui product decisions rather than durable system architecture.
**Phase:** mecatui conversation readability
**Status:** in-progress, 2026-09-25. Implementation authorized by the directing human with an explicit waiver of the Plan / Interface PR merge gate.
**Delivery:** Split. The new client configuration surface and intentional default presentation change require review before implementation.
**Expected tasks:** deferred to orchestration after Plan / Interface review.
**Issue:** None — initiated directly by the user.
**Plan PR:** [#1913](https://github.com/stacklok/mecatl/pull/1913)
**Approved baseline:** `69bbb5ef8962998bac3538a56a3cc4049bb503c5` under the directing human's explicit 2026-09-25 waiver of the Plan / Interface PR merge gate.

Mecatui will make collapsed tool calls in the active conversation scannable by moving a bounded argument summary into the status/name header and limiting resolved output previews to three display rows by default. The complete arguments and output remain available there through the existing `ExpandTools` action (`ctrl+t` by default).

Operators may replace the three-row default through the client-owned settings file. This remains a local presentation choice in embedded and connected clients; it does not alter model-facing tool calls, server events, recorded results, or approval content.

## Human decisions

- [x] Collapsed tool-result previews use three display rows by default. — Decision: the user selected three rows.
- [x] The preview-row limit is user configurable. — Decision: add the positive-integer `tool_cards.collapsed_result_rows` key to the strict user-global mecatui settings file, with default `3`.
- [x] Tool arguments move into the collapsed header. — Decision: collapsed cards show a single bounded header with the status, display tool name, and prioritized compact argument segments; `Skill` prioritizes `name` then non-empty `asset`, while complete arguments remain in the expanded view.

## Interface contract

- **gRPC / protobuf:** None — existing tool-call and tool-result events remain byte-for-byte wire compatible; the client alone changes their projection.
- **Exported Go APIs / interfaces:** None — no exported Go API changes. Composition may add package-private settings and renderer inputs, but exact internal symbol names are non-contractual.
- **Tool schemas:** None — tool names, input schemas, execution, and model-facing results remain unchanged. `Skill` still accepts `name` and optional `asset`; only its client presentation changes.
- **CLI / config:** `$XDG_CONFIG_HOME/mecatui/settings.yaml` gains an optional strict client-owned section `tool_cards` with one key, `collapsed_result_rows`. The value is a positive integer, defaults to `3` when the section or key is absent, and values less than one fail startup with a field-specific configuration error. There is no CLI or environment override. One launch-time snapshot applies in embedded and connected modes and requires restart to reload.
- **Events / persistence:** None — event payloads, session snapshots, transcript storage, and replay data do not change. The setting applies only to the active conversation renderer; the `/sessions` stored-transcript inspector retains its current independent preview behavior because it has no `ExpandTools` interaction.
- **Security / authority:** None — the card continues to render only the already-delivered sanitized tool name, arguments, and result. Collapsing or expanding changes presentation only and grants no tool, file, execution, or disclosure authority; approval surfaces remain unchanged.
- **Compatibility / migration:** The YAML schema change is additive. Existing client files continue to parse and select the new three-row default. The active conversation's collapsed presentation intentionally changes from separate argument rows plus up to twelve result rows to one bounded summary header plus up to three result rows. `ExpandTools` restores the current complete wrapped tool-name header, complete arguments, and complete output; it does not retain a truncated collapsed header as the only tool-name presentation. Existing `/sessions` stored-transcript previews, `Edit`/`Write` diffs, and Subagent/Team specialized bodies keep their current semantics and independent limits.

## In scope — 2 scenarios, in implementation order

### Scenario 1 — one-line collapsed header and three-row output preview

A user following tool activity in the main conversation sees a single bounded first row containing the status glyph, the existing plain or MCP-friendly tool name, and as many complete prioritized compact argument segments as fit. The row is sanitized and never wraps. The status glyph has highest priority; the display tool name is next and is truncated at display-cell boundaries with an omission marker only when it cannot fit in full. Argument segments appear only after the complete display name fits. When arguments are omitted, the renderer adds an omission marker only if space remains after preserving the status and complete display name; an exact-fit status/name header has no marker. For `Skill`, the order is the skill `name`, then `asset` when non-empty, so an ordinary activation reads like `✓ Skill · panel-review · asset: checklist`. Empty assets are not rendered. Other ordinary tools use the existing deterministic argument-priority order, with long strings, arrays, and objects represented by the existing compact-value forms rather than copied in full.

Collapsed ordinary-tool cards do not repeat their summarized JSON arguments below the header. Expanded cards restore the current complete wrapped tool-name header, including the complete raw MCP name where applicable, and show the complete pretty-printed arguments plus complete result. `Edit`/`Write` diffs and Subagent/Team specialized bodies remain below their header because they are distinct operator-facing projections, not generic argument dumps. This preserves the raw-content-before-decoration and complete-expanded-content invariants in the landed [mecatui card-layout plan](mecatui-card-layout.md), the logical anchor and selection rules in [ADR 0301](../adr/0301-logical-conversation-anchors.md), the client-only tool-I/O boundary in [`docs/tui.md`](../tui.md), and the repository's minimal-change and user-facing documentation requirements in [AGENTS.md](../../AGENTS.md).

**Acceptance:**
- AC1.1: A collapsed ordinary tool card renders one non-wrapping header ordered as status glyph, display tool name, then a deterministic prefix of complete compact argument segments. If the complete tool name cannot fit, it is display-cell truncated with an omission marker and no argument segment is shown. Otherwise, omitted arguments receive a best-effort omission marker only when space remains after the complete status and display name; when those exactly fill the row, the marker is omitted. No generic argument rows are repeated below the header.
  - verify: `TestMecatuiCompactToolCards_Scenario1_CollapsedHeaderSummarizesArguments`
- AC1.2: A collapsed `Skill` card displays `name` and then non-empty `asset` in its header when they fit, omits an empty asset, and keeps the full argument object available in the expanded view.
  - verify: `TestMecatuiCompactToolCards_Scenario1_SkillHeaderNameAndAsset`
- AC1.3: A resolved collapsed tool result shows at most three wrapped display rows by default plus an omission/expansion marker when content remains; typed artifact rows share the same result-row budget, while expanded mode preserves the complete result and intentional blank paragraphs.
  - verify: `TestMecatuiCompactToolCards_Scenario1_DefaultThreeResultRows`
- AC1.4: The collapsed header preserves the status glyph when at least one display cell is available, cell-truncates an overlong tool name before considering arguments, omits the argument marker when the status and complete tool name exactly fill the row, and fits every exact-fit and tiny-to-capped viewport without wrapping; expanded cards preserve the complete ordinary and MCP raw tool name, complete arguments, error styling, terminal sanitization, and full result. `Edit`/`Write` diffs and Subagent/Team specialized bodies preserve their existing content semantics and independent caps.
  - verify: `TestMecatuiCompactToolCards_Scenario1_HeaderWidthSafetyAndSpecializedCards`
- AC1.5: Prepared collapsed and expanded cards retain one lockstep provenance record per rendered row; when argument rows become header chrome or reappear on expansion, ADR 0301's same-card anchor fallback applies, and a selection survives only when its exact visible text and endpoint context remain provably identical.
  - verify: `TestADR_0301_CompactToolHeaderProvenanceAndSelection`

### Scenario 2 — strict user-global collapsed-row configuration

A terminal user configures the preview size in the client-owned settings document described by [Customize mecatui](https://github.com/stacklok/mecatl/blob/main/user-docs/mecatui/customization.md):

```yaml
tool_cards:
  collapsed_result_rows: 5
```

The setting is parsed once at launch, passed through the host composition boundary, and used by the active-conversation renderer, including every reconstruction caused by theme auto-detection or a later theme switch. It affects only resolved result preview rows: the `/sessions` stored-transcript inspector, argument-header packing, Edit/Write diff caps, delegation metadata, approval views, and expanded output do not consume or reinterpret it. Tests use isolated XDG state under the repository's offline and test-owned-state requirements in [AGENTS.md](../../AGENTS.md).

**Acceptance:**
- AC2.1: Missing client settings, a missing `tool_cards` section, or a missing `collapsed_result_rows` key selects `3`; a configured positive integer is retained exactly, while zero, negative, wrong-typed, or unknown nested keys fail strict startup parsing with safe field-specific guidance.
  - verify: `TestMecatuiCompactToolCards_Scenario2_StrictClientSetting`
- AC2.2: A configured value controls the active conversation's wrapped display-row budget for text, error, summarized JSON, and typed-artifact results and survives active-renderer reconstruction during theme auto-detection or switching; it does not change expanded output, stored-transcript inspection, or the independent diff/delegation/approval limits.
  - verify: `TestMecatuiCompactToolCards_Scenario2_ConfiguredRowsSurviveRendererRebuild`
- AC2.3: The owning public customization guide documents the settings location, default, positive-integer validation, restart boundary, and a copyable example; the TUI workflow links to that owner while continuing to explain `ExpandTools` access to complete details.
  - verify: `task site:build`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Per-tool or per-card preview-row settings | Later evidence-driven design | One client-wide positive row budget keeps the first configuration surface small. |
| Per-card expansion state | Separate interaction change | `ExpandTools` remains the existing global complete-details toggle in the active conversation. |
| Applying the new limit to `/sessions` stored-transcript inspection | Separate transcript interaction change | The inspector has no `ExpandTools` route today, so it keeps its current independent preview rather than hiding more content behind an unusable affordance. |
| Configuring argument priorities or header templates | Later evidence-driven design | The renderer owns one deterministic safe summary policy. |
| Changing Edit/Write diff, delegation trace, approval, or MCP-resource preview limits | Separate surface-specific work | `collapsed_result_rows` applies only to resolved conversation tool results. |
| Server-, project-, or session-provided card settings | Not planned | Local presentation remains user-global and client-owned. |
| Suppressing tool results entirely | Not planned | Positive integers preserve at least one visible result row. |

## Definition of done

1. Focused `cmd/mecatui` and `cmd/mecatui/ui` tests pass with isolated client settings.
2. `task lint`, `task test:race`, `task docs`, `task site:build`, and `task api:check` pass on the implementation candidate.
3. `task ac-trace-strict` resolves every named proof when this plan becomes `landed`.
4. `go run ./cmd/mecademo` remains green; no engine or server behavior changes.
5. The implementation PR links the Plan / Interface PR and approved commit and reports interface conformance.
6. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- Narrow terminals may fit only the status glyph and a truncated tool name; arguments are considered only after the complete display name fits. The complete tool name and arguments remain reachable through `ExpandTools`, and implementation must not wrap the collapsed first row merely to force another segment into view.
- A large configured value intentionally permits a verbose collapsed transcript. It changes rendering volume only; tool results are already held by the conversation and no new result copy or persistence is introduced.
- Existing tool-card render caches must include the effective preview-row setting in their render context or be rebuilt from immutable launch-time renderer configuration, so a theme-driven renderer replacement cannot revert to the shipped default.
