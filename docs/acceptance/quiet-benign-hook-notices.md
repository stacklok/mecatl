# Quiet benign guardrail notices — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — changes only mecatui's presentation of existing structured contextual-guardrail events and adds client-local display configuration; server authority, engine outcomes, public APIs, wire contracts, and persistence remain unchanged.
**Decision record:** None — ADR 0363 already owns contextual guardrail review semantics; this plan applies a bounded client rendering policy to its existing machine assessment, inspection, and disposition fields.
**Phase:** contextual guardrail TUI presentation
**Status:** landed, 2026-09-25. Implementation candidate complete under the recorded direct-human process waiver; authoritative on merge. Feature, lint, documentation, site, and focused race gates pass; aggregate `task test`/`task test:race` reproduce the unrelated `mecak8s` Helm namespace fixture mismatch already present on `origin/main`.
**Delivery:** Split contract with a direct-human waiver of the merged Plan / Interface PR checkpoint for this named work; implementation remains stacked on the planning commit.
**Expected tasks:** deferred to orchestration
**Plan PR:** absent under the named process waiver
**Approved baseline:** this amended planning commit on `plan/quiet-benign-hook-notices`
**Process waiver:** The directing human instructed: “commit them in one branch in which the base is origin/main. Then checkout to a new implementation branch, based out of the branch with documents and proceed with the implementation”. This explicitly waives only the merged Plan / Interface PR checkpoint for quiet benign guardrail notices; interface conformance, verification, review, and human merge gates remain required.

Mecatui uses the contextual guardrail event's existing machine fields to distinguish routine successful inspections from outcomes that require attention. It captures benign hook and live detail text but hides it by default, while keeping uncertain, failed, advisory, blocked, withheld, denied, and approval-related outcomes visible.

## Human decisions

- [x] Default visibility — Decision: a review with a non-empty review ID, recognized `action` or `inbound` job, completed inspection, acceptable assessment, and disposition `execute` or `release_result` is benign and hidden by default; every other or unknown combination remains visible.
- [x] Explicit reveal — Decision: the existing rebindable `ExpandTools` action (`ctrl+t` by default) temporarily reveals retained benign notices, and client setting `hook_notices.show_benign: true` makes them visible by default.
- [x] Configuration ownership — Decision: visibility is local mecatui presentation configuration with no server, project, or new CLI surface.
- [x] Contract amendment — Decision: after contextual guardrails landed, remove the obsolete exported-engine/protobuf proposal and use the already-shipped structured guardrail metadata instead.

## Interface contract

- **gRPC / protobuf:** None — existing `Hook.guardrail` review metadata and guardrail-detail RPC fields already carry the required classification and text; no enum, message, field, or RPC changes.
- **Exported Go APIs / interfaces:** None — changes remain inside `cmd/mecatui` client and UI implementation; `engine/` and public SDK contracts remain unchanged.
- **Tool schemas:** None — model-facing tool names and schemas do not change.
- **CLI / config:** Add optional strict client YAML `hook_notices.show_benign` in `$XDG_CONFIG_HOME/mecatui/settings.yaml`, default `false`. `true` keeps benign hook and detail notices visible while conversation details are collapsed. The existing rebindable `ExpandTools` action reveals retained benign notices while expanded. No CLI flag, server setting, or project-tier setting.
- **Events / persistence:** No event or persistence change. Mecatui derives benign presentation only when the review ID is non-empty, the job is `action` or `inbound`, `inspection=complete`, `assessment=acceptable`, and disposition is `execute` or `release_result`. Durable guardrail hook events remain captured for live and replay rendering. Live-only detail RPC responses remain conversation blocks for the requesting active session but are not made durable by this work.
- **Security / authority:** Presentation never changes inspection, holding, release, execution, approval, diagnostics, or permission authority. Missing identity/job metadata and every unknown, unresolved, prohibited, operational-failure, advisory, ask, withhold, deny, and warning disposition fail visible. Live details are accepted only for the requesting session and exact review ID; stale-session responses are discarded, mismatched identities fail visible, and approval details remain on their matching approval surface. Classification uses structured fields only, never checker-authored prose.
- **Compatibility / migration:** Existing servers already provide the structured fields. Older clients remain noisy but lose no data. New clients hide only the exact known-benign combination and display older, absent, or unknown metadata. No persisted-data migration.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — Existing machine outcomes determine attention

The classifier consumes the durable contextual-review projection defined by [ADR 0363](../adr/0363-contextual-investigative-guardrails.md), without parsing the human text rendered by `guardrailHookText`.

**Acceptance:**
- AC1.1: A guardrail review is benign only when it has a non-empty review ID, a recognized `action` or `inbound` job, a complete inspection, an acceptable assessment, and disposition `execute` or `release_result`.
  - verify: `TestQuietBenignGuardrailNotices_Scenario1_ExactBenignMatrix`
