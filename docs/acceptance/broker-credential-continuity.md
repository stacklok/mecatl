# Broker credential custody continuity — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — this plan records durable credential-envelope security and broker-continuity decisions in ADR 0365, alongside the acceptance contract for the already-implemented custody mechanism.
**Decision record:** [ADR 0365](../adr/0365-broker-replacement-fresh-authority.md)
**Phase:** architectural singleton MCP broker credential-continuity closure.
**Status:** draft, 2026-09-27. Local contract for review; none of the fixes below have
landed yet.
**Delivery:** Split Plan / Interface review explicitly waived by the directing human on
2026-09-28. Implement directly on the current branch against this acceptance contract; this
is not a Combined route.
**Expected tasks:** Decompose locally on this branch as implementation proceeds.

Once a singleton `mecabroker` process is replaced, a session with previously-staged encrypted
custody can recover its upstream OAuth grant without redoing browser authorization
(ADR 0365). The mechanism — 4 gRPC RPCs, an encrypted envelope store, and host-side recovery
orchestration — is fully implemented but was reviewed with zero acceptance-plan coverage.
This plan gives it that coverage and specifies exactly which already-agreed fixes must land
before each acceptance criterion below can pass. It does not claim distributed-broker
durability, replica takeover, or exactly-once upstream effects — those remain out of scope
per ADR 0365 and ADR 0364. It preserves [ADR 0311](../adr/0311-per-upstream-mcp-broker-oauth-grants.md),
[ADR 0312](../adr/0312-confidential-toolhive-broker-client.md), and
[ADR 0314](../adr/0314-mcp-broker-dcr-client.md), and holds replay behavior to
[ADR 0364](../adr/0364-bounded-singleton-mcp-broker-correctness.md) Scenario 1.

## Human decisions

- [x] Drop status-message matching for `INCARNATION_LOST`, `CONTINUITY_UNAVAILABLE`, and `CONTINUITY_PROFILE_CHANGED`. - Decision: yes, for all three together, not just the two continuity reasons; the structured `BrokerErrorDetail` code check already covers the real invariant, and the message check contradicts the proto's own "without status-text inference" promise.
- [x] Split `CONTINUITY_UNAVAILABLE` into capacity, tombstone, and genuinely-opaque cases. - Decision: yes; capacity exhaustion moves to `CAPACITY_REACHED`, tombstoned custody gets a new terminal `BROKER_ERROR_REASON_CONTINUITY_REVOKED` returned only after a successful guard match, and `CONTINUITY_UNAVAILABLE` keeps only its deliberately opaque cases.
- [x] Replace KEK-wrap with per-seal HKDF key derivation. - Decision: yes; this avoids accumulating random-nonce GCM encryptions under the shared KEK, uses no new dependency, and needs no shared counter across restarts or replicas. Each seal gets an independently random 256-bit salt; key reuse is negligible, not mathematically impossible (at roughly 2^28 seals, the chance of any salt collision is about 2^-201).
- [x] Fix the resource-ledger/ADR gap in ADR 0365 and ADR 0027, leave AC4.7 in the singleton plan untouched. - Decision: yes; AC4.7 never claimed to cover custody resources and is not stale, so the fix lands as ADR 0365's own resource-ledger amendment plus a new ADR 0027 List 1 row, mirroring the ADR 0363/ADR 0364 precedent, rather than editing the already-accepted ADR 0364.
- [x] Add a `deploy:check` CI fixture that renders the managed-Redis credential store. - Decision: yes, as a new file (`ci/broker-managed-redis-values.yaml`), not an extension of the existing external-Redis fixture, since the chart rejects configuring both at once (now its own AC7.7) and several existing tests pin that fixture's external-Redis shape.
- [x] Add `credential_continuity` to `RecoverCredentialAttachmentResponse`; stop hardcoding false. - Decision: yes; recovery doesn't consume custody, so a recovered attachment must report its real continuity capability or a later enrollment on that handle can silently skip staging.
- [x] Normalize the four continuity RPCs and their receipt-expiry checks to the server's injectable clock. - Decision: yes, mechanical; `s.clock` already exists and is already override-able, so this is a call-site fix with no constructor change.

## Interface contract

