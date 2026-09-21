# Mecatl Studio knowledge — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — adds the knowledge product surface (configured and learned skills, learning proposals, session reflection, user memory and its consolidation) to the Studio BFF and the knowledge workspace to the web app, inside the boundary ADR 0351 fixed; no new durable architecture decision.
**Decision record:** None — the boundary and the published-SDK rule are ADR 0351's; the daemon-side learning, skills, and memory contracts already exist (see [architecture](../architecture.md#evidence-backed-reflection)); this plan only projects them through the BFF.
**Phase:** capability — third feature layer of the Studio stack
**Status:** proposed, 2026-09-21. Seventh layer of the Studio `gh stack`; scope decisions were taken by the assistant under the directing human's go-ahead and are recorded below for review.
**Delivery:** Split, under the same stacking exception as [the bootstrap plan](studio-bootstrap.md): this plan is a stack layer, the implementation is the next layer, and nothing merges until the whole series is reviewed.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#1736](https://github.com/stacklok/mecatl/issues/1736).
**Plan PR:** [stacklok/mecatl#1759](https://github.com/stacklok/mecatl/pull/1759)
**Approved baseline:** absent until the stack root merges

Port the prototype's knowledge feature onto Studio: the BFF projects the daemon's configured
skills, learned skills (with versions, diffs, lifecycle history, and revision-checked actions),
learning proposals (decide, undo promotion), session reflection receipts, and the user model
memory with its daemon-curated consolidation plans; each surface is gated on its own
deployment capability. The web app gains the knowledge workspace: configured and learned skill
views, a learned-skill detail page with a line diff between versions, the learning review, and the memory
consolidation dialog.

## Human decisions

- [x] Scope of the port. — Decision: the whole prototype knowledge feature, BFF and web; nothing daemon-side changes.
- [x] Contract provenance. — Decision: schemas, paths, and operation ids are ported byte-for-byte from the prototype (fourteen routes).
- [x] Capability gating per surface. — Decision: each surface reads its own flag live from the negotiated runtime snapshot — `skills`, `learnedSkills`, `learningProposals`, `reflection`, `userModel`, and `manualDream.userModel.{generate,decide}` — and answers `501` with a surface-specific code (`learned_skills_unsupported`, `learning_proposals_unsupported`, `reflection_unsupported`, `user_memory_unsupported`, `memory_consolidation_unsupported`, `memory_consolidation_decision_unsupported`); the two list routes for skills and learned skills never fail and return `supported: false` with a reason instead. The consolidation surfaces relay the daemon's own `unavailableReason` verbatim.
- [x] Optimistic concurrency. — Decision: learned-skill actions carry the daemon's `expectedRevision` and `version`, proposal decisions and undo carry `expectedVersion`; the BFF requires them non-empty and relays the daemon's conflict answer through the bootstrap's error mapping (a stale token is the daemon's `409`/`412`, never a silent retry).
- [x] Mutations and the bootstrap's security gates. — Decision: every state-changing knowledge route sits behind the same-origin + double-submit CSRF check and, with interactive login active, the `401` session gate.

## Interface contract

