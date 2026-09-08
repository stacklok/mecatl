# Singleton MCP broker review remediation — acceptance plan

**Phase:** production broker correctness closure
**Status:** landed, 2026-09-08. Converts the completed implementation self-review into executable acceptance proof.
**ADR:** [ADR 0318](../adr/0318-bounded-singleton-mcp-broker-correctness.md) — bounded receipts, admission, readiness, and deployment truth.
**Accumulator branch:** `acc/singleton-mcp-broker-review-remediation` (off `acc/initial-production-mcp-broker`).

The smallest set of work that closes every blocker, important finding, and test gap from the
initial production broker self-review without importing distributed-broker or exactly-once
claims. The scenarios use running boundaries and hostile peers rather than source-substring
proxies.

## Why these scope cuts

- [ADR 0318](../adr/0318-bounded-singleton-mcp-broker-correctness.md) keeps correctness process-local and bounded while closing ADR 0316's incomplete receipt contract.
- [ADR 0317](../adr/0317-single-replica-mcp-broker-topology.md) remains the one-replica `Recreate` topology; this work makes its readiness, drain, and deployment claims true.
- [ADR 0311](../adr/0311-per-upstream-mcp-broker-oauth-grants.md) and [ADR 0312](../adr/0312-confidential-toolhive-broker-client.md) continue to own multi-upstream authorization and confidential credential custody.

## In scope — 5 scenarios, in implementation order

### Scenario 1 — A lost broker response never causes a second process-local dispatch

A hostile transport client repeats lifecycle and Execute RPCs after losing responses. The
broker returns immutable bounded receipts, validates request identity and response correlation,
and classifies uncertainty without relying on human-readable status text. This closes the
remote-adapter contract in [ADR 0318](../adr/0318-bounded-singleton-mcp-broker-correctness.md)
and preserves the session pairing and valid-UTF-8 invariants in [AGENTS.md](../../AGENTS.md).

**Work:**
- contracts: method-specific protobuf messages and a closed structured error-reason detail;
- adapter: absolute lease-bound lifecycle and Execute receipt registries, request digests,
  response correlation, and conservative dispatch classification;
- tests: concurrent duplicate, mismatch, expiry, hostile response, cancellation, and sweep races.

**Acceptance:**
- AC1.1: Concurrent or sequential duplicate Abort and Close requests execute the underlying operation once and receive the same terminal outcome until an absolute lease deadline that polling cannot extend; after reclamation they receive structured `state_unavailable`.
  - verify: `TestSingletonBrokerRemediation_Scenario1_LifecycleReceiptsAreAbsoluteAndReplayable`
- AC1.2: Duplicate Execute requests for the same incarnation, handle, call ID, tool, item identity, and argument digest atomically join or replay one immutable bounded receipt and dispatch once, including after loss of the first response. Polling cannot extend the absolute receipt lease; after reclamation the same request returns structured `state_unavailable` and never redispatches.
  - verify: `TestSingletonBrokerRemediation_Scenario1_ExecuteReceiptPreventsRedispatch`
- AC1.3: Reusing a call ID with different request content fails closed before tool dispatch, and a response whose call ID differs from the request is rejected before session state can consume it.
  - verify: `TestSingletonBrokerRemediation_Scenario1_CallIdentityAndResponseCorrelation`
- AC1.4: Only a recognized, method-bound structured response proving failure before the server dispatch marker remains an ordinary RPC error; cancellation, deadline, transport loss, absent response, malformed proof, and unknown reason or version after possible dispatch produce the fixed correlated ambiguous result without retry, hedge, rebind, or replay.
  - verify: `TestSingletonBrokerRemediation_Scenario1_DispatchClassificationIsConservative`
- AC1.5: Broker error identity uses structured reasons, preserves unknown reasons as protocol failures, and never infers state loss from status-message text.
  - verify: `TestSingletonBrokerRemediation_Scenario1_StructuredErrorReasons`
- AC1.6: Every stale-incarnation attachment, lifecycle, enrollment, authorization-control, Execute, observation, and bound-delete request is rejected before registry or logical broker state access.
  - verify: `TestSingletonBrokerRemediation_Scenario1_StaleIncarnationRejectedAcrossSurface`
- AC1.7: Raw arguments, JSON-object schemas, textual and binary result parts, and MIME tokens round-trip byte-exact where allowed; invalid UTF-8, malformed schemas, unknown outcomes, and malformed response shapes fail before state mutation.
  - verify: `TestSingletonBrokerRemediation_Scenario1_HostilePeerBoundary`
