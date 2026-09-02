# Listener-scoped workspace authority — acceptance plan

**Phase:** workspace-selection security hotfix
**Status:** draft, superseded 2026-09-02 by [ADR 0290](../adr/0290-server-owned-session-placement.md). This historical plan is retained for context only; its listener-scoped path-authority proofs were deleted by the clean break and are not strict traceability claims.
**ADR:** [ADR 0237](../adr/0237-listener-scoped-workspace-authority.md) — client paths are authority only on a local deployment; network deployments assign the root.
**Accumulator branch:** `acc/listener-scoped-workspace-authority` (off `main`).

The smallest set of work that prevents a remote API caller from choosing the
server's filesystem root, while preserving the embedded and loopback developer
workflow. It proves the boundary at the common Service creation path, not in a
particular wire handler, so gRPC and HTTP/SSE agree.

The plan deliberately makes the single-root network case safe now. It does not
turn path strings into a remote multi-workspace authorization protocol.

## Why these scope cuts

- [ADR 0237](../adr/0237-listener-scoped-workspace-authority.md) — a network
  deployment assigns an operator-configured root and requires an empty client
  workspace rather than comparing request paths.
- [ADR 0032](../adr/0032-worktree-binding.md) — client-selected worktrees are
  an operator-driven mecatui workflow; they remain available in the local
  trust domain.
- [ADR 0095](../adr/0095-root-aware-project-trust.md) — workspace trust and
  project ingestion remain a separate root-aware decision; this plan does not
  weaken or repurpose that trust fold as path authorization.

## In scope — 5 scenarios, in implementation order

### Scenario 1 — A network service assigns its filesystem root

The server is the only layer that sees all create paths: gRPC and HTTP/SSE
already delegate to the same Service. A composition-injected workspace
authority policy therefore belongs in `internal/adapter/server`, while each
`cmd/` root selects its policy from listener topology. This keeps socket and
flag knowledge out of the server adapter, consistent with the composition
boundary in [`architecture.md`](../architecture.md) and the deployment decision
in [ADR 0237](../adr/0237-listener-scoped-workspace-authority.md).

**Acceptance:**
- AC1.1: With server-assigned filesystem authority and a configured deployment
  root, a default-profile `CreateSession` request whose workspace is empty
  creates a session rooted at that configured root.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario1_EmptyWorkspaceUsesConfiguredRoot`
- AC1.2: With server-assigned filesystem authority, every non-empty requested
  workspace—including whitespace, relative, traversal, and an exact textual
  match of the configured root—is rejected as `InvalidArgument` with a message
  explaining that the deployment assigns the workspace. Rejection occurs before
  path cleaning, filesystem access, trust evaluation, or environment creation;
  the value is neither compared with nor silently replaced by the configured
  root.
  - verify: `TestInvariant_server_assigned_workspace_requires_empty_request`
- AC1.3: An explicit no-FS request remains valid with an empty workspace under
  server-assigned authority and is rejected with a non-empty workspace under
  the existing no-FS contract.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario1_ServerAssignedProfileMatrix`
- AC1.4: The same acceptance and rejection behavior is observed through both
  the gRPC and HTTP session-create surfaces, because they share the Service
  policy.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario1_GrpcAndHTTPAgree`
- AC1.5: A filesystem-bearing network configuration with no authoritative
  workspace fails configuration validation before either API listener starts.
  A no-FS deployment remains valid without a filesystem root.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario1_NetworkFilesystemRequiresConfiguredRoot`

---

### Scenario 2 — Local developer workflows retain operator-selected workspaces

A loopback-only `mecated` and embedded `mecatui` share a local operator trust
domain. They retain the existing ability to create a session in an absolute
client-selected root, including the worktree workflow described by
[ADR 0032](../adr/0032-worktree-binding.md). The server policy is explicit,
not inferred inside the Service from an address string.

**Acceptance:**
- AC2.1: Under client-selectable workspace authority, an absolute workspace
  supplied by the local client creates a session rooted at that workspace even
  when it differs from the deployment default.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario2_LocalClientWorkspacePreserved`
- AC2.2: A loopback-only `mecated` configuration selects client-selectable
  authority. Any non-loopback, wildcard, or mixed public/loopback gRPC or
  HTTP/SSE listener selects server-assigned authority using the existing
  fail-closed parsed-address classifier, including IPv6 loopback coverage.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario2_MecatedPolicyFollowsAPIListenerTopology`
