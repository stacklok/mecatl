# Mecatl Studio settings routes and About — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — extends Studio's existing settings and authenticated BFF product API with section routes and a safe provider detail; ADR 0351 already fixes the browser, BFF, SDK, and daemon boundaries.
**Decision record:** None — the new presentation and read-only projections stay inside ADR 0351's existing ownership and trust decisions; they create no durable daemon or deployment architecture decision.
**Phase:** Studio design alignment, settings and reference
**Status:** in-progress, 2026-09-24. Directing-user choices recorded in the Plan / Interface PR; implementation is stacked for review.
**Delivery:** Split. The new BFF response and route compatibility need a separate Plan / Interface review before implementation.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#1846](https://github.com/stacklok/mecatl/issues/1846).
**Plan PR:** this Plan / Interface PR.
**Approved baseline:** absent until the plan PR merges.

Give every settings section a direct URL, preserve existing settings links, and show
the current principal's facts that Studio can actually read. Provider details and version
facts come through the BFF. Personal browser preferences remain editable; settings
owned by the deployment explain their owner and show no unbacked controls. The
implementation follows [ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md).

## Human decisions

- [x] Keep Memory in settings despite its omission from the twelve-section issue list. — Decision: the directing user chose Memory as a thirteenth section on 2026-09-24. Keep its list route and send old `?item=` links to the separate `/workspace/memory?item=` fact-detail route with the same encoded key; #1854 owns the later Memory presentation pass.
- [x] Identify the Studio image independently of the connected daemon. — Decision: the directing user chose a verified Studio build stamp on 2026-09-24. Inject the release tag into published BFF builds, show "Not reported" in local builds, and show the actual installed SDK version and daemon build ID separately. The current `0.0.0` package version is not a release identity.
- [x] Include an authenticated per-provider display endpoint. — Decision: the directing user chose a safe provider-detail read on 2026-09-24. Add `GET /api/v1/settings/provider?providerId=` with a required encoded ID, using the published SDK's `server.info({providerId})` only for caller-visible provider IDs, with a response allowlist and no configuration controls. A query parameter preserves SDK provider IDs containing `/`.
- [x] Preserve operator control of deployment settings. — Decision: #1846 and the [design review](https://github.com/stacklok/mecatl/issues/1779) require read-only agent behavior, permission posture, provider configuration, model routing/default, MCP setup, storage operation, and diagnostics settings until a separate write contract is approved. Existing personal browser preferences and approved learning decisions retain their current actions.
- [x] Keep the later feature tracks distinct. — Decision: #1851 owns MCP inventory for chat, #1854 owns Memory presentation, and #1856 owns the local Labs demo. This plan gives their settings destinations honest current states without inventing those features.

## Interface contract

The BFF adds `GET /api/v1/settings/provider?providerId=<ID>` with operation ID
`getProviderSettings`. The required, non-empty `providerId` query value is
percent-encoded in URLs and decoded once before exact inventory matching;
missing, empty, or repeated values get `400 invalid_request`. A `200` response has exactly
`{provider, models, displayEndpoint}`: `provider` uses the existing
`providerInventoryItemSchema`, `models` uses the existing
`modelInventoryItemSchema[]` filtered by the exact decoded provider ID, and
`displayEndpoint` is a sanitized display string or `null`. That string is the
SDK-validated diagnostic projection containing only scheme, host, optional
port, and escaped clean path; Studio renders it as text, never as a link. The BFF gets the provider and models
from the live, capability-gated `models.list` inventory and calls the published
SDK's `server.info({providerId})` only after the ID matches that inventory and
the `server_info` feature is advertised. The endpoint is display data, never a
connection instruction. When `server_info` is absent or its display endpoint is
empty, a known provider returns `200` with `displayEndpoint: null`. An unknown
ID returns `404 provider_not_found`; disabled
model selection returns `501 models_unsupported`; unavailable runtime returns
`503 runtime_unavailable`; an upstream `server.info` failure also gets that
generic `503` without forwarding the daemon error. The existing `GET /api/v1/settings/runtime` shape
remains compatible; its selector-free `providerEndpoint` is not presented as a
provider's endpoint.