- AC1.8: Every broker RPC has method-specific request and response messages and the generated contract passes Buf lint and freshness checks.
  - verify: `TestInvariant_singleton_broker_method_specific_rpc_contract`
- AC1.9: Cancelling one active operation does not cancel a sibling, and active Execute versus Close, Abort, expiry, and shutdown races preserve one terminal owner without leaks or duplicate dispatch.
  - verify: `TestSingletonBrokerRemediation_Scenario1_ConcurrentCancellationAndLifecycleIsolation`
- AC1.10: The broker-enabled main and per-session engine factories place the unknown-outcome reconciliation instruction in their owned system-prompt layer, while broker-disabled engines do not receive it.
  - verify: `TestSingletonBrokerRemediation_Scenario1_BrokerRecoveryInstructionUsesRealFactories`

---

### Scenario 2 — Restart recovery is fresh only before a prompt and retained state is bounded

After confirmed broker-process loss, composition discards the pinned remote client and creates
one new owned connection for pre-prompt enrollment. A parked protected call never rebinds.
Logical sessions and request receipts remain finite without evicting active authority. This is
the recovery boundary described by [`IMPLEMENTATION-NOTES.md`](../design/IMPLEMENTATION-NOTES.md)
and tightened by [ADR 0318](../adr/0318-bounded-singleton-mcp-broker-correctness.md).

**Acceptance:**
- AC2.1: Confirmed state loss before a prompt closes the stale remote client, obtains a fresh composition-owned client, and can enroll against a replacement incarnation.
  - verify: `TestSingletonBrokerRemediation_Scenario2_FreshClientPrePromptRecovery`
- AC2.2: A live or restored protected-call continuation never invokes the fresh-client factory and settles state loss deterministically without reattachment.
  - verify: `TestSingletonBrokerRemediation_Scenario2_ProtectedContinuationNeverRebinds`
- AC2.3: Empty, malformed, and overlong logical session IDs are rejected before allocation. Configured limits for logical sessions, attachment handles, Execute receipts, and broker-owned pending enrollment/control entries reject new admission with a structured capacity reason rather than evicting, overwriting, or extending active, attached, or parked authority; each registry has absolute expiry and bounded cleanup work.
  - verify: `TestSingletonBrokerRemediation_Scenario2_BoundedAdmissionAcrossBrokerRegistries`
- AC2.4: Unattached logical sessions expire after their absolute configured retention, while attached or active sessions remain protected; shutdown joins every sweeper and closes every owned client exactly once.
  - verify: `TestSingletonBrokerRemediation_Scenario2_RetentionAndOwnership`

---

### Scenario 3 — Readiness and public transport report real bounded health

A production-configured broker becomes ready only after its actual TLS, verifier, profile,
ToolHive discovery, and protected-route dependencies are usable. Later verifier/JWKS failure
removes readiness within a finite bound without user login or tool execution. Public HTTP
traffic is bounded without truncating valid configured Execute calls. The production boundary
is anchored by [ADR 0317](../adr/0317-single-replica-mcp-broker-topology.md) and the diagnostics
and resource rules in [AGENTS.md](../../AGENTS.md).

**Acceptance:**
- AC3.1: The real `cmd/mecabroker` factory refuses malformed or plaintext protected upstream, issuer, authorization, token, and callback URLs; one canonical validator enforces HTTPS, forbids userinfo and invalid components, and gives every configured or OIDC-discovered endpoint bounded no-redirect fetching and response sizes without excluding deliberately configured private identity infrastructure.
  - verify: `TestSingletonBrokerRemediation_Scenario3_ProtectedURLsAndOIDCDiscoveryFailClosed`
- AC3.2: Loopback HTTP relaxation is unavailable to production code and remains usable only by test-compiled helpers.
  - verify: `TestInvariant_singleton_broker_loopback_relaxation_is_test_only`
- AC3.3: Production readiness runs a bounded, side-effect-free OIDC/JWKS health check plus profile, ToolHive discovery, and protected-route checks; failure closes readiness without authenticating a user, minting a credential, or executing a tool.
  - verify: `TestSingletonBrokerRemediation_Scenario3_ProductionReadinessUsesRealDependencies`
- AC3.4: A successful readiness probe does not silently extend the verifier's accepted-key staleness window, and stale or unavailable keys fail closed according to the configured bound.
  - verify: `TestSingletonBrokerRemediation_Scenario3_ReadinessDoesNotLaunderStaleKeys`
- AC3.5: The public TLS listener enforces finite header, connection/read/write/idle, and per-route request-body limits. Oversized, incomplete, unsupported-content-type, or over-deadline callback requests are rejected before callback-state consumption, code exchange, ToolHive invocation, or unrelated state mutation, while a valid HTTP/2 gRPC Execute lasting up to its configured deadline still completes.
  - verify: `TestSingletonBrokerRemediation_Scenario3_PublicListenerBoundsRejectBeforeCallbackSideEffects`
