# Initial production MCP broker — acceptance/design plan

**Phase:** production topology slice after session-scoped MCP authorization
**Status:** landed (2026-09-06)
**Plan branch / HEAD:** `acc/initial-production-mcp-broker` at `b245a14d1`
**Accumulator:** `acc/initial-production-mcp-broker` (current harness-owned-native integration worktree).

This plan is the committed acceptance contract for the orchestration that follows. It does
**not** claim restart continuity, HA, ownership, distributed fences, or exactly-once
upstream effects.

The plan is scenario-first: each scenario describes a running demonstration, retains its
AC IDs and proof names, then gives the minimum implementation contract needed to make
that demonstration unambiguous.

## Decision and scope closure

- The application seam remains [`internal/mcpbroker/broker.go`](../../internal/mcpbroker/broker.go)
  (`Service` and `Attachment`), not an engine API. `Runtime.Binding()` in
  [`internal/adapter/mcpbroker/runtime.go`](../../internal/adapter/mcpbroker/runtime.go)
  is the existing opaque logical-incarnation value and remains the persisted
  `session.ExternalBinding`.
- Add the versioned `mecatl.broker.v1` gRPC protocol under
  `contracts/proto/mecatl/broker/v1/` and a separate
  `internal/adapter/mcpbrokergrpc/` client/server adapter. It wraps the neutral seam;
  it must not import, expose, or become part of the ToolHive runtime package.
- The remote server issues an **ephemeral attachment handle** on Attach. The handle is
  not `ExternalBinding`, is never persisted, and identifies one server-registry entry.
  Normal attachment RPCs carry only that transient handle and the broker incarnation;
  they do not carry durable binding fields. A handle cannot be used after close or expiry.
- `session.ExternalBinding` is the durable opaque identity of the exact logical-session
  incarnation. Normal remote attachment operations do not carry it; remote Delete requires
  the persisted binding and fails closed when it is absent. Preserve
  `Service.DeleteSession(ctx, id)` only as the explicitly local compatibility path.
  `DeleteSessionIfBinding(ctx, id string, expected session.ExternalBinding)
  (DeleteOutcome, error)` compares and deletes under the Runtime binding lock. The
  server deletion caller passes the loaded session's persisted binding, so stale remote
  cleanup cannot delete a fresh logical session recreated under the same ID. This is an
  intentional root-internal contract change, not a new `engine/` API.
- The remote descriptor is frozen at attachment/catalogue creation and contains exactly:
  `name`, `description`, raw schema bytes, `read_only`, `dispatch_serial`, and
  `authorization_capable`. The client reconstructs frozen `tool.Tool` wrappers and all
  applicable markers, including `tool.DispatchSerial` and
  `tool.AuthorizationRequester`. If the attachment implements
  `WorkspaceEnrollmentHandle`, expose and map all three methods:
  Begin, Observe, and Cancel workspace enrollment.
- Tool Execute is a transient RPC containing the frozen tool name, call ID, and raw
  argument bytes. Provider-local invocation bookkeeping stays client-side; the client
  correlates the returned result to its original call. The server invokes the selected
  MCP proxy with a server-owned environment, never a serialized workspace, runner, path,
  or client-selected placement. It never serializes a
  pending conversation `session.ToolCall`, history, `PreparedRun`, or authorization
  continuation. The mecatl session remains the sole owner of those values.
- Tool-result parts are mapped structurally and byte-preservingly: binary `Data`, MIME
  token, raw schema/argument bytes, and typed parts are copied, not JSON-remarshaled.
  Textual fields are checked for valid UTF-8 at the peer boundary; malformed peer data
  is rejected before it can reach session state. This complements—not contradicts—the
  existing runtime producer repair at `session.RepairToolResult`: local arbitrary-byte
  producers are repaired at the loop choke point, while a malformed remote peer is a
  failed protocol response.