The authenticated `GET /api/v1/runtime` gains optional `studioBuildId` and
`sdkVersion` strings for About. `studioBuildId` is the exact immutable release
tag baked into the BFF bundle in published images; local builds omit it and a
runtime environment override cannot change it. `sdkVersion`
comes from the installed published SDK package metadata and is absent if that
metadata cannot be read. The existing `/settings/runtime` `buildId` remains
the daemon build identity. #1845 owns the minimal anonymous status projection;
its runtime-schema edits and this authenticated response change require joint
review in `apps/contracts/src/schemas/runtime.ts`, `apps/server/src/app.ts`, and
generated OpenAPI/client output. Detailed runtime, build, provider, and model
facts remain session-gated in OIDC mode.

- **gRPC / protobuf:** None — existing published-SDK `models.list` and `server.info` calls provide the facts; no daemon message or method changes.
- **Exported Go APIs / interfaces:** None — Studio changes no Go package.
- **Tool schemas:** None — settings adds no model-facing tool.
- **CLI / config:** The `publish-studio` release job supplies its `VERSION` as a Docker build argument compiled into the BFF as `studioBuildId`; local builds omit it. No operator setting, daemon flag, or runtime write option is added.
- **Events / persistence:** No daemon event or persistence change. Existing browser keys for profile, appearance, and `studio.chat.models.disabled` retain their meanings; there is no provider, permission, storage, MCP, or diagnostics write. The provider-detail HTTP response is a read-only projection with no cache of credentials or configuration.
- **Security / authority:** In OIDC mode, every detailed BFF read uses the existing `/api/v1/*` session gate and request-scoped SDK credential; anonymous browsers get `401` and see only #1845's minimal public status. In static or no-auth mode, the existing ADR 0351 service-identity behavior applies: all browsers reaching Studio act as one principal, and the image requires the operator's `STUDIO_ALLOW_UNAUTHENTICATED=1` opt-in. The provider ID must match that principal's visible inventory before `server.info` runs. The BFF allowlists fields and never returns credentials, raw endpoints, environment values, or daemon errors. Personal model visibility affects this browser's pickers only.
- **Compatibility / migration:** Additive BFF route and optional authenticated-runtime fields, with generated contracts updated from source. The web route and redirect matrix below is the early-access browser URL migration; the old BFF settings-runtime response remains valid. Memory fact keys use a query parameter on the new detail page because valid keys contain `/`; legacy `?item=` links retain the exact key. #1845 reviews the runtime-schema intersection before either implementation merges.

## In scope — 5 scenarios, in implementation order

### Scenario 1 — settings sections and legacy links have stable destinations

The shared settings navigation uses these exact paths. The sources and owners
follow the current browser preferences, BFF routes, and operator configuration
boundary ([current settings route](../../apps/web/src/routes/workspace.settings_.$section.tsx),
[settings inventory](../../apps/contracts/src/schemas/settings.ts),
[runtime schema](../../apps/contracts/src/schemas/runtime.ts), and
[ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md)).

| Section | Direct route | Legacy route and resolution | Data source | Ownership |
|---|---|---|---|---|
| Profile | `/workspace/settings/profile` | `/workspace/settings/profile` stays; `/workspace/settings` redirects here. | Browser profile preferences; BFF `/api/v1/auth/session` for signed-in account facts when present. | Personal choices; identity claim is read-only. |
| Appearance | `/workspace/settings/appearance` | `/workspace/settings/appearance` stays. | Browser theme, palette, and interface preferences. | Personal. |
| Agent | `/workspace/settings/agent` | `/workspace/settings/agent` stays. | Browser agent name/avatar; BFF `/api/v1/runtime` for available agent behavior. | Personal display identity; deployment-managed behavior is read-only. |
| Permissions | `/workspace/settings/permissions` | New section. | BFF `/api/v1/runtime` `capabilities.posture`. | Deployment-managed, read-only. |
| Providers | `/workspace/settings/providers` | Provider cards move out of the old combined Models view. | BFF `/api/v1/settings/runtime` and provider detail route. | Deployment-managed, read-only. |
| Models | `/workspace/settings/models` | `/workspace/settings/models` stays; provider configuration moves to Providers. | BFF `/api/v1/settings/runtime` inventory; browser `studio.chat.models.disabled`. | Deployment-managed inventory, default, and routing; personal picker visibility only. |
| MCP tools | `/workspace/settings/mcp-tools` | New section. | BFF `/api/v1/runtime` MCP capability flags; no inventory until #1851. | Deployment-managed, read-only. |
| Storage | `/workspace/settings/storage` | `/workspace/settings/storage` stays. | BFF `/api/v1/storage/health`. | Deployment-managed, read-only. |
| Memory | `/workspace/settings/memory` | `/workspace/settings/memory` stays; `/workspace/settings/memory?item=<KEY>` redirects to `/workspace/memory?item=<KEY>`. | User-memory BFF reads. | Personal facts; store controls are deployment-managed. |
| Learning | `/workspace/settings/learning` | `/workspace/settings/learning` stays. | Learning-proposal and reflection BFF reads. | Personal, approved proposal decisions; learning configuration is deployment-managed. |
| Diagnostics | `/workspace/settings/diagnostics` | New section. | BFF `/api/v1/runtime`, `/api/v1/settings/runtime`, and storage health. | Deployment-managed, read-only. |
| Labs | `/workspace/settings/labs` | New section. | Product-defined availability text and BFF runtime capabilities. | Deployment-managed availability; no local demo switch before #1856. |
| About | `/workspace/settings/about` | `/workspace/settings/about` stays. | Runtime, settings-runtime, and auth-session BFF reads; static help links. | Deployment/build facts are read-only; sign-out uses the existing personal session action. |