- AC3.6: A production-assembled drain rejects new gRPC and callback work, lets admitted work run to the finite deadline, cancels the remainder, joins propagation work, stops listeners, and closes verifier, broker, and ToolHive resources in order.
  - verify: `TestSingletonBrokerRemediation_Scenario3_ProductionDrainAndCleanup`
- AC3.7: Every public broker RPC requires an accepted workload bearer over verified TLS before attachment, receipt, logical-session, or ToolHive state access; absent or malformed bearer, wrong issuer/audience/time/signature, plaintext transport, and wrong or untrusted server identity fail closed with no production anonymous or insecure fallback.
  - verify: `TestSingletonBrokerRemediation_Scenario3_PublicRPCAuthenticationPrecedesBrokerState`

---

### Scenario 4 — The Helm and release surfaces enforce what operators are told

Rendering both charts with production values yields the exact ingress identities, remote-client
credentials, immutable image reference, and shutdown budget the running binaries require.
Release jobs use pinned tools and never interpolate registry credentials into shell source.
This makes the deployment consequences of [ADR 0317](../adr/0317-single-replica-mcp-broker-topology.md)
and [ADR 0318](../adr/0318-bounded-singleton-mcp-broker-correctness.md) executable.

**Acceptance:**
- AC4.1: The mecabroker chart schema accepts one honest `networkPolicy.publicFrom` peer union for the multiplexed public port, rejects the ineffective `mecak8sFrom` and `browserCallbackFrom` keys, and documents that vanilla NetworkPolicy cannot provide route-level separation between gRPC and browser callbacks.
  - verify: `TestSingletonBrokerRemediation_Scenario4_NetworkPolicyValuesRenderExactly`
- AC4.2: The mecak8s chart accepts remote-broker address, CA Secret/key, expected DNS name, token audience, and bounded token lifetime only as one all-or-none block; it disables automatic service-account-token mounting where compatible, renders a read-only audience-bound projected token at a fixed path, and passes every required client flag without exposing token data.
  - verify: `TestSingletonBrokerRemediation_Scenario4_Mecak8sRemoteBrokerProjection`
- AC4.3: A running mecak8s remote client rereads the projected workload-token file for every RPC: after atomic rotation the next call uses only the replacement audience-bound token without restart, cached-bearer reuse, static fallback, or anonymous transport.
  - verify: `TestSingletonBrokerRemediation_Scenario4_ProjectedTokenRotation`
- AC4.4: Production rendering for both remote-broker workloads requires a canonical `sha256:` digest with 64 lowercase hexadecimal characters, rejects empty or malformed digests and simultaneous tags, and renders every container image as `repository@digest`.
  - verify: `TestSingletonBrokerRemediation_Scenario4_DigestRequired`
- AC4.5: Every release `setup-ko` step selects the same explicit approved `ko` version, and registry credentials enter shell steps only through step-scoped environment variables quoted into `--password-stdin`.
  - verify: `TestInvariant_singleton_broker_release_supply_chain_hardening`
- AC4.6: A required CI deployment job installs pinned Helm and kubeconform, runs `task deploy:check`, renders every production fixture including remote-broker mecak8s, and runs both semantic chart-test packages in a mode where a missing Helm executable fails rather than skips.
  - verify: `TestSingletonBrokerRemediation_Scenario4_DeploymentGateIsExecutable`
- AC4.7: The ADR 0318 resource-ledger amendment inventories the drain coordinator and propagation waiter, active operations, attachment/lifecycle and Execute receipts, logical-session retention, every sweeper/timer, verifier/readiness resources, ToolHive process, and replacement remote clients, with owner, capacity/retention, close/join order, and restart disposition tied to their constructors and shutdown paths.
  - verify: inspection — resource-inventory completeness requires constructor/shutdown review plus the docs gate
- AC4.8: Complete production chart rendering preserves exactly one `Recreate` broker replica, no PDB/autoscaler/HA surface, a loopback-only administration listener absent from public Services, restrictive workload security, and default-deny ingress/egress with explicit operator egress.
  - verify: `TestSingletonBrokerRemediation_Scenario4_SingletonTopologyAndExposure`

---

### Scenario 5 — The named proofs traverse the real authorization and deployment paths