- **gRPC / protobuf:** Additive only, and free to change since this slice is unshipped (does
  not exist on `main`). Add `BROKER_ERROR_REASON_CONTINUITY_REVOKED = 9` to `BrokerErrorReason`.
  Add `bool credential_continuity` to `RecoverCredentialAttachmentResponse`. Document
  sensitivity/opaqueness/idempotency/incarnation-scope on every credential-custody message and
  field (currently undocumented; lint doesn't require it, but it's the only place several
  load-bearing facts live).
- **Exported Go APIs / interfaces:** Internal only
  (`internal/mcpbroker`, `internal/adapter/mcpbroker`, `internal/adapter/mcpbrokergrpc`,
  `internal/adapter/server`). No `engine/` or public SDK changes.
- **Tool schemas:** None — this feature has no model-visible tool name, schema, or behavior surface.
- **CLI / config:** None — the operator-facing KEK-ring shape in `values.yaml` (`activeID`, `keys[]`) is unchanged; the HKDF change alters only the internal envelope format, not the key-ring configuration surface.
- **Events / persistence:** The credential-envelope wire format changes
  (KEK-wrap segments replaced by a per-seal salt); free to change since unshipped, no
  migration needed. New CI-only Helm fixture; no persisted-data migration.
- **Security / authority:** This plan is almost entirely about authority and confidentiality
  guarantees for the recovery path — see Scenarios 1–5 below.
- **Compatibility / migration:** None — no consumer has shipped against the current wire shape, since this proto slice does not exist on `main`.

## In scope — 7 scenarios

### Scenario 1 — Recovery grant is capped, non-refreshable, and confined to its own call path

The bearer minted for a recovered attachment is process-local, access-only, and cannot
outlive the original custody window no matter how many times it's reissued.