- AC1.2: Nil metadata and every unknown, unresolved, prohibited, operational-failure, advisory, ask, withhold, deny, and warning combination remain attention-worthy and visible.
  - verify: `TestQuietBenignGuardrailNotices_Scenario1_FailsUnknownVisible`
- AC1.3: Classification is invariant under changes to concern, source, next-action, route, tool, and other human-readable text.
  - verify: `TestQuietBenignGuardrailNotices_Scenario1_DoesNotParseProse`

### Scenario 2 — Mecatui is quiet by default without losing evidence

Mecatui applies presentation policy after event capture and preserves the existing live/replay reduction described in [TUI architecture](../tui.md) and the [contextual guardrail event flow](../architecture/hooks-and-guardrails.md).

**Acceptance:**
- AC2.1: With shipped defaults, benign live hook cards and their correlated live detail notices remain in conversation state but contribute no rendered lines; attention-worthy hooks, details, approvals, and existing error surfaces remain visible. Detail responses from a stale session are discarded, while an in-session response with a mismatched review ID fails visible without displaying the mismatched text.
  - verify: `TestQuietBenignGuardrailNotices_Scenario2_DefaultLiveVisibility`, `TestQuietBenignGuardrailNotices_Scenario2_DetailResponseIdentity`
- AC2.2: A replayed benign guardrail hook remains captured and hidden by default. The existing `ExpandTools` action reveals retained benign live and replay blocks, then hides them again without mutation, duplication, or re-fetching.
  - verify: `TestQuietBenignGuardrailNotices_Scenario2_ExpandLiveAndReplay`
- AC2.3: Multiple consecutive benign hook/detail blocks create no blank-row residue while hidden; when shown, their sanitized text remains ordered, width-bounded, and cache-equivalent.
  - verify: `TestQuietBenignGuardrailNotices_Scenario2_RenderingAndCache`
- AC2.4: A guardrail approval always requests and displays its correlated detail regardless of the benign classifier, so required human attention cannot disappear.
  - verify: `TestQuietBenignGuardrailNotices_Scenario2_ApprovalDetailAlwaysVisible`

### Scenario 3 — Operators can choose persistent verbose display

The local setting follows the ownership rules in `user-docs/mecatui/customization.md` and leaves the [server-side guardrail boundary](../architecture/hooks-and-guardrails.md) unchanged: presentation remains client-owned and behaves identically with embedded and remote servers.

**Acceptance:**
- AC3.1: Missing `hook_notices` or missing `show_benign` resolves to false; `hook_notices.show_benign: true` reaches mecatui's renderer and makes retained benign live and replay notices visible while details are collapsed, including after renderer/theme reconstruction.
  - verify: `TestQuietBenignGuardrailNotices_Scenario3_ClientSetting`, `TestQuietBenignGuardrailNotices_Scenario3_ShowSettingSurvivesThemeReconstruction`
- AC3.2: The strict client parser rejects unknown `hook_notices` members and non-boolean `show_benign` values without disclosing configuration values; server and deprecated settings files cannot enable the behavior.
  - verify: `TestQuietBenignGuardrailNotices_Scenario3_StrictConfigOwnership`, `TestQuietBenignGuardrailNotices_Scenario3_DeprecatedMecatlSettingsIgnored`
- AC3.3: The owning public customization and TUI pages document the default, persistent setting, and rebindable temporary reveal once implementation ships.
  - verify: `task docs` and `task site:build` — generated links and the public site must build

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Changing contextual guardrail review, approval, holding, or release semantics | ADR 0363 follow-up | This plan changes client presentation only. |
| New engine hook outcomes or protobuf decisions | Never for this behavior | Existing structured guardrail metadata is sufficient. |
| Persisting live-only guardrail detail responses | Separate design | Durable hooks retain machine review evidence; detail lifetime is unchanged. |
| Parsing checker prose to determine visibility | Never | Structured machine fields are authoritative and unknown values fail visible. |
| A new keybinding, slash command, CLI flag, server setting, or project setting | Later design | Reuse `ExpandTools`; persistent policy belongs only to strict client settings. |

## Definition of done

1. `task lint`, `task test:race`, `task docs`, and `task site:build` pass on the final candidate, apart from independently reproduced baseline failures documented with exact evidence.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green for runtime changes.
4. The implementation branch remains based on the amended planning commit and reports interface conformance.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- `ExpandTools` is deliberately global: revealing tool details also reveals benign guardrail notices. A separate guardrail-only control is deferred unless use shows the shared details state is too broad.
- Live detail text remains unavailable in historical replay because ADR 0363 defines it as live-only; the durable hook summary remains available and follows the same benign visibility policy.