- Define closed protocol outcome/error vocabularies for Attach, Commit, Abort, Close,
  Delete, Cancel, authorization status, enrollment status, and Execute. Known sentinels
  map exactly: `ErrStateUnavailable`, `ErrAttachmentClosed`, and
  `ErrAuthorizationNotFound`. Unknown enum/string/value or malformed oneof fails closed.
  A gRPC `Unavailable` transport failure is **not** proof that an incarnation was lost:
  it does not clear an enrollment or rebind live authorization state.

  | Closed broker error/outcome | gRPC code | Client treatment |
  |---|---|---|
  | malformed request, oneof, enum, or UTF-8 | `InvalidArgument` | fail closed; do not mutate local state |
  | unauthenticated caller | `Unauthenticated` | fail closed before broker state access |
  | `state_unavailable` | `Unavailable` | transient transport/state loss; do not invalidate a live binding |
  | `attachment_closed`, binding mismatch, or illegal lifecycle transition | `FailedPrecondition` | terminal for that handle; no rebind in a live continuation |
  | `authorization_not_found` | `NotFound` | settle through the existing deterministic authorization path |
  | Execute may have dispatched but response is unknown | `Aborted` | return the fixed ambiguous-outcome tool error; never replay |

- Execute has no application retry interceptor, retry loop, hedging, or service-config retry
  policy. gRPC transparent retries that occur **before** server application dispatch retain
  their normal transport semantics; the server's per-request dispatch marker makes at most
  one server invocation for a client call once dispatch begins. A lost response after
  possible dispatch maps to one fixed model-visible ambiguous-outcome error and is not
  replayed; a later model retry is a new action. A caller deadline/cancellation does not
  prove the remote or upstream effect was cancelled.
- `openBrokerAttachment` must cache only after a successful Attach and successful
  attachment Commit. Cache invalidation occurs only after confirmed exact-binding state
  loss, never merely on a network outage. A fresh attach/re-enrollment is available only
  at the existing pre-prompt workspace-enrollment seam; it must never repair a live
  protected-call continuation. Stage 3 `PreparedRun` and `ClaimAuthorization` remain
  unchanged.
- `app.Build` gains a broker implementation selection interface. Local/default behavior
  continues to construct the local Runtime and own its ToolHive Process/handlers.
  Remote mode constructs neither ToolHive nor local OAuth secrets; only `mecabroker`
  owns them. Workspace enrollment discovery is an explicit attachment capability, not a
  guessed ToolHive side channel.
- Add a new **proposed topology ADR during implementation**, linked from living docs,
  to record this remote single-replica topology and resource ledger. Do not edit frozen
  ADR 0027 to rewrite its historical local inventory: the new ADR is its
  amendment/successor for this topology and carries the new List 1/List 2-style ledger.
  This is necessary because ADR 0027 currently records local broker state and optional
  ToolHive Redis; it does not already decide this remote topology. The deploy slice uses
  process-local ToolHive storage. Existing optional ToolHive-owned Redis remains distinct
  inner storage and does not create an outer continuity promise.
- **Proposed operational defaults (not shipped facts):** one TLS public listener on
  `:8443` multiplexes gRPC and the fixed HTTPS callback routes; a separate loopback-only
  admin listener on `127.0.0.1:8081` exposes liveness, readiness, and pre-stop drain only.
  Default dial timeout is 5s; lifecycle RPCs are 10s; Execute is 2m; handle idle lease is
  5m with a 30s sweep; drain propagation is 2s, drain deadline is 55s, listener shutdown is
  5s, and pod termination grace is 70s. All are positive, finite operator settings. The
  public listener requires TLS and workload authentication; the admin listener is not a
  workload or callback ingress.

## Donor reuse policy

The read-only donor is `plan-distributed-broker-contract/11-production-composition` at
`621505b5e861c2765f281add7e7d97be36600b28`. Manually port only its bounded drain,
readiness/liveness and isolated-admin probe shape, restricted pod/NetworkPolicy hardening,
and Taskfile/image build mechanics, adapting them to this single-replica contract. Do not
wholesale cherry-pick it or port `internal/broker`, outer Redis/distributed state,
fences/generations, owner credentials, callback failover, or its distributed protocol.
Modern/sessionless donor qualification remains deferred to B2; legacy MCP compatibility,
KMS custody, and federation are not implied by this reuse.