- AC2.3: A reverse-proxied loopback deployment can explicitly select
  server-assigned authority; an explicit policy selection overrides the
  topology-derived local default.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario2_ExplicitAuthorityOverridesTopology`
- AC2.4: Existing worktree discovery and session binding still accept a
  client-selected local workspace, while `ListWorktrees` remains inert for a
  server-assigned request whose workspace is empty.
  - verify: `TestADR_0032_WorktreeBindingRemainsClientSelectableOnly`

---

### Scenario 3 — Remote mecatui does not disclose or request its cwd

`mecatui connect` already distinguishes loopback and non-loopback targets for
credential transport. It must use that same client-side topology fact only to
avoid sending a local workspace path. The server remains authoritative: this
client change is privacy and usability hardening, not the access-control gate.
This separation follows [ADR 0237](../adr/0237-listener-scoped-workspace-authority.md).

**Acceptance:**
- AC3.1: A non-loopback `mecatui connect` invocation with no explicit
  workspace sends an empty workspace in `CreateSession`; it does not resolve
  or transmit the client cwd.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario3_RemoteConnectSendsEmptyWorkspace`
- AC3.2: Embedded and loopback connect paths still resolve an omitted
  workspace to the local cwd and send that absolute path.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario3_LocalConnectPreservesWorkspaceDefault`
- AC3.3: A non-loopback `mecatui connect` invocation with an explicit workspace
  is rejected locally before it resolves a cwd or sends `CreateSession`. A
  stale or non-mecatui client remains subject to the server's
  `InvalidArgument` rejection; neither path may create a client-selected
  workspace.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario3_RemoteExplicitWorkspaceIsRejectedLocally`

---

### Scenario 4 — mecak8s is file-less by default

A `mecak8s` pod is a network deployment whose durable state is Redis, not a
workspace volume. The existing no-FS profile is the honest default: it creates
a no-FS environment and omits filesystem tools. This follows the no-FS session
profile contract in [`AGENTS.md`](../../AGENTS.md) and the cloud/no-root
posture in [ADR 0032](../adr/0032-worktree-binding.md).

**Acceptance:**
- AC4.1: A new `mecak8s` session that omits a profile uses the no-FS profile
  and succeeds with an empty workspace.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario4_Mecak8sDefaultsToNoFS`
- AC4.2: The default mecak8s session catalog excludes `Read`, `Edit`, `Write`,
  `Grep`, `Glob`, `Bash`, `Parallel`, and `SkillDraft`, while retaining the
  documented no-FS-safe tools.
  - verify: `TestNoFSCatalogProfile`
- AC4.3: mecak8s accepts an explicit no-FS profile and treats the wire's empty
  (omitted/default) profile as no-FS. It rejects every non-no-FS profile and
  every non-empty workspace, so a caller cannot bypass the file-less deployment
  default.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario4_Mecak8sRejectsFilesystemProfileAndWorkspace`
- AC4.4: The offline mecak8s fixture creates and runs a default session
  without using the container root as its agent workspace.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario4_Mecak8sFixtureRunsNoFS`

---

### Scenario 5 — Stored and internal state cannot revive an unauthorized root

Session snapshots and scheduled-fire specifications are not independent grants
of filesystem authority. Server-assigned authority has five concrete paths:

1. external creation through the shared `Service.CreateSession` path used by
   gRPC and HTTP/SSE;
2. persisted-session run entry and rehydration through `StartRunContent`;
3. scheduled-fire session creation;
4. legacy-session adoption through `PreflightSessionAdoption` and
   `AdoptSession`; and
5. composition-created session overrides, which may provide only the configured
   deployment environment.

There is no separate carryover constructor. A new creation path must apply the
same authority policy before it creates or resolves an environment. This keeps
rehydration and adoption from becoming post-restart or migration bypasses.

For stored filesystem roots, identity is deliberately lexical and
fail-closed: both the configured root and the stored value must be non-empty,
absolute, clean paths, and their cleaned strings must be equal. A relative
path, traversal spelling, symlink alias, or a root changed in
configuration is not equivalent and fails before filesystem access. The
comparison is only for already-stored server state; client request values still
remain uninspected non-empty rejections under AC1.2.

**Acceptance:**
- AC5.1: Resuming or rehydrating a persisted filesystem session whose stored
  root is not the configured root under the stored-root identity contract
  fails with a clear failed precondition before any workspace factory,
  environment resolver, or filesystem access receives that stored path.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario5_PersistedOffRootSessionFailsClosed`
- AC5.2: The persisted-root identity contract rejects a relative path,
  traversal spelling, symlink alias, or a root made stale by a configuration
  change. It accepts only the same cleaned absolute configured root without
  resolving symlinks.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario5_PersistedRootIdentityIsLexicalAndFailClosed`
- AC5.3: A scheduled fire under server-assigned authority has this profile
  matrix: no-FS schedules carry no workspace; a filesystem schedule is stamped
  with the configured root by the server, not a stored or tool-supplied client
  path; an explicit filesystem schedule in the no-FS mecak8s deployment is
  rejected. A legacy off-root schedule fails before claim, session creation,
  environment creation, or filesystem access and records an operator-visible
  error.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario5_ScheduledFireCannotReviveOffRoot`
