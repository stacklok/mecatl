# Mecatl Studio settings — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — adds the read-only runtime-settings and storage-health surface to the Studio BFF and the settings workspace to the web app, inside the boundary ADR 0351 fixed; no new durable architecture decision.
**Decision record:** None — the boundary and the published-SDK rule are ADR 0351's; provider and routing configuration stay deployment-managed (the operator-tier posture of [architecture](../architecture.md), "Build identity" and providers), so Studio only displays them.
**Phase:** capability — fourth feature layer of the Studio stack
**Status:** proposed, 2026-09-21. Ninth layer of the Studio `gh stack`; scope decisions were taken by the assistant under the directing human's go-ahead and are recorded below for review.
**Delivery:** Split, under the same stacking exception as [the bootstrap plan](studio-bootstrap.md): this plan is a stack layer, the implementation is the next layer, and nothing merges until the whole series is reviewed.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#1736](https://github.com/stacklok/mecatl/issues/1736).
**Plan PR:** [stacklok/mecatl#1761](https://github.com/stacklok/mecatl/pull/1761)
**Approved baseline:** absent until the stack root merges

Port the prototype's settings feature onto Studio: the BFF gains a read-only runtime-settings
route (build identity, provider display endpoint, the model and provider inventory, and the
deployment-managed management flags with their reasons) and a storage-health route, both
capability-gated; the web app gains the settings workspace — profile, agent, appearance, memory,
learning, models, storage, and about sections — and the chat workspace's model picker, which the
chat layer deferred until an inventory exists, becomes live over the new inventory.

## Human decisions

- [x] Scope of the port. — Decision: the whole prototype settings feature, BFF and web, including the browser-only profile and appearance preferences; nothing daemon-side changes.
- [x] Contract provenance. — Decision: schemas, paths, and operation ids are ported byte-for-byte from the prototype (two `GET` routes).
- [x] Management stays deployment-managed. — Decision: provider credentials/configuration and model routing are never editable from the browser; the runtime-settings response carries `management.providerConfiguration: false` and `routingConfiguration: false` with fixed reasons so the UI capability-disables those controls instead of emulating them.
- [x] Safe projection only. — Decision: the runtime-settings response exposes the build id, server implementation, the provider's *display* endpoint, and the model/provider inventory — never a credential, API key, raw base URL with credentials, or environment value.
- [x] Chat's model picker. — Decision: the chat and side-thread composers re-wire to `getRuntimeSettings` and the browser's disabled-model preference, so the model and effort control the chat layer renders only when an inventory exists appears.
- [x] Shared UI restored for settings. — Decision: the `sheet`, `switch`, and `use-mobile` primitives the settings workspace imports (deleted as unused in the chat layer) return in this layer.

## Interface contract

- **gRPC / protobuf:** None — the BFF drives the daemon exclusively through the published SDK's `server.info`, `models.list`, and `storage.getHealth`.
- **Exported Go APIs / interfaces:** None — no Go source changes.
- **Tool schemas:** None — Studio adds no model-facing tool.
- **CLI / config:** None — no new variable, flag, task, or compose setting.
- **Events / persistence:** No streams and no mutations. Routes under `/api/v1`: `GET /settings/runtime` (`getRuntimeSettings` → `{buildId, serverImplementation, providerEndpoint, modelsSupported, modelsReason, models[], providers[], management}`) where a model row is `{id, providerId, displayName, contextLimit, image, reasoning}`, a provider row is `{id, state, hint, modelCount, availableNotDefault, defaultModelAutoSelected}` sorted by id, and `management` is `{providerConfiguration, providerConfigurationReason, routingConfiguration, routingConfigurationReason}`; `GET /storage/health` (`getStorageHealth` → `{supported, available, activeJob, lastFailure, sessionCount, mainCount, childCount, scheduledCount, unknownCount, corruptCount, currentBytes, reclaimableBytes}`) with counts as decimal strings and the two byte figures `null` when the daemon marks them unavailable. Browser-only persistence in `localStorage`: `mecatl-studio-theme` (theme), the profile keys studio.profile.agent-avatar studio.profile.agent-name studio.profile.expand-details studio.profile.session-list-side studio.profile.show-tool-calls studio.profile.start-on studio.profile.ui-scale studio.profile.user-avatar studio.profile.user-name (already namespaced by the chat layer), and `studio.chat.models.disabled`. Malformed values are treated as defaults, never thrown on.
- **Security / authority:** Both routes are reads behind the bootstrap's `401` session gate when interactive login is active; there is no mutation, so no CSRF surface. The runtime-settings projection is an allowlist: only the fields named above leave the BFF, so a daemon `ServerInfo` or model row gaining a sensitive field cannot leak without a contract change. `server.info` is called only when the negotiated snapshot advertises the `server_info` feature; `models.list` only when `capabilities.modelSelection` is on; `storage.getHealth` only when `capabilities.storageHealth` is on — each read live, never cached at startup.
- **Compatibility / migration:** Additive on the BFF (two routes, regenerated OpenAPI and client). The web nav gains `Settings` after `Skills`, and the auth control (sign-out) that the bootstrap left unmounted is surfaced in the about section. The chat workspace's model control, deferred until an inventory exists, now receives the live inventory; no route or schema changes.

## In scope — 4 scenarios, in implementation order

### Scenario 1 — the runtime-settings route is a safe, capability-gated inventory

Provider configuration is operator-tier ([architecture](../architecture.md)); Studio displays what the daemon publishes and nothing it does not ([ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md)).

**Acceptance:**
- AC1.1: with model selection on, the route returns the build id, server implementation, provider display endpoint, every model row, and one provider row per provider sorted by id, with `management` fixed to the read-only flags and reasons; no field outside the contract leaves the BFF.
  - verify: vitest:apps/server/src/routes/settings.test.ts#cmV0dXJucyBvbmx5IHNhZmUgcnVudGltZSBhbmQgbW9kZWwgaW52ZW50b3J5 — `apps/server/src/routes/settings.test.ts :: "returns only safe runtime and model inventory"`
- AC1.2: a model whose provider reports no status row gets a synthesised provider row (`state: available`, counted from the models).
  - verify: vitest:apps/server/src/mecatl/settings.test.ts#c3ludGhlc2lzZXMgcHJvdmlkZXIgcm93cyBmb3IgbW9kZWxzIHdob3NlIHByb3ZpZGVyIHJlcG9ydHMgbm8gc3RhdHVz — `apps/server/src/mecatl/settings.test.ts :: "synthesises provider rows for models whose provider reports no status"`
- AC1.3: with model selection off the response carries `modelsSupported: false`, a reason, and empty inventories without calling `models.list`; without the `server_info` feature `server.info` is not called and the identity fields are empty with `serverImplementation: "unknown"`.
  - verify: vitest:apps/server/src/mecatl/settings.test.ts#c2tpcHMgdGhlIG1vZGVscyBjYWxsIGFuZCBzZXJ2ZXIgaW5mbyB3aGVuIHRoZSBjYXBhYmlsaXRpZXMgYXJlIG9mZg — `apps/server/src/mecatl/settings.test.ts :: "skips the models call and server info when the capabilities are off"`
- AC1.4: without a runtime the route answers `503` `runtime_unavailable`; with interactive login active and no session it answers `401`.
  - verify: vitest:apps/server/src/routes/settings.test.ts#YW5zd2VycyA1MDMgd2l0aG91dCBhIHJ1bnRpbWUgYW5kIDQwMSB3aXRob3V0IGEgc2Vzc2lvbg — `apps/server/src/routes/settings.test.ts :: "answers 503 without a runtime and 401 without a session"`

### Scenario 2 — storage health is honest about support and byte availability

The daemon's storage health is an operator capability; the BFF maps it without inventing figures ([ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md)).

**Acceptance:**
- AC2.1: with the capability off the response is `supported: false` with zero counts and `null` bytes, and the daemon is not called.
  - verify: vitest:apps/server/src/mecatl/storage.test.ts#cmVwb3J0cyB1bnN1cHBvcnRlZCB3aXRob3V0IGNhbGxpbmcgdGhlIGRhZW1vbiB3aGVuIHRoZSBjYXBhYmlsaXR5IGlzIG9mZg — `apps/server/src/mecatl/storage.test.ts :: "reports unsupported without calling the daemon when the capability is off"`
- AC2.2: `bigint` counts become decimal strings, `activeJob`/`lastFailure` derive from non-empty daemon strings, and each byte figure is `null` unless the daemon marks it available.
  - verify: vitest:apps/server/src/mecatl/storage.test.ts#bWFwcyB0aGUgU0RLJ3MgYmlnaW50IGNvdW50cyBhbmQgYnl0ZSBhdmFpbGFiaWxpdHkgZmxhZ3M — `apps/server/src/mecatl/storage.test.ts :: "maps the SDK's bigint counts and byte availability flags"`
- AC2.3: the supported flag is read on every call from the live snapshot.
  - verify: vitest:apps/server/src/mecatl/storage.test.ts#cmVhZHMgdGhlIHN1cHBvcnRlZCBmbGFnIGZyZXNobHkgb24gZXZlcnkgY2FsbCBmcm9tIGEgZnVuY3Rpb24gc291cmNl — `apps/server/src/mecatl/storage.test.ts :: "reads the supported flag freshly on every call from a function source"`
- AC2.4: the route serves the contract shape and answers `503` without a runtime.
  - verify: vitest:apps/server/src/routes/storage.test.ts#c2VydmVzIHN0b3JhZ2UgaGVhbHRoIGFuZCBhbnN3ZXJzIDUwMyB3aXRob3V0IGEgcnVudGltZQ — `apps/server/src/routes/storage.test.ts :: "serves storage health and answers 503 without a runtime"`

### Scenario 3 — the settings workspace renders eight sections from pure helpers

Browser-side summaries are pure functions with ported tests; components render from them ([ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md), decision 3).

**Acceptance:**
- AC3.1: the storage section derives a needs-attention/healthy state from availability, corrupt counts, a recent failed clean-up, and an active job, joins non-zero counts in a fixed order with singular forms, and formats bytes through KB/MB/GB.
  - verify: vitest:apps/web/src/features/settings/storage-settings.test.ts#cmVwb3J0cyBuZWVkcy1hdHRlbnRpb24gd2hlbiBzdG9yYWdlIGlzIHVuYXZhaWxhYmxl — `apps/web/src/features/settings/storage-settings.test.ts :: "reports needs-attention when storage is unavailable"`
  - verify: vitest:apps/web/src/features/settings/storage-settings.test.ts#am9pbnMgb25seSB0aGUgbm9uLXplcm8gY291bnRzLCBpbiBhIGZpeGVkIG9yZGVy — `apps/web/src/features/settings/storage-settings.test.ts :: "joins only the non-zero counts, in a fixed order"`
  - verify: vitest:apps/web/src/features/settings/storage-settings.test.ts#c2NhbGVzIHRocm91Z2ggS0IvTUIvR0I — `apps/web/src/features/settings/storage-settings.test.ts :: "scales through KB/MB/GB"`
- AC3.2: the avatar picker scales the longest edge of an uploaded image and never yields an empty canvas.
  - verify: vitest:apps/web/src/features/settings/avatar-utils.test.ts#cHJlc2VydmVzIHNtYWxsIGltYWdlcyBhbmQgc2NhbGVzIHRoZSBsb25nZXN0IGVkZ2U — `apps/web/src/features/settings/avatar-utils.test.ts :: "preserves small images and scales the longest edge"`
  - verify: vitest:apps/web/src/features/settings/avatar-utils.test.ts#bmV2ZXIgcmV0dXJucyBhbiBlbXB0eSBjYW52YXM — `apps/web/src/features/settings/avatar-utils.test.ts :: "never returns an empty canvas"`
- AC3.3: the models section lists providers and models from the runtime-settings inventory, lets the user disable models locally (`studio.chat.models.disabled`), and names each deployment-managed control (provider configuration, model routing) with the BFF's reason instead of offering it; the learning and memory sections reuse the knowledge layer's review and consolidation components; the about section hosts the sign-out control.
  - verify: inspection — the section components; `pnpm --filter @mecatl-studio/web typecheck` proves the generated query bindings.
  - verify: vitest:apps/web/src/features/settings/management-notes.test.ts#bmFtZXMgZWFjaCBkZXBsb3ltZW50LW1hbmFnZWQgY29udHJvbCB3aXRoIHRoZSByZWFzb24gdGhlIEJGRiBnaXZlcw — `apps/web/src/features/settings/management-notes.test.ts :: "names each deployment-managed control with the reason the BFF gives"`

### Scenario 4 — settings joins the shell and chat gets its model picker back

Navigation follows the earlier layers ([studio-chat](studio-chat.md); [ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md)).

**Acceptance:**
- AC4.1: the nav's fourth item is `Settings`; `/workspace/settings` opens the profile section and `/workspace/settings/{section}?item=` the named section; an unknown section redirects to the index.
  - verify: inspection — route files and `nav-items.ts`; the web typecheck proves the typed links.
- AC4.2: the chat and side-thread composers query `getRuntimeSettings` and offer the inventory minus locally disabled models, and image attachments follow the draft model's `image` flag again.
  - verify: inspection — `chat-workspace.tsx` and `side-thread-panel.tsx` diffs against the chat layer, where the control renders only when `models.length > 0`.
- AC4.3: `task studio:check` passes with the regenerated OpenAPI document and client committed; `task ac-trace` resolves every proof named here.
  - verify: inspection — the CI `studio` job.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Search, shortcuts reference page | later stack layers, one Bounded plan each | bootstrap plan's per-feature decision |
| Editing provider credentials or routing from the browser | never for this plan | management stays deployment-managed |
| Storage clean-up or migration actions | a later plan if the product asks | the prototype exposes health only |
| `user-docs/` pages for Studio | the top layer of the stack | bootstrap plan's documentation decision |

## Definition of done

1. `task studio:check`, `task lint:actions`, `task docs`, and `task ac-trace` pass.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `docs/architecture.md`'s Studio section names the settings surface and its safe-projection rule.
4. The implementation layer records the exact commit of this plan it built against.

## Deferred decisions and known risks

- The `about` section shows the build id and server implementation the daemon publishes; a daemon without the `server_info` feature shows `unknown`.
- Risk: the mock daemon may not list models; the live smoke then covers the degraded path and the SDK-fake proofs the supported one.
