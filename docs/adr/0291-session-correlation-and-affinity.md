# ADR 0291 — End-to-end session correlation and affinity

- Status: Proposed
- Date: 2026-09-02
- Scope: session-bound gRPC and HTTP ingress, mecak8s ownership and lease-loss handling, mecatui and TypeScript SDK clients, and outbound LLM-provider requests
- Supersedes: ADR 0216
- Superseded by: none

## Context

ADR 0216 introduced `X-Mecatl-Session-ID` as optional outbound provider metadata,
bound from the authoritative engine-run context. That lets provider logs correlate an
inference request with a durable mecatl session, but it does not let a gateway route a
session-bound client request back to the replica that owns the session lease. A
multi-replica mecak8s deployment therefore has a gap between client ingress,
application-level single-writer ownership, and provider correlation.

Affinity also cannot substitute for fencing. A gateway can race endpoint changes, a
pod can die without releasing its lease, and an already-running stale owner can retain
in-memory state after losing renewal. Blindly forwarding an ingress header to a
provider would additionally let untrusted transport metadata override the actual run
identity.

The contract must remain compatible with existing clients that send no header and with
mecatui's existing `OpenConverse(ctx)` API. It must not require a protobuf change, and
mecak8s's Helm chart must remain neutral about the operator's Gateway API
implementation.

## Decision

Use the proprietary `X-Mecatl-Session-ID` field as one exact byte-for-byte
client→gateway→mecak8s→provider correlation and affinity contract.

Define the header name, a 256-byte maximum, and its legal-value predicate once in the
stdlib-only root transport package `contracts/sessionaffinity`. A legal cross-transport affinity value is
non-empty printable ASCII (`0x20`–`0x7e`) with no leading or trailing space, so gRPC
metadata and browser `Headers` can both carry it byte-for-byte without normalization.
It is never trimmed, decoded, encoded, case-folded, truncated, or otherwise normalized.
A durable external session ID outside that set or longer than 256 bytes remains usable by high-level clients
when it was supplied by the server and no explicit affinity bind was requested; those
calls omit the field. An explicit TypeScript `withSessionAffinity` bind rejects such a
value synchronously with an actionable error without changing caller headers. Automatic
high-level propagation instead omits affinity and removes any stale affinity header while
preserving unrelated caller headers. Root server and client consumers use that exported
contract.

The independently versioned provider submodules continue to require the released
standalone `engine` v0.12.0 under `GOWORK=off`. ADR 0093 forbids local `replace`
directives, so provider production code remains private and release-independent rather
than importing the root transport package. The OpenAI Responses, OpenAI Chat Completions,
and Anthropic production adapters therefore retain their existing private header
constants and legal-value validators in this PR, preserving ADR 0216 byte behavior.
One repository-owned vector fixture records both the exact cross-transport decisions and
explicit outbound-provider compatibility exceptions. Engine and provider suites consume
those respective expectations, so the broader private rules cannot silently become the
official ingress/client contract. Preserve ADR 0216's outbound behavior: providers derive the value from the
authoritative run-bound context, attach it as a per-request option on every attempt and
fallback, and omit it without failing inference when that context is absent or illegal.
Per-request options must not mutate a shared provider client: a race-enabled concurrent
proof shares one client between two sessions while interleaving their initial attempts,
retries, and provider-specific fallbacks, and verifies that every outbound request
carries only its originating run-bound session ID. They never blindly forward transport
ingress metadata.

At every session-bound gRPC unary and server-streaming entry, accept an absent header
for compatibility. Reject duplicate values, an illegal value, or a value that differs
byte-for-byte from the request's authoritative session ID. `CreateSession` derives that
authoritative ID only when exactly one of `source_session_id` or
`debug_target_session_id` is present and rejects the ambiguous dual-reference shape. `Converse` validates
metadata before stream work begins and validates equality with the first prompt or
retry frame's session ID; later control frames remain bound to that established
session. HTTP session routes apply the same rules against the decoded path ID. Failure
uses the transport's ordinary invalid-argument response and never reflects either
value. No protobuf field is added.

