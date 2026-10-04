# Mecatui canonical status surfaces — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — removing duplicate presentation paths also changes the no-source UI and composes debug safety chrome with generated header content; these observable choices need an acceptance contract but do not change the status-source boundary.
**Decision record:** None — preserve the renderer/source ownership recorded in historical [ADR 0247](../adr/0247-mecatui-status-line.md) and the current [ADR 0289 command boundary](../adr/0289-hardened-status-command-boundary.md); neither changes, so no new durable architecture decision is needed.
**Phase:** mecatui presentation cleanup
**Status:** proposed, 2026-10-03. Operator chose a minimal no-source identity, source-backed ordinary status, and mandatory debug warnings.
**Delivery:** Split. The no-source and debug behavior plus removal of old tests deserve a separate behavioral review before implementation.
**Expected tasks:** deferred to orchestration.

The normal header identity and right-side footer status come only from the selected status source: shipped responsive templates unless the operator configures templates or a command. Remove the legacy nil-source header and `fitFooter` paths rather than maintaining two formatters. Keep mandatory renderer chrome independent of customization. This refines the [existing status-line plan](mecatui-status-line.md) and follows the UI's [layout and safety boundary](../tui.md).

## Human decisions

- [x] A UI constructed without a status source retains a minimal session identity, not the legacy status generator — Decision: display `session` followed by the safe short handle when a session ID exists, without a generated right-side footer; leave the identity blank while the ID is unknown. Keep left activity, keyboard help, and mandatory header indicators.
- [x] Debugging remains visibly identifiable even with a configured or missing source — Decision: prepend a non-customizable `⚠ DEBUG target` followed by the safe target handle to the generated header when one fits, shed generated identity before the warning, and retain the separate privacy disclosure, including in fatal and narrow views.
- [x] Legacy formatting parity is not a goal — Decision: keep the existing stock-template responsive tiers; retain only meaningful stock-source tests, fixing misleading unknown and estimated context presentation rather than copying each old width tier or golden.

## Interface contract

- **gRPC / protobuf:** None — only local mecatui presentation changes; session RPCs and fields remain unchanged.
- **Exported Go APIs / interfaces:** None — `ui.New`, `ui.Deps.StatusSource`, and `customization.Source` signatures remain unchanged; an omitted source is still accepted.
- **Tool schemas:** None — status templates and debugger tools retain their existing schemas.
- **CLI / config:** None — debug invocation and user-global `status_customization` settings/precedence do not change.
- **Events / persistence:** None — no stored session or event format changes.
- **Security / authority:** Mandatory debug identity and privacy disclosure remain renderer-owned and cannot be hidden by a custom header, missing result, source error, or fatal view. Debugger authority, target binding, and status input allowlist do not change.
- **Compatibility / migration:** No-source `ui.New` callers lose the old model/mode/usage/context generators and see only a safe session handle when available, plus mandatory chrome. Debug sessions gain generated header content alongside their fixed warning rather than always bypassing templates. Ordinary source-backed status preserves the existing source-selected fitting and empty/oversize behavior; no configuration migration.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — The selected source owns ordinary status

For an ordinary mecatui session, the [status-source contract](../adr/0247-mecatui-status-line.md) supplies the header identity and right-aligned status. The UI retains only its layout, theme, navigation/safety chrome, footer activity, and help. A missing or oversized source surface never triggers legacy content (`cmd/mecatui/ui/view.go`, `cmd/mecatui/customization/defaults.go`).

**Acceptance:**
- AC1.1: With a stock source, wide and narrow terminal frames render fitting stock header/footer variants beside the renderer-owned chrome, with no additional renderer-built identity or usage segment.
  - verify: `TestCanonicalStatus_Scenario1_StockSourceOwnsSurfaces`
- AC1.2: With an empty or oversized custom source result, the replaceable header/right-footer content is absent while mandatory chrome remains; no legacy `fitFooter` or identity content reappears.
  - verify: `TestCanonicalStatus_Scenario1_EmptySourceDoesNotFallBack`
- AC1.3: With no source, a known session displays only `session` and its safe short handle as header identity, while an unknown session displays no identity; neither case renders the old right-side footer. The handle derived from a hostile ID containing terminal or layout controls stays display-safe; left activity/help and mandatory indicators remain usable.
  - verify: `TestCanonicalStatus_Scenario1_NoSourceMinimalIdentity`