**Design anchors:** [ADR 0365](../adr/0365-broker-replacement-fresh-authority.md) ("adapter-private,
access-only bearer... bounded to two minutes and without a refresh token").

**Acceptance:**
- AC1.1: Repeated `RecoverCredentialAttachment` calls across the custody's fixed retention
  window each mint an access-only bearer whose expiry never exceeds
  `min(now+2m, custody.ExpiresAt)`.
  - verify: `TestRecoveredCredentialComponent_ReissuesAfterAttemptDeadlineWhileCustodyCurrent`,
    `TestRecoveredCredentialComponent_IssuesFreshJWTThroughToolHiveMiddleware`
- AC1.2: No recovery-minted bearer response ever carries a refresh token or any other
  externally-usable grant field.
  - verify: `TestRecoveredCredentialComponent_NeverReturnsRefreshTokenOrExternalGrant`
- AC1.3: A bearer minted for recovery is never exposed outside the recovered
  catalogue-discovery call path — it lives only inside a private, per-attachment token
  source that is torn down on Commit — and carries no distinguishing OAuth scope or
  audience a resource server could itself enforce; isolation here is a process-internal
  capability discipline, not a resource-server audience check, and this AC makes no
  stronger claim than that.
  - verify: `TestRecoveredCredentialBearerNeverEscapesDiscoveryCallPath`
- AC1.4: Once `custody.ExpiresAt` has passed, no further reissue succeeds even mid-attempt,
  and the attachment fails closed rather than serving a stale cached token.
  - verify: `TestRecoveredCredentialComponent_CustodyLoadCurrentRejectsExpiry`

### Scenario 2 — Custody is bound to the exact session/owner/workload triple that created it

Names the confused-deputy property as its own acceptance criterion.

**Design anchors:** [ADR 0365](../adr/0365-broker-replacement-fresh-authority.md) ("Forks,
clear successors, and carryover sessions never copy custody").

**Acceptance:**
- AC2.1: A session with a genuinely different, well-formed owner or workload partition cannot
  recover or commit another session's staged/current custody, even when it supplies the
  correct recovery reference.
  - verify: `TestRecoverCredentialAttachmentRejectsWrongOwnerOrWorkloadPartition`
- AC2.2: Forked, cleared-successor, and carryover sessions never inherit a source session's
  custody reference, and each opens an independent broker attachment/logical session.
  - verify: `TestSuccessorSessionsNeverCopyCustody`
- AC2.3: A fresh ordinary `AttachSession` and a `RecoverCredentialAttachment` racing for the
  same logical session ID never both succeed; the loser gets a specific, non-corrupting
  rejection, and no session ends up straddling both a freshly-authorized and a recovered
  credential source.
  - verify: `TestFreshAttachRacingRecoverForSameSessionIDHasOneWinner`

### Scenario 3 — Profile change and custody revocation invalidate recovery deterministically, and cleanup always succeeds

An operator profile change or an explicit tombstone must each make custody permanently
unusable for authority purposes, be classified distinctly from transient failure, and never
block cleanup itself.

**Design anchors:** [ADR 0365](../adr/0365-broker-replacement-fresh-authority.md) decides the
*behavior* ("Invalidation removes host authority before best-effort custody tombstoning");
the specific `CONTINUITY_PROFILE_CHANGED`/`CONTINUITY_REVOKED` reason taxonomy in AC3.3/AC3.4
is this plan's own Human decisions #1/#2, not something ADR 0365's text itself names.

**Acceptance:**
- AC3.1: After an operator profile change, Commit and Recover against the old guard fail with
  `CONTINUITY_PROFILE_CHANGED`, and the host invalidates (tombstones) rather than retrying.
  - verify: `TestProfileChangedCommitInvalidatesInsteadOfRetrying`,
    `TestProcessContinuityAfterProfileChangeTombstonesOldGuard`
- AC3.2: Tombstone against a stale, no-longer-current profile still succeeds using the stored
  guard, and the resulting record can never be resurrected to `staged`/`current` by any later
  call with any guard.
  - verify: `TestTombstoneAfterProfileChangeIsPermanentAndNeverResurrected`
- AC3.3: Commit and Recover against already-tombstoned custody return the distinct
  `CONTINUITY_REVOKED` reason (never the transient `CONTINUITY_UNAVAILABLE` a CAS-retry
  exhaustion would return), and the host invalidates locally on receiving it rather than
  retrying indefinitely.
  - verify: `TestTombstonedCustodyReturnsRevokedReason`,
    `TestHostInvalidatesCustodyOnRevocation`
- AC3.4: A caller presenting a non-matching guard receives only the generic, opaque
  `CONTINUITY_UNAVAILABLE` — never `CONTINUITY_REVOKED` or `CONTINUITY_PROFILE_CHANGED` — so a
  caller without the correct guard cannot distinguish "tombstoned" from "never existed" or
  "wrong guard."
  - verify: `TestNonMatchingGuardNeverDisclosesCustodyState`

### Scenario 4 — Custody expiry never exceeds the original upstream grant, even across replacement and retries

**Design anchors:** [ADR 0365](../adr/0365-broker-replacement-fresh-authority.md) ("refresh
and recovery do not extend that expiry"), [ADR 0314](../adr/0314-mcp-broker-dcr-client.md)
for AC4.3.

**Acceptance:**
- AC4.1: Staging custody derives retention as the earliest of every configured provider's
  native ToolHive session expiry, fixed at Stage time regardless of how many times the
  process is replaced or how many recovery attempts occur before it.
  - verify: `TestCredentialCustodyStageDerivesEarliestNativeSessionExpiry`
- AC4.2: A full broker-kill-and-replace cycle followed by repeated recovery attempts never
  produces a session whose effective upstream access outlives the original grant's
  `SessionExpiresAt`.
  - verify: `TestBrokerReplacementRecoveryNeverOutlivesOriginalGrantExpiry`
- AC4.3: Recovery through a DCR-registered (`dcr` client-mode) upstream whose expired cached
  upstream access token requires ToolHive token validation or refresh fails closed when that
  path observes invalidated or rotated DCR client registration credentials — no recovered
  attachment is returned and no authenticated upstream call is made. This criterion does not
  require remote validation for an already-valid upstream bearer whose upstream validity has
  not yet been reevaluated; an unexpired token alone is outside this invalidation/rotation
  observation boundary.
  - verify: `TestDCRUpstreamRecoveryFailsClosedOnRotatedClientRegistration`

### Scenario 5 — The credential envelope fails closed on malformed or tampered input and derives a per-seal key

Each seal derives a key for one GCM encryption using a fresh random salt. The 256-bit salt makes accidental derived-key reuse negligible at expected volumes, but not impossible; acceptance tests verify the salt is part of derivation rather than claiming absolute uniqueness. The parser must reject malformed or tampered input without returning plaintext or revealing key-ring state, and validate encoding and bounds before decryption.

**Design anchors:** [ADR 0312](../adr/0312-confidential-toolhive-broker-client.md),
[ADR 0365](../adr/0365-broker-replacement-fresh-authority.md).

**Acceptance:**
- AC5.1: A well-formed envelope with a single flipped ciphertext or tag byte fails to open
  with the generic envelope-unavailable error, never partially decrypting.
  - verify: `TestCredentialEnvelope_TamperedCiphertextFailsClosed`
- AC5.2: An envelope whose version, algorithm, or key-ID segment doesn't match the active
  ring, or whose key-ID was present at seal time but has since been removed from
  configuration, fails to open without panicking and without exposing which segment was
  wrong.
  - verify: `TestCredentialEnvelope_UnknownOrRetiredKeyIDFailsClosed`
- AC5.3: Non-base64, truncated, or over-length nonce/salt/payload segments, and a value
  exceeding the maximum envelope size, are all rejected, and no decryption ever succeeds or
  partially succeeds for any such input.
  - verify: `TestCredentialEnvelope_MalformedSegmentsRejectedBeforeDecrypt`
- AC5.4: AAD mismatch (wrong session/provider/field binding) on an otherwise valid envelope
  fails to open, proving the field/session/provider binding is enforced at the codec layer,
  not only at the storage-decorator layer.
  - verify: `TestCredentialEnvelope_AADMismatchFailsClosed`
- AC5.5: Flipping only the stored per-seal salt on an otherwise valid envelope fails
  decryption, proving the salt is bound into key derivation rather than carried as an
  unauthenticated label. The HKDF design is recorded as an ADR 0365 sub-decision by AC7.5.
  - verify: `TestCredentialEnvelope_SaltIsBoundIntoKeyDerivation`
- AC5.6: An envelope sealed under key ID A opens successfully after the ring's `activeID`
  rotates to B, while A remains present in the ring; new seals made after that rotation use
  B, not A.
  - verify: `TestCredentialEnvelope_RetiredButPresentKeyStillOpensAfterRotation`
- AC5.7: Structured logs and diagnostics for every envelope-open failure (AC5.1–AC5.5),
  and for a `CONTINUITY_REVOKED` or `CONTINUITY_PROFILE_CHANGED` classification, contain no
  credential plaintext, envelope ciphertext, per-seal salt, or guard value — only the closed
  reason/outcome, per AGENTS.md's default secret-scrubbing rule.
  - verify: `TestCredentialContinuityDiagnosticsNeverDiscloseCredentialOrGuardMaterial`

### Scenario 6 — A lost credential-custody response never duplicates custody or recovery authority, and every reason classifies structurally

A transport client repeats Stage, Commit, Recover, and Tombstone after losing responses or
cancelling. This holds the credential-custody RPC family to the same replay-safety bar
lifecycle and Execute RPCs already meet. Because this proto slice is unshipped (does not
exist on `main`), there is no client/server version-skew case for AC6.5's taxonomy change
to cover — see the Interface contract's gRPC/protobuf line.

**Design anchors:** [ADR 0364](../adr/0364-bounded-singleton-mcp-broker-correctness.md)
Scenario 1 ("A lost broker response never causes a second process-local dispatch") for
AC6.1–AC6.8, AC6.10, AC6.11, AC6.13 — AC6.13 specifically mirrors that scenario's own
AC1.9 ("cancelling one active operation does not cancel a sibling") for the
credential-custody RPC family. AC6.9 and AC6.12 trace instead to this plan's own Human
decisions (items 6 and 1/2 respectively): capability disclosure and contract documentation.
AC6.14–AC6.15 trace to decision 7's injected-clock requirement; these criteria are not
replay-safety properties.

**Acceptance:**
- AC6.1: Cancelling the leader caller of a Stage or Recover request does not become the
  receipt's stored outcome. Concurrent waiters and exact retries before `attempt_deadline`
  receive the real result, not the leader's cancellation.
  - verify: `TestBrokerCredentialContinuity_LeaderCancellationDoesNotPoisonReceipt`
- AC6.2: After a lost Stage response, a host retry using the identical request ID and the
  *original* cached attempt deadline (not a freshly recomputed one) receives the same
  recovery reference, and exactly one staged custody row exists and is later committed — no
  orphaned row.
  - verify: `TestBrokerCredentialContinuity_HostStageRetryReplaysSameCustody`
- AC6.3: A duplicate Commit after a lost response succeeds without a second state
  transition, shown through a real client, server, and Process, not a direct
  storage-layer call.
  - verify: `TestBrokerCredentialContinuity_DuplicateCommitConvergesOverWire`
- AC6.4: A duplicate Tombstone of an already-tombstoned record succeeds, and a Tombstone
  of a reference that was never staged also succeeds — neither errors nor causes a second
  state transition — shown the same way, over the real wire.
  - verify: `TestBrokerCredentialContinuity_DuplicateAndMissingTombstoneConvergeOverWire`
- AC6.5: `INCARNATION_LOST`, `CONTINUITY_UNAVAILABLE`, `CONTINUITY_PROFILE_CHANGED`, and
  `CONTINUITY_REVOKED` are each accepted purely from their structured code and reason
  detail; changing the status message text alone, with the detail unchanged, never
  changes classification.
  - verify: `TestBrokerCredentialContinuity_ReasonsClassifyFromCodeNotMessage`
- AC6.6: An unknown or duplicate `BrokerErrorDetail` reason fails as an unclassified
  protocol error rather than silently passing as one of the four reasons in AC6.5.
  - verify: `TestBrokerCredentialContinuity_UnknownOrDuplicateReasonFailsClosed`
- AC6.7: A non-empty `dispatch_method` on a reason that doesn't require one (any of the
  four in AC6.5) fails as malformed rather than being silently accepted.
  - verify: `TestBrokerCredentialContinuity_UnexpectedDispatchMethodFailsClosed`
- AC6.8: Continuity receipt-capacity exhaustion, Recover's `MaxHandles`, and the
  recovered-attempts cap each return `CAPACITY_REACHED` with an empty `dispatch_method`,
  never the generic `CONTINUITY_UNAVAILABLE`. A reused request ID presented with a
  mismatched request body returns `InvalidArgument`.
  - verify: `TestBrokerCredentialContinuity_BoundedAdmissionUsesCapacityReason`
- AC6.9: `RecoverCredentialAttachmentResponse.credential_continuity` reports the serving
  process's real capability, never a hardcoded value; a subsequent enrollment on a recovered
  handle stages custody rather than silently skipping it when continuity is genuinely
  offered.
  - verify: `TestRecoveredAttachmentReportsRealCredentialContinuity`,
    `TestBrokerRecoveryStagesNewCustodyWhenContinuityIsOffered`
- AC6.10: A recovery attempt's internally-cached expected binding — reused via
  `AttachSessionExpectedBinding`, the same rollback path `Attach`'s own wire-level
  `expected_binding` field drives — rejects the recovered attachment if it would reattach
  under a different binding than the one cached for that attempt; the host never adopts a
  binding it didn't originally cache. (This exercises the pre-existing Attach-rollback
  mechanism through Recover, not a new field: none of the four credential-custody RPC
  messages carries `expected_binding` — it exists only on `AttachRequest`.)
  - verify: `TestBrokerCredentialContinuity_RecoveredBindingRollbackRejectsMismatch`
- AC6.11: Stage with a stale or absent broker incarnation is rejected before a receipt is
  reserved or a custody capability is reached. Commit, Tombstone, and Recover — incarnation-free
  by design, since recovery must work against a replacement incarnation — succeed regardless
  of incarnation.
  - verify: `TestBrokerCredentialContinuity_IncarnationBoundaries`
- AC6.12: Every credential-custody message and field documents its sensitivity, opaqueness,
  idempotency, and incarnation scope in the proto source, and the contract passes Buf lint
  and freshness checks.
  - verify: `TestInvariant_broker_continuity_contract_documented`
- AC6.13: Concurrent credential-custody RPCs racing on the same guard — Stage versus
  Recover, Recover versus Tombstone, and Commit versus Tombstone — each preserve one
  terminal outcome without a dangling attachment handle, an orphaned receipt, or a caller
  left waiting past `attempt_deadline`.
  - verify: `TestBrokerCredentialContinuity_ConcurrentCredentialCustodyRPCsPreserveOneTerminalOutcome`
- AC6.14: At the server RPC boundary, Stage, Commit, Recover, and Tombstone validate
  `attempt_deadline` against the injected `Server.WithClock` time; advancing a deterministic
  test clock past the deadline causes each RPC to reject it before invoking custody work.
  - verify: `TestBrokerCredentialContinuity_AllRPCsValidateAgainstInjectedClock`
- AC6.15: Continuity receipt expiry and sweeping use the same injected clock; advancing that
  clock past a receipt's expiry makes the receipt reclaimable without waiting for wall-clock
  time.
  - verify: `TestBrokerCredentialContinuity_ReceiptExpiryUsesInjectedClock`

### Scenario 7 — The credential-custody deployment surface renders schema-true and its ledger is current

Chart-managed credential-custody resources render exact, schema-valid Kubernetes objects
under the same gate every other production fixture passes, and the resource inventory
covering them is discoverable from an accurate ADR.

**Design anchors:** [ADR 0027](../adr/0027-cloud-native.md),
[ADR 0363](../adr/0363-single-replica-mcp-broker-topology.md),
[ADR 0365](../adr/0365-broker-replacement-fresh-authority.md),
[the architecture guide](../architecture.md).

**Acceptance:**
- AC7.1: `task deploy:check`'s fixture set includes a configuration with
  `broker.credentialStore.managedRedis.enabled: true` alongside an OAuth-configured MCP
  server, and the resulting `StatefulSet`/`Service`/`NetworkPolicy`/`PersistentVolumeClaim`
  pass `kubeconform -strict` alongside every other production fixture.
  - verify: extend `TestSingletonBrokerRemediation_Scenario4_DeploymentGateIsExecutable` (or a
    sibling) to assert a managed-Redis fixture is in the `ci/*.yaml` set `deploy:check`
    consumes.
- AC7.2: A KEK ring with more than one entry (a retired ID plus the active ID) renders
  exactly one projected Secret item per configured key at its documented path. (Whether the
  active key alone is actually selected for new seals is a Go-runtime property, not a
  chart-rendering one, and is covered by the runtime rotation acceptance criterion AC5.6.)
  - verify: `TestBrokerCredentialContinuity_MultiKeyRotationProjection`
- AC7.3: ADR 0027 List 1 gains a new row for the chart-managed credential Redis
  `StatefulSet`/`PVC`/`Service`/`NetworkPolicy`
  (`deploy/helm/mecak8s/templates/credential-redis.yaml`) — a Kubernetes workload distinct
  from the process-side resources already correctly inventoried at List 1 rows 79–83 (the
  protected Redis client, the encrypted-storage decorator and KEK ring, the custody core,
  the continuity receipt registry, and the recovery-attempt correlation map) — with owner
  (the Helm release), cleanup (pod removed with the release; PVC retained, deletion
  operator-owned), and restart disposition (decoupled from the broker Deployment's
  `Recreate` rollout, confirmed by AC7.6).
  - verify: inspection — ADR 0027 List 1 contains this row.
- AC7.4: List 1 row 81's List 2 cross-reference is corrected from "row 47" to row 48
  (Protected ToolHive credential custody records), row 82's is corrected from "row 48" to
  row 49 (Stage/Recover continuity receipt registry), and row 83 gains the List 2 row 50
  cross-reference (Host broker recovery request correlation) it currently lacks entirely.
  - verify: inspection — ADR 0027 List 1 rows 81/82/83 cite List 2 rows 48/49/50
    respectively.
- AC7.5: ADR 0365's Consequences section no longer states, unqualified, that "this ADR
  does not add managed-Redis resources" — a sentence the chart's own optional managed-Redis
  rendering makes misleading once AC7.3's row exists — and instead says what's actually
  true (the managed-Redis StatefulSet is optional, chart-rendered, and durable but not HA
  or zero-loss). Per the Human decisions section, ADR 0365 also gains its own "Resource
  ledger amendment to ADR 0027" section pointing at AC7.3/AC7.4's rows, matching the ADR
  0363/ADR 0364 precedent, and records the HKDF per-seal-key envelope design (Scenario 5,
  AC5.5) as its own sub-decision, so that design has an owning ADR rather than none. AC4.7
  in the singleton remediation plan is left unchanged, since it never claimed to cover
  these resources.
  - verify: inspection — ADR 0365's Consequences section and its own ledger-amendment
    section read as described.
- AC7.6: The chart-managed credential Redis's `Recreate` rollout of the sibling broker
  Deployment never restarts, evicts, or re-renders the credential-custody StatefulSet; a
  rendered diff across a broker-only image-digest bump touches no `credential-redis`
  resource.
  - verify: `TestBrokerCredentialContinuity_BrokerRestartDoesNotTouchCustodyStore`
- AC7.7: Rendering the chart with both `broker.credentialStore.managedRedis.enabled: true`
  and a non-empty `redis.address` or `redis.caSecret` fails the template render with the
  guard's own explicit message, rather than silently preferring one configuration or
  rendering an internally inconsistent chart.
  - verify: `TestBrokerCredentialContinuity_ManagedAndExternalRedisAreMutuallyExclusive`

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| Cross-process durable custody shared across multiple broker replicas | distributed broker phase | [ADR 0365](../adr/0365-broker-replacement-fresh-authority.md) and [ADR 0364](../adr/0364-bounded-singleton-mcp-broker-correctness.md) explicitly bound this to one incarnation |
| Cloud-KMS-backed KEK wrapping | later production-hardening decision | The HKDF redesign (Scenario 5, AC5.5) removes the old wrap step a KMS integration would have reused; a future integration needs its own envelope version |
| Non-destructive `PROFILE_CHANGED`/`CONTINUITY_REVOKED` handling under a rolling multi-replica config change | distributed broker phase | Today's topology is singleton-only ([ADR 0363](../adr/0363-single-replica-mcp-broker-topology.md)); destructive handling is correct for one replica |
| Credential-Redis egress `NetworkPolicy` hardening | later, if wanted | Chart-wide egress remains platform-owned per ADR 0363's stated scope cut; not a regression introduced here |

## Sequencing recommendation

Land Scenario 6 (wire-layer replay safety, error taxonomy, the two retry bugs) first —
Scenario 3's host-facing tombstone behavior and Scenarios 1/2/4's OAuth guarantees all assume
a correct wire layer under retry. Specifically, Scenario 3's AC3.3/AC3.4 depend on the
`BROKER_ERROR_REASON_CONTINUITY_REVOKED` enum value added in Scenario 6's Interface contract
— land Scenario 6's proto change no later than Scenario 3. Then Scenario 5 (the envelope/HKDF
change), since it alters the on-disk format: its existing AC5.1–AC5.4 tests must be rewritten
— not merely supplemented — against the new segment layout, in the same change that adds
AC5.5's new test. Scenario 7 (docs/ledger fix and the CI fixture) is independent of the code
changes and can land in parallel with either.

