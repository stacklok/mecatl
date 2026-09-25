# Mecatl Studio session activity — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — adds browser presentation of delegation facts already carried by Studio's run stream; the daemon protocol, published SDK, and browser/BFF authority boundary stay intact.
**Decision record:** None — [ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md) owns the Studio boundary, and [ADR 0079](../adr/0079-delegation-observability-convergence.md) owns the existing bounded delegation projection. This plan makes product display choices within those decisions.
**Phase:** Studio design alignment
**Status:** proposed, 2026-09-24. Reconciled with the merged [#1847 Plan / Interface PR #1880](https://github.com/stacklok/mecatl/pull/1880) and [implementation PR #1886](https://github.com/stacklok/mecatl/pull/1886); awaiting #1849 Plan / Interface approval.
**Delivery:** Split. The three event families, replay behavior, and panel content boundary warrant a separate Plan / Interface review.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#1849](https://github.com/stacklok/mecatl/issues/1849).
**Plan PR:** [stacklok/mecatl#1885](https://github.com/stacklok/mecatl/pull/1885)
**Approved baseline:** absent until this Plan / Interface PR merges

Delegated work appears as compact cards under its assistant turn and in a session activity panel. The panel keeps Subagents, Parallel groups, and Teams separate and shows only facts projected by their events. This plan consumes the flat transcript and stream ownership seam landed by [#1847 Plan / Interface PR #1880](https://github.com/stacklok/mecatl/pull/1880) and [implementation PR #1886](https://github.com/stacklok/mecatl/pull/1886). The stacked implementation follows that merged chat layout; the plan can be reviewed independently. The [Studio architecture topic](../architecture.md#mecatl-studio), [chat contract](studio-chat.md), and [ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md) remain the browser/BFF boundary.

The activity panel owns its roster, tabs, selected delegation, tasks, findings, and trace content. It mounts in the existing chat preview host for this feature; [#1850](https://github.com/stacklok/mecatl/issues/1850) owns extraction of the shared responsive side-panel shell, including its reusable frame and resize behavior. The activity content accepts only browser projection types, so that shell can host it without owning its event parsing. The implementation PR updates `user-docs/building/deployment/studio.md`, the public behavior owner, after the feature exists; this plan does not describe unimplemented behavior there. [#1779](https://github.com/stacklok/mecatl/issues/1779) owns maintainer desktop/mobile and interaction review.

## Human decisions

- [x] Opening policy. — Decision: cards and a session activity control open the panel on request; incoming events never move focus or open it.
- [x] Detail density. — Decision: inline cards show identity, observed state, current tool or count when present, and the terminal outcome. Trace previews live in the panel.
- [x] Trace retention. — Decision: retain the latest 12 trace entries per child, branch, or team member in browser memory, with an older-entry omission count. Keep latest task/finding snapshots and terminal outcomes independently of that trace window.
- [x] Inline placement. — Decision: place a family-specific card row under the assistant turn that owns the delegation, alongside the normal tool presentation. Match updates by the event's parent call and child/branch/member identity; a missing parent tool call does not suppress an observed card.
- [x] Panel selection. — Decision: activating a card opens the matching family and child, Parallel group, or team member. The session activity control opens the session roster, choosing a live family first. The panel remains read-only.

## Interface contract

- **gRPC / protobuf:** None — `subagent.start|tool|end`, `parallel.start|branch|end`, and `team.start|member|tasks|findings|end` already carry the required facts through the published SDK. No daemon message, method, field, or number changes.
- **Exported Go APIs / interfaces:** None — no engine or host Go symbol changes.
- **Tool schemas:** None — delegation tools and their model-facing arguments stay unchanged.
- **CLI / config:** None — no flag, environment variable, replay limit, or preference key is added. The existing `STUDIO_ACTIVITY_REPLAY_MAX` and `STUDIO_ACTIVITY_MAX_STREAMS` still govern activity reads.
- **Events / persistence:** Keep [the BFF run stream](../../apps/contracts/src/schemas/chat.ts) and its `run.started | run.event | run.truncated | run.error` union unchanged. `serializedMecatlEventSchema.payload` is already JSON-safe `unknown`; [the BFF serializer](../../apps/server/src/mecatl/chat.ts) forwards known delegation payloads with camel-case fields, decimal-string `bigint` values, and the original `kind`, `runId`, and decimal-string `seq`. The browser parses only `unknown: false` events with one of the eleven exact kinds above, without importing SDK or daemon types. Its session-owned `DelegationFleet` has separate `subagents`, `parallelGroups`, and `teams`; each activity key is `(sessionId, runId, family, parentCallId)`, with `childId` for a Subagent, zero-based `branchIndex` for a Parallel branch, and `teamId` plus member name for a Team lane. A missing start may yield a partial observed card; absent fields remain unknown. Dedupe durable frames by `(sessionId, runId, seq)` before adding counts, text, or traces; compare decimal sequences without `Number` conversion. A repeated `run.started` is a no-op for an already-followed run. `run.truncated: bound` continues from its cursor under the existing reattach budget; `gap`, exhausted reattach, and a stream without a terminal delegation event leave its outcome unknown and disclose incomplete activity. Keep at most 12 trace entries per child/branch/member, each `text`/`detail` preview capped to 201 Unicode code points including the daemon's possible ellipsis, and count omitted entries. Cap concatenation of adjacent `message.delta` previews too. `team.tasks`/`team.findings` replace their prior snapshots, including with an empty snapshot; `team.end` supplies final tasks, findings, dispositions, and stop. #1847's merged title fields and optional scheduled-delivery annotation coexist with this unchanged delegation payload. No browser storage, new BFF route, new SSE field, or OpenAPI generation change is needed.
- **Security / authority:** The panel is read-only. Studio has no authenticated BFF route for child control, and the published SDK's high-level `RunControls` has no `cancelChild`; a lower-level daemon route does not authorize a browser affordance. Whole-run cancel remains the existing exact `{sessionId, runId}` action outside this panel. Treat goal, role, task description, finding body, `text`, `detail`, stop, and cause as untrusted plain text in React text nodes, with no Markdown, HTML, link execution, or raw payload display. Browser parsing allowlists fields; it ignores `raw`, child workspaces/paths, and malformed payloads. The panel uses only the authenticated same-origin BFF stream and preserves session/account isolation and the shipped CSP.
- **Compatibility / migration:** Existing chat URLs, session/run IDs, transcript rows, BFF routes, storage keys, and generated browser client stay valid. #1847's merged message anchors and `run.event` consumption are the integration seam used by the stacked implementation. `SessionActivityContent` accepts local `DelegationFleet`, optional local focus, and a focus callback; it owns the three family views while #1850 can replace its host shell without importing event or SDK types. Reopening a session rebuilds only the delegation history the bounded activity replay actually supplies; the transcript is authoritative for conversation text, not a fabricated source of child state. No stored-state migration or SDK release is required.

## In scope — 5 scenarios, in implementation order

### Scenario 1 — interleaved and replayed events keep three distinct projections

The [current chat reducer](../../apps/web/src/features/chat/chat-state.ts) handles message, tool, permission, and result kinds but does not project delegation events. The [chat activity contract](studio-chat.md) already provides durable `seq`, bounded replay, and cursor reattachment behind the BFF boundary in [ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md).

**Acceptance:**
- AC1.1: interleaved `subagent.*`, `parallel.*`, and `team.*` frames from multiple runs build separate, stable entries. Replayed frames, including a repeated start and duplicate `seq`, do not duplicate cards, tool counts, task snapshots, or text; an invalid or future event remains safely ignored.
  - verify: vitest:apps/web/src/features/chat/delegation-fleet.test.ts#c2VwYXJhdGVzIGludGVybGVhdmVkIGRlbGVnYXRpb24gZmFtaWxpZXMgYW5kIGRlZHVwbGljYXRlcyByZXBsYXkgYnkgcnVuIGFuZCBzZXF1ZW5jZQ — `delegation-fleet.test.ts :: "separates interleaved delegation families and deduplicates replay by run and sequence"`
- AC1.2: a bounded replay resumes from its cursor without resetting activity; a gap, spent reattach budget, missing start, or absent terminal event leaves only the observed facts visible with an incomplete-history or unknown-outcome notice. Switching sessions cannot carry a former session's activity into the new view.
  - verify: vitest:apps/web/src/features/chat/delegation-fleet.test.ts#a2VlcHMgcGFydGlhbCBhY3Rpdml0eSBob25lc3QgYWNyb3NzIGJvdW5kZWQgcmVwbGF5IGFuZCBnYXBz — `delegation-fleet.test.ts :: "keeps partial activity honest across bounded replay and gaps"`
- AC1.3: the browser projection consumes Studio contract events and local view models only; no browser feature imports the published SDK or daemon protocol types. The existing BFF event envelope carries all required delegation facts without an added route or schema field.
  - verify: inspection — inspect `apps/web/src/features/chat` imports, `apps/contracts/src/schemas/chat.ts`, and `apps/server/src/mecatl/chat.ts` alongside the generated-client drift gate.

### Scenario 2 — a Subagent card reflects its child run

The [Subagent projection](../architecture/subagents-and-teams.md) sends a flat child lifecycle and bounded previews, including background work and a terminal stop. It is distinct from [Parallel's fork-join group](../architecture/parallelism.md).

**Acceptance:**
- AC2.1: `subagent.start` shows the observed child and goal; `subagent.tool` assigns its cumulative `toolCount`, shows the current tool and capped trace preview, and `subagent.end` alone settles the child using its `stop`, optional failure `cause`, and duration. A background child remains active after the Subagent tool's immediate `tool.result` while the child runs; its delivered `subagent.end` precedes the parent run's terminal `result`. A resumed call with a new parent call gets a distinct card.
  - verify: vitest:apps/web/src/features/chat/delegation-fleet.test.ts#cHJlc2VydmVzIHN1YmFnZW50IHRvb2wgY291bnRzIGFuZCB0ZXJtaW5hbCBzdG9wcyBhY3Jvc3MgcmVwbGF5 — `delegation-fleet.test.ts :: "preserves subagent tool counts and terminal stops across replay"`
- AC2.2: the inline Subagent card is readable as a button with a text state and stop reason, and opening it focuses that child in the panel. Failed and completed children stay distinguishable without color or hover text.
  - verify: vitest:apps/web/src/features/chat/delegation-card.test.tsx#cmVuZGVycyBhIHN1YmFnZW50IGNhcmQgd2l0aCBpdHMgb2JzZXJ2ZWQgb3V0Y29tZQ — `delegation-card.test.tsx :: "renders a subagent card with its observed outcome"`

### Scenario 3 — Parallel shows a group, branches, and a declared winner

[Parallel events](../architecture/parallelism.md) use one `parentCallId` for a fork-join group and zero-based `branchIndex` for its branches. `parallel.end` is the sole winner source.

**Acceptance:**
- AC3.1: `parallel.start` records `join` and branch count; each `parallel.branch` transition updates only its indexed branch, including its own goal, tool summary, failed flag, and terminal stop. Only `parallel.end` can mark a winning branch; winner `0` is valid, and `-1` or `join=all` shows no winner. A branch is never labeled or keyed as a standalone Subagent.
  - verify: vitest:apps/web/src/features/chat/delegation-fleet.test.ts#a2VlcHMgcGFyYWxsZWwgYnJhbmNoZXMgc2VwYXJhdGUgYW5kIHNlbGVjdHMgb25seSBhIGRlY2xhcmVkIHdpbm5lcg — `delegation-fleet.test.ts :: "keeps parallel branches separate and selects only a declared winner"`
- AC3.2: the inline Parallel cards and panel name the group and branch states separately, show the join strategy and winner only when known, and retain failed and completed branches in the same group.
  - verify: vitest:apps/web/src/features/chat/delegation-card.test.tsx#cmVuZGVycyBwYXJhbGxlbCBncm91cCBhbmQgYnJhbmNoIHN0YXR1cyBkaXN0aW5jdGx5IGZyb20gYSBzdWJhZ2VudA — `delegation-card.test.tsx :: "renders parallel group and branch status distinctly from a subagent"`

### Scenario 4 — Team shows its roster and latest shared work

[Team events](../architecture/subagents-and-teams.md) add a roster, task and finding snapshots, per-member observations, and terminal dispositions. These structures are Team-only under [ADR 0079](../adr/0079-delegation-observability-convergence.md).

**Acceptance:**
- AC4.1: `team.start` establishes lead and member labels; `team.member` updates only that member's observed tool/message trace and round state. `team.tasks` and `team.findings` replace the respective snapshots. `team.end` supplies final snapshots and distinguishes done from stopped members, mapping published numeric reasons `1=error`, `2=cancelled`, `3=budget`, with `errorRounds` kept separate from final disposition. A failed round does not declare the whole team failed.
  - verify: vitest:apps/web/src/features/chat/delegation-fleet.test.ts#YXBwbGllcyBsYXRlc3QgdGVhbSB0YXNrIGZpbmRpbmcgYW5kIHRlcm1pbmFsIGRpc3Bvc2l0aW9uIHNuYXBzaG90cw — `delegation-fleet.test.ts :: "applies latest team task finding and terminal disposition snapshots"`
- AC4.2: the Team view shows the roster, current tasks and dependencies, findings, stop reason, and per-member state using observed text and explicit labels. A task or finding containing HTML-like input is displayed as text.
  - verify: vitest:apps/web/src/features/chat/delegation-panel.test.tsx#cmVuZGVycyB0ZWFtIHJvc3RlciB0YXNrcyBhbmQgZmluZGluZ3MgYXMgcGxhaW4gdGV4dA — `delegation-panel.test.tsx :: "renders team roster tasks and findings as plain text"`

### Scenario 5 — the activity panel stays usable and read-only

The [existing chat preview host](../../apps/web/src/features/chat/content-preview-panel.tsx) supplies the current panel frame under [ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md). [#1850](https://github.com/stacklok/mecatl/issues/1850) owns its eventual shared shell, while [#1779](https://github.com/stacklok/mecatl/issues/1779) owns visual and interaction review.

**Acceptance:**
- AC5.1: a card opens its exact delegation in the appropriate panel tab; the session control opens the session roster. Keyboard users can open, switch tabs and roster rows, read the selected details, close with Escape or the close button, and return focus to the opener. New events update content without opening the panel, stealing focus, or resetting selection.
  - verify: vitest:apps/web/src/features/chat/delegation-panel.test.tsx#b3BlbnMgYWN0aXZpdHkgZnJvbSBhIGNhcmQgYW5kIHJlc3RvcmVzIGZvY3VzIG9uIGNsb3Nl — `delegation-panel.test.tsx :: "opens activity from a card and restores focus on close"`
- AC5.2: at 320, 500, and desktop widths the panel keeps its title, controls, roster, and selected details reachable without page-level horizontal scrolling. Each child/branch/member trace retains its latest 12 entries and a count of older omitted entries; repeated message deltas never make one row unbounded. State changes are announced without reading every trace update aloud. A browser journey coordinated through #1776 records desktop/mobile focus and layout.
  - verify: vitest:apps/web/src/features/chat/delegation-panel.test.tsx#a2VlcHMgYWN0aXZpdHkgdXNhYmxlIGF0IG1vYmlsZSB3aWR0aHMgYW5kIGJvdW5kcyB0cmFjZSByb3dz — `delegation-panel.test.tsx :: "keeps activity usable at mobile widths and bounds trace rows"`
  - verify: inspection — desktop/mobile screenshots and keyboard journey under the served Studio CSP, including a long interleaved run.
- AC5.3: model-influenced labels, task and finding text, and trace previews cannot execute markup or navigate on their own. The panel has no child cancel action; the separate whole-run Stop control keeps its existing target and behavior.
  - verify: vitest:apps/web/src/features/chat/delegation-panel.test.tsx#ZG9lcyBub3QgcmVuZGVyIGNoaWxkIGNhbmNlbGxhdGlvbiBvciBleGVjdXRlIG1vZGVsIGluZmx1ZW5jZWQgbWFya3Vw — `delegation-panel.test.tsx :: "does not render child cancellation or execute model influenced markup"`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Shared resizable side-panel shell and other inspection panels | [#1850](https://github.com/stacklok/mecatl/issues/1850) | #1849 supplies activity content and a usable mount in the current preview host. |
| Per-child cancellation or inspection controls | Separate control contract | Requires a published high-level SDK operation and authenticated BFF route with exact child/run authority. |
| New daemon event fields or broader child transcripts | Separate protocol decision | Current bounded projections and replay define the facts available here. |
| Approval interaction, MCP inventory, and local demo | [#1848](https://github.com/stacklok/mecatl/issues/1848), [#1851](https://github.com/stacklok/mecatl/issues/1851), [#1856](https://github.com/stacklok/mecatl/issues/1856) | Those features own their controls and data. |

## Definition of done

1. Focused reducer, component, and browser proofs pass with `task studio:check` and `task site:build`; maintainers review desktop/mobile screenshots and keyboard flow under #1779.
2. Applicable `task lint`, `task test:race`, `task docs`, and the offline demo pass on the implementation candidate; `task api:check` remains unchanged because no exported Go API changes.
3. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
4. The implementation PR links this approved Plan / Interface PR and commit, reports interface conformance, and updates the public Studio owning page.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- The published stream has no separate child-history query. A replay gap or exhausted reattach can leave older cards absent; the panel must say the observed history is incomplete.
- The merged #1847 transcript and stream ownership seam is the integration baseline. Preserve this three-family event contract if message composition changes; material drift returns to Plan / Interface review.