The final proof starts a production-equivalent offline broker, drives an engine session into
Stage 3, completes browser authorization through the real callback handler, and resumes the
exact parked call through remote transport and embedded ToolHive. Hostile callbacks and broker
restart are exercised against the same assembly. This replaces name-only traceability with the
observable behavior required by the [original acceptance record](initial-production-mcp-broker.md)
and the repository's offline-test rule in [AGENTS.md](../../AGENTS.md).

**Acceptance:**
- AC5.1: An offline production-equivalent engine/session run parks one protected ToolHive call, completes authorization through the mounted browser callback, and resumes that exact call once through authenticated remote broker transport.
  - verify: `TestSingletonBrokerRemediation_Scenario5_Stage3RemoteVertical`
- AC5.2: The real fixed ToolHive callback bundle and final callback accept success only through opaque high-entropy broker-created state bound to one enrollment and expiry, consume it once, and never trust browser-supplied session, owner, backend, route, or principal selectors. Missing, malformed, expired, duplicate, replayed, and cross-enrollment state gets the same generic public rejection before exchange or unrelated mutation, with no state, code, token, enrollment, or upstream identity in responses or diagnostics.
  - verify: `TestSingletonBrokerRemediation_Scenario5_CallbackCorrelationReplayAndNonDisclosure`
- AC5.3: The production-equivalent callback and refresh flow preserves ADR 0312 custody: the generated broker-client secret is high-entropy, process-memory-only, stored by ToolHive only as its required hash, used only as `client_secret_basic`, and absent from form bodies, protobuf/metadata, snapshots/events, callbacks, diagnostics, metrics, rendered configuration, and upstream MCP requests.
  - verify: `TestADR_0312_SingletonBrokerConfidentialClientCustody`
- AC5.4: Killing and replacing the broker during pre-prompt enrollment proves fresh-client recovery, while replacement during the parked continuation proves deterministic interruption with no redispatch.
  - verify: `TestSingletonBrokerRemediation_Scenario5_RestartBoundary`
- AC5.5: The required build gate creates an executable `mecabroker` binary and the deployment gate renders and validates complete production fixtures rather than asserting source substrings.
  - verify: `TestSingletonBrokerRemediation_Scenario5_ProductionArtifacts`
- AC5.6: The original production-broker scenario tests themselves drive the production factories and externally observable boundaries named by their ACs; replacing any factory with a hand-written attachment, callback, coordinator, or source-substring fixture makes the proof fail.
  - verify: `TestInvariant_singleton_broker_named_proofs_use_production_paths`

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| Cross-process durable Execute receipts or exactly-once upstream effects | distributed broker phase | [ADR 0318](../adr/0318-bounded-singleton-mcp-broker-correctness.md) explicitly bounds receipts to one incarnation |
| Interchangeable replicas, callback failover, and shared outer authority | distributed broker phase | [ADR 0317](../adr/0317-single-replica-mcp-broker-topology.md) remains single-replica `Recreate` |
| Sessionless Modern MCP data plane | B2 qualification | Original plan deferral remains unchanged |
| KMS-backed key custody | later production-hardening decision | No signing/key-custody surface changes here |

## Sequencing recommendation

Close the wire and registry invariants first because fresh-client recovery and vertical tests
depend on their structured outcomes. Then land bounded session ownership and readiness, followed
by chart/release changes. Finish by replacing shallow named proofs with the production-equivalent
vertical and running aggregate gates.

## Definition of done

1. `task generate`, `task lint`, and `task test` pass.
2. Focused race-enabled tests for `internal/adapter/mcpbrokergrpc`, `internal/adapter/mcpbroker`, `internal/adapter/mcpbrokerserver`, `internal/adapter/server`, `internal/app`, `cmd/mecabroker`, and `cmd/mecak8s` pass.
3. `task docs`, `task site:build`, `task api:check`, `task deploy:check`, and release-workflow tests pass.
4. `task ac-trace-strict` passes with this plan marked `landed` and with no name-only substitution for semantic production-path proof.
5. The named tests are grep-locatable and green.
6. `go run ./cmd/mecademo` prints a complete offline session.
7. A four-axis `/panel-review` reports no blocker or important finding against this plan or the original self-review list.

## Deferred decisions and known risks

- **Process-local guarantee.** Receipt loss on broker replacement remains explicit uncertainty; distributed durability is intentionally deferred.
- **Readiness dependency.** A live verifier probe can remove the only replica from service; this is honest for a singleton and does not trigger unsafe local fallback.
- **Protocol rollout.** Method-specific protobuf descriptors require coordinated internal client/server rollout; no compatibility shim may weaken structured failure handling.

## Exit criteria

When every point under *Definition of done* holds on the accumulator, every blocker, important
finding, and advisory test/documentation gap in the initial production broker self-review is
closed.