## Definition of done

1. `task generate`, `task lint`, and `task test` pass with the proto, error-reason, and field
   changes above landed.
2. Focused race-enabled tests for `internal/adapter/mcpbrokergrpc`, `internal/adapter/mcpbroker`,
   `internal/mcpbroker`, `internal/adapter/server`, and `deploy/helm/mecak8s` pass.
3. `task docs` passes for the ADR 0365/ADR 0027 amendment.
4. `task deploy:check` passes, including the new managed-Redis fixture.
5. The named tests above are grep-locatable and green.
6. ADR 0365's status moves from Proposed to Accepted once every point above holds, mirroring
   how ADR 0364 was finalized alongside its own acceptance plan.

## Deferred decisions and known risks

- **Destructive revocation under future replication.** `PROFILE_CHANGED` and
  `CONTINUITY_REVOKED` both destroy custody outright, which is correct for a singleton but
  would need a non-destructive skew reason or a profile epoch before a distributed phase
  introduces multiple replicas that could disagree on the current profile mid-rollout.
- **KMS handoff cost.** Moving to per-seal HKDF derivation (Scenario 5) forecloses a simple
  future handoff of the wrap step to a cloud KMS; that would need a new envelope version
  regardless of what ships now.
- **Redis egress posture.** The credential-Redis `NetworkPolicy` is ingress-only by design,
  consistent with the chart's stated egress-is-platform-owned posture — not a gap introduced
  by this plan, but worth another look if that posture ever changes chart-wide.

## Exit criteria

When every point under *Definition of done* holds, ADR 0365 is Accepted and the
credential-custody/continuity feature has acceptance-plan and test-plan coverage matching
every other RPC family in the broker's protocol.