Official clients propagate the header automatically for every session-bound operation
and control. mecatui gains an additive session-bound converse opener while the existing
`OpenConverse(ctx)` remains source-compatible. The TypeScript SDK's high-level session
and run APIs attach the field automatically on both transports; additive raw helpers
allow an explicit session binding without changing generated protobuf types.

Treat affinity as an optimization only. The header is not authentication,
authorization, caller ownership, lease ownership, a fencing token, tracing,
idempotency, cache identity, or provider conversation state. Caller identity and
ownership checks remain independent and authoritative.

Every session mutation is serialized under session-scoped lease ownership when a
`SessionLease` is configured, including `SetMode` and other out-of-band snapshot, delete,
event-sidecar, tool-sidecar, and metadata-index mutations. The mutation inventory is
audited rather than limiting the rule to prompt entry. Lease renewal loss cancels the
owning run and locally invalidates its mutation capability before new application
mutations begin. Later local saves, deletes, appends, tool-call records, and
metadata/sidecar mutations are prevented. This is not backend fencing: an
already-started storage call may complete after lease loss, and no token-bearing
SessionStore, EventLog, ToolCallRecorder, sidecar, or Redis epoch protocol is introduced.

Lease loss while the local run is awaiting approval has a narrower settlement rule: stop
and invalidate that local run and retract its local permission-ask delivery, but neither
resolve, cancel, overwrite, nor destroy the already-durable awaiting snapshot or its
`PendingAsk`. The stale process has lost ownership and cannot approve, deny, or otherwise
resolve that ask. If local cancellation cannot settle the run, do not explicitly release
merely because it was cancelled; TTL/takeover remains the ownership transition. After
expiry, the successor reloads and resumes the exact persisted ask.

Leasing remains optional. With no `SessionLease`, this mutation hardening adds no lease
acquisition, invalidation, or changed admission/close/drain/awaiting behavior: the
non-leased path remains byte-identical. `ErrLeaseUnsupported` keeps its existing
sticky-disable diagnostic and fallback semantics rather than making leasing mandatory.

`CloseSession` returns `FailedPrecondition` while a local run is active or awaiting and
does not release its lease or tear down its engine, policy, or environment; the caller
must cancel or settle the local run first. A persisted awaiting session with no live
local run retains its durable `PendingAsk` resume point; closing it may release local
resources and its lease without destroying that state. Both gRPC and HTTP expose these
same semantics.

Graceful drain first stops admission. It preserves durable awaiting state, cancels and
joins executing runs, and explicitly releases a lease only after its run has joined. If
persistence fails, injected diagnostics report it and the previous durable state remains
authoritative; the design does not promise a save while Redis is blocked. If a run cannot
join before the shutdown bound, mecak8s does not explicitly release its lease: process
death and TTL govern takeover. Leases remain session-scoped; no run-scoped lease or
routing-derived ownership is introduced. Mecak8s gives Service drain, gRPC graceful stop,
HTTP shutdown, and final resource close separate positive operator flags. With defaults,
the complete sequential Kubernetes budget is 3s preStop propagation + 15s drain + 10s
gRPC + 5s HTTP + 5s close + 5s telemetry = 43s, strictly below the chart's configurable
`terminationGracePeriodSeconds` default of 60s. Operators who increase a component must
preserve that strict inequality.

On an ungracefully killed owner, the modeled test fixture drops its stream. Until lease
TTL expiry, a survivor cannot acquire ownership or start new application work. After
expiry, one survivor acquires the session lease, reloads authoritative Redis state,
repairs a crash-orphaned `running` snapshot through the existing run-entry abandonment
seam, and continues the session. The mecatl PR proves this with two Service instances,
fake-clock lease control, and transport fixtures; it does not claim that offline tests
prove Kubernetes EndpointSlice removal or real Envoy/Gateway routing. Those behaviors
are verified by the separate infrastructure PR and live rollout. Exactly-once external
tool or provider side effects are not claimed.

