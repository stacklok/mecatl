# Kubernetes session VM isolation — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — it strengthens the native Kubernetes execution security boundary from a shared node kernel to one VM per environment. It also changes the retained-environment lifecycle when the last reference is removed, and makes mecak8s refuse every host command-execution path while execution is enabled.
**Decision record:** [ADR 0373](../adr/0373-kubernetes-session-vm-isolation.md)
**Phase:** native Kubernetes execution follow-up (runtime and user isolation for the Kubernetes development-flow agent)
**Status:** proposed, 2026-10-01. Directing-human decisions recorded in the authoring conversation.
**Delivery:** Split. The isolation claim, the lifecycle change, the startup refusals, and the qualification lane need human review before implementation.
**Expected tasks:** deferred to orchestration
**Issue:** none yet; open a tracking issue on request.
**Plan PR:** [stacklok/mecatl#2052](https://github.com/stacklok/mecatl/pull/2052)
**Approved baseline:** absent until approved

This plan follows the [native Kubernetes execution plan](native-kubernetes-execution.md) and
[ADR 0364](../adr/0364-native-kubernetes-execution.md). The smallest demonstrable behavior has three parts:

- **Own VM per session.** Each mecak8s session with its own execution environment gets an executor
  that runs in its own Kata VM, with a distinct guest kernel.
- **Remote-only commands.** The agent can execute commands only in that VM, through the existing
  `ExecutionProviderService` gRPC API, and never inside the mecak8s Pod.
- **VM stops on delete.** Deleting the session that holds an environment's last reference stops
  the VM and retains the workspace PVC.

The plan also completes the parent plan's five pending end-to-end journeys under the Kata runtime.
An automated Kind+Kata lane proves all of it on KVM-capable Linux CI. The existing `runc` lane
remains the macOS path.

The owning current-behavior page is [execution environments](../../user-docs/features/execution-environments.md).
Planned behavior stays here until implemented.

## Human decisions

- [x] Isolation strength — Decision: a true VM per session (Kata RuntimeClass) is required; gVisor or a container boundary is insufficient.
- [x] Plan shape — Decision: a new follow-up plan to `native-kubernetes-execution`, not an amendment of it.
- [x] Qualification venue — Decision: Kind with Kata on Linux hosts that expose `/dev/kvm`, including CI; macOS is covered by the existing `runc` Kind lane only.
- [x] API boundary — Decision: the existing `ExecutionProviderService` gRPC API, the engine and Harness APIs, and tool schemas are unchanged. The agent may execute commands only on the runtime Pod through that API, never in the mecak8s Pod.
- [x] VM lifetime — Decision: stop the VM when the session is deleted, retaining the PVC. Temporary persistence for resumption and warm VM pools are post-POC decisions.
- [x] Agent-definition hooks — Decision: with execution enabled, mecak8s refuses at startup any loaded agent definition that carries `hooks:`.
- [x] Parent journeys — Decision: complete all five pending journeys from the parent plan, run under the Kata runtime.
- [x] Scope — Decision: GitHub/Git credentials, the coding workload image, and hostname-based egress are out of scope.
- [x] Kata hypervisor for qualification — Decision: `kata-clh` (Cloud Hypervisor) is the qualified handler for the fixture RuntimeClass and the documented VM-isolating profile; `kata-qemu` is not qualified by this plan, and `kata-fc` is rejected because it needs the devmapper snapshotter.
- [x] Last-reference stop semantics — Decision: reuse the existing retirement lifecycle (`type: RetireEnvironment`). The environment ends `Retired` with its PVC retained, and keeps its `maxEnvironments` slot, PVC and quota count until an operator calls `DeleteRetiredEnvironment`. A resumable stopped state and automatic PVC deletion are not part of this plan.
- [x] Explicit `--shell` with `--execution-enabled` — Decision: reject at startup with `--shell has no effect with --execution-enabled`.

## Interface contract

- **gRPC / protobuf:** None — `mecatl.execution.v1.ExecutionProviderService` is unchanged: no method, message, field, number or error-classification change. The last-reference stop is controller-internal. It reacts to the existing `ConfirmReferenceDelete`, `AbortReference` and `ReleaseReference` outcomes. After a stop, `AttachEnvironment`, `AcquireRun`, `Files` and `StartCommand` against the environment return their existing not-ready/retired precondition classifications.
- **Exported Go APIs / interfaces:** None — `engine/tool.Environment`, `CommandRunner`, Harness, `server.PlacementProvider` and related host interfaces are unchanged. All changes are in `internal/` packages (`internal/app`, `internal/adapter/executioncontroller`, `cmd/mecak8s`) and test fixtures; `task api:check` snapshots are unchanged.
- **Tool schemas:** None — Shell, ShellStatus and file-tool schemas and the remote-execution posture note are unchanged. Delegation, schedule and SkillDraft tools remain absent in remote sessions, as today.
- **CLI / config:** Two mecak8s startup refusals under `--execution-enabled`, no provider/profile/chart changes, and a new Kata qualification task, fixture and CI job:
  - **mecak8s with `--execution-enabled`:**
    - An explicitly set `--shell` fails startup with `--shell has no effect with --execution-enabled`.
    - Any agent definition loaded from `--agents-dir`, conventional directories or the agent-source driver that declares `hooks:` fails startup with a named error that includes the agent name and no hook values.
    - The defaults of `--shell` and `--no-shell` and their behavior without `--execution-enabled` are unchanged.
  - **Execution provider:** no new flags, profile fields or chart values. A VM-isolating profile sets the existing `runtimeClassName` to a scheduling-free Kata RuntimeClass, which the operator creates. The chart does not install a RuntimeClass.
  - **Qualification:**
    - New task `e2e:k8s:execution:kata`, which is not part of `task test`.
    - New fixture files under `deploy/mecatl-execution-kind/kata/`: a Kind config that mounts `/dev/kvm`, a digest-pinned `kata-deploy` install, a scheduling-free `mecatl-kata` RuntimeClass with handler `kata-clh` and declared `overhead`, and Kata execution values.
    - New CI job `execution-kata-e2e` on `ubuntu-24.04`, path-gated like `execution-production-e2e`.
- **Events / persistence:** No new events or CRD fields; the stop reuses the retirement record:
  - **No new session events.** Session deletion events are unchanged. `ExecutionEnvironment` stays at schema version 2 with no new CRD fields.
  - **Last-reference stop record.** The stop writes the existing `status.lifecycleOperation` with the existing enum value `type: RetireEnvironment`, and uses the existing `terminationProof`, `Retired` and `ExecutorTerminated` fields. Its `id` is `last-reference:` followed by a digest of the environment name and immutable revision, so controller restarts converge on one operation. The CRD `lifecycleOperation.type` enum and `phase` enum are unchanged, and no CRD-before-controller upgrade ordering is introduced.
  - **Upgrade sweep.** On upgrade, environments that are already active with an empty reference set are stopped by the same reconciliation.
- **Security / authority:** A per-environment kernel boundary and no host execution in mecak8s; all other authority unchanged:
  - **Isolation.** Separate environments share no kernel when the profile names a Kata RuntimeClass. The controller verifies on every Pod read that `runtimeClassName` and `spec.overhead` match the profile RuntimeClass, and fails closed on mismatch through existing RuntimeClass `get` RBAC; no new RBAC.
  - **No host execution in mecak8s.** With execution enabled, mecak8s constructs no host `CommandRunner`, and no hook runner with configured commands, for any session placement.
  - **Unchanged.** OIDC owner binding, creator `clientHash`, Ed25519 grants, run claims, fencing, credential-free workload requests, default-deny NetworkPolicy and the Pod security context are all unchanged.
  - **What ADR 0373 claims.** A per-environment kernel boundary on a qualified Kata RuntimeClass. It does not claim hostile public tenancy, egress control or provider-side runtime detection.
- **Compatibility / migration:** Behavioral breaks only for draft-feature deployments; no in-place runtime conversion:
  - **Behavioral change for draft deployments.** Explicit `--shell` and hook-bearing agent definitions now fail startup when execution is enabled. Zero-reference environments now stop instead of running indefinitely.
  - **Switching profiles.** Moving an existing profile from `runc` to Kata changes its profile identity. Operators first retire and delete the old profile's environments through the existing administrative RPCs; there is no in-place runtime conversion.
  - **Supported paths.** CRD and chart upgrade paths are those of ADR 0364. Deployments without `--execution-enabled` are unaffected.

## In scope — 5 scenarios, in implementation order

### Scenario 1 — each environment runs in its own Kata VM

[ADR 0373](../adr/0373-kubernetes-session-vm-isolation.md) chooses one VM per environment. Profiles
already carry `runtimeClassName`, and preflight already rejects RuntimeClasses that define scheduling
(`internal/adapter/executioncontroller/controller.go:92-101`). The current fixture is `runc` only
(`deploy/mecatl-execution-kind/runtimeclass.yaml:1-4`).

**Acceptance:**
- AC1.1: Under the Kata qualification profile, two concurrent sessions of one user and one session of a second user each report distinct `/proc/sys/kernel/random/boot_id` values through `StartCommand`. Every value differs from the Kind node's `boot_id`. Each guest `uname -r` differs from the node kernel release, and the node shows one Kata hypervisor process per live executor Pod.
  - verify: `TestKindExecutionKataSessionVMIsolation`
- AC1.2: Under Kata, the existing coding qualification passes unchanged over the existing gRPC API: file tools, read-before-edit, CAS, a foreground Shell test command and restart/data-survival. The Pod security context and credential-free executor requests are unchanged. The Kata fixture installs Calico, as the production lane does, so default-deny workload ingress and egress is proven with an enforcing CNI.
  - verify: `TestKindExecutionQualification` run by `task e2e:k8s:execution:kata`
- AC1.3: The controller rejects an executor Pod whose `runtimeClassName` or `spec.overhead` differs from the profile RuntimeClass. The environment fails closed, and nothing is replaced under another runtime.
  - verify: `TestADR_0373_Scenario1_RejectsRuntimeClassOverheadMismatch`
- AC1.4: When the named Kata RuntimeClass's handler cannot start a sandbox, the existing bind wait ends at the CreateSession request deadline (`internal/adapter/executionclient/client.go:795-833`), and session creation fails with placement unavailable. The PendingCreate reference is aborted, or later reconciled through `ListReferenceIntents`. The resulting zero-reference environment is stopped under AC3.5 and AC3.4. No executor runs under another RuntimeClass, and nothing falls back to the mecak8s host.
  - verify: `TestKindExecutionKataUnavailableHandlerFailsClosed`

### Scenario 2 — the mecak8s Pod executes nothing on behalf of a session

The [AGENTS.md](../../AGENTS.md) placement rules require exact, fail-closed placement with no
cwd-based fallback. With execution enabled, Shell is registered for remote execution
(`internal/app/build.go:5677-5688`). But the composition still builds a host runner
(`internal/app/build.go:2315-2319`). Agent definitions can carry hooks that run `sh` on the host
(`internal/app/agentdefs.go:713-736`, `internal/app/build.go:3009-3016`).

**Acceptance:**
- AC2.1: With `RemoteExecution`, `app.Build` binds no environment with a host `CommandRunner` on any path: the default profile, the no-FS profile, Clear/Fork successors, exact reattach and attempt recovery. Remote environments carry only the execution-client runner.
  - verify: `TestADR_0373_Scenario2_NoHostCommandRunner`
- AC2.2: With `--execution-enabled`, an explicitly set `--shell` fails mecak8s startup with the named error. The default `--shell` value and `--no-shell` are accepted.
  - verify: `TestADR_0373_Scenario2_RejectsExplicitShell`
- AC2.3: With `--execution-enabled`, an agent definition that declares `hooks:` fails startup with an error naming the agent and no hook values. This holds whether it comes from `--agents-dir`, a conventional directory or the agent-source driver. Definitions without hooks still load. The refusal is enforced at the single hook-runner construction point (`defHookRunner`), so any definition admitted after startup is refused at admission with the same named error. That includes session-scoped definitions such as those proposed in #2003.
  - verify: `TestADR_0373_Scenario2_RefusesHookBearingAgentDefinitions`
- AC2.4: In a full journey, a model-directed Shell command that prints `/proc/sys/kernel/random/boot_id` and `hostname` returns the session executor's VM identity. That identity is independently read over typed gRPC and differs from the mecak8s Pod's node `boot_id` and hostname. Absence of host execution is proven structurally by AC2.1 to AC2.3, not by sampling a process table.
  - verify: `TestNativeTUIOIDCBlankWorkspaceCodingJourney`

### Scenario 3 — deleting the last reference stops the VM and retains the workspace

Today, removing the last reference leaves the executor running and unreachable, and only
administrative RPCs act on it (`internal/adapter/executioncontroller/store_lifecycle.go:118-160`,
`internal/adapter/executioncontroller/controller.go:202-269`). The existing retirement lifecycle
already quiesces, persists terminal proof and retains the PVC
(`internal/adapter/executioncontroller/controller_lifecycle.go:56-165`). [ADR 0373](../adr/0373-kubernetes-session-vm-isolation.md)
reuses it.

**Acceptance:**
- AC3.1: Deleting a session whose environment has no other reference removes its reference through the existing Prepare/Confirm flow. The controller then terminates the executor Pod under UID preconditions, persists termination proof, and leaves the environment `Retired` with `ExecutorTerminated`. The PVC stays Bound with unchanged contents, and the node no longer has that environment's hypervisor process.
  - verify: `TestKindExecutionKataSessionDeleteStopsVM`
- AC3.2: While any reference remains, the executor keeps running. This covers a Fork or Clear successor and PendingCreate or PendingDelete intents. `CancelReferenceDelete` leaves it running. Ambiguous deletes reconciled through `ListReferenceIntents` stop it only after confirmation.
  - verify: `TestADR_0373_Scenario3_StopWaitsForEveryReference`
- AC3.3: A stop that begins while a run claim is active admits no new command. It does not create a replacement. If termination cannot be proven, the environment enters FenceUnknown with its identity retained.
  - verify: `TestADR_0373_Scenario3_StopFencesActiveRun`
- AC3.4: A controller restart at any stop phase converges on one deterministic stop operation. An already-active zero-reference environment found on upgrade is stopped by the same reconciliation.
  - verify: `TestADR_0373_Scenario3_StopIsRestartIdempotent`
- AC3.5: The automatic stop is admitted from every non-terminal state, not only from Ready and idle:
  - **No executor Pod ever recorded:** retires immediately.
  - **A recorded executor that is not Ready**, for example one still provisioning or whose sandbox never started: terminated under UID preconditions, with termination proof.
  - **An in-progress administrative lifecycle operation or `ReplaceExecutor`:** the stop starts when that operation completes.
  - **FenceUnknown:** stays FenceUnknown with its identity retained, and the stop starts only after an authorized `RecoverEnvironment`.

  It never starts by bypassing an unexpired run claim.
  - verify: `TestADR_0373_Scenario3_StopAdmissionFromEveryState`
- AC3.6: While a stop is in progress, the existing reference RPCs keep their classifications:
  - a retried `ConfirmReferenceDelete` for the already-removed reference returns the existing retryable not-ready classification during the stop, and succeeds idempotently once the environment is `Retired`;
  - mecak8s session deletion completes for the client without waiting for VM teardown;
  - `ListReferenceIntents` reconciliation converges with no residual intent.
  - verify: `TestADR_0373_Scenario3_ReferenceReplayDuringStop`
- AC3.7: A stopped environment keeps holding its profile slot until authorized `DeleteRetiredEnvironment` reclaims the PVC and the slot.
  - verify: `TestNativeCapacityRetirementRestoresAllocation`

### Scenario 4 — the parent plan's five journeys pass under the Kata runtime

The [native Kubernetes execution plan](native-kubernetes-execution.md) names five journey targets
that remain PENDING under [ADR 0364](../adr/0364-native-kubernetes-execution.md) §7. This plan supplies them. Each one runs in the Kata lane and in the `runc` lane.
They use the deterministic provider; no paid model call is required.

**Acceptance:**
- AC4.1: A company user signs in to mecatui with OIDC and connects. In a blank session the agent creates and edits files, reaches a protected Ask that the user approves, and receives foreground test results from the executor.
  - verify: `TestNativeTUIOIDCBlankWorkspaceCodingJourney`
- AC4.2: Two OIDC users behind one mecak8s creator identity concurrently create separate environments, each in its own VM. Each is denied list, read, run, control and delete on the other's environment, including wrong-owner use of an exact reference.
  - verify: `TestNativeSameDeploymentOIDCUserIsolation`
- AC4.3: After a TUI disconnect and reconnect, followed by provider and mecak8s restarts, the same session reattaches to the exact `EnvironmentRef` and observes byte-identical files.
  - verify: `TestNativeTUIReconnectAndRestartPreservesWorkspace`
- AC4.4: Cancellation or authority loss during a command yields no competing writer and no local fallback.
  - verify: `TestNativeCancellationAndAuthorityLossFailClosed`
- AC4.5: Profile capacity is exhausted by a mix of live sessions and automatically stopped (`Retired`) environments. The ledger then denies the next allocation instead of leaving a quota-pending Pod. An operator's authorized `DeleteRetiredEnvironment` on one stopped environment restores the allocation. Explicit `RetireEnvironment` on a referenced environment is still refused. The fixture's ResourceQuota is sized for `maxEnvironments` executors including Kata overhead.
  - verify: `TestNativeCapacityRetirementRestoresAllocation`

### Scenario 5 — the VM lane runs automatically on KVM Linux, and macOS keeps `runc`

CI already provides `/dev/kvm` on `ubuntu-24.04` (`.github/workflows/microvm-e2e.yml:97`). The
execution production lane is path-gated (`.github/workflows/k8s-e2e.yml:207-241`).

**Acceptance:**
- AC5.1: `task e2e:k8s:execution:kata` fails with a named precondition before creating any cluster when `/dev/kvm` is absent or inaccessible. It never runs its scenarios under `runc`.
  - verify: `TestKataLaneRequiresKVM`
- AC5.2: CI job `execution-kata-e2e` runs the Kata lane on `ubuntu-24.04` for changes under the same paths as `execution-production-e2e`. It uses a uniquely owned cluster, bounded failure artifacts and ownership-scoped cleanup.
  - verify: inspection — workflow review of `.github/workflows/k8s-e2e.yml`, plus a green job run on the implementation PR
- AC5.3: The existing `e2e:k8s:execution` and `e2e:k8s:execution:production` lanes run unchanged on macOS and on non-KVM hosts, and make no VM claim.
  - verify: `TestKindExecutionQualification` run by `task e2e:k8s:execution`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Per-user Git/GitHub credentials, cloning, PRs and review follow-up | GitHub development-flow plan | Directing-human scope decision; [ADR 0364](../adr/0364-native-kubernetes-execution.md) deferral stands |
| Published coding workload image (git, gh, toolchains) | GitHub development-flow plan | Directing-human scope decision |
| Hostname/FQDN egress allowlists | Later networking plan | Directing-human scope decision; CIDR-only profile egress unchanged |
| Warm VM pools and resumable stopped environments | Post-POC | Directing-human decision; [ADR 0373](../adr/0373-kubernetes-session-vm-isolation.md) consequence |
| A per-session VM for Fork/Clear successors | Not planned | ADR 0364 shared-environment semantics retained |
| Provider-side runtime detection or a profile isolation field | Not planned | The RuntimeClass is operator-qualified configuration |
| Firecracker (`kata-fc`), gVisor, and macOS Kata | Not planned | Needs devmapper / not a VM / no KVM in Docker Desktop |
| Remote command hooks or VM-side hooks | Later | Hook-bearing definitions are refused instead |
| Multi-replica mecak8s product qualification | Parent plan deferral | Unchanged |

## Definition of done

1. Applicable `task lint`, `task test:race`, `task docs`, and `task api:check` gates pass on the final candidate, with unchanged API snapshots.
2. `task e2e:k8s:execution:kata` passes in CI (`execution-kata-e2e`), and `task e2e:k8s:execution:production` stays green.
3. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
4. `go run ./cmd/mecademo` stays green.
5. The implementation PR links the Plan / Interface PR and the approved commit, reports interface conformance, and updates [execution environments](../../user-docs/features/execution-environments.md) and the [mecak8s deployment guide](../../user-docs/building/deployment/mecak8s.md). The guide must state the requirement that the ResourceQuota covers `maxEnvironments × (profile limits + RuntimeClass overhead)`. It also reviews [ADR 0027](../adr/0027-cloud-native.md) Lists 1 and 2 for retired-environment ownership.
6. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- The parent plan remains `draft`, and its contract is not an approved baseline. This plan builds
  on the merged runtime code. If implementation finds parent-contract drift, implementation stops
  under the development process. It does not silently amend the parent.
- Kata inside Kind depends on the `kata-deploy` version, the nested-KVM behavior of the CI runner,
  and Kind's containerd configuration. Pinning by digest bounds the first two, but runner image
  changes can still break the lane.
- `emptyDir` size limits and FSGroup ownership are enforced by the host kubelet outside the guest.
  The qualification checks observed behavior, not mechanism.
- Every VM boots at bind time, which adds latency to session creation. The CreateSession deadline must exceed Kata boot time on the qualified nodes. Pools are deferred.
- The ledger counts environments, not resources. A production quota sized without Kata overhead rejects Pods at admission while the ledger still has room. That surfaces as `ExecutorUnavailable`, and the sizing requirement in the deployment guide is the only mitigation in this plan.