## Scenario 1 — The remote adapter preserves the Stage 3 logical contract

An offline gRPC client/server around the current broker seam attaches, commits, aborts,
closes, bound-deletes, executes transient invocations, and uses authorization and optional
enrollment operations without exposing ToolHive types or moving a pending call from its
mecatl session.

**Design anchors:** [ADR 0312](../adr/0312-confidential-toolhive-broker-client.md)
keeps the ToolHive client confidential; the [architecture guide](../architecture.md)
documents the current session-scoped broker boundary that this adapter preserves.

**Implementation contract and locations**

1. Add `contracts/proto/mecatl/broker/v1/broker.proto`; run `task generate`. Its package
   is `mecatl.broker.v1`, not the old donor protocol. Define Attach returning binding,
   handle, attach outcome, frozen descriptors, and capability bits; normal attachment
   operations carry only handle plus broker incarnation. Delete carries session ID plus
   the mandatory durable expected binding.
2. Add `internal/adapter/mcpbrokergrpc/server.go`, `client.go`, `codec.go`, and focused
   tests. The server owns a capped, mutex-protected handle registry. It registers an
   attachment only after Attach succeeds; Commit/Abort/Close mutate the entry idempotently;
   every operation verifies handle + binding before touching the local attachment.
3. Implement a bounded idle lease for orphaned handles: active-operation count protects an
   entry from expiry; all idle entries, including committed attachments, are reclaimed
   after the configured lease. Expired active entries close only after the last operation
   releases them. Close/Abort retries retain bounded terminal receipts until lease expiry;
   after reclamation they return explicit state-unavailable, never fabricate success.
   Expiry closes only that attachment and never logically deletes a session. There is no
   durable handle ledger. This process-lifetime registry, sweep goroutine/timer, and
   shutdown are inventoried by the new topology ADR.
4. Add `DeleteSessionIfBinding` to `internal/mcpbroker`; Runtime performs its exact-binding
   comparison and deletion atomically under the same binding lock. Adapt
   `internal/adapter/server/mcp_broker.go` to call it with the loaded binding. Keep the old
   `DeleteSession` as the local compatibility path.

**Acceptance**

- AC1.1: A remote attachment returns the same opaque binding and complete frozen tool catalogue as the wrapped local attachment, including read-only/serial and authorization-capable behavior needed by dispatch.
  - verify: `TestInitialProductionMCPBroker_Scenario1_AttachmentParity`
- AC1.2: Commit, abort, close, and delete preserve the Stage 3 idempotency and local-close-versus-logical-delete distinction across the wire.
  - verify: `TestInitialProductionMCPBroker_Scenario1_LifecycleOutcomes`
- AC1.3: Tool arguments and results round-trip without lossy re-encoding, while malformed payloads, unknown versions, invalid outcomes, and invalid UTF-8 fail closed before entering session state.
  - verify: `TestInitialProductionMCPBroker_Scenario1_ToolRoundTrip`
- AC1.4: The broker protocol contains no raw pending conversation `ToolCall`, OAuth code, verifier, access token, refresh token, client secret, ToolHive storage type, or Redis generation/fence field.
  - verify: `TestInvariant_initial_broker_protocol_is_neutral_and_secret_free`
- AC1.5: `engine/` and `engine/port` gain no broker, protobuf, gRPC, ToolHive, or remote-transport dependency.
  - verify: `TestInvariant_initial_broker_preserves_engine_layering`
- AC1.6: The implementation depends on the current `internal/mcpbroker` contract and contains no donor `internal/broker` package, Redis broker store, fence/generation field, distributed owner credential, or distributed broker protocol type.
  - verify: `TestInvariant_initial_broker_excludes_distributed_donor_contract`

## Scenario 2 — Every broker RPC is an authenticated TLS call

Every public gRPC method, including attachment lifecycle and enrollment operations, is
admitted through one shared gate before broker-state access. Browser callback handling is a
different OAuth state-protected flow, not workload gRPC authentication.

