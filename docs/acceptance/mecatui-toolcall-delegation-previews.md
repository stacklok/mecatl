# Mecatui tool-call detail and delegation previews — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — client-local presentation and inspector navigation change, using existing received tool calls and bounded delegation projections without changing durable ownership, protocol, or authority.
**Decision record:** None — the existing scrollback, delegation-observability boundary, and approval integrity contract already define the data available; the new choices concern how mecatui presents it.
**Phase:** coordinated first slice of #2123: #2122 and #2115.
**Status:** proposed, 2026-10-05. Operator requested one plan for both issues and synchronization with Subagent, Parallel, and Team views; review of this PR is the interface checkpoint.
**Delivery:** Split. Child-preview inventory, selection, and truthful availability need contract review before implementation.
**Expected tasks:** deferred to orchestration
**Issues:** [#2122](https://github.com/stacklok/mecatl/issues/2122), [#2115](https://github.com/stacklok/mecatl/issues/2115); [umbrella #2123](https://github.com/stacklok/mecatl/issues/2123).

The existing `/toolcalls` inspector retains full **top-level** call arguments and results but currently presents Edit arguments as generic fields. It will show a colored, argument-derived Edit request diff without losing any received fields or claiming the edit was applied. It will also expose the **currently retained Subagent tool-activity previews** as visibly subordinate, preview-only rows in the inspector, with parent attribution and an honest limit on what can be known. It will not fetch or reconstruct child calls. See the [existing inspector contract](mecatui-toolcalls-inspector.md), [shared delegation projection](../architecture/subagents-and-teams.md), and [ADR 0079](../adr/0079-delegation-observability-convergence.md).

The Subagent, Parallel, and Team activity already shares a bounded trace shape. Wherever those same facts appear in the inline card, F6 Agents view, and `/toolcalls`, they will use the same semantic tool name, preview text, error cue, and boundedness label. Width, color, lane grouping, selection, and available detail remain surface-specific; the plan does not flatten three distinct delegation families into identical screens. The reader's operational entry point is [mecatui's TUI usage guide](../../user-docs/mecatui/using-the-tui.md); planned behavior stays here until implemented.

## Human decisions