### Scenario 2 — Debug identity cannot be customized away

A debug session is separately bound to a stored target and must continue disclosing that bounded target evidence may reach its model. The fixed warning is placed ahead of any generated header surface; the UI submits a `HeaderAvailCols` budget after reserving that warning and the other mandatory header indicators, so template and command sources select against the actual available width. It remains visible without a source or generated result and on the fatal path; very narrow widths may wrap mandatory text rather than truncate it (`cmd/mecatui/ui/view.go`, [ADR 0247's mandatory chrome boundary](../adr/0247-mecatui-status-line.md), [ADR 0289's command boundary](../adr/0289-hardened-status-command-boundary.md), [debug session guidance](../../user-docs/mecatui/sessions.md)).

**Acceptance:**
- AC2.1: Stock and configured header surfaces render after the fixed `⚠ DEBUG target <safe handle>` cue when they fit; narrower frames drop generated content before the cue, and hostile target bytes cannot inject terminal controls.
  - verify: `TestCanonicalStatus_Scenario2_DebugWarningPrecedesGeneratedHeader`
- AC2.2: A missing, empty, stale, or oversized generated header cannot hide the target warning or privacy disclosure; fatal and narrow frames with sufficient rows display the full disclosure and fatal exit control inside the measured viewport, not below it.
  - verify: `TestCanonicalStatus_Scenario2_DebugDisclosureSurvivesMissingSourceAndFatal`
- AC2.3: Debug warning and posture/navigation reservations are included in submitted `HeaderAvailCols` for stock and command sources; resizing or changing a reserved indicator resubmits the changed budget, so the source can select a fitting variant without displacing mandatory chrome.
  - verify: `TestCanonicalStatus_Scenario2_DebugReservationReachesSource`

### Scenario 3 — Stock context readings remain truthful

Stock meter helpers must represent unknown and estimated occupancy accurately at every responsive width, without duplicating the old candidate ladder. Context figures come from the existing display-safe snapshot and semantic StatusML renderer (`cmd/mecatui/customization/template.go`, `cmd/mecatui/ui/statusline_source.go`), following [ADR 0247's template/renderer split](../adr/0247-mecatui-status-line.md).

**Acceptance:**
- AC3.1: With an unknown used count and known window, full, compact, and minimal stock variants show unknown occupancy, not a fabricated 0% bar or pressure severity; full may show the known denominator.
  - verify: `TestCanonicalStatus_Scenario3_UnknownContextDoesNotClaimZeroPressure`
- AC3.2: With estimated used count and known window, each stock variant visibly marks the percentage as estimated (including compact/minimal), and a known non-estimated count keeps the ordinary percentage and pressure cue.
  - verify: `TestCanonicalStatus_Scenario3_EstimatedContextMarkedAtEveryWidth`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Recreating the legacy eight-tier width ladder, Agents-key advertisement, and byte-for-byte formatting | No planned migration | Current stock templates are canonical; a separate product request can change them. |
| Templating footer activity, help, posture/navigation indicators, or the mandatory debug warning/disclosure | Separate design | These are renderer-owned chrome under ADR 0247. |
| Changing debugger evidence access, prompts, authority, and terminal title | Separate work | They are independent of status presentation. |
| Adding a source inside `ui.New` or changing caller-owned source lifecycle | Separate design | Nil is an accepted minimal-view case, not implicit source construction. |

## Definition of done

1. Remove the obsolete nil-source header identity generator and right-side `fitFooter` machinery, including exclusively legacy tests/benchmarks; retain shared formatters and mandatory debug/layout helpers still in use. Do not retain a hidden secondary generator in tests.
2. Focused offline UI/customization tests prove each criterion; delete obsolete tests rather than recreating all legacy goldens. Update the owning public status-line guide only for verified user-visible differences when implementation lands.
3. On the integrated candidate run `task test`, `task lint`, `task test:race`, `task docs`, `task site:build` for the public guide change, `task ac-trace-strict`, the offline demo, and `/panel-review`; report the results in the implementation PR.

## Deferred decisions and known risks

- Generated surfaces can arrive after the first UI frame. Keep source-backed no-fallback behavior during that interval; mandatory debug chrome must never wait for a source result.
- Shared token humanization, context data assembly, and team counts serve nonlegacy code. Remove by call graph, not by deleting `footer.go` wholesale.