**Design anchors:** [ADR 0206](../adr/0206-oidc-authn-module.md) is the existing OIDC
validator boundary; [AGENTS.md](../../AGENTS.md) requires gRPC/protobuf dependencies to
remain outside `engine/`.

**Implementation contract and locations**

- Reuse the existing authentication/OIDC validator rather than parsing claims in the broker.
  Broker configuration requires strict HTTPS issuer, explicit trust and audience, and a
  non-zero bounded JWKS staleness. Invalid identity returns `Unauthenticated`; a key source
  unavailable beyond its staleness bound returns `Unavailable`; neither becomes anonymous.
  Token claims admit the workload only and never select session, owner, backend, or route.
- `internal/adapter/mcpbrokergrpc` provides unary/stream interceptors that run the same
  admission gate used by mounted HTTP handlers, before registry or Runtime access. Tests
  use a broker state-touch counter to prove rejection precedes state access.
- The mecak8s credential source rereads its projected workload-token file for every RPC,
  with a bounded read and fail-closed empty/read-error behavior. Use
  `grpc.PerRPCCredentials` with `RequireTransportSecurity() == true`. Apply the proposed
  5s dial, 10s lifecycle-RPC, and 2m Execute defaults above (operator-tunable). Configure
  TLS CA trust and an explicit expected DNS server name; no deployable insecure-production
  flag exists. Any loopback bypass is unexported and available only to package tests.
- Reuse `HandlerBundle.Mount` for the complete fixed ToolHive internal callback prefix and
  mount the separately configured final callback path. Callback state, not callback fields,
  binds authority; replay/malformed/cross-enrollment state is rejected generically. There
  is no raw-MCP data plane.

**Acceptance**

- AC2.1: A valid configured workload token over verified TLS can invoke every broker RPC, and the same calls without a bearer are rejected as unauthenticated before broker state is read.
  - verify: `TestInitialProductionMCPBroker_Scenario2_AuthenticatedTLS`
- AC2.2: Wrong issuer, wrong audience, expired token, invalid signature, malformed bearer, an untrusted or wrong-name server certificate, and plaintext non-loopback configuration fail closed; production exposes no insecure-TLS escape hatch.
  - verify: `TestInitialProductionMCPBroker_Scenario2_RejectsInvalidIdentity`
- AC2.3: Identity, bearer values, tool arguments, callback state, presentation URLs, and credentials never appear in diagnostics or metric labels; bounded diagnostics use closed operation/outcome labels.
  - verify: `TestInvariant_initial_broker_observability_is_bounded_and_secret_free`
- AC2.4: Browser callback fields and model/client input cannot select a mecatl session, owner, backend, route, or authenticated principal; canonical authority comes from broker-created state and operator configuration.
  - verify: `TestInvariant_initial_broker_callback_cannot_supply_authority`
- AC2.5: Production OIDC verification uses HTTPS with explicit trust, exact issuer and audience, and a non-zero bounded JWKS-staleness policy; once that bound is exceeded, key-source outage returns unavailable and never authenticates from indefinitely stale keys or downgrades to anonymous access.
  - verify: `TestInitialProductionMCPBroker_Scenario2_JWKSFailureIsBounded`
- AC2.6: The embedded ToolHive client remains confidential: registration persists only its hash with `client_secret_basic`; code exchange and refresh use HTTP Basic and omit `client_secret` from form bodies; the raw secret appears in no gRPC message/metadata, snapshot, event, diagnostic, metric, profile, or upstream request and is intentionally lost on broker restart.
  - verify: `TestADR_0312_RemoteBrokerPreservesConfidentialClient`

## Scenario 3 — A protected tool call authorizes and continues through the remote broker

A remote-attached mecak8s protected tool call still parks and resumes through the existing
Stage 3 continuation. ToolHive owns browser authorization; mecatl owns the exact parked
call and its one dispatch attempt.