- **gRPC / protobuf:** None — the BFF drives the daemon exclusively through the published SDK namespaces `skills`, `learnedSkills`, `learningProposals`, `reflection`, `userModel`, and `dreamPlans`.
- **Exported Go APIs / interfaces:** None — no Go source changes.
- **Tool schemas:** None — Studio adds no model-facing tool.
- **CLI / config:** None — no new variable, flag, task, or compose setting.
- **Events / persistence:** No streams and no browser persistence. Routes under `/api/v1`: `GET /skills` (`listConfiguredSkills` → `{supported, reason, items[]}`), `GET /learned-skills` (`listLearnedSkills` → `{supported, reason, complete, items[]}`), `GET /learned-skills/changes` (`listLearnedSkillChanges` → `{complete, items[]}`), `GET /learned-skills/{skillId}?ownerAgent&version` (`getLearnedSkill`), `GET /learned-skills/{skillId}/diff?ownerAgent&fromVersion&toVersion` (`diffLearnedSkillVersions` → `{diff, fromVersion, toVersion}`), `POST /learned-skills/{skillId}/actions` (`actOnLearnedSkill` `{action: activate|archive|reject|rollback, expectedRevision, version, ownerAgent, targetVersion?}` → the updated skill), `GET /learning-proposals?status` (`listLearningProposals` → `{supported, reason, complete, items[]}`), `POST /learning-proposals/{proposalId}/decisions` (`decideLearningProposal` `{decision: approve|reject, expectedVersion, reason}`), `POST /learning-proposals/{proposalId}/undo` (`undoLearningPromotion` `{expectedVersion}`), `POST /sessions/{sessionId}/reflection` (`reflectSession` → a receipt `{reflectionId, disposition, abstained, message, reason, staged, promoted, queued, conflicted}`), `GET /user-memory` (`listUserMemory`), `GET /user-memory/{memoryKey}` (`getUserMemory`), `POST /user-memory/consolidation/plans` (`generateMemoryConsolidationPlan` → `201` plan `{id, target: "user_model", expiresAt, operations[], plannedOperationCount, plannedSourceCount}`), `POST /user-memory/consolidation/plans/{planId}/decisions` (`decideMemoryConsolidationPlan` `{decision: apply|dismiss}` → a receipt). A learned skill is `{id, name, description, body, ownerAgent, version, revision, state, supersedes, evidenceCount, updatedAt, actions: {activate, archive, reject, rollback}}`; a proposal carries its `decisions[]` history, `promotionAvailable` with a reason, `projectScoped`, and `triggers[]`. Timestamps are ISO 8601 strings or `null`. Problem codes: `runtime_unavailable` (`503`), the six surface-specific `501` codes above, plus the daemon's own codes relayed.
- **Security / authority:** Mutations inherit the CSRF and session gates. Concurrency tokens (`expectedRevision`, `version`, `expectedVersion`) are required non-empty, and so is `targetVersion` for a rollback; `reason` is bounded to 2,000 characters. Learned-skill actions always address a skill by id plus `ownerAgent`, never by name. The BFF never widens what the daemon authorises: per-skill `actions` flags, `promotionAvailable`, and every capability are the daemon's answers relayed, and the consolidation plan is generated and applied by the daemon — the browser only chooses apply or dismiss on a daemon-issued plan id. Skill and memory bodies are returned as opaque text; problem details keep the bootstrap's clamping and redaction.
- **Compatibility / migration:** Additive on the BFF (fourteen routes, regenerated OpenAPI and client). The web nav gains `Skills` after `Scheduled`. No existing route or schema changes.

## In scope — 5 scenarios, in implementation order

### Scenario 1 — every knowledge surface tells the truth about its capability

