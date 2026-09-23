# ADR 0351 — Provider-scoped model discovery ownership

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
open policy recommendations and their exact bounds.

## Decision

Use one Build-owned discovery owner in `internal/app`. Background work, explicit
ListModels/picker requests, run admission, and existing bootstrap probes request or join
its provider-local attempts. It is the sole publisher of accepted live observations.
Registry membership/default selection, protocol adapters, and credential custody remain
separate responsibilities; the owner borrows their existing listers.

Represent unattempted, in-flight, succeeded, empty, and failed explicitly. Keep latest
attempt outcome separate from retained successful observations. Catalogue/configuration
floors are not successful discovery. A first unknown-window demand starts a fetch;
concurrent demand joins it. Retry eligibility follows the actual provider attempt and
completion time, not whether some caller was previously rejected. Another provider's
completion or cooldown has no bearing on the selected provider.

Publish observations, outcome, admission evidence, and inventory/status projections as
one immutable snapshot. Accept a completion only for the current live attempt, and wake
waiters only after publication. A skipped provider contributes no update. Readers perform
pure exact-provider/model resolution with explicit provenance; they do not fabricate
readiness or trigger discovery. Wire responses use one captured projection, without
requiring instantaneous agreement across separate network requests.

The recommended policy is process-lifetime retention of positive last-good observations,
including after unauthorized or empty discovery, with the latest failure/empty outcome
still visible. That retention is metadata policy, not permission to infer: adapters still
authenticate each inference request. An unknown target cannot use stale successful omission
evidence after a failed/empty outcome. Global and exact configured windows, positive live
or catalog windows, no-lister fallback, and a healthy non-empty listing's omitted
passthrough model/window retain their defined precedence and admission behavior.

The owner bounds each ordinary attempt; cancelling a waiter cancels only its wait.
Shutdown cancels and joins shared work before borrowed resources close. Retries are
explicit-demand and provider-local, with no periodic refresher or durable metadata cache.
Native authenticated listing remains demand-only. Existing ToolHive/Codex bootstrap
selection and shorter bounds remain explicit exceptions, using the same owner rather
than independent metadata writers.

Keep the neutral server admission callback and engine interfaces. Admission must resolve
a context window or a permitted fallback before recording a prompt, preparing a failed-step
retry, consuming a restored approval, compacting, or inferring. Otherwise return the
existing retryable `context_window_unavailable` condition. Mecatui projects that structured
condition into safe recovery guidance while retaining the transcript and original draft.

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
limits or entitlements. Explicit picker refresh can perform more listing requests than
the old gateway-only stale path; the provider-local cooldown bounds that activity. Shared
work can outlive its final waiter until the attempt deadline. These trade-offs require the
plan's human decisions before acceptance.

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