The provider detail has the direct URL
`/workspace/provider?providerId=<PERCENT_ENCODED_ID>` and links back to
Providers. The Memory fact detail has the direct URL
`/workspace/memory?item=<PERCENT_ENCODED_KEY>` and links back to Memory. Its
required non-empty `item` value is decoded once and passed unchanged to the
existing user-memory BFF lookup; a missing or empty value replaces to the
Memory list. Both detail pages accept a reload as well as in-app navigation.
An unrecognized settings section redirects to Profile; it does not turn an
arbitrary path component into a settings capability.

**Acceptance:**

- AC1.1: every row opens by direct URL and browser reload, highlights its navigation entry, and remains reachable by desktop and mobile navigation; back and forward preserve the selected route.
  - verify: vitest:apps/web/src/features/settings/settings-routes.test.tsx#b3BlbnMgZWFjaCBzZXR0aW5ncyBzZWN0aW9uIGRpcmVjdGx5IGFuZCBwcmVzZXJ2ZXMgbmF2aWdhdGlvbg — planned route test; browser demonstration at desktop and mobile widths.
- AC1.2: `/workspace/settings` replaces to Profile; an old Memory `?item=` link replaces to the same fact key at `/workspace/memory?item=<PERCENT_ENCODED_KEY>`, including keys containing `/`; an invalid section replaces to Profile without rendering a false settings page.
  - verify: vitest:apps/web/src/features/settings/settings-routes.test.tsx#cmVkaXJlY3RzIG9sZCBtZW1vcnkgaXRlbSBVUkxzIHRvIGZhY3QgZGV0YWlscw — planned redirect test.
- AC1.3: a direct settings or detail URL in interactive-login mode returns to that URL after sign-in, without exposing the authenticated content to an anonymous browser.
  - verify: browser flow coordinated with #1845 and the existing [AuthGate](../../apps/web/src/features/auth/auth-gate.tsx); a route test covers the restored path.
- AC1.4: the Memory list links to `/workspace/memory?item=<PERCENT_ENCODED_KEY>`; that route renders the same fact value and history on direct load and reload, including after a legacy `?item=` redirect. A slash-containing key reaches the exact existing `/api/v1/user-memory/{memoryKey}` lookup. A missing or empty key returns to the Memory list.
  - verify: vitest:apps/web/src/features/settings/settings-routes.test.tsx#bG9hZHMgYSBtZW1vcnkgZmFjdCBhZnRlciBhIGxlZ2FjeSByZWRpcmVjdA — planned fact-route test; BFF route test with a percent-encoded slash in the key.
- AC1.5: a provider ID containing `/` round-trips exactly through the provider link, direct URL, browser reload, and BFF query; an unknown ID shows a provider-not-found state.
  - verify: vitest:apps/web/src/features/settings/settings-routes.test.tsx#cm91bmQgdHJpcHMgcHJvdmlkZXIgSURzIGNvbnRhaW5pbmcgc2xhc2hlcw — planned route test; BFF detail test uses the same ID.

### Scenario 2 — provider and model details are safe BFF facts

Provider status and model metadata come from the published SDK, while provider
configuration stays with the deployment ([provider architecture](../architecture/providers.md),
[existing settings service](../../apps/server/src/mecatl/settings.ts), and
[ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md)).

**Acceptance:**

