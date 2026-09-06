# Initial production MCP broker — acceptance plan

**Phase:** production topology slice after session-scoped MCP authorization
**Status:** draft, 2026-09-06. Synthesised from the accepted Stage 3 contract and the decision to ship a single-replica remote broker before distributed ownership.
**Base branch:** `recon/session-mcp-auth-stack` at `2adf71a3ba7c588596005e521601bc04a4186c47`.
**Accumulator branch:** `acc/initial-production-mcp-broker` (off this plan branch).

The smallest set of work that makes the existing Stage 3 MCP broker contract available as an independently deployed, authenticated production service. A `mecak8s` process uses a remote broker adapter without changing the session aggregate, exact-call authorization continuation, or ToolHive-owned OAuth behavior.

This slice deliberately runs one broker replica. It establishes the process and transport boundary, but makes no claim that broker state survives broker restart or that replicas are interchangeable. Distributed ownership, shared broker correctness state, and callback failover remain a later B1 capability.

The doc is organized scenario-first because acceptance is about what the running harness can demonstrate, not which packages exist on disk.

## Why these scope cuts

- [ADR-0301](../adr/0301-per-upstream-mcp-broker-oauth-grants.md) keeps callback state, PKCE/code exchange, refresh, provider routing, and upstream-token custody inside ToolHive. The remote service wraps that implementation; it does not create a second OAuth controller.
- [ADR-0302](../adr/0302-confidential-toolhive-broker-client.md) keeps each generated ToolHive broker-client secret in the broker process and off every public surface.
- [`architecture.md`](../architecture.md) explicitly identifies durable/remote outer ownership and multi-replica routing as later work. This plan crosses the remote process boundary only.
- [`AGENTS.md` — layering rule](../../AGENTS.md#the-layering-rule-the-thing-to-get-right) keeps the broker protocol and ToolHive adapters out of `engine/`; the existing root-internal `internal/mcpbroker` contract remains the application-facing seam.

## Donor branch and reuse policy

The abandoned distributed implementation remains a read-only donor at
`plan-distributed-broker-contract/11-production-composition`, commit
`621505b5e861c2765f281add7e7d97be36600b28`. Port selected hunks manually;
do not cherry-pick its implementation commits wholesale because they combine useful
production mechanics with rejected Redis, HA, owner-binding, and distributed-state
assumptions.

Reuse in this plan where it still fits:

- the admission gate, bounded drain coordinator, admin probes, secret-free observability tests, and production HTTP-client defaults from `cmd/mecabroker/`;
- defensive verified-bearer claim validation and its negative tests, adapted to the new gRPC authentication boundary rather than the donor's distributed credential model;
- non-root/seccomp/read-only-root-filesystem/capability-drop, probe, pre-stop, ServiceAccount-isolation, and NetworkPolicy manifest hardening;
- small `Taskfile.yml`, `.ko.yaml`, and release-workflow additions that build and publish `mecabroker`;
- the exact-continuation scenario as a behavioral test idea, rewritten against the actual Stage 3 contract and remote adapter.

Retain only for a later Modern/sessionless qualification effort: the donor's route
validation, configured-authority redirect confinement, single-shot ToolHive client,
and Modern-across-replicas tests. The initial broker exposes the Stage 3 logical
broker contract; it does not add a parallel raw-MCP HTTP data plane.

Do not port `internal/broker/**`, distributed broker Redis state, callback/refresh
claims, protected-call ledgers, migrations, the distributed protobuf contract,
replica-interchangeability claims, or its owner-binding model.

## In scope — 5 scenarios, in implementation order

Scenarios are listed in implementation order. Each is independently demoable; later scenarios assume earlier ones but do not change their acceptance criteria.

### Scenario 1 — The remote adapter preserves the Stage 3 logical contract

An offline broker client connects to an in-process test gRPC server wrapping the existing `internal/mcpbroker.Service`. Attach/commit/abort/close/delete, opaque incarnation binding, frozen tool metadata, tool execution, authorization request/presentation/status/cancel, and explicit outcomes cross the wire without exposing ToolHive types or moving the exact pending `ToolCall` out of the mecatl session snapshot. The adapter remains under `internal/`, consistent with the [architecture layering](../architecture.md#1-what-it-is) and the root-internal contract described in [`IMPLEMENTATION-NOTES.md`](../design/IMPLEMENTATION-NOTES.md).

**Work:**

- contracts: add a versioned broker gRPC service whose messages carry only the neutral information required to implement `internal/mcpbroker.Service`, `Attachment`, and the tool capabilities Stage 3 consumes;
- adapter: add a server wrapper over the existing in-process broker and a client adapter implementing the same `internal/mcpbroker` interfaces;
- composition: keep the in-process implementation available while allowing `mecak8s` to select the remote adapter;
- generated code: update contracts only through `task generate`.

**Acceptance:**

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

---

### Scenario 2 — Every broker RPC is an authenticated TLS call

A broker accepts only a verified workload bearer with the configured issuer and audience. Signature, expiry, issuer, and audience validation use the existing OIDC authentication adapter rather than hand-parsed trust. The authenticated identity is transport admission for this single trusted mecak8s deployment; it does not invent Stage 4 end-user ownership or accept session/backend authority from bearer claims. TLS is required outside explicit loopback test mode. This follows the existing server authentication boundary in [`architecture.md`](../architecture.md).

**Acceptance:**

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
  - verify: `TestADR_0302_RemoteBrokerPreservesConfidentialClient`

---

### Scenario 3 — A protected tool call authorizes and continues through the remote broker

A `mecak8s` session attached through the remote adapter calls a protected ToolHive-backed tool. The call parks in `StateAuthorizing`; the caller obtains the live presentation URL through the existing mecatl control surface; ToolHive handles its callback and token exchange inside `mecabroker`; recheck observes the exact opaque authorization and the existing `PreparedRun` continuation executes exactly the parked call once. The browser URL remains ephemeral and the exact effective `ToolCall`, deferred siblings, conversation, run usage, and continuation winner remain mecatl-owned as required by [ADR-0301](../adr/0301-per-upstream-mcp-broker-oauth-grants.md).

**Acceptance:**

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

---

### Scenario 4 — Broker restart and network failure are honest and bounded

The first release has one broker replica and process-local outer broker state. Local `mecak8s` reconnect and client transport reconstruction are allowed, but broker process death does not imply logical reattachment. A pending protected call whose broker incarnation is lost settles through the existing unavailable/binding-mismatch path and is never silently rebound or replayed. A completed authorization may be usable only to the extent already guaranteed by the in-process ToolHive/runtime contract; the plan makes no cross-restart continuity promise. This preserves the explicit limitation in [`architecture.md`](../architecture.md).

**Acceptance:**

- AC4.1: Connection establishment and per-RPC deadlines are finite configuration values; transient establishment failure returns unavailable, and a later fresh attachment can reconnect only while the same broker process/incarnation remains authoritative.
  - verify: `TestInitialProductionMCPBroker_Scenario4_TransientReconnect`
- AC4.2: Replacing the broker process while a call is authorizing produces deterministic interruption without executing the protected call or replacing its opaque binding.
  - verify: `TestInitialProductionMCPBroker_Scenario4_RestartInterruptsPendingCall`
- AC4.3: A stale client attachment cannot commit, execute, present, observe, cancel, or delete against a different broker incarnation.
  - verify: `TestInvariant_initial_broker_rejects_stale_incarnation`
- AC4.4: Caller cancellation and deadlines propagate across the transport; the server stops the affected operation, closes its local attachment, and the test joins all associated streams/goroutines within the configured deadline.
  - verify: `TestInitialProductionMCPBroker_Scenario4_CancellationAndCleanup`
- AC4.5: No test, documentation, deployment label, readiness condition, or API description claims replica interchangeability, callback failover, restart durability, exactly-once external effects, or HA.
  - verify: `TestInvariant_initial_broker_makes_no_distributed_claims`
- AC4.6: If a tool RPC reaches the broker but its response is lost, the remote adapter returns a distinct model-visible ambiguous-outcome failure and never automatically retries the tool, rebinds the attachment, recreates authorization, or reports success; a later retry is a new ordinary tool action.
  - verify: `TestInitialProductionMCPBroker_Scenario4_AmbiguousToolResultIsNotReplayed`
- AC4.7: After broker restart, a persisted pre-prompt workspace enrollment discards its stale outer correlation and may begin one fresh enrollment, while a live protected-call authorization never rebinds to the new broker incarnation.
  - verify: `TestInitialProductionMCPBroker_Scenario4_PrePromptReenrollmentOnly`

---

### Scenario 5 — The standalone broker deploys safely as one replica

The `mecabroker` composition root owns the ToolHive runtime, authenticated gRPC listener, required browser callback HTTP routes, and a separate administration surface. Startup validates configuration before listening. Readiness means this process can accept new work; it is not a correctness fence. Drain rejects new work, waits for endpoint propagation, bounds active operations, shuts down listeners, and closes local broker resources. The image and manifest are production-shaped but intentionally specify one replica. New long-lived resources are inventoried under [ADR-0027](../adr/0027-cloud-native.md), and operator-visible configuration is documented according to [`AGENTS.md`](../../AGENTS.md#workflow).

**Acceptance:**

- AC5.1: `task build` produces `bin/mecabroker`, and the release image configuration includes the binary without changing existing image entrypoints.
  - verify: `TestInitialProductionMCPBroker_Scenario5_BuildSurface`
- AC5.2: The deployment runs exactly one broker replica with explicit CPU/memory requests and limits, non-root, seccomp, read-only-root-filesystem, dropped-capability, ServiceAccount-isolation, TLS/identity material, readiness/liveness, pre-stop, and NetworkPolicy rules that admit the documented mecak8s gRPC and browser callback ingress plus only the configured DNS/OIDC/upstream egress.
  - verify: `TestInitialProductionMCPBroker_Scenario5_DeploymentSecurity`
- AC5.3: Readiness remains false until TLS identity, OIDC verification, route/profile configuration, ToolHive runtime, and an injected selected-profile probe are successful; each failed prerequisite makes it false, and readiness is never used as an ownership or stale-worker fence.
  - verify: `TestInitialProductionMCPBroker_Scenario5_Readiness`
- AC5.4: Drain atomically rejects new gRPC and callback work, waits a configured non-negative endpoint-propagation interval, allows active work until a finite configured drain deadline, then cancels/settles remaining operations, stops listeners, and closes ToolHive/local resources without leaking secrets.
  - verify: `TestInitialProductionMCPBroker_Scenario5_Drain`
- AC5.5: `mecak8s` can select the remote broker using operator-configured address, TLS trust, expected audience, and a reloadable workload-token source; missing identity in authenticated mode refuses startup and never downgrades to anonymous transport.
  - verify: `TestInitialProductionMCPBroker_Scenario5_Mecak8sComposition`
- AC5.6: Architecture, usage, public user docs, deployment examples, and ADR-0027 inventories describe the one-replica availability boundary and provide no Redis/HA setup for this slice.
  - verify: inspection — documentation and resource-inventory correctness require review plus `task docs`/`task site:build`
- AC5.7: A running mecak8s client uses a rotated workload token on a later RPC after the original token expires, without restart, cached-bearer reuse, or anonymous fallback.
  - verify: `TestInitialProductionMCPBroker_Scenario5_WorkloadTokenRotation`

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| Multiple interchangeable broker replicas, shared outer state, callback/refresh claims, fencing, and migration | B1 distributed broker | Explicitly deferred by [`architecture.md`](../architecture.md) |
| Redis broker correctness records or duplicated provider credentials | B1 only if justified | ToolHive remains authoritative under [ADR-0301](../adr/0301-per-upstream-mcp-broker-oauth-grants.md) |
| Raw/sessionless Modern MCP HTTP data plane | B2 qualification | Donor material is retained only as evidence |
| Legacy MCP reconstruction or affinity guarantees | B3 qualification | No initial production claim |
| End-user owner binding, ownerless capability mode, or a second principal model | Stage 4 | Transport authenticates the trusted workload only |
| KMS, broker-key rotation, distributed secret encryption, and multi-cluster federation | Later hardening | No broker correctness store in this slice |
| Generic exactly-once upstream mutations | Never implied | Stage 3 continuation uniqueness does not prove external effects |

## Cross-cutting deliverables

- Add a new ADR recording the independently deployed, authenticated, single-replica broker topology and its explicit restart/HA boundary. Choose its number at implementation time to avoid colliding with concurrent ADR work.
- Keep one authoritative broker protocol; generated files are never hand-edited.
- Inventory every listener, HTTP client, goroutine, token/JWKS cache, attachment registry, and drain tracker that outlives one call in ADR-0027 List 1; add List 2 entries only for state whose loss affects reconstruction.
- Preserve credential redaction and valid-UTF-8 handling at every new proto mapping boundary.

## Sequencing recommendation

First define and conformance-test the neutral gRPC mapping around `internal/mcpbroker`. Add authenticated TLS transport before any production composition can use it. Then prove exact Stage 3 authorization continuation through the remote adapter. Only after the failure semantics are pinned should `mecabroker`, `mecak8s` wiring, deployment manifests, release configuration, and user documentation land. Donor hunks are ported only within the scenario that owns their behavior, after comparison with the current target implementation.

## Named tests landing in this plan

- `TestInitialProductionMCPBroker_Scenario1_AttachmentParity`
- `TestInitialProductionMCPBroker_Scenario1_LifecycleOutcomes`
- `TestInitialProductionMCPBroker_Scenario1_ToolRoundTrip`
- `TestInitialProductionMCPBroker_Scenario2_AuthenticatedTLS`
- `TestInitialProductionMCPBroker_Scenario2_RejectsInvalidIdentity`
- `TestInitialProductionMCPBroker_Scenario2_JWKSFailureIsBounded`
- `TestADR_0302_RemoteBrokerPreservesConfidentialClient`
- `TestInitialProductionMCPBroker_Scenario3_ProtectedCallContinuation`
- `TestInitialProductionMCPBroker_Scenario3_TerminalOutcomeParity`
- `TestInitialProductionMCPBroker_Scenario3_ProfileParity`
- `TestInitialProductionMCPBroker_Scenario3_CallbackRoutingAndReplay`
- `TestInitialProductionMCPBroker_Scenario4_TransientReconnect`
- `TestInitialProductionMCPBroker_Scenario4_RestartInterruptsPendingCall`
- `TestInitialProductionMCPBroker_Scenario4_CancellationAndCleanup`
- `TestInitialProductionMCPBroker_Scenario4_AmbiguousToolResultIsNotReplayed`
- `TestInitialProductionMCPBroker_Scenario4_PrePromptReenrollmentOnly`
- `TestInitialProductionMCPBroker_Scenario5_BuildSurface`
- `TestInitialProductionMCPBroker_Scenario5_DeploymentSecurity`
- `TestInitialProductionMCPBroker_Scenario5_Readiness`
- `TestInitialProductionMCPBroker_Scenario5_Drain`
- `TestInitialProductionMCPBroker_Scenario5_Mecak8sComposition`
- `TestInitialProductionMCPBroker_Scenario5_WorkloadTokenRotation`

## Definition of done

1. `task lint` and `task test` pass (both modules, `-race`).
2. `task docs` regenerates `llms.txt` and passes the strict matlatl link gate.
3. `task site:build` passes after the operator-facing broker deployment documentation is updated.
4. `task api:check` passes; if an intentional exported engine API change proves unavoidable, `task api:update` and the required `engine/CHANGELOG.md` classification land together.
5. `task ac-trace-strict` resolves every AC proof after this plan is marked `landed`.
6. `go run ./cmd/mecademo` still prints a complete offline session.
7. The offline protected-call demonstration traverses a real gRPC client/server boundary and real embedded ToolHive authorization flow without live network access.
8. The deployment and documentation consistently state one replica and the broker-restart interruption boundary.

## Deferred decisions and known risks

- **Remote protocol compatibility.** The initial protobuf package is versioned; backward-compatibility and rolling mixed-version policy become binding only after its first release. Unknown fields remain tolerated by protobuf, while unknown enum/string outcomes fail closed in the adapter.
- **Broker restart loses outer correlation.** This is intentional for the first release and must remain user-visible. B1 must replace, not obscure, this behavior.
- **Callback and gRPC may use separate listeners.** The implementation may choose separate public data and browser HTTP ports, but both must share one lifecycle and documented ingress contract; neither may rely on pod affinity as an unstated correctness mechanism.
- **Workload token rotation.** The client must read or refresh credentials without requiring mecak8s restart. The exact Kubernetes projection integration is an adapter/composition choice, not a session-domain concern.
- **Donor drift.** Donor code predates the current Stage 3 contract. Every reused hunk is reviewed against the target branch and its tests are rewritten rather than copied as evidence of compatibility.

## Exit criteria

When every point under *Definition of done* holds on the accumulator, the initial production MCP broker is ready for a human-reviewed PR. B1 distributed ownership does not begin automatically.