- [x] Preview scope — Decision: use only existing client-received, bounded delegation activity; no new wire fields, server fetch, child transcript, broader redaction promise, or persistence/reload guarantee. Existing [#2114](https://github.com/stacklok/mecatl/issues/2114) owns reload fidelity.
- [x] Inspector inventory — Decision: show top-level calls as before and, under a Subagent parent with available activity, individually navigable **retained tool-preview rows** labeled as previews. Do not present a preview as an independently identified complete child call. Parallel and Team activity keep their family-specific F6 navigation and grouping; this slice synchronizes their existing trace presentation rather than adding their branches/members to the `/toolcalls` list.
- [x] Presentation parity — Decision: apply the same UI-local trace reduction and semantic formatting to Subagent, Parallel, and Team inline/F6 views and the inspector preview, preserving family-specific layout and the approval's separate verbatim raw-args floor. A tool-call-only preview has a neutral/pending cue; success/error requires an observed result safely associated with a retained call. Associate by tool name only when the name is nonempty and belongs to exactly one retained call in that lane (still pending), with no unresolved call evicted; a missing or repeated name or orphaned result is explicitly unattributed, and after an unresolved eviction subsequent results in that lane remain unattributed until the lane ends. Do not reconstruct the lost argument preview. The inspector's Edit diff is an additional view of received top-level arguments, not a result rendering or a view of child Edit calls.

## Interface contract

- **gRPC / protobuf:** None — consume existing tool-call/result and Subagent/Parallel/Team observability messages; do not add fields or fetch child history.
- **Exported Go APIs / interfaces:** None — only private mecatui presentation and UI-local selection state change.
- **Tool schemas:** None — tool execution and model-visible descriptions are unchanged.
- **CLI / config:** None — `/toolcalls`, its configured navigation/pointer actions, F6, and approval key ownership remain as shipped; no flags, keys, or commands are added.
- **Events / persistence:** None — no event or store schema change, second child history, or change to live-event/reconnect semantics. Inspector preview rows are derived only from currently received bounded traces; reconstructed transcripts may contain top-level calls without child previews.
- **Security / authority:** None — only current-session, already received display projections are shown. No child transcript injection into the parent conversation, hidden data reconstruction, new permission decision, or child approval ownership. Sanitize hostile text before styling and retain the sanitized verbatim approval raw-args view required by [ADR 0222](../adr/0222-mecatui-ask-args-view.md); bounded server previews are not a guarantee of secret redaction.
- **Compatibility / migration:** Additive inspector presentation plus a corrected neutral pending/unattributed cue in all three existing delegation trace views (today a non-error call-only chip uses a success glyph). Top-level list/detail, complete received arguments/results, conversation anchors, F6 grouping and selection, and approval behavior remain available. The implementation updates the owning public TUI usage page to describe bounded previews and their reload limitation, and corrects the delegation guide's claim about child detail without duplicating usage instructions.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — compare a proposed Edit in the inspector

The [inline request diff](../../cmd/mecatui/ui/render.go) and the [inspector's argument rows](../../cmd/mecatui/ui/toolcalls.go) currently interpret the same received Edit argument payload differently. Preserve [ADR 0222](../adr/0222-mecatui-ask-args-view.md)'s approval raw-args floor and the existing inspector's complete-detail contract.

**Acceptance:**
- AC1.1: A valid multiline Edit shows a readable path, removed and added lines in contrasting theme colors, and the `replace_all` flag in `/toolcalls` detail. All original received arguments, including extra fields, and the complete received result remain reachable in that detail; the diff is explicitly a view of the *request*, never evidence of an applied patch. The inline/approval Edit request and inspector agree on the meaning of path, removals, additions, and replace-all, while their viewport and line caps may differ.
  - verify: `TestMecatuiToolcallPreviews_Scenario1_EditArgumentParity`
- AC1.2: Pending, failed, provisional, canonical, and resumed top-level Edit calls show their true lifecycle and result independently of the request diff. Malformed/incomplete/non-object arguments use the existing readable original-argument fallback; hostile terminal controls, long lines, and narrow widths neither execute nor hide reachable detail.
  - verify: `TestMecatuiToolcallPreviews_Scenario1_EditLifecycleAndSafety`

### Scenario 2 — discover bounded Subagent activity without inventing child calls

The [current inspector inventory](../../cmd/mecatui/ui/toolcalls.go) uses scrollback block identity, whereas [Subagent traces](../../cmd/mecatui/ui/conversation.go) retain only the trailing twelve preview entries, not child tool-call IDs or full results. [ADR 0079](../adr/0079-delegation-observability-convergence.md) bounds client-only child observation; the parent tool call keeps its full-detail contract while preview rows are a different kind of entry.

**Acceptance:**
- AC2.1: `/toolcalls` retains chronological top-level calls and shows each currently retained Subagent **tool** trace entry from that parent's existing scrollback snapshot underneath the parent as a visibly nested, clearly labeled bounded preview with tool name, child/parent attribution when received, and only the currently available preview text/status cue. It never joins an unrelated F6 fleet lane by guessed child or parent ID. Enter or a visible-row click opens preview-only detail that explains its limits (server control scrub and length cap, not a promise of secret redaction); the parent remains separately selectable with complete top-level arguments and result. Child message snippets are not misclassified as tool calls, and no guessed path, arguments, result, or success appears.
  - verify: `TestMecatuiToolcallPreviews_Scenario2_SubagentRowsAndDetail`
- AC2.2: A preview row is selected by parent scrollback block ID plus its slot in that parent's bounded trace, **not** by a fabricated child-call ID or tool name. Selection remains on that slot across append and in-place result updates only while the preceding retained entries have not shifted and the row's tool name and kind still identify that slot; reflow alone does not change it. A cap eviction, ambiguous replacement, removed row, or session reconstruction returns selection to its parent instead of another preview. Earlier top-level selection, list follow, width reflow, pointer hits, Escape and conversation reading anchor remain stable. A finished or failed parent does not turn a pending child preview into a fabricated success.
  - verify: `TestMecatuiToolcallPreviews_Scenario2_LiveSelectionAndEviction`
- AC2.3: A freshly loaded transcript that reconstructs a parent without child activity still lists its top-level Subagent and says “activity preview unavailable; history may be incomplete” in its detail, rather than treating the absence as proof of no tools or assuming reload is the only possible cause. If bounded activity is subsequently received/replayed for that session, show only that recent projection and retain the incomplete-history caveat; do not merge it into a fabricated exhaustive timeline. Replacement sessions, overlapping IDs, and reconnects never borrow another session's previews.
  - verify: `TestMecatuiToolcallPreviews_Scenario2_ReloadAndSessionIsolation`

### Scenario 3 — present shared delegation facts consistently

[Shared trace reduction](../../cmd/mecatui/ui/conversation.go) and [trace rendering](../../cmd/mecatui/ui/render.go) already serve Subagent, Parallel, and Team with different [F6 ownership and grouping](../../cmd/mecatui/ui/agents_overlay.go). Share a semantic projection only where two consumers show the same available fact; keep each view's shape and [logical anchors](../adr/0301-logical-conversation-anchors.md).

**Acceptance:**
- AC3.1: Given the same bounded tool-call/result trace, the Subagent inline/F6 display and its `/toolcalls` preview agree on tool name, whether preview text reflects the latest available argument or result, and the status cue: neutral until a safely associated result is observed, then success/error. Repeated same-name calls, name-less Team results, evicted calls, and result-only events follow the conservative association rule in Human decisions and show an unattributed result instead of changing a guessed call. Parallel branches and Team members render the same trace semantics in inline/F6 wherever their projections have those facts. No view treats a missing result as success or labels a twelve-entry rolling trace as a complete history. The same hostile/long preview remains sanitized and width-bounded across ordinary and narrow screens.
  - verify: `TestMecatuiToolcallPreviews_Scenario3_DelegationFamilyParityAndSafety`
- AC3.2: F6 retains child-ID, branch-index, and team-member navigation, task/findings and other family-specific detail; `/toolcalls` adds no Parallel/Team child inventory and does not move F6 selection. Approval continues to own its keys and raw argument tier. The public TUI usage guide explains how to navigate preview rows versus F6 and when activity may be unavailable; the owning delegation guide no longer promises full child details from bounded previews.
  - verify: `TestMecatuiToolcallPreviews_Scenario3_OwnerBoundaries`; inspection — compare implemented UI with the two owning pages; run `task docs` and `task site:build`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Authoritative child tool IDs, complete args/results, child transcript reading, durable/reloaded activity | [#2114](https://github.com/stacklok/mecatl/issues/2114) and later data-contract review | Do not make a preview look complete or modify the server's projection. |
| Parallel branch or Team member rows in `/toolcalls` | Subsequent [#2123](https://github.com/stacklok/mecatl/issues/2123) slice if useful | Synchronize their trace semantics with existing F6/inline views, without inventing flattening/navigation rules. |
| Write all-additions view and richer result-body rendering | [#2122](https://github.com/stacklok/mecatl/issues/2122) follow-up if warranted; [#2066](https://github.com/stacklok/mecatl/issues/2066) | Implement the Edit request diff first; result rendering is distinct. |
| Shell approval pretty-tier and broad UI package refactor | [#552](https://github.com/stacklok/mecatl/issues/552), [#2077](https://github.com/stacklok/mecatl/issues/2077) | Keep the shared projection small and anchored in actual consumers. |

## Definition of done

1. Focused UI tests verify each acceptance criterion with offline fixtures and controlled widths; two-consumer parity and fresh-client reload absence are explicit.
2. The implementation candidate passes `task lint`, `task test:race`, `task docs`, `task site:build`, `task ac-trace-strict`, and the offline demo's tool/permission/result flow.
3. `/panel-review` reports no ship blockers; the implementation PR references the approved plan baseline and only a human merges it.

## Implementation guidance (non-binding)

- Consider a small client-local internal package for **domain-specific tool presentation** if it simplifies at least two real consumers—for example, Edit request rows shared by the active card and `/toolcalls`, or semantic delegation trace rows shared by F6 and the inspector. The existing [`cmd/mecatui/internal/renderfmt`](../../cmd/mecatui/internal/renderfmt/renderfmt.go) is one candidate; a UI-internal package or package-private helpers may fit better depending on dependencies. Choose the boundary during implementation rather than requiring a package extraction in this contract.
- Share interpretation of received facts, not styled ANSI strings, viewport geometry, selection, or a cached replacement for the canonical payload. Keep approval's sanitized verbatim raw arguments accessible; do not build a generic renderer framework for unrelated tools or change the distinct full-call versus bounded-child-preview data contracts.

## Deferred decisions and known risks

- Bounded trace slots have no child-call IDs; duplicate-looking events and evictions require conservative selection fallback, not correlation by tool name. A result can replace the stored call-argument preview, so the UI cannot recover both from one trace entry.
- An absent delegation trace after reload is not proof that the child ran no tools. Do not infer completeness from a top-level parent result or change the existing event replay contract in this slice.