- AC2.1: Providers lists the caller-visible provider rows and statuses; a direct provider detail loads its status, safe hint, count, related model rows, and its selected provider's sanitized display endpoint when the daemon reports one. Missing endpoint data is labeled unavailable.
  - verify: vitest:apps/server/src/mecatl/settings.test.ts#c2VydmVzIGEgc2FmZSBkZXRhaWwgZm9yIGEga25vd24gcHJvdmlkZXI — planned BFF test; a provider-page component test.
- AC2.2: with OIDC active, an anonymous detail request gets `401`; an unknown provider gets `404` without a provider-specific info call; disabled model selection gets `501`; an unready runtime gets `503`. Static/no-auth mode follows the existing shared-principal opt-in. No response exposes credentials, raw endpoint configuration, or extra SDK fields.
  - verify: vitest:apps/server/src/routes/settings.test.ts#ZG9lcyBub3QgZXhwb3NlIHByb3ZpZGVyIGRldGFpbHMgdG8gYW5vbnltb3VzIGNhbGxlcnM — planned auth and failure test with capability and unknown-ID cases.
- AC2.3: Models displays the BFF's ID, display name, provider, context limit, image, and reasoning facts. Its Visible control changes only `studio.chat.models.disabled`; default model and routing show their deployment owner without a selector or save action. Unsupported and empty inventories say which state applies.
  - verify: vitest:apps/web/src/features/settings/settings-workspace.test.tsx#a2VlcHMgbW9kZWwgdmlzaWJpbGl0eSBwZXJzb25hbCBhbmQgZGVmYXVsdHMgbWFuYWdlZA — planned Models component test; existing [model preference tests](../../apps/web/src/lib/model-preferences.test.ts).

### Scenario 3 — managed settings show facts without false controls

Studio can explain operator settings but cannot edit them through its current
BFF ([runtime schema](../../apps/contracts/src/schemas/runtime.ts),
[settings service](../../apps/server/src/mecatl/settings.ts), and
[ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md)).

**Acceptance:**

- AC3.1: Profile and Appearance preserve their current personal browser preferences. Agent distinguishes the personal display name/avatar from deployment-managed behavior. Learning keeps the approved proposal actions, while learning configuration remains read-only. The Memory list keeps its existing approved consolidation plan and apply/dismiss flow.
  - verify: vitest:apps/web/src/features/settings/settings-workspace.test.tsx#c2VwYXJhdGVzIHBlcnNvbmFsIGFnZW50IGlkZW50aXR5IGZyb20gbWFuYWdlZCBiZWhhdmlvcg — planned ownership component test; vitest:apps/web/src/features/settings/settings-workspace.test.tsx#a2VlcHMgbWVtb3J5IGNvbnNvbGlkYXRpb24gb24gdGhlIHNldHRpbmdzIGxpc3Q — planned Memory action test; [knowledge plan](studio-knowledge.md) fixes the approved action.
- AC3.2: Permissions, MCP tools, Storage, Diagnostics, and Labs state their source and management owner. They show only supported BFF facts, with no editable posture, MCP setup, storage cleanup, log/usage, or demo controls lacking a write or feature contract.
  - verify: vitest:apps/web/src/features/settings/settings-workspace.test.tsx#ZXhwbGFpbnMgbWFuYWdlZCBzZWN0aW9ucyB3aXRob3V0IHdyaXRlIGNvbnRyb2xz — planned read-only component test.
- AC3.3: loading, unsupported, empty, offline, and failed reads have distinct feedback. An offline page does not present cached deployment facts as current, and a missing capability does not produce a working-looking control.
  - verify: vitest:apps/web/src/features/settings/settings-workspace.test.tsx#ZGlzdGluZ3Vpc2hlcyBvZmZsaW5lIHVuc3VwcG9ydGVkIGFuZCBmYWlsZWQgc2V0dGluZ3Mgc3RhdGVz — planned response-state test; browser offline demonstration.

### Scenario 4 — About and shortcuts report what Studio knows

About uses BFF facts under Studio's current auth mode. The shortcut reference
reads product-defined, read-only bindings from the browser registry and
deployment-managed feature availability from BFF runtime capabilities
([settings schema](../../apps/contracts/src/schemas/settings.ts),
[shortcut registry](../../apps/web/src/features/shortcuts/shortcut-registry.ts),
and [ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md)).

**Acceptance:**