The mecak8s chart creates no `Gateway`, Route, `BackendTrafficPolicy`, certificate, or
general network-policy resource. Its guard test explicitly includes
`BackendTrafficPolicy`. The ordinary Kubernetes pod-spec `affinity` scheduling value
remains supported alongside topology spread, node selectors, and tolerations; it is not
a Gateway or session-affinity policy surface. The gateway affinity policy and its production rollout belong
to a separate infrastructure repository and PR. That rollout is blocked on a live
infrastructure acceptance item: authenticated admission plus request/header-size bounds
and client, IP, and principal rate limiting must apply before or independently of
session-affinity routing. Otherwise legal attacker-chosen session IDs can make an
unbounded targeted-replica sink, a resource-consumption concern covered by
[CWE-400](https://cwe.mitre.org/data/definitions/400.html) and
[OWASP API4:2023](https://owasp.org/API-Security/editions/2023/en/0xa4-unrestricted-resource-consumption/).
This is a Gateway/mesh responsibility, not a claim that the chart enforces it.

## Consequences

A cooperating gateway can keep a session on its current mecak8s owner while every
provider attempt carries the identical durable session identity. Existing clients and
custom providers remain compatible when the field is absent. One shared predicate and
negative tests make malformed or ambiguous metadata fail consistently without turning
session IDs into an oracle.

Correctness still depends on single-owner leasing and durable Redis state, not on
routing. Where leasing is configured, this broadens the lease audit to every
session-family mutation, but does not change `port.Lease.Token` into a Redis fencing
epoch or widen storage APIs with lease tokens. Local invalidation prevents new
application mutations after lease loss; an already-started storage operation may still
complete. An awaiting lease loss retracts only local ask delivery and leaves the durable
`PendingAsk` for TTL/takeover; the stale owner cannot resolve it. A future ADR and
acceptance plan must make any storage-level fencing decision, including its atomicity and
sidecar scope. Where leasing is absent or unsupported, existing optional-lease fallback
continues unchanged: `ErrLeaseUnsupported` sticky-disables leasing with the established
diagnostic and the non-leased behavior remains byte-identical.

Operators must supply and validate their own gateway policy. The mecak8s PR's offline
proof is intentionally modeled; EndpointSlice/Gateway behavior is an infrastructure
PR/live-rollout responsibility. Endpoint removal and TTL expiry create a visible
interruption during hard failure, and work performed after the last durable save may be
retried. External side effects remain at-least-once unless the external system supplies
its own idempotency contract.

Close and drain acquire explicit failure behavior: a live local run is not closed, a
persisted unattended awaiting session remains resumable, and an unjoined run retains
its lease for process-death/TTL takeover. A local awaiting run that loses renewal is
invalidated and its local ask is retracted without replacing its durable resume point;
only the successor that acquires after expiry may resume that exact ask. A failed
persistence attempt is diagnosed but does not replace the previously authoritative
durable state.

Provider production code intentionally retains its private ADR-0216 validator so each
provider module remains independently releasable. The shared vector fixtures and parity
and concurrent-isolation tests guard compatibility without coupling providers to the root
transport package.

Owner-to-owner live forwarding is deliberately deferred. If later required, it needs a
separate ADR and acceptance plan because it adds a new authenticated internal protocol,
lifecycle, and failure surface rather than refining affinity.

## See also

- [ADR 0216 — provider session correlation header](./0216-provider-session-correlation-header.md)
- [ADR 0027 — cloud-native arc](./0027-cloud-native.md)
- [ADR 0048 — mecak8s](./0048-mecak8s.md)
- [ADR 0212 — caller ownership enforcement](./0212-caller-ownership-enforcement.md)
- [ADR 0278 — mecak8s edge-terminated TLS](./0278-mecak8s-edge-terminated-tls.md)
- [Session affinity and handoff acceptance plan](../acceptance/session-affinity-and-handoff.md)