**Design anchors:** [ADR 0311](../adr/0311-per-upstream-mcp-broker-oauth-grants.md)
and the [architecture guide](../architecture.md) define the current broker grant and
pre-prompt enrollment boundary.

**Implementation contract and locations**

- Client descriptor wrappers invoke transient Execute and translate closed outcomes into
  the existing broker semantics. They do not construct or persist `PreparedRun` data.
- Wire Present/Status/Cancel only through exact handle/binding/authorization references.
  Lost state maps to existing deterministic settlement; an uncertain Execute maps only to
  the fixed ambiguous error. No remote retry policy can replay it.
- Keep `internal/adapter/server/mcp_broker.go` pre-prompt-only re-enrollment narrow:
  confirmed `ErrStateUnavailable`/exact binding mismatch may invalidate cached attachment
  only while dropping a pending workspace enrollment before a prompt. Transport errors,
  protected authorization control, and a running continuation never use that path.

**Acceptance**

- AC3.1: An offline end-to-end test drives protected-call pending → browser presentation → ToolHive callback completion → granted recheck through the remote adapter and receives the protected tool result on the original run.
  - verify: `TestInitialProductionMCPBroker_Scenario3_ProtectedCallContinuation`
- AC3.2: The existing prepared continuation dispatches its exact parked call at most once through the broker adapter, pairs all deferred siblings, and admits no client-supplied replacement arguments or success status; this does not claim the upstream effect occurred exactly once.
  - verify: `TestInvariant_remote_broker_continues_exact_parked_call`
- AC3.3: Pending, denied, expired, interrupted, cancelled, unknown, binding-mismatch, and broker-unavailable outcomes preserve the existing deterministic Stage 3 settlement behavior.
  - verify: `TestInitialProductionMCPBroker_Scenario3_TerminalOutcomeParity`
- AC3.4: Presentation URLs are returned only live, never persisted in session snapshots/events or broker protocol records intended for reattachment, and no credential material appears in tool specs, arguments, results, events, snapshots, or diagnostics.
  - verify: `TestInvariant_remote_broker_keeps_presentation_and_credentials_private`
- AC3.5: Anonymous configured MCP profiles and authenticated workspace enrollment continue to work through the same remote service without introducing a second callback or authorization workflow.
  - verify: `TestInitialProductionMCPBroker_Scenario3_ProfileParity`
- AC3.6: The mounted browser ingress carries ToolHive's complete fixed callback prefix and the separately configured final callback path through the same lifecycle; missing, malformed, expired, duplicate, replayed, or cross-enrollment state is generically rejected without code exchange, catalogue publication, unrelated-state mutation, or sensitive response/log data.
  - verify: `TestInitialProductionMCPBroker_Scenario3_CallbackRoutingAndReplay`

## Scenario 4 — Broker restart and network failure are honest and bounded

One broker process owns process-local outer correlation. Network failure is uncertain;
confirmed incarnation loss is different. The deployment never claims that a cancellation
stops an already-dispatched upstream mutation.

**Design anchors:** [ADR 0027](../adr/0027-cloud-native.md) supplies the resource-ledger
model; [AGENTS.md](../../AGENTS.md) requires every outlives-a-call resource to be
inventoried there or in its successor ADR.

**Implementation contract and locations**

- Server operation cancellation releases only that operation's resources. It must not call
  `Attachment.Close` globally, because sibling RPCs may share the attachment. Explicit
  Close and bounded orphan-handle reclamation own attachment closure.
- A drain/admission component linearizes reject-before-state-access across gRPC and mounted
  HTTP callbacks. On drain it rejects new work, waits the configured propagation interval,
  permits admitted work until the finite deadline, then cancels operations, stops listeners,
  closes handles/Runtime, and waits only within the bounded shutdown budget.
- Restart/concurrency tests must prove stale handles and stale bound deletes cannot affect
  a fresh incarnation, a lost Execute response is ambiguous and unreplayed, and client
  cancellation/deadline leaves external-effect certainty explicitly unknown.

**Acceptance**