- AC5.4: `CreateSession`, persisted-session run entry/rehydration, scheduled
  fires, adoption preflight/adoption, and composition-created environment
  overrides each apply server-assigned authority or use the configured
  deployment environment. No path attaches a client- or snapshot-supplied
  off-root filesystem session.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario5_AllCreationPathsRespectAuthority`

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| Remote callers selecting among multiple workspaces | Scoped resource grants / filesystem-service design | [ADR 0237](../adr/0237-listener-scoped-workspace-authority.md) |
| Per-request filesystem verb grants, mounts, or leases | Scoped resource grants | [ADR 0237](../adr/0237-listener-scoped-workspace-authority.md) |
| Redesigning trust, project ingestion, or permission posture | Existing root-aware trust model | [ADR 0095](../adr/0095-root-aware-project-trust.md) |
| Hardening a deliberately configured container-root workspace or Kubernetes secret mounts | Separate deployment-hardening issue | [ADR 0237](../adr/0237-listener-scoped-workspace-authority.md) |

## Cross-cutting deliverables

- Update the living deployment/API documentation and `user-docs/` to state
  which deployment topologies accept a client workspace, the server-assigned
  request contract, and mecak8s's default no-FS behavior.
- Regenerate `llms.txt`; do not hand-edit it.
- Preserve the engine boundary: this is Service/composition/client work and
  must not widen `engine/port` or add a filesystem policy to the agent loop.

## Sequencing recommendation

First add the Service policy and its direct tests, then wire listener topology
and startup validation in the two network composition roots. Next adapt the
mecatui connect default and finally make mecak8s's omitted profile resolve to
no-FS. Finish with HTTP/gRPC and fixture coverage plus documentation.

## Named tests landing in this plan

- `TestInvariant_server_assigned_workspace_requires_empty_request`
- `TestListenerScopedWorkspaceAuthority_Scenario1_EmptyWorkspaceUsesConfiguredRoot`
- `TestListenerScopedWorkspaceAuthority_Scenario1_ServerAssignedProfileMatrix`
- `TestListenerScopedWorkspaceAuthority_Scenario1_GrpcAndHTTPAgree`
- `TestListenerScopedWorkspaceAuthority_Scenario1_NetworkFilesystemRequiresConfiguredRoot`
- `TestListenerScopedWorkspaceAuthority_Scenario2_LocalClientWorkspacePreserved`
- `TestListenerScopedWorkspaceAuthority_Scenario2_MecatedPolicyFollowsAPIListenerTopology`
- `TestListenerScopedWorkspaceAuthority_Scenario2_ExplicitAuthorityOverridesTopology`
- `TestADR_0032_WorktreeBindingRemainsClientSelectableOnly`
- `TestListenerScopedWorkspaceAuthority_Scenario3_RemoteConnectSendsEmptyWorkspace`
- `TestListenerScopedWorkspaceAuthority_Scenario3_LocalConnectPreservesWorkspaceDefault`
- `TestListenerScopedWorkspaceAuthority_Scenario3_RemoteExplicitWorkspaceIsRejectedLocally`
- `TestListenerScopedWorkspaceAuthority_Scenario4_Mecak8sDefaultsToNoFS`
- `TestListenerScopedWorkspaceAuthority_Scenario4_Mecak8sRejectsFilesystemProfileAndWorkspace`
- `TestListenerScopedWorkspaceAuthority_Scenario4_Mecak8sFixtureRunsNoFS`
- `TestListenerScopedWorkspaceAuthority_Scenario5_PersistedOffRootSessionFailsClosed`
- `TestListenerScopedWorkspaceAuthority_Scenario5_PersistedRootIdentityIsLexicalAndFailClosed`
- `TestListenerScopedWorkspaceAuthority_Scenario5_ScheduledFireCannotReviveOffRoot`
- `TestListenerScopedWorkspaceAuthority_Scenario5_AllCreationPathsRespectAuthority`

## Definition of done

1. `task lint` and `task test` pass.
2. `task docs` passes, regenerating `llms.txt` and running the strict link gate.
3. `task api:check` passes; no engine exported API change is expected.
4. `task ac-trace-strict` passes when this plan becomes `landed`.
5. The named tests resolve and pass, including offline gRPC, HTTP, mecated
   topology, mecatui configuration, and mecak8s fixture coverage.
6. `go run ./cmd/mecademo` still prints a full offline session.
7. `task site:build` passes after the operator-facing docs update.

## Deferred decisions and known risks

- **Scoped multi-workspace grants.** A remote deployment that must let callers
  choose among repositories needs opaque, operator-issued grants rather than a
  path-comparison exception; this plan intentionally supplies only the
  single-root case.
- **Container-root and secret-mount hardening.** Server-assigned authority
  prevents a caller from choosing `/`; it cannot make an operator-configured
  root `/` safe. Kubernetes volume and mount design remains a separate
  deployment-hardening item.
- **Topology behind proxies.** Bind topology selects the default only. An
  operator exposing a loopback listener through a proxy must explicitly choose
  server-assigned authority; automatic proxy-provenance discovery is out of
  scope.

## Exit criteria

When every point under *Definition of done* holds on the accumulator, this
plan is satisfied.
