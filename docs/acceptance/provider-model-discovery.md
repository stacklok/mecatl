# Provider-scoped model discovery - acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — establishes one publication owner and provider-local readiness, retry, and freshness rules across composition, run admission, and inventory projections.
**Decision record:** [ADR 0353](../adr/0353-provider-scoped-model-discovery.md)
**Phase:** Discovery ownership and context-safe run admission
**Status:** proposed, 2026-09-23. All six human decisions are resolved; the plan PR remains unmerged.
**Delivery:** Split. Review the discovery and admission contract before implementation.
**Expected tasks:** deferred to orchestration
**Related PR:** [PR 1800](https://github.com/stacklok/mecatl/pull/1800), the domain-model prerequisite.

A cold resumed session's first prompt starts or joins discovery for its selected provider,
then runs with the resolved context window. Opening the model picker first is unnecessary.
Background refresh, picker refresh, and admission share one composition-owned discovery
owner instead of coordinating through global settlement and prior-rejection flags.

This plan is stacked on `docs/provider-discovery-model` while PR 1800 is open. It implements
[ProviderDiscovery](../architecture/mecatl.modelith.md#providerdiscovery) and context admission
at the named Service session-entry paths, not full enforcement of the
[resolution invariants](../architecture/mecatl.modelith.md#invariants), using
[ModelMetadata](../architecture/mecatl.modelith.md#modelmetadata) as the distinction between
observations and resolved fallbacks. It does not change the domain-model documents.
The contract below specifies the policies approved by the directing human in Human decisions.

## Human decisions

- [x] Approve the single-owner replacement and internal interfaces below, including a read-only, combined inventory/status snapshot. — Decision: replace the existing writers in one implementation, including bootstrap producers, rather than retain a second publication path.
- [x] Approve last-good usability. — Decision: retain the last non-empty successful observations for the lifetime of this Build after failures, unauthorized responses, or empty responses; positive exact metadata remains usable without an age TTL. Keep the latest outcome truthful. A failed/empty latest attempt cannot authorize an unknown model's omission fallback. Discovery is metadata evidence, not an inference authorization check; inference still authenticates normally. This accepts possibly stale positive windows after an upstream change, not a guarantee of current entitlement.
- [x] Approve shared-request cancellation. — Decision: the owner, not the first caller, owns the fetch context. Cancelling any or all waiters leaves the bounded fetch running for publication; Build shutdown cancels and joins it. A caller's cancellation ends only its wait and never counts as a provider failure.
- [x] Approve request and retry bounds. — Decision: ten seconds per ordinary provider attempt and per admission/ListModels operation; ten-second per-provider cooldown from owner-published terminal outcome, including timeout before the lister returns. Reuse the slot only after return; no sleeps, overlapping attempts, or automatic retry loop. Unattempted demand starts immediately, in-flight demand joins, and failed/empty demand retries on the next eligible request. Any ListModels demand, including client startup or SDK calls, can refresh a healthy provider after cooldown. Startup is one-shot; native authenticated providers remain demand-only. Preserve ToolHive 1.5-second and Codex five-second bootstrap budgets/default selection. This replaces two-second stale fetches and the global cooldown, and extends recovery to every available lister provider.
- [x] Approve the freshness and admission boundary. — Decision: ship coherent publication and pure context resolution at named Service session-entry gates; preserve resolve-at-use windows. Direct delegated, utility, and team engine entry remains outside the gate: a known parent can bypass discovery while an unknown child override still uses the 128000 floor. Defer that admission slice without adding engine APIs. Also defer generation-bound capability/effort reconstruction, factory reordering, reasoning-field presence, and UI identity/footer reconciliation. Authoritative server echo zero is included here. No periodic refresh, durable cache, or persisted effective-model identity is added.
- [x] Approve complete rejected-submission recovery. — Decision: retain one bounded prepared text/media payload and its editable draft/staged paste-image state until SessionInit or explicit disposal. For a typed pre-SessionInit admission rejection, explicit Retry sends the identical prepared payload without re-expansion; Back restores editing state (confirm replacement if a newer draft exists). Preserve current limits and unrelated transport/auth recovery; retain no durable submission or automatic replay.

## Interface contract

- **gRPC / protobuf:** No new fields, RPCs, or reason codes. Preserve `ListModelsResponse.models` and `provider_status`, `ResolvedModel.context_window`, and the existing `context_window_unavailable` ErrorInfo reason in domain `mecatl.stacklok.com` with gRPC `Unavailable` and HTTP 503. Both ListModels transports assemble their existing response from one host snapshot. An unknown blocked window echoes 0; a policy-admitted unknown window echoes 128000. UI copy is selected from the structured reason, never raw server text.
- **Exported Go APIs / interfaces:** No changes to `engine/`, provider-module APIs, or `port.LLMRequest`. The host-internal `server.ModelInventory` becomes read-only: `CurrentModelSnapshot() ModelSnapshot`, where `server.ModelSnapshot` contains `Models []*mecatlv1.ModelInfo` and `ProviderStatus []*mecatlv1.ProviderStatus`. Add `Service.ListModelSnapshot(context.Context) ModelSnapshot` for the two wire handlers; retain `Service.ListModels` as a models-only projection for existing callers. Keep `server.Config.AwaitContextWindow func(context.Context, string, string) error` and `SetModelsRefresher(func(context.Context))`. The TUI client adds `IsContextWindowUnavailable(error) bool`, checking code/domain/reason exactly, so `ui` stays proto-free. No generic coordinator port is introduced. Schedule selector validation receives the same pure `ModelInventory` reader instead of the Service's separately seeded models atomic; standalone fixtures retain their nil-inventory fallback. Private UI retention reuses `client.MediaResult` and existing staged-attachment types, with no public RPC or engine type.
- **Tool schemas:** None — `DiscoverModels` keeps its existing exact provider/model filters, result limits, schema, and read-only behavior. It reads the owner's inventory projection without requesting discovery. Tool `Spec`, capability reads, and resolved-model reads remain free of discovery and credential I/O.
- **CLI / config:** None — retain global `--context-window-override`, exact provider/model entries under `models.context_windows`, and existing provider configuration. Bounds are internal constants, not new flags. No force-refresh, TTL, retry, or cache settings are added.
- **Events / persistence:** None — keep session selectors, snapshots, event taxonomy, and pending/retry records unchanged. Discovery state, attempt IDs, completion channels, and snapshots are Build-local and reset on restart. A rejected prompt creates no user-prompt event or persisted turn. Existing terminal-state recovery and session-lifetime lease retention remain as described in ADR 0342.
- **Security / authority:** Preserve deployment-scoped provider credentials and native demand-only authentication from ADRs 0329/0333. The owner borrows existing listers; it does not enroll, store tokens, forward caller bearers, broaden inventory authority, or treat a listing as an inference allowlist. Retain redirect refusal, bounded adapter responses, and safe provider-status projection. Expose neither raw listing errors nor endpoint/credential details through the new UI branch.
- **Compatibility / migration:** Replace only volatile host coordination; no durable data migration or operator config rewrite. Preserve exact provider/model identity, explicit selectors, floating default-selector restart semantics, passthrough selection, and provider-neutral engine construction. The change intentionally removes the first-demand rejection without a fetch, global retry interference, and unsupported provider exclusions from recovery. The combined inventory interface is an internal host change; migrate all its implementations/callers together. Retain unconfigured Service fixture fallbacks, but no compatibility writer may update the Build-owned inventory.

## Ownership and internal seams

Implement one private `providerDiscovery` in `internal/app`, created after provider
registration and before any bootstrap listing. Registry membership, adapter construction,
credential lifecycle, and default selection remain their existing owners. The discovery
owner borrows a frozen provider-to-lister set and owns only attempts and published metadata.
`Built.Close` and every later Build-failure cleanup cancel/join it before closing borrowed
credential/transport resources.

| Seam | Exact responsibility |
|---|---|
| `(*providerDiscovery).request(ctx context.Context, providerID string, reason discoveryReason) (*discoverySnapshot, error)` | Only effectful listing entry. Reasons are `bootstrap`, `startup`, `picker`, and `admission`. Uses provider-local attempt/cooldown state; returns after a joined attempt, a terminal/cooldown decision, or caller cancellation. The snapshot carries failed/empty outcomes; returned errors describe caller/owner cancellation, not raw provider bodies. |
| `(*providerDiscovery).snapshot() *discoverySnapshot` | Pure immutable read. Returns a view containing all provider states and the combined public projection. Consumers load once per resolution or response. |
| `(*providerDiscovery).Close()` | Idempotent stop: prohibit new attempts/publications, cancel active fetches, and join workers. |
| `resolveModelWindow(cfg Config, view *discoverySnapshot, providerID, modelID string) windowResolution` | Pure exact-target resolution. Result has `tokens int`, `source windowSource`, and `admissible bool`; sources distinguish global override, exact config, live observation, catalog, policy fallback, and unknown. No I/O, clock mutation, or admission bookkeeping. |

Each provider has `unattempted`, `in-flight`, `succeeded`, `empty`, or `failed` state;
a monotonically increasing attempt ID; start/terminal-publication times; a safe classified
latest outcome; retained observations/time; next eligible attempt time; and one fetch slot
with cancellation and outcome-ready signals. Outcome completion and fetch return are distinct:
a timed-out provider is `failed` even while its uncooperative lister still occupies the slot.
Empty means an actual zero-entry success, before adding catalog/configured floors.
No-lister eligibility is registry data, not fabricated discovery.

The owner arbitrates each terminal outcome once. Before accepting a successful return it
checks current attempt identity, liveness, and fetch-context/deadline validity. At the deadline
it cancels the fetch and publishes `failed(timeout)` independently of lister return, then
wakes waiters. A return processed at/after the deadline terminalizes as timeout even if
the timer callback has not run. `completedAt` is the injected clock time of terminal publication;
cooldown ends at `completedAt + 10s`. A late success/error cannot change that outcome or
restart cooldown. The slot becomes reusable only when the old lister has returned AND
cooldown has elapsed. Calls after timeout while the slot remains occupied return the
published outcome immediately. Shutdown instead invalidates attempts, cancels/joins work,
and wakes waiters with owner cancellation; it publishes no fabricated timeout/failure.
Supported listers must honor cancellation for bounded Close; an uncooperative fake is
released by the test after proving bounded waiter completion and absence of overlap.

For an accepted non-empty result, order the local completion tail as follows: accepted
attempt → registry-owned default healing using the candidate metadata, outside the
publication lock → capture resolved default-selection facts → build and atomically publish
the candidate projections → wake waiters. Serialize local completion tails with each other,
owner shutdown, and timeout terminalization; check identity/liveness before effects
and commit. Once a valid result is reserved, a later timer cannot replace its accepted
outcome mid-tail. No network runs in that tail. Keep the publication lock short and never
remint under it. The existing bootstrap/default remint consumes candidate capabilities;
this does not rebuild already-constructed session engines. Merge only the completed
provider's delta into the latest snapshot at commit; skipped providers contribute nothing.

`projectModelEntry`, its `modelCapability` input, and `providerStatusProto` must take the
candidate snapshot plus explicit registry/default-selection facts, not reread `reg.meta`,
`reg.outcomes`, or changing defaults. Default facts include provider/model and auto-selected
status; serialize capture with default healing so the completion response reports the heal.
Retain registry ownership of default selection without another metadata writer.
Internal snapshot maps/slices remain private read-only data. Deep-copy lister-owned entries
on acceptance; `CurrentModelSnapshot` is the defensive projection boundary and returns
detached slices and protobuf messages. Consumers cannot mutate owner state through a
returned model/status, modality slice, or map.

The owner stores live observations separately from catalog/configuration floors. A failure
or empty response retains last non-empty observations but changes the latest outcome.
A later non-empty success replaces that provider's observations, including removal of models
omitted from that response. For that success, picker membership uses the returned live list
plus the existing custom-provider configured-default merge; it does not append an entire
built-in catalog or invent Codex entitlements. On failure/empty, use the retained non-empty
list if present, otherwise the existing `providerInventoryFloor`. Retained rows resolve
live metadata before catalog fallback, and safe status visibility rules remain unchanged.
Floors never count as discovery evidence. `resolvedModelInventory`
becomes a read-only view of this snapshot, not another atomic writer. `liveMetaStore` field
accessors may remain pure views over it; their private snapshot and settlement state do not.
The Service's wire handlers read models and status together, rather than calling
`ListModels` and `ProviderStatuses` across separate publications. Schedule validation uses
that same pure inventory reader; it never requests discovery.

Remove the context-window catalog fill from `openAICodexLister.ListModels`. An absent live
`ContextLimit` stays absent until `resolveModelWindow` applies `catalogContextWindow` and
`metadataCatalogProviderID` (Codex → OpenAI metadata namespace). Keep entitled membership
and displayed resolved windows unchanged without labelling a catalog value as live or
assigning it a live observation time. Reasoning/modality enrichment is not redesigned here.

### Request policy and resolution

Existing constants establish the baseline: `liveModelRefreshTimeout` and
`refreshStaleModelsCooldown` are ten seconds, stale individual fetches are two seconds
in [modellister.go](../../internal/app/modellister.go), and bootstrap bounds are 1.5/five
seconds in [registry.go](../../internal/app/registry.go). The approved policy uses
one ten-second ordinary attempt budget instead of path-dependent two/ten-second fetches.
A joined attempt keeps its original deadline; no waiter extends it.

- **Bootstrap:** ToolHive protocol probes and Codex default discovery use the same owner
  with their existing shorter budgets and eligibility. Preserve ToolHive's parallel
  protocol probes, sole/default-empty failure, and unreachable-but-bootable behavior;
  preserve Codex entitlement selection/failure. Their accepted results need no later
  `bootstrapModels` re-publication.
- **Startup:** retain the existing one-shot delay policy. Request every eligible
  unattempted non-native lister independently; join any active demand instead of starting
  another fetch. Skip completed attempts, including completed bootstrap attempts. There
  is no startup-wide readiness bit or periodic work.
- **Picker/ListModels:** `picker` means any ListModels demand, including client startup
  and SDK requests, not proof of a human action. `SetModelsRefresher` requests available
  lister providers concurrently under one ten-second caller wait bound. Each provider
  starts/joins independently; one slow provider cannot delay another provider's publication
  or occupy its admission budget. Concurrency is at most one fetch per registered provider.
- **Admission:** resolve first, bypass I/O if admissible, otherwise request only the exact
  selected provider and resolve the returned snapshot. A cooldown rejection is immediate;
  a new attempt never chains another retry. Existing failure/empty outcomes are retried
  after cooldown for built-ins as well as gateways. Neither first rejection nor reads
  change retry eligibility. Caller cancellation returns that caller's context error;
  exhausting the internal admission wait returns `ErrContextWindowUnavailable` when
  resolution is still blocked, without cancelling a shared attempt.

| Evidence for the exact target, in precedence order | Context resolution and admission |
|---|---|
| Positive global override | Use it; no listing or wait. |
| Positive exact provider/model config | Use it; no listing or wait. Never match a model-only suffix. |
| Positive retained live context window | Use it even after failed/empty/latest unauthorized discovery; preserve live provenance and observation age. |
| Positive exact catalog window | Use it; no listing or wait. |
| No lister | Permit 128000 policy fallback immediately. |
| Latest completed non-empty success, selected model/window omitted | Permit 128000 policy fallback. Omission does not invalidate a passthrough selector. |
| Unattempted/in-flight, or latest failed/empty, without any positive value above | Start/join/retry as eligible; otherwise `ErrContextWindowUnavailable`. Echo remains 0. |

During an in-flight refresh, the preceding completed successful evidence remains usable;
starting an attempt alone does not revoke a previously admitted fallback. At completion,
failed/empty evidence cannot newly admit an unknown window. Engine closures remain
resolve-at-use and always return a positive scalar; their defensive 128000 floor is not
admission evidence. At the named Service session-entry gates, blocked targets cannot reach
prompt recording, retry preparation, approval execution, compaction, or inference. This
is not admission for direct delegated/utility engines or `RunTeam`: a known parent window
can bypass discovery while an unknown child model override still reaches the engine's
128000 floor. No engine API or run-generation pinning is added in this slice.

### Ownership migration and deletion

| Current owner/path | Replacement and deletion obligation |
|---|---|
| `startLiveModelRefresh`, `liveModelSnapshotForPublish`, `resolveToolhiveModelsForPublish`, `refreshStaleModels` in `internal/app/modellister.go` | Call the owner, then remove independent fetching/publication orchestration. Keep protocol listers and pure projection helpers. |
| `refreshStaleModelsState.last`, `.admitted`, `retryAdmission`; `liveMetaStore.completed`, `.settled`, `.settle`, `awaitRefresh`, `markRefreshCompleted` | Delete global settlement, prior-rejection history, and global cooldown. Replace with actual provider attempts and their completion channels. |
| `liveOutcomeStore`, `publishSnapshot`, registry `publishMu`, writable `liveMetaStore` snapshot, writable `resolvedModelInventory` | Transfer state/publication to the owner and delete duplicate writers/stores. Reuse pure field-resolution code; do not wrap the old coordination in a new facade. |
| `probeToolhive`, `bootstrapOpenAICodexDefault`, registry `bootstrapModels` | Route fetch/publication through the owner; retain registry default-choice policy and remove replayed bootstrap snapshots. |
| `build.go` refresh closure and `AwaitContextWindow` callback | Both close over the same owner. `awaitContextWindowWithin` becomes resolution plus selected-provider request, with no swapper or retry-state parameter. |
| `server.Service` inventory, HTTP/gRPC ListModels assembly, and `ScheduleManager` models pointer | Use the combined read-only snapshot for the wired Build, including `Service.New`'s manager binding and `validateScheduleSelector`. Keep only the standalone nil-inventory fixture fallback; no `SetModels` mirroring to a second atomic. Schedule reads remain pure and retain exact-selector validation/authority. |
| `projectModelEntry`, `modelCapability`, `providerStatusProto`, registry default healing | Pass candidate metadata and captured default facts explicitly; remove prior-publication rereads from candidate projection and bootstrap/default remint. Keep healing registry-owned outside the publication lock. |
| `openAICodexLister` context enrichment | Remove only its catalog-to-live `ContextLimit` fill; apply the existing metadata namespace mapping in pure context resolution. Preserve entitlement membership. |
| `Service.resolveModelWindow` | Assign a wired callback's zero as authoritative unknown, overriding a seeded positive default. A nil callback still retains the supplied value. This is server echo semantics, not deferred footer reconciliation. |
| `cmd/mecatui/client` classifier and `ui.failStartupRunEntry` | Preserve safe generic errors and transcript restoration. For typed admission rejection, replace text-only retry capture with the bounded prepared-submission retention below. |

Retain one private UI `admissionSubmission`, scoped to session ID and stream generation:
`draft, text string`, `media, pendingMedia client.MediaResult`, `staged map[string]stagedAttachment`,
`pastes map[string]string`, and original media/paste counters. Capture after existing
aggregate validation but before `submitPrompt` clears staging; `text/media` are the exact
prepared send, while `draft/pendingMedia/staged/pastes` restore editing. Own detached
bytes/maps (client-layer cloning handles Content messages); reuse existing representations
rather than a new transport schema. Current text/paste/media limits remain unchanged,
including 10 MiB per media part, 20 MiB aggregate media, and 16 parts.

Only typed admission rejection before SessionInit enables this record's Retry/Back path.
Retry sends the retained text/parts directly to the same session; it neither expands
placeholders nor rereads files/clipboard. Back restores editor text, pending media,
markers, attachment bytes, and counters; if a newer draft exists, require explicit
replacement confirmation and preserve both on cancellation. Back transfers ownership to
the editor and clears the recovery record. Successful SessionInit, explicit discard,
session replacement/exit, or an unrelated terminal error releases it; unrelated
transport/auth recovery keeps its current behavior. An explicit retry transfers the same
single record to the new stream generation; it does not accumulate copies or auto-replay.

## In scope — 5 scenarios, in implementation order

Proof names below are future offline implementation tests, not claims that tests already
exist. Use controlled fake listers, clock/deadline seams, mock inference, and channel
barriers rather than live providers or operator state.

### Scenario 1 — One provider attempt serves every request path

[Provider-local discovery](../architecture/mecatl.modelith.md#providerdiscovery) replaces the
coordination in [modellister.go](../../internal/app/modellister.go). Bootstrap/default
selection remains governed by [provider architecture](../architecture/providers.md).

**Acceptance:**
- AC1.1: Build makes no native authenticated listing. The first unknown-window native admission starts discovery immediately; simultaneous picker, admission, and eligible startup requests share exactly one fetch and observe its completed publication.
  - verify: `TestProviderModelDiscovery_Scenario1_FirstDemandAndSharedAttempt`
- AC1.2: A blocked provider A neither prevents provider B's first demand from starting nor consumes B's timeout/cooldown. Picker fan-out publishes B before A finishes; concurrent callers join rather than skip an active B attempt.
  - verify: `TestProviderModelDiscovery_Scenario1_ProviderIsolation`
- AC1.3: ToolHive and Codex bootstrap results pass through the owner, preserve existing selection/failure behavior and budgets, and do not produce duplicate startup listings. No-lister/mock configurations spawn no discovery workers.
  - verify: `TestProviderModelDiscovery_Scenario1_BootstrapAndNoLister`
- AC1.4: `Spec`, `DiscoverModels`, capabilities, and resolved-model reads make zero listing/credential calls and do not alter attempt state. Discovery availability accounts for demand-only native listers without starting them.
  - verify: `TestProviderModelDiscovery_Scenario1_PureReads`

### Scenario 2 — Publication preserves evidence and exact resolution

[ModelMetadata](../architecture/mecatl.modelith.md#modelmetadata) separates observations
from fallback policy. Migrate the [live field resolvers](../../internal/app/livemeta.go)
and [inventory projection](../../internal/app/agent_model_discovery.go) together.

**Acceptance:**
- AC2.1: A completion publishes observations, outcome, evidence, models, and safe statuses from the candidate, never the previous publication. The bootstrap/default-heal completion response already includes the accepted default and auto-selected status. Channel barriers expose no successful evidence before matching context metadata or mixed model/status generations in one HTTP/gRPC response; DiscoverModels and ListModels project identical captured rows. Mutating lister-owned or returned projection data cannot mutate owner state.
  - verify: `TestProviderModelDiscovery_Scenario2_CoherentPublication`, `TestProviderModelDiscovery_Scenario2_CandidateDefaultHealing`, `TestProviderModelDiscovery_Scenario2_ProjectionIsolation`
- AC2.2: Owner timeout publishes one failure and wakes waiters even while a held lister has not returned. Its late success is rejected; even after cooldown, no second fetch starts before that return. The next eligible demand then succeeds and publishes. Shutdown completions cannot publish. Another provider's delta and a skipped native provider preserve unrelated observations byte-for-byte.
  - verify: `TestProviderModelDiscovery_Scenario2_TimeoutAndLateReturn`, `TestProviderModelDiscovery_Scenario2_ShutdownAndSkippedPublications`
- AC2.3: Resolution follows the exact precedence table, distinguishes observed and fallback provenance, and isolates identical model IDs under different providers. Global/exact overrides and catalog-known windows admit with zero I/O even while discovery fails or hangs. No-lister and successful non-empty omission cases admit at 128000; failed/empty unknown cases remain blocked and echo 0.
  - verify: `TestProviderModelDiscovery_Scenario2_ResolutionMatrix`
- AC2.4: A success followed by failed, unauthorized, or empty discovery retains positive last-good metadata with its original observation time while reporting the new outcome. Unknown targets do not borrow old healthy omission evidence after those outcomes. A newer non-empty success replaces only its own provider observations, including changed windows and omitted rows.
  - verify: `TestProviderModelDiscovery_Scenario2_LastGoodAndLatestOutcome`
- AC2.5: A newly listed exact provider/model becomes valid for schedule creation; removal by a newer successful inventory makes that selector invalid, without `SetModels` or another writer. Schedule validation performs no listing, and standalone fixture fallback behavior remains intact.
  - verify: `TestProviderModelDiscovery_Scenario2_ScheduleInventoryReader`
- AC2.6: The real `openAICodexLister` wrapper over an offline entitled-model fixture leaves its omitted window absent. The resolver obtains that model's window through the OpenAI metadata namespace with source `catalog`, not a live value/timestamp. Inventory still displays the resolved window and advertises no unentitled catalog models.
  - verify: `TestProviderModelDiscovery_Scenario2_CodexCatalogProvenance`

### Scenario 3 — Cold resume admits before changing conversation state

Keep [ADR 0342](../adr/0342-context-window-admission.md)'s ordering guarantee at the
[Service run-entry gates](../../internal/adapter/server/service.go), after effective
provider/mode identity resolution. Replace the global-wait policy, not its safety boundary.
These are Service session-entry proofs, not proofs for direct child/utility `Engine.Run`
or [team execution](../../internal/adapter/server/team.go).

**Acceptance:**
- AC3.1: An offline two-Build cold-resume fixture with an explicit native provider/model and enough history to trigger false 128000-window compaction accepts the first prompt without ListModels. The fake lister returns 1050000; the first inference uses that window, preserves the genuine transcript, records the prompt once, and emits SessionInit rather than an admission error.
  - verify: `TestProviderModelDiscovery_Scenario3_ColdResumeFirstPrompt`
- AC3.2: Service prompt entry for new/default sessions, explicit selectors, mode-selected targets, failed-step retry, and restart approval resumption gates the actual effective identity. Failed admission records no new prompt, compaction, inference, retry preparation, or approval consumption on those paths; pending/retry data and genuine history survive. Existing reopen/history-repair and lease-retention semantics remain intact.
  - verify: `TestProviderModelDiscovery_Scenario3_ServiceRunEntryPaths`
- AC3.3: An unattempted provider cannot be declared ready by another provider's startup completion. A failed/empty first listing causes one truthful retryable rejection; after the provider-local cooldown, a successful second request admits without a picker visit, including for a built-in lister previously excluded from stale recovery.
  - verify: `TestProviderModelDiscovery_Scenario3_RecoveryWithoutPicker`
- AC3.4: Discovery neither rewrites durable selectors nor persists observation state. Explicit provider/model selectors survive restart unchanged; a zero selector retains existing floating-default behavior. Context engine/echo projections agree for the same admitted evidence without claiming construction-time capabilities were refreshed.
  - verify: `TestProviderModelDiscovery_Scenario3_IdentityAndContextProjection`
- AC3.5: A default/shared session seeded with a positive 128000 fallback echoes zero after the latest listing fails with no positive exact metadata, and admission rejects. The wired zero overrides the seed; a nil resolver retains the existing supplied value. No protobuf field or TUI footer change is required.
  - verify: `TestProviderModelDiscovery_Scenario3_AuthoritativeUnknownEcho`

### Scenario 4 — Cancellation and retries have one bounded owner

The [composition lifecycle](../../internal/app/build.go) owns shared work, separately from
[provider credential custody](../adr/0329-native-llm-endpoint-gateway-credentials.md).

**Acceptance:**
- AC4.1: Cancelling one waiter returns its context error promptly and leaves another waiter and the fetch intact. Cancelling all waiters still permits publication before the fixed attempt deadline. A late join cannot extend that deadline or create a second fetch.
  - verify: `TestProviderModelDiscovery_Scenario4_WaiterCancellation`
- AC4.2: Failure, empty response, unauthorized response, and timeout set cooldown from the owner's terminal-publication timestamp, never waiter exit or late lister return. A new attempt requires both cooldown expiry and a free fetch slot. Calls during either restriction do not fetch or sleep; provider B is independent of A. ListModels requests from picker, startup client, or SDK can refresh a healthy provider after cooldown; elapsed time alone performs no work.
  - verify: `TestProviderModelDiscovery_Scenario4_BoundedRetryPolicy`
- AC4.3: Build failure and normal Close cancel and join owner workers before closing borrowed resources. Shutdown publishes no success/failure evidence from cancelled work, wakes waiters, rejects new requests, and leaks no goroutines. A fresh Build starts without previous observations or cooldown state.
  - verify: `TestProviderModelDiscovery_Scenario4_CloseAndRestart`
- AC4.4: The final implementation has one listing/publication owner, with the migration table's coordination state and writable duplicates removed. The cloud-native inventory records owner, scope, cleanup, and reset-by-design fidelity for every retained resource.
  - verify: inspection — audit all production lister call sites and the migration table; compare ADR 0027 resource/fidelity rows with the implemented owner, because runtime tests alone cannot prove deletion or inventory completeness.

### Scenario 5 — Admission errors preserve the resumed chat and draft

The existing [startup failure reducer](../../cmd/mecatui/ui/update.go) has Retry/Back
controls and a [raw-error leak guard](../../cmd/mecatui/ui/session_continuity_scenario6_test.go).
Keep those protections while projecting [ADR 0342](../adr/0342-context-window-admission.md)'s
retryable admission condition rather than a transcript failure.

**Acceptance:**
- AC5.1: The client recognizes only gRPC Unavailable plus the exact ErrorInfo domain/reason. Missing details, wrong codes/domains, and arbitrary raw text containing `context_window_unavailable` do not enter the specialized branch. The existing HTTP 503 reason remains unchanged.
  - verify: `TestProviderModelDiscovery_Scenario5_TypedAdmissionError`
- AC5.2: A resumed first prompt rejected before SessionInit displays fixed bounded copy explaining that model context metadata is unavailable and execution has not started, with restore-discovery/exact-window-configuration and Retry/Back guidance. It does not describe the transcript as unavailable or print server/provider error text, URLs, paths, credentials, or untrusted override examples.
  - verify: `TestProviderModelDiscovery_Scenario5_TruthfulSafeStartupError`
- AC5.3: Plain text, staged paste, image-only, and mixed text/paste/image submissions retain the adopted transcript, session binding, exact prepared text/parts, and editable draft/staging after a typed pre-SessionInit rejection. Explicit Retry sends byte-identical content without placeholder re-expansion, file/clipboard reads, or auto-replay. Back restores text, markers, bytes, pending media, and counters; a newer draft requires replacement confirmation and remains intact on cancellation. Successful retry records one prompt and no duplicate transcript row.
  - verify: `TestProviderModelDiscovery_Scenario5_TranscriptDraftAndExplicitRetry`, `TestProviderModelDiscovery_Scenario5_PasteImageAndMixedRecovery`
- AC5.4: Ordinary new-session admission rejection offers the same bounded payload recovery. Capture respects current text/media validation and aggregate limits. SessionInit, explicit discard, session replacement/exit, and unrelated terminal errors release retained payloads; retries keep one record scoped to the active submission/generation. Unrelated startup/transport/auth failures retain existing handling, including the tenant-path leak guard.
  - verify: `TestProviderModelDiscovery_Scenario5_NewSessionAndUnrelatedErrors`, `TestProviderModelDiscovery_Scenario5_RetentionBoundsAndCleanup`, `TestADR_0108_FirstPromptRevalidatesAtomically`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Direct delegated, utility, and team admission | Separate admission slice | [Child `Engine.Run`](../../engine/agent/subagent.go) and [RunTeam](../../internal/adapter/server/team.go) bypass Service session-entry gating. A known-window parent can run without discovery while an unknown child override uses 128000. This slice does not establish the entire metadata-before-execution invariant or add engine APIs. |
| Rebuild frozen capabilities/effort after admission; bind every collaborator to a metadata generation | Separate capability/factory ordering slice | This contract fixes context admission and publication, not full `model-resolution-coherent` realization. `sessionEngineFactoryWithTools` still computes capabilities before the later Service gate. No clone-and-swap or incidental MCP reconnection is permitted here. |
| Reasoning supported/unsupported/unknown and per-field presence across adapters | Separate metadata representation slice | Preserve existing modality/thinking presence semantics; do not present the existing reasoning bool as a completed tri-state model. |
| UI provider-plus-model matching, decreasing context windows, zero-only footer healing | Separate projection reconciliation slice | Independent of typed pre-SessionInit rejection and the cold-resume context-safety proof. |
| Persist effective model identity or change floating/default selectors | Separate identity decision | Discovery changes knowledge, not session selection/restart semantics. |
| Periodic discovery, disk cache, cross-replica coordination, new operator controls | No addition in this slice | Explicit demand and existing one-shot/bootstrap work suffice. Cold restart reacquires metadata. |
| Registry/adapter/credential-manager redesign or engine provider knobs | Existing owners retained | Borrow listers and keep host policy out of engine/provider-neutral ports. |

## Definition of done

1. Every scenario has its offline proof, including deterministic race/cancellation coverage under `-race`; `task ac-trace-strict` resolves every proof when the plan becomes `landed`.
2. The ownership migration is complete in the implementation PR, with no parallel settlement, retry, outcome, or inventory writer left on the Build path.
3. `task lint`, `task test`, `task test:race`, `task api:check`, `task docs`, and `go run ./cmd/mecademo` pass. The engine API snapshots stay unchanged.
4. Update the living [provider architecture](../architecture/providers.md) and [context and compaction guide](../architecture/context-and-compaction.md); update ADR 0027's resource inventory and fidelity ledger for the discovery owner and retained UI submission when they actually exist. Record shipped/deferred scope only in PRODUCTION-READINESS. Preserve accepted ADR 0342 decision text.
5. In the implementation PR, update the owning [model-selection guide](https://mecatl.dev/docs/features/choose-models) (`user-docs/features/choose-models.md`) and [deployment guidance](https://mecatl.dev/docs/building/deployment/mecated) (`user-docs/building/deployment/mecated.md`) for first-demand/retry behavior and safe recovery, following `user-docs/_README.md`; run `task site:build`. This plan PR publishes no changed runtime instructions.
6. The implementation PR links the approved Plan / Interface PR and commit, reports interface conformance, and has no unwaived ship blockers from `/panel-review`.

## Deferred decisions and known risks

No material policy choice is delegated to an implementation worker; all six choices
in Human decisions are resolved. Residuals include construction-time capability/effort staleness and
ungated direct child/utility/team entry. Retaining positive observations for a whole Build
accepts metadata age risk after an upstream change; discovery cannot prove authorization
or guarantee the provider's real inference limit. The deadline bounds waiter outcomes,
not a noncooperative lister's physical return; cancellation/join contracts remain essential.
The single rejected-submission record holds bounded text/media only until recovery or
cleanup. Future model generations or age policy require a separate contract, not extra
conditionals in read-side helpers.