- AC4.1: Connection establishment and per-RPC deadlines are finite configuration values; transient establishment failure returns unavailable, and a later fresh attachment can reconnect only while the same broker process/incarnation remains authoritative.
  - verify: `TestInitialProductionMCPBroker_Scenario4_TransientReconnect`
- AC4.2: Replacing the broker process while a call is authorizing produces deterministic interruption without executing the protected call or replacing its opaque binding.
  - verify: `TestInitialProductionMCPBroker_Scenario4_RestartInterruptsPendingCall`
- AC4.3: A stale client attachment cannot commit, execute, present, observe, cancel, or delete against a different broker incarnation.
  - verify: `TestInvariant_initial_broker_rejects_stale_incarnation`
- AC4.4: Caller cancellation and deadlines propagate across the transport and release only the affected operation; they do not globally close a shared attachment. Explicit close and bounded orphan-handle cleanup close attachments, and the test joins all associated streams/goroutines within the configured deadline without claiming the upstream effect stopped.
  - verify: `TestInitialProductionMCPBroker_Scenario4_CancellationAndCleanup`
- AC4.5: No test, documentation, deployment label, readiness condition, or API description claims replica interchangeability, callback failover, restart durability, exactly-once external effects, or HA.
  - verify: `TestInvariant_initial_broker_makes_no_distributed_claims`
- AC4.6: If a tool RPC reaches the broker but its response is lost, the remote adapter returns a distinct model-visible ambiguous-outcome failure and never automatically retries the tool, rebinds the attachment, recreates authorization, or reports success; a later retry is a new ordinary tool action. The transport proof permits a transparent gRPC retry only when it fails before application dispatch, and proves neither the handler nor proxy is invoked twice after dispatch.
  - verify: `TestInitialProductionMCPBroker_Scenario4_AmbiguousToolResultIsNotReplayed`
- AC4.7: After broker restart, a persisted pre-prompt workspace enrollment discards its stale outer correlation and may begin one fresh enrollment, while a live protected-call authorization never rebinds to the new broker incarnation.
  - verify: `TestInitialProductionMCPBroker_Scenario4_PrePromptReenrollmentOnly`

## Scenario 5 — The standalone broker deploys safely as one replica

`cmd/mecabroker` is the sole remote composition root that creates the ToolHive Process,
workload-authenticated gRPC service, mounted callback routes, and isolated administration
surface. It runs one replica using Recreate; it has no HA or PDB claim.

**Design anchors:** [ADR 0027](../adr/0027-cloud-native.md) governs the new topology
resource ledger; [AGENTS.md](../../AGENTS.md) makes `cmd/` the composition-root layer and
requires Taskfile builds.

**Implementation contract and locations**

- Add `cmd/mecabroker/`, composition wiring in `internal/app/`, build/image entries, and
  a separate `deploy/helm/mecabroker/` chart. Do not alter mecak8s's chart into a shared
  broker deployment. The chart uses one replica/Recreate, a distinct ServiceAccount with
  no Kubernetes API permissions, strict security context, explicit resources and secret
  mounts; no PDB and no scaling/HA values.
- Readiness validates configuration, TLS identity, bounded OIDC verifier health, ToolHive
  construction, anonymous discovery, and static validation of protected routes. It does
  not log in a user, execute a tool, or treat construction alone as indefinite OIDC health.
  Admin probes are isolated, omit profiling, and the pre-stop endpoint is not routable by
  other workloads.
- NetworkPolicy permits ingress only from the configured mecak8s/workload and browser
  paths. Kubernetes NetworkPolicy cannot express DNS hostnames: the chart requires
  operator-provided CIDR/namespace/pod egress rules for OIDC/JWKS, upstream OAuth, and MCP
  destinations, documents dynamic endpoint limitations, and never promises fictional FQDN
  enforcement.
- Offline E2Es use a real gRPC boundary and embedded ToolHive with `httptest` fixture
  issuer/upstream; no external network. Include real factory coverage, not only
  grep/schema checks.

**Concise deployment policy (manually retained from the donor; proposed for this slice)**

