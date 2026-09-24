# ADR 0357 — Provider-scoped model discovery ownership

- Status: Proposed
- Date: 2026-09-23
- Scope: composition-owned model observations, discovery attempts, publication, and context-window admission
- Supersedes: [ADR 0342](./0342-context-window-admission.md) only for global settlement ownership, prior-rejection-based retry, and discovery-readiness policy. Its before-prompt/retry/approval/compaction/inference safety boundary and healthy omission fallback remain in force.

## Context

Native authenticated providers deliberately skip Build-time discovery. A global startup
settlement signal therefore says nothing about whether a selected native provider has
been attempted. The current admission path can reject its first prompt without listing,
then discover successfully on a later request. Opening the picker happens to prime the
same process, making a cold resumed chat behave differently from a freshly selected one.

The three publication paths also split metadata, attempt outcomes, and inventory across
owners. A publication lock serializes writes, but does not order the network attempts
that produced them. A late failed attempt can publish fallback over a newer successful
observation. Global cooldown and sequential unrelated refreshes couple providers that
have independent availability and context evidence.

The domain model gives [ProviderDiscovery](../architecture/mecatl.modelith.md#providerdiscovery)
one lifecycle per provider per host instance and separates
[ModelMetadata](../architecture/mecatl.modelith.md#modelmetadata) from resolution policy.
This decision establishes that ownership boundary in composition. The associated
[acceptance plan](../acceptance/provider-model-discovery.md#human-decisions) contains the
approved policies and their exact bounds.

## Decision

Use one Build-owned discovery owner in `internal/app`. Background work, ListModels
requests (including client startup and SDK calls), Service session-entry admission, and
existing bootstrap probes request or join its provider-local attempts. It is the sole
publisher of accepted live observations.
Registry membership/default selection, protocol adapters, and credential custody remain
separate responsibilities; the owner borrows their existing listers.

Ownership is per `app.Build`, normally one server process per replica. Embedded `mecatui`
uses its embedded server's owner; connected clients share the remote `mecated` owner's
knowledge. Each `mecak8s` replica has independent observations, outcomes, and cooldowns,
even when session/event/schedule storage is shared. Multiple Builds within one process
also remain independent. Evidence belongs to the configured registry and its borrowed
credential-backed listers, not to a cluster-global or caller-selected credential cache.
This maps the existing domain model's host-instance scope to deployment lifetimes without
changing the model.

Ordinary startup discovery warms eligible providers; existing bootstrap requirements
remain unchanged. Correctness at the covered Service gates depends on admission-time
resolution: the replica that passes session ownership and lease checks obtains its own
required evidence on demand. A cold replacement needs neither a picker visit nor another
replica's completed discovery. Different replicas can admit differently when their evidence
differs, including warm last-good admission versus cold rejection during a listing outage.
The coherence guarantee applies to the same target and evidence/configuration basis.

Represent unattempted, in-flight, succeeded, empty, and failed explicitly. Keep latest
attempt outcome separate from retained successful observations. Catalogue/configuration
floors are not successful discovery. A first unknown-window demand starts a fetch;
concurrent demand joins it. Retry eligibility follows the actual provider attempt and
completion time, not whether some caller was previously rejected. Another provider's
completion or cooldown has no bearing on the selected provider.

Publish observations, outcome, evidence, and inventory/status as one immutable snapshot.
For a valid accepted result, perform registry-owned default healing against candidate
metadata outside the publication lock, capture its default-selection facts, then atomically
publish and wake waiters. Serialize local completion tails with each other and shutdown/timeout arbitration;
check identity/liveness before effects and commit. Candidate projection/capability helpers
read their explicit candidate/default inputs, not the previous published store. Preserve
existing bootstrap/default remint without rebuilding session capabilities. Skipped providers
contribute no delta. Defensive copies at lister ingestion and the public projection boundary
prevent consumers from mutating the owner through shared slices, maps, or messages.

Readers perform pure exact-target resolution. Schedule selector validation uses the same
pure inventory reader rather than a separately seeded Service atomic. Wire responses use
one captured projection; separate network requests need not see simultaneous updates.
A wired context echo resolver's zero overrides a seeded positive value; nil remains distinct.
Codex's lister leaves missing live context absent: the existing OpenAI metadata namespace
supplies a catalog-sourced window during pure resolution, preserving entitlement membership
and display without inventing live evidence. Reasoning tri-state remains outside this slice.

The approved policy is process-lifetime retention of positive last-good observations,
including after unauthorized or empty discovery, with the latest failure/empty outcome
still visible. That retention is metadata policy, not permission to infer: adapters still
authenticate each inference request. An unknown target cannot use stale successful omission
evidence after a failed/empty outcome. Global and exact configured windows, positive live
or catalog windows, no-lister fallback, and a healthy non-empty listing's omitted
passthrough model/window retain their defined precedence and admission behavior.

The owner arbitrates one terminal outcome. Success requires a current live attempt and
valid fetch context at acceptance; a reserved valid result completes its local publication
tail without a timer replacing it mid-tail. Otherwise the deadline cancels the fetch,
publishes timeout once, and wakes waiters even before the lister returns. Cooldown starts
at that owner terminal-publication timestamp, not waiter exit or later fetch return. The
slot remains occupied until return; only then, after cooldown, can another attempt start.
Late success is discarded. This prevents overlap rather than adding a replacement-attempt
API. Cancelling a waiter affects only its wait. Owner shutdown invalidates and cancels/joins
work without publishing a failure or timeout from shutdown cancellation. Publication and
waiter notification precede diagnostic delivery. Physical cleanup requires listers to honor
cancellation and synchronous diagnostics delivery to return; attempt and admission deadlines
do not bound arbitrary collaborator cleanup. Deployment drain rejects/cancels run admissions
without itself closing discovery. `Built.Close` stops discovery before closing borrowed
credential and transport resources.

Retries are ListModels/admission-demand and provider-local, with no periodic refresher or
durable cache. ListModels is not a human-action signal. Native authenticated listing remains
demand-only. ToolHive/Codex bootstrap selection and shorter bounds remain explicit
exceptions using the same owner.

Keep the neutral server callback and engine interfaces. At the named Service session-entry
paths, admission resolves a context window or permitted fallback before recording a prompt,
preparing a failed-step retry, consuming a restored approval, compacting, or inferring;
otherwise it returns `context_window_unavailable`. Direct delegated, utility, and team entry
remain a separate slice: a known parent can bypass discovery while an unknown child override
still runs with the engine's 128000 floor. This is not the entire metadata-before-execution
invariant.

Session leasing remains separate from discovery. A metadata rejection retains the existing
session-lifetime lease; another replica can fail ownership acquisition before discovery
admission. This decision adds no lease-holder routing, transparent takeover, or guarantee
that Retry succeeds on an arbitrary replica. Mecak8s readiness remains drain/storage
readiness, not model admissibility or inference health. Schedule validation reads the
handling replica's inventory; a fire encounters the executing replica's Service admission.
Scheduler leadership does not share discovery knowledge. See the
[deployment ownership contract](../acceptance/provider-model-discovery.md#local-and-replicated-deployment-ownership)
for the local/cloud mapping and cross-Build proofs.

Mecatui maps the structured rejection to safe guidance. Retain one bounded pre-SessionInit
submission with exact prepared text/media and editable staged paste/image state. Explicit
Retry sends that prepared payload without re-expansion or automatic replay; Back restores
editing state, requiring confirmation before replacing a newer draft. SessionInit, explicit
disposal, session replacement/exit, or unrelated terminal error releases retention. Limits,
wire/persistence, and unrelated transport/auth recovery stay unchanged.

This decision does not persist effective model identity or alter floating-selector restart
semantics. It establishes publication and context resolution coherence, not generation-bound
capability/effort reconstruction. Already-built engines can retain construction-time
capabilities until a separate factory-ordering change; the plan names that residual
rather than implying discovery remints all session collaborators.

## Consequences

Cold resume no longer depends on picker activity or a sacrificial first rejection.
Discovery state becomes auditable in one place, and each provider has bounded independent
work. The implementation removes the global settlement channel, prior-rejection map,
global cooldown, and independent outcome/inventory publishers instead of retaining them
behind a new facade.

Process-lifetime last-good metadata can age and no longer describe the provider's current
limits or entitlements. Listing traffic and cooldowns are per Build, so adding replicas
can increase provider traffic. ListModels demand can perform more listing requests than
the old gateway-only stale path; provider-local cooldown bounds each owner's activity.
The deadline bounds waiter outcomes, while physical cleanup depends on cooperative listers
and returning diagnostics calls. A rejected-submission
record retains one bounded text/media payload in memory until recovery or disposal. These
trade-offs are recorded in the plan's resolved human decisions.

There is no durable migration. A cold process reacquires metadata, and a discovery outage
can still reject a run without losing its draft or recording a turn. Credential ownership
and deployment-wide authority from ADRs 0329 and 0333 remain unchanged. The implementation
must inventory the owner, attempts, and immutable snapshot in ADR 0027's resource and
restart-fidelity ledgers when they ship.

## See also

- [Acceptance plan](../acceptance/provider-model-discovery.md)
- [Provider architecture](../architecture/providers.md)
- [ADR 0342](./0342-context-window-admission.md)
- [ADR 0329](./0329-native-llm-endpoint-gateway-credentials.md)
- [ADR 0333](./0333-unified-provider-configuration-and-mecatui-provider-commands.md)
- [ADR 0027](./0027-cloud-native.md)
