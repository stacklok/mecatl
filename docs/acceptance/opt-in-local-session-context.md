# Opt-in local session context — acceptance plan

**Phase:** privileged local-client workspace-context projection
**Status:** draft, 2026-09-03. Derived from the proposed ADR 0296 decision and issue #1057.
**Issue:** [stacklok/mecatl#1057](https://github.com/stacklok/mecatl/issues/1057).
**ADR:** [ADR-0296](../adr/0296-opt-in-local-session-context.md) — a separately registered, opt-in local-only workspace-root observation.
**Accumulator branch:** `acc/opt-in-local-session-context` (off `main`).

The smallest set of work that lets embedded Mecatui give an active session's exact
local root to its status command, without reopening a public filesystem-path or
placement-authority surface. It proves the root comes only from owner-authorized,
exact server reattachment and that a delayed lookup cannot affect another session.

The plan is scenario-first because acceptance is about what the running harness can
demonstrate, not which packages exist on disk.

## Why these scope cuts

- [ADR-0296](../adr/0296-opt-in-local-session-context.md) keeps this deliberately
  narrow: one observational gRPC operation, separate from `HarnessService`, with no
  HTTP route, placement selector, or environment-reference projection.
- [ADR-0291](../adr/0291-server-owned-session-placement.md) keeps exact,
  server-owned placement and its public no-path rule. This capability must not become
  a creation or successor-placement mechanism.
- Standalone `mecated` registration is deferred. Starting embedded Mecatui is the
  explicit v1 opt-in: it registers the service only on its per-instance, owner-only
  Unix socket. This plan introduces no standalone startup flag or listener-admission
  policy.

## In scope — 4 scenarios, in implementation order

### Scenario 1 — Privileged local-only gRPC contract

A local client can call one dedicated `LocalSessionContextService` operation with a
session ID and receive only `workspace_path`. The ordinary `HarnessService`, HTTP
routes, public placement metadata, event projections, and client session inventory
remain path-free, as required by [ADR-0296](../adr/0296-opt-in-local-session-context.md)
and [ADR-0291](../adr/0291-server-owned-session-placement.md).

**Acceptance:**
- AC1.1: Generated gRPC bindings expose `LocalSessionContextService` separately from
  `HarnessService`, with exactly `GetLocalSessionContext(session_id)` and a response
  containing only `workspace_path`.
  - verify: `TestADR_0296_PrivilegedProtoSurfaceIsNarrow`
- AC1.2: A server that does not register the privileged service returns gRPC
  `UNIMPLEMENTED` for its method; no empty success response implies unavailability.
  - verify: `TestADR_0296_UnregisteredServiceIsUnimplemented`
- AC1.3: The public Harness/HTTP/client placement projections accept and return no
  physical root or exact environment reference after the privileged contract exists.
  - verify: `TestADR_0296_PublicPathInventoryRemainsClosed`

---

### Scenario 2 — Exact, authorized local-root observation

For an authorized caller, the server loads the persisted session ref and reattaches
through the existing placement provider before observing the rooted workspace. This
uses the server-owned placement seam rather than metadata, a ref parser, current
default workspace, or client worktree discovery, preserving the runtime-affinity
contract in [ADR-0211](../adr/0211-execution-environment-runtime-seam.md) and exact
reattachment rule in [ADR-0291](../adr/0291-server-owned-session-placement.md).

**Acceptance:**
- AC2.1: An owned local default session returns the root of the environment that exact
  reattachment produced, not a configured/default root inferred independently.
  - verify: `TestADR_0296_LocalContextUsesExactReattachment`
- AC2.2: An owned selected-worktree successor returns its reattached worktree root,
  including when it differs from the embedded daemon's launch workspace.
  - verify: `TestADR_0296_LocalContextReturnsSelectedWorktreeRoot`
- AC2.3: An unknown session and a session owned by another caller both return
  `NOT_FOUND` and disclose neither a path nor placement detail.
  - verify: `TestADR_0296_LocalContextOwnerIsolation`
- AC2.4: An owned no-FS or non-local environment returns `FAILED_PRECONDITION` with
  no root; a failed reattachment due to provider unavailability returns `UNAVAILABLE`
  with no root.
  - verify: `TestADR_0296_LocalContextFailsClosedForIneligibleOrUnavailablePlacement`
- AC2.5: A ref/binding mismatch, nil workspace, or empty local root returns
  `FAILED_PRECONDITION` with no path and does not fall back to metadata, ref IDs, or
  the current default workspace.
  - verify: `TestADR_0296_LocalContextRejectsInvalidReattachment`
- AC2.6: Client-facing `NOT_FOUND`, `FAILED_PRECONDITION`, and `UNAVAILABLE` status
  messages exclude physical roots, exact refs, provider names, and wrapped backend
  errors, while private diagnostics retain only the existing permitted detail.
  - verify: `TestADR_0296_LocalContextErrorsDoNotLeakPathOrEnvironmentRef`

---

### Scenario 3 — Embedded private-listener registration

Embedded Mecatui registers the privileged service only on the private Unix-domain
socket it owns. The service remains absent from normal server assembly and has no
HTTP gateway registration, honoring ADR-0296's local-client transport boundary and
the path-surface inventory of [ADR-0291](../adr/0291-server-owned-session-placement.md).

**Acceptance:**
- AC3.1: Starting embedded Mecatui is the explicit v1 opt-in and registers the
  local-context service only alongside its per-instance UDS gRPC listener whose parent
  directory is owner-only; the embedded local client can obtain an owned local session's
  root through that RPC.
  - verify: `TestADR_0296_EmbeddedMecatuiServesLocalSessionContext`
- AC3.2: The registration seam refuses an ineligible listener and cannot register the
  privileged service on TCP, loopback, HTTP, or a UDS without the embedded private
  listener's ownership/permission guarantee.
  - verify: `TestADR_0296_LocalContextRequiresEmbeddedPrivateListener`
- AC3.3: Standard `mecated` startup does not register or advertise the service, and
  its ordinary TCP/HTTP endpoints do not provide an equivalent path-returning route.
  - verify: `TestADR_0296_StandaloneMecatedDoesNotExposeLocalSessionContext`
- AC3.4: Registering the privileged service does not retain a session environment or
  placement binding beyond the call; any provisional provider resource is closed.
  - verify: `TestADR_0296_LocalContextClosesProvisionalBinding`

---

### Scenario 4 — Status commands use the matching active local root

Mecatui's status-line source retrieves the privileged context asynchronously, caches
it by active session ID, and applies it only if that session remains active. A direct
status command receives the matched root as its process CWD only; templates and the
status-command JSON input remain unchanged. This preserves the split between safe
placement metadata and privileged local observation in [ADR-0296](../adr/0296-opt-in-local-session-context.md)
and avoids treating labels as path basenames.

**Acceptance:**
- AC4.1: After local-context lookup for an active local session succeeds, a direct
  status command runs with that session root as CWD, including a selected worktree;
  the root appears in neither command arguments, environment, JSON stdin, templates,
  nor rendered status state.
  - verify: `TestADR_0296_StatusCommandReceivesRootOnlyAsCWD`
- AC4.2: Lookup is invalidated and refreshed when Mecatui creates, adopts, clears,
  forks, or switches its active session; a late result for an older session cannot
  change the current status command CWD.
  - verify: `TestADR_0296_StatusContextDiscardsStaleSessionResult`
- AC4.3: When the privileged service is unavailable or yields no eligible root, the
  status command preserves its existing launch-directory fallback without exposing a
  root in status templates or JSON input.
  - verify: `TestADR_0296_StatusContextUnavailableRetainsFallbackAndNoProjection`

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| Standalone `mecated` flag, UDS admission, and privileged-service registration | a separately approved follow-up | operator deferred during ADR-0296 implementation planning |
| HTTP gateway route or universal Harness operation | never in this capability | [ADR-0296](../adr/0296-opt-in-local-session-context.md) |
| Creation-time placement request, workspace inventory, or public selector | separate placement-selection ADR | [ADR-0296](../adr/0296-opt-in-local-session-context.md) |
| Roots for remote, memory, ACP, no-FS, or future environment kinds | separately reviewed provider-locality capability | [ADR-0296](../adr/0296-opt-in-local-session-context.md) |
| Root in StatusML/templates or status-command JSON | demonstrated future disclosure need | planning decision for this capability |

## Sequencing recommendation

Establish the isolated generated contract and server exact-reattachment behavior
first. Then prove embedded-only registration and absence from ordinary server/HTTP
assembly. Finally wire the Mecatui client and status refresh state against the
already-tested RPC. Keep all root-bearing values within the server, embedded/client
transport, and process-CWD boundary.

## Named tests landing in this plan

- `TestADR_0296_PrivilegedProtoSurfaceIsNarrow`
- `TestADR_0296_UnregisteredServiceIsUnimplemented`
- `TestADR_0296_PublicPathInventoryRemainsClosed`
- `TestADR_0296_LocalContextUsesExactReattachment`
- `TestADR_0296_LocalContextReturnsSelectedWorktreeRoot`
- `TestADR_0296_LocalContextOwnerIsolation`
- `TestADR_0296_LocalContextFailsClosedForIneligibleOrUnavailablePlacement`
- `TestADR_0296_LocalContextRejectsInvalidReattachment`
- `TestADR_0296_LocalContextErrorsDoNotLeakPathOrEnvironmentRef`
- `TestADR_0296_EmbeddedMecatuiServesLocalSessionContext`
- `TestADR_0296_LocalContextRequiresEmbeddedPrivateListener`
- `TestADR_0296_StandaloneMecatedDoesNotExposeLocalSessionContext`
- `TestADR_0296_LocalContextClosesProvisionalBinding`
- `TestADR_0296_StatusCommandReceivesRootOnlyAsCWD`
- `TestADR_0296_StatusContextDiscardsStaleSessionResult`
- `TestADR_0296_StatusContextUnavailableRetainsFallbackAndNoProjection`

## Definition of done

1. `task lint` and `task test` pass.
2. `task generate` regenerates protobuf bindings and `llms.txt`.
3. `task docs` passes the strict documentation/link gate.
4. `task site:build` passes after the embedded Mecatui behavior is documented in
   `user-docs/`.
5. `task ac-trace-strict` passes after this plan is `landed`.
6. The named `TestADR_0296_*` tests are green and grep-locatable.
7. `go run ./cmd/mecademo` still prints a full offline session.

## Deferred decisions and known risks

- **Standalone privileged listener admission.** A normal `mecated` deployment remains
  unregistered in this plan. Its explicit opt-in/configuration and proof of a
  single-local-client transport need a dedicated follow-up decision.
- **Open environment kinds.** V1 recognizes only `session.EnvKindLocal`; a future
  provider must not receive path disclosure by merely returning a rooted workspace.
- **Status input schema.** The root reaches only the direct command CWD. Adding it to
  status JSON or templates requires a concrete use case and separate disclosure
  review.

## Exit criteria

When every point under *Definition of done* holds on the accumulator, this plan is
satisfied.