- `task build` builds `bin/mecabroker`; the release image builds `cmd/mecabroker` directly
  and does not change existing image entrypoints. The dedicated chart remains separate from
  mecak8s and renders exactly one Recreate-managed replica, no PDB or scale/HA setting.
- The public TLS listener serves only gRPC and the fixed callback routes. The second,
  loopback-only admin listener serves `/healthz`, `/readyz`, and pre-stop drain; it is not
  published by the workload Service or NetworkPolicy. Liveness checks process progress;
  readiness checks the finite prerequisites listed above and does not exercise login or a
  tool.
- The pod uses a tokenless dedicated ServiceAccount with no Kubernetes API permissions,
  explicit resource requests/limits, `runAsNonRoot`, `RuntimeDefault` seccomp,
  `allowPrivilegeEscalation: false`, a read-only root filesystem, and all Linux capabilities
  dropped. Start from default-deny ingress/egress; operators add concrete workload/browser
  ingress and DNS/CIDR/namespace/pod egress rules. No policy pretends to enforce external
  DNS names.
- Pre-stop invokes the isolated drain endpoint. Use the proposed 2s propagation + 55s drain
  + 5s listener-shutdown budget inside the proposed 70s termination grace. This is bounded
  process cleanup, not a lease, ownership transfer, or callback failover mechanism.

**Acceptance**

- AC5.1: `task build` produces `bin/mecabroker`, and the release image configuration includes the binary without changing existing image entrypoints.
  - verify: `TestInitialProductionMCPBroker_Scenario5_BuildSurface`
- AC5.2: The deployment runs exactly one broker replica with explicit CPU/memory requests and limits, non-root, seccomp, read-only-root-filesystem, dropped-capability, ServiceAccount-isolation, TLS/identity material, readiness/liveness, and pre-stop. Its NetworkPolicy ingress admits only documented mecak8s gRPC and browser callback traffic; egress is operator-supplied CIDR/namespace/pod policy for OIDC/JWKS, upstream OAuth, and MCP destinations, with dynamic endpoint limitations documented rather than fictional FQDN enforcement.
  - verify: `TestInitialProductionMCPBroker_Scenario5_DeploymentSecurity`
- AC5.3: Readiness remains false until TLS identity, bounded OIDC verifier health, route/profile configuration, ToolHive construction, anonymous discovery, and static protected-route validation succeed; it performs no user login or tool execution. Each failed prerequisite makes it false, and readiness is never used as an ownership or stale-worker fence.
  - verify: `TestInitialProductionMCPBroker_Scenario5_Readiness`
- AC5.4: Drain atomically rejects new gRPC and callback work, waits a configured non-negative endpoint-propagation interval, allows active work until a finite configured drain deadline, then cancels/settles remaining operations, stops listeners, and closes ToolHive/local resources without leaking secrets.
  - verify: `TestInitialProductionMCPBroker_Scenario5_Drain`
- AC5.5: `mecak8s` can select the remote broker using operator-configured address, TLS CA
  trust, **expected TLS DNS server name**, expected audience, and a reloadable
  workload-token source; missing identity in authenticated mode refuses startup and never
  downgrades to anonymous transport.
  - verify: `TestInitialProductionMCPBroker_Scenario5_Mecak8sComposition`
- AC5.6: Architecture, usage, public user docs, deployment examples, and the planned topology ADR/resource ledger describe the one-replica availability boundary and provide no outer-broker Redis/HA setup for this slice; ADR 0027 remains frozen historical context.
  - verify: inspection — documentation and resource-inventory correctness require review plus `task docs`/`task site:build`
- AC5.7: A running mecak8s client uses a rotated workload token on a later RPC after the original token expires, without restart, cached-bearer reuse, or anonymous fallback.
  - verify: `TestInitialProductionMCPBroker_Scenario5_WorkloadTokenRotation`

## Landing map and proof strategy