The daemon enables skills, learning, reflection, and the user model independently
([architecture](../architecture.md#evidence-backed-reflection)); Studio reads each flag live and degrades per surface rather than failing wholesale ([ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md)).

**Acceptance:**
- AC1.1: with `skills` on, `GET /api/v1/skills` lists configured skills (name, description, active version, agent ownership) with `supported: true`; with it off the list is empty with `supported: false` and a reason.
  - verify: vitest:apps/server/src/routes/knowledge.test.ts#bGlzdHMgY29uZmlndXJlZCBza2lsbHM — `apps/server/src/routes/knowledge.test.ts :: "lists configured skills"`
- AC1.2: each surface answers `501` with its own code when its capability is off — learned-skill detail, diff, changes, and actions; proposal decisions and undo; reflection; consolidation generate and decide (relaying the daemon's `unavailableReason`); memory detail — while the four lists (configured skills, learned skills, learning proposals, and memory) degrade to `supported: false` with a reason instead of failing.
  - verify: vitest:apps/server/src/routes/knowledge.test.ts#Y2FwYWJpbGl0eS1kaXNhYmxlcyBsZWFybmVkLXNraWxsIG11dGF0aW9ucw — `apps/server/src/routes/knowledge.test.ts :: "capability-disables learned-skill mutations"`
  - verify: vitest:apps/server/src/routes/knowledge.test.ts#Y2FwYWJpbGl0eS1kaXNhYmxlcyBlYWNoIGtub3dsZWRnZSBzdXJmYWNlIHdpdGggaXRzIG93biA1MDEgY29kZQ — `apps/server/src/routes/knowledge.test.ts :: "capability-disables each knowledge surface with its own 501 code"`
- AC1.3: without a runtime every knowledge route answers `503` `runtime_unavailable`.
  - verify: vitest:apps/server/src/routes/knowledge.test.ts#YW5zd2VycyA1MDMgcnVudGltZV91bmF2YWlsYWJsZSBvbiBldmVyeSBrbm93bGVkZ2Ugcm91dGUgd2l0aG91dCBhIHJ1bnRpbWU — `apps/server/src/routes/knowledge.test.ts :: "answers 503 runtime_unavailable on every knowledge route without a runtime"`
- AC1.4: the BFF builds the capability set from the live snapshot: `manualDream.userModel.generate/decide` and its `unavailableReason` feed the consolidation gates, and a missing snapshot reads as every capability off.
  - verify: vitest:apps/server/src/mecatl/knowledge.test.ts#ZGVyaXZlcyB0aGUga25vd2xlZGdlIGNhcGFiaWxpdHkgc2V0IGZyb20gdGhlIGxpdmUgcnVudGltZSBzbmFwc2hvdA — `apps/server/src/mecatl/knowledge.test.ts :: "derives the knowledge capability set from the live runtime snapshot"`

### Scenario 2 — learned skills: detail, diff, history, and revision-checked actions

Learned skills are versioned daemon artefacts; the BFF addresses them by id and owner agent and
forwards the daemon's concurrency tokens unchanged ([ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md)).

**Acceptance:**
- AC2.1: detail returns the requested version's body and metadata with action flags that offer each action only in the lifecycle states the daemon accepts it for (`activate` for a `staged` version whose latest evaluation passed, `reject` for `draft`/`evaluated`/`staged`, `archive` for `active`, `rollback` for an `active` version that supersedes another); diff returns the unified diff between two versions; the changes route lists lifecycle history newest first with `complete`.
  - verify: vitest:apps/server/src/routes/knowledge.test.ts#cmV0dXJucyBsZWFybmVkLXNraWxsIGRldGFpbCwgZGlmZiwgYW5kIGxpZmVjeWNsZSBoaXN0b3J5 — `apps/server/src/routes/knowledge.test.ts :: "returns learned-skill detail, diff, and lifecycle history"`
  - verify: vitest:apps/server/src/mecatl/knowledge.test.ts#b2ZmZXJzIGVhY2ggbGVhcm5lZC1za2lsbCBhY3Rpb24gb25seSBpbiB0aGUgbGlmZWN5Y2xlIHN0YXRlcyB0aGUgZGFlbW9uIGFjY2VwdHM — `apps/server/src/mecatl/knowledge.test.ts :: "offers each learned-skill action only in the lifecycle states the daemon accepts"`
- AC2.2: `activate`, `archive`, and `reject` call the SDK operation of the same name with `{id, ownerAgent, expectedRevision}`; `rollback` calls `rollback` with the request's `targetVersion`, which is required because rollback reactivates an archived version, never the active one; a missing token or rollback target is a `400` before any SDK call.
  - verify: vitest:apps/server/src/mecatl/knowledge.test.ts#bWFwcyBsZWFybmVkLXNraWxsIGFjdGlvbnMgb250byB0aGUgU0RLIG9wZXJhdGlvbnMgd2l0aCB0aGUgZXhwZWN0ZWQgcmV2aXNpb24gYW5kIHJvbGxiYWNrIHRhcmdldA — `apps/server/src/mecatl/knowledge.test.ts :: "maps learned-skill actions onto the SDK operations with the expected revision and rollback target"`
  - verify: vitest:apps/server/src/routes/knowledge.test.ts#cmVmdXNlcyBhIHJvbGxiYWNrIHdpdGhvdXQgYSB0YXJnZXQgdmVyc2lvbiBiZWZvcmUgYW55IFNESyBjYWxs — `apps/server/src/routes/knowledge.test.ts :: "refuses a rollback without a target version before any SDK call"`
- AC2.3: the daemon's conflict on a stale `expectedRevision` (`proposal_conflict`, gRPC Aborted) is relayed as `409` problem details with the daemon's code, never retried.
  - verify: vitest:apps/server/src/mecatl/knowledge.test.ts#cmVsYXlzIGEgc3RhbGUtcmV2aXNpb24gY29uZmxpY3QgZnJvbSB0aGUgZGFlbW9uIHdpdGhvdXQgcmV0cnlpbmc — `apps/server/src/mecatl/knowledge.test.ts :: "relays a stale-revision conflict from the daemon without retrying"`

### Scenario 3 — learning review and session reflection

Proposals are the daemon's evidence-backed learning queue ([architecture](../architecture.md#evidence-backed-reflection)); Studio lets a human decide them and trigger a reflection over a completed session.

**Acceptance:**
- AC3.1: proposals list by `status` with their decision history and promotion availability; `approve`/`reject` forward `expectedVersion` and the bounded `reason`; undo forwards `expectedVersion`; reflecting a session returns the daemon's receipt including an abstention message when it abstained.
  - verify: vitest:apps/server/src/routes/knowledge.test.ts#cmV2aWV3cyBwcm9wb3NhbHMgYW5kIHJlZmxlY3RzIGNvbXBsZXRlZCBzZXNzaW9ucw — `apps/server/src/routes/knowledge.test.ts :: "reviews proposals and reflects completed sessions"`
- AC3.2: the browser summarises a receipt's materialised counts and prefers the daemon-authored abstention message over a generic one.
  - verify: vitest:apps/web/src/features/knowledge/learning-review.test.ts#c3VtbWFyaXplcyBtYXRlcmlhbGl6ZWQgcmVmbGVjdGlvbiBjb3VudHM — `apps/web/src/features/knowledge/learning-review.test.ts :: "summarizes materialized reflection counts"`
  - verify: vitest:apps/web/src/features/knowledge/learning-review.test.ts#cHJlZmVycyB0aGUgZGFlbW9uLWF1dGhvcmVkIGFic3RlbnRpb24gbWVzc2FnZQ — `apps/web/src/features/knowledge/learning-review.test.ts :: "prefers the daemon-authored abstention message"`

### Scenario 4 — user memory and daemon-curated consolidation

The user model is daemon-owned memory; consolidation is a two-step daemon plan the human
applies or dismisses ([architecture](../architecture.md#evidence-backed-reflection); [ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md)).

**Acceptance:**
- AC4.1: the memory list and the exact-key detail return the daemon's entries; generating a plan returns `201` with the plan's operations and counts; deciding `apply` or `dismiss` returns the daemon's receipt.
  - verify: vitest:apps/server/src/routes/knowledge.test.ts#cmV0dXJucyBleGFjdCBtZW1vcnkgZGV0YWls — `apps/server/src/routes/knowledge.test.ts :: "returns exact memory detail"`
  - verify: vitest:apps/server/src/routes/knowledge.test.ts#Z2VuZXJhdGVzIGFuZCBkZWNpZGVzIGRhZW1vbi1jdXJhdGVkIG1lbW9yeSBjb25zb2xpZGF0aW9uIHBsYW5z — `apps/server/src/routes/knowledge.test.ts :: "generates and decides daemon-curated memory consolidation plans"`
- AC4.2: the browser summarises apply receipts and recognises the daemon's stale-plan error so the user is told to regenerate rather than retry.
  - verify: vitest:apps/web/src/features/knowledge/memory-consolidation.test.ts#c3VtbWFyaXplcyBhcHBseSByZWNlaXB0cw — `apps/web/src/features/knowledge/memory-consolidation.test.ts :: "summarizes apply receipts"`
  - verify: vitest:apps/web/src/features/knowledge/memory-consolidation.test.ts#cmVjb2duaXplcyBzdGFsZSBkYWVtb24gcGxhbiBlcnJvcnM — `apps/web/src/features/knowledge/memory-consolidation.test.ts :: "recognizes stale daemon plan errors"`
- AC4.3: every knowledge mutation is refused with `403` `cross_site_request` without the CSRF pair and with `401` `unauthenticated` without a session when interactive login is active.
  - verify: vitest:apps/server/src/routes/knowledge.test.ts#a25vd2xlZGdlIG11dGF0aW9ucyByZXF1aXJlIHRoZSBDU1JGIHBhaXIgYW5kIGEgc2Vzc2lvbiB3aGVuIGludGVyYWN0aXZlIGxvZ2luIGlzIGFjdGl2ZQ — `apps/server/src/routes/knowledge.test.ts :: "knowledge mutations require the CSRF pair and a session when interactive login is active"`

### Scenario 5 — the knowledge workspace joins the shell

Navigation and routing follow the chat and schedules layers ([studio-chat](studio-chat.md); [ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md)).

**Acceptance:**
- AC5.1: the nav's third item is `Skills`; `/workspace/skills?view=configured|learned&item=…` renders the workspace and `/workspace/skills/{view}/{item}` the detail (learned skills open the diff-capable detail page); an unknown view redirects to `configured`.
  - verify: inspection — route files and `nav-items.ts`; `pnpm --filter @mecatl-studio/web typecheck` proves the typed links resolve.
- AC5.2: the learned-skill detail recovers the old and new description and body from the daemon's whole-body diff and renders a real line diff, so unchanged lines stay unchanged and a markdown bullet is not read as a deletion; text in another shape falls back to unified-diff classification without treating file headers as changes.
  - verify: vitest:apps/web/src/features/knowledge/learned-skill-detail.test.ts#Y2xhc3NpZmllcyB1bmlmaWVkIGRpZmYgbGluZXMgd2l0aG91dCB0cmVhdGluZyBmaWxlIGhlYWRlcnMgYXMgY2hhbmdlcw — `apps/web/src/features/knowledge/learned-skill-detail.test.ts :: "classifies unified diff lines without treating file headers as changes"`
  - verify: vitest:apps/web/src/features/knowledge/learned-skill-detail.test.ts#cmVuZGVycyB1bmNoYW5nZWQgbGluZXMgYW5kIG1hcmtkb3duIGJ1bGxldHMgaW4gd2hvbGUtYm9keSBkaWZmcyBieSB0aGVpciByZWFsIGNoYW5nZQ — `apps/web/src/features/knowledge/learned-skill-detail.test.ts :: "renders unchanged lines and markdown bullets in whole-body diffs by their real change"`
- AC5.3: `task studio:check` passes with the regenerated OpenAPI document and client committed; `task ac-trace` resolves every proof named here.
  - verify: inspection — the CI `studio` job.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Settings, search, shortcuts reference page | later stack layers, one Bounded plan each | bootstrap plan's per-feature decision |
| Project-scoped memory consolidation (`manualDream.projectMemory`) | a later plan | the prototype exposes the user-model target only |
| Editing skill or memory bodies from the browser | never for this plan | the daemon curates them; the browser decides, it does not author |
| `user-docs/` pages for Studio | the top layer of the stack | bootstrap plan's documentation decision |

## Definition of done

1. `task studio:check`, `task lint:actions`, `task docs`, and `task ac-trace` pass.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `docs/architecture.md`'s Studio section names the knowledge surface and its per-capability gates.
4. The implementation layer records the exact commit of this plan it built against.

## Deferred decisions and known risks

- Risk: the six `501` codes are Studio's own vocabulary; if the daemon later exposes richer capability reasons per surface, the codes stay and the `detail` carries the daemon's text.
- The mock daemon may expose none of these capabilities, so the live smoke covers the degraded paths; the supported paths are proven by the SDK-fake unit proofs.