- AC4.1: About identifies Studio, the installed SDK, and the connected daemon separately; shows `studioBuildId`, `sdkVersion`, daemon `buildId`, implementation, runtime source, connection, and deployment label only from BFF responses; and labels any absent version fact "Not reported". A copied support summary contains only these safe fields.
  - verify: vitest:apps/web/src/features/settings/about-page.test.tsx#c2hvd3MgdmVyc2lvbiBmYWN0cyBmcm9tIGF1dGhlbnRpY2F0ZWQgQkZGIHJlc3BvbnNlcw — planned About component test; BFF schema test for absent build metadata.
- AC4.2: About provides documentation, report-problem, keyboard-shortcuts, and sign-out paths. `/workspace/shortcuts` works by direct URL and shows current bindings from the shared registry and enabled help features from BFF runtime capabilities; an unavailable runtime has an explicit state.
  - verify: vitest:apps/web/src/features/shortcuts/shortcut-reference.test.tsx#cmVuZGVycyBzaG9ydGN1dHMgZnJvbSB0aGUgcmVnaXN0cnkgb24gZGlyZWN0IGxvYWQ — planned shortcut component test; existing [shortcut tests](../../apps/web/src/features/shortcuts/shortcut-registry.test.ts).
- AC4.3: a BFF bundle built with the release job's version reports exactly that version as `studioBuildId` through `/api/v1/runtime`, even when its runtime environment supplies a different value; a local bundle without a release stamp omits the field. The value is separate from the daemon's `buildId` and installed SDK's `sdkVersion`.
  - verify: a build-script/BFF test with and without an injected build argument; inspection of the `publish-studio` build argument in [release.yml](../../.github/workflows/release.yml) and the published image build in [Dockerfile](../../apps/Dockerfile).

### Scenario 5 — settings remains usable at desktop and mobile widths

The final layout consumes #1843's shared tokens and controls and #1844's shell
navigation; #1779 reviews the visual and interaction result
([design review](https://github.com/stacklok/mecatl/issues/1779),
[Studio shell](../../apps/web/src/components/shell/workspace-shell.tsx), and
[ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md)).

**Acceptance:**

- AC5.1: at desktop and below the 500px mobile pivot, every section and both detail routes have a visible way back, usable touch targets, keyboard focus, and a readable page without horizontal clipping.
  - verify: maintainer-reviewed screenshots at 1280px and 390px in light and dark themes, plus a keyboard and touch browser demonstration; visual inspection is required for layout and focus.
- AC5.2: the provider/model list, read-only explanations, About facts, and shortcut table keep their meaning on narrow screens. The selected section remains identifiable without relying on color alone.
  - verify: desktop/mobile screenshots and focused component accessibility assertions; `task studio:check` covers component and contract tests.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Daemon settings writes or restart/controller paths | A separately approved write contract | #1846 and #1779 keep deployment controls read-only. |
| Public detailed runtime data | #1845 | #1845 owns the minimal anonymous status contract; joint review covers shared schema edits. |
| MCP tool, prompt, and resource inventory | #1851 | This section reports capability/ownership only. |
| Memory store redesign and enable/disable | #1854 | This plan preserves Memory routing and existing fact reads. |
| Local Labs demo tour | #1856 | A route exists without an unbacked switch. |

## Definition of done

1. Focused BFF contract, route, and component tests pass; `task studio:check` passes with generated OpenAPI and client output committed.
2. `task docs`, `task site:build`, and applicable repository lint/race gates pass on the implementation candidate. `task ac-trace-strict` resolves named proofs when this plan becomes `landed`.
3. The implementation updates the owning public `user-docs/building/deployment/studio.md` page for shipped settings behavior. This plan PR does not describe proposed behavior as current behavior there.
4. #1779 records maintainer review of desktop/mobile screenshots, keyboard and touch flow, and light/dark focus and contrast. Final styling integrates after #1843; final shell navigation integrates after #1844.
5. The implementation PR cites this merged Plan / Interface PR and approved commit, reports interface conformance, and receives `/panel-review` with no unresolved ship blocker. Human review merges the PR.

## Deferred decisions and known risks

- #1845 and #1846 may edit the authenticated runtime schema concurrently. Joint review must preserve #1845's minimal anonymous status boundary and this plan's authenticated About facts.
- An older daemon can omit `server_info` or model selection. The BFF keeps the endpoint absent and the UI shows the capability state; it does not infer provider or build facts from the browser or deployment URL.