| Area | Planned location | Named proofs |
|---|---|---|
| Versioned contract and generated bindings | `contracts/proto/mecatl/broker/v1/`, `contracts/gen/` | AC1.3, AC1.4, AC1.6 schema/neutrality guards |
| Remote client/server, handle lifecycle, codecs | `internal/adapter/mcpbrokergrpc/` | AC1.1–1.3; AC4.1, AC4.3, AC4.4, AC4.6 |
| Existing seam and server cache/delete behavior | `internal/mcpbroker/broker.go`, `internal/adapter/server/mcp_broker.go` | AC1.2; AC3.2–3.3; AC4.2, AC4.7 |
| Broker composition, ToolHive ownership, callbacks, OIDC/admission/drain | `internal/app/`, `cmd/mecabroker/` | AC2.1–2.6; AC3.1, AC3.4–3.6; AC4.4; AC5.3–5.4 |
| Remote mecak8s selection and credential source | `cmd/mecak8s/`, `internal/app/` | AC5.5, AC5.7 |
| Build/release/chart/docs/ADR | `Taskfile.yml`, image config, `deploy/helm/mecabroker/`, `docs/adr/`, `docs/architecture.md`, `docs/usage.md`, `user-docs/` | AC5.1–5.2, AC5.6 |

Place focused offline transport tests next to `internal/adapter/mcpbrokergrpc`; place real
factory and embedded-ToolHive E2Es next to the composition root that builds them. The test
suite must additionally cover: every-RPC authentication with the state-touch sentinel;
handle lifecycle concurrency and orphan expiry; restart versus transport ambiguity;
bound-delete protection; callback replay; raw-part/binary preservation; invalid UTF-8 peer
rejection; Execute dispatch-marker at-most-once behavior (while allowing only pre-dispatch
transparent gRPC retry semantics); and a full real-factory gRPC/ToolHive `httptest`
flow. Static protocol/schema checks supplement these behavioral proofs; they do not replace
them.

## Sequencing

1. Close the topology decision in the planned ADR, including the new resource ledger and
   the distinction from ADR 0027's historical local rows; add contract types and codec
   conformance tests.
2. Implement verified TLS, shared gRPC/HTTP admission interceptors, the two-listener
   boundary, and their state-touch/order tests before any production app can select remote
   broker mode.
3. Implement the remote registry/client with exact-binding guards, dispatch-marker
   at-most-once Execute semantics, and lifecycle/concurrency tests.
4. Wire app selection and remote mecak8s credentials/TLS; prove Stage 3 protected
   continuation and pre-prompt-only re-enrollment over real offline transport.
5. Add the broker root, drain/callback lifecycle, deployment chart, and operator/user
   documentation; then run the aggregate gates.

## Out of scope

| Item | Deferred to |
|---|---|
| Interchangeable replicas, durable outer broker state, callback failover, fencing, migration | B1 distributed broker |
| Redis as an outer broker correctness store | B1 only if justified |
| Raw/sessionless Modern MCP HTTP data plane or donor qualification | B2 qualification |
| Legacy MCP transport compatibility | Deferred; not part of this broker slice |
| KMS-backed broker key custody or key federation | Deferred; no KMS/federation contract here |
| End-user owner binding or a second principal model | Stage 4 |
| Exactly-once upstream mutations | Never implied |

## Verification on implementation

Run `task generate`; focused package tests for the contract, `internal/mcpbroker`,
`internal/adapter/mcpbrokergrpc`, `internal/adapter/server`, `internal/app`,
`cmd/mecabroker`, and `cmd/mecak8s`; then `task lint`, `task test`, `task docs`,
`task site:build`, `task api:check`, `task ac-trace`, and `go run ./cmd/mecademo`.
Run `task ac-trace-strict` only when the plan is marked landed and all named proofs exist.
No implementation tests are required merely to revise this draft plan.

## Remaining implementation choice

- The new ADR number is intentionally not preallocated. It must explicitly relate to ADR
  0027 and ADRs 0301/0302 without modifying their frozen accepted text. The listener
  topology and proposed finite defaults above are plan decisions, not choices left to the
  implementation.

## Exit criteria

When every acceptance proof and verification command above passes on the future accumulator,
the slice is ready for human-reviewed PR. B1 does not begin automatically.
