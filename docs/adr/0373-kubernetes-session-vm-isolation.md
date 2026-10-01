# ADR 0373 — Per-session VM isolation for Kubernetes execution

- Status: Accepted (effective when Plan / Interface PR #2052 merges)
- Date: 2026-10-01
- Scope: native Kubernetes execution provider isolation boundary, executor lifecycle on last-reference removal, mecak8s host command-execution boundary, and Kata qualification
- Supersedes: none (follows [ADR 0364](0364-native-kubernetes-execution.md))

## Context

[ADR 0364](0364-native-kubernetes-execution.md) binds each mecak8s session to an owner-scoped
`ExecutionEnvironment`: a retained PVC plus a replaceable executor Pod reached only through the
private mTLS `ExecutionProviderService`. Its isolation claim is deliberately limited: same-company
authenticated users, a container boundary, and "not hardened hostile multitenancy". Profiles already
require a `runtimeClassName`, and the controller verifies it, but qualification uses only a `runc`
RuntimeClass (`deploy/mecatl-execution-kind/runtimeclass.yaml`).

The next goal is mecatl agents that run a development flow in Kubernetes: clone a repo, change it,
open a PR, and respond to feedback. Those agents run arbitrary model-directed shell, and they will
later hold user-scoped repository credentials. A shared node kernel between sessions is the wrong
boundary for that workload. The mecak8s Pod also holds provider and harness credentials, so no
model-influenced process may start there.

Three facts shape the decision:

- **Commands already route remotely.** With `--execution-enabled`, Shell routes to the remote
  environment and delegation tools are not in the catalog. But mecak8s still constructs a host
  command runner (`internal/app/build.go:2315-2319`, `:5676`), because `--shell` defaults to
  `/bin/sh`. Agent definitions that carry `hooks:` run `sh` on the mecak8s host
  (`internal/app/agentdefs.go:713-736`).
- **A dereferenced environment is unreachable but still running.** Removing the last reference
  retains the environment and leaves its executor running indefinitely. Nothing in the existing
  API can reattach to an environment with zero references: Attach needs an attached reference,
  Ensure for the same binding conflicts, and ReserveSuccessor needs a Published source. With one VM
  per executor, that is a leaked VM per deleted session.
- **Kata runs in Kind.** Kata Containers runs inside Kind when the host exposes `/dev/kvm`. The
  repository's Linux CI runners already expose it (`.github/workflows/microvm-e2e.yml:97`).

The local microVM backend ([ADR 0368](0368-microvm-execution-environments.md)) is single-operator
and repository-scoped by decision, so it is not reused.

## Decision

1. **One VM per execution environment.** A VM-isolating deployment's profile names a
   scheduling-free Kata RuntimeClass. Each environment's executor Pod runs in its own guest kernel
   and hypervisor process. Sessions with separate environments, including two sessions of the same
   user, never share a kernel. Clear and Fork successors keep ADR 0364's shared-environment
   semantics and therefore share that environment's VM. The controller continues to verify the
   RuntimeClass on read and also verifies that the Pod's `spec.overhead` equals the RuntimeClass
   overhead. A mismatch fails closed. The provider does not detect or substitute runtimes; VM
   isolation is a property of the operator-qualified RuntimeClass.
2. **The mecak8s Pod executes nothing on behalf of a session.** With `--execution-enabled`:
   - mecak8s constructs no host command runner for any placement, including no-FS, successors,
     reattach and attempt recovery.
   - An explicitly set `--shell` is a startup error.
   - Any loaded agent definition carrying `hooks:` is a startup error.
   - Model-directed command execution reaches only the session's executor, through the unchanged
     `ExecutionProviderService` API.
3. **Removing the last reference stops the VM and retains the data.** When an environment's
   reference set becomes empty, the controller retires it through the existing
   quiesce/termination-proof/finalizer lifecycle. A reference set becomes empty through
   ConfirmReferenceDelete, AbortReference or ReleaseReference. Retirement leaves the environment
   `Retired` with its PVC retained, and it never needs a caller-supplied identity. It uses the
   existing `RetireEnvironment` operation type, so the CRD schema is unchanged, and it is admitted
   from not-Ready and never-provisioned states as well as from Ready. The existing
   fencing rules apply unchanged: an executor that cannot be proven terminated goes to
   FenceUnknown, and no replacement is created. Explicit authorized deletion
   (`DeleteRetiredEnvironment`) still reclaims the PVC and the profile slot. This supersedes
   two rules in ADR 0364 §6: that last-reference removal leaves the environment active, and that
   environments retire only through explicit authorized retirement. Data retention
   and explicit data deletion are unchanged.
4. **Qualify the VM boundary in Kind on Linux with KVM.** A dedicated Kind+Kata lane runs in CI
   and fails before cluster creation when `/dev/kvm` is absent. It never degrades to `runc`. The
   existing `runc` lanes remain the macOS and non-KVM path, and they make no VM claim.
5. **Keep every contract surface unchanged.** These are the provider protobuf, engine and Harness
   APIs, tool schemas, the profile schema and the chart values. Warm VM pools, resumable stopped
   environments, Git credentials, workload images and hostname egress are separate decisions.

## Consequences

**Positive.**
- A compromised or prompt-injected session no longer shares a kernel with another session.
- The mecak8s Pod's credentials are no longer reachable through model-influenced process creation
  on that host.
- Deleting a session releases its VM's compute.
- The boundary is proven by an automated Linux CI lane, not asserted.

**Costs and risks.**
- Every live environment pays Kata's memory and CPU overhead and VM boot latency on bind. Pools
  are the deferred mitigation.
- Kind+Kata needs a KVM-capable Linux host, so macOS developers cannot run the VM lane locally.
- Retired environments still hold a profile slot and a PVC until an operator deletes them.
- Operators with explicit `--shell` or hook-bearing agent definitions must remove them before
  enabling execution.
- A RuntimeClass name is operator configuration. A deployment that points the profile at a
  non-VM handler gets no VM boundary, and the provider cannot detect that.
- Fork and Clear successors share a VM by design.
- Egress, image, and credential posture are unchanged. Hostile public tenancy is still not claimed.

## See also

- [Kubernetes session VM isolation acceptance plan](../acceptance/kubernetes-session-vm-isolation.md)
- [ADR 0364 — Optional native Kubernetes execution environments](0364-native-kubernetes-execution.md)
- [Native Kubernetes execution acceptance plan](../acceptance/native-kubernetes-execution.md)
- [Execution environments](../../user-docs/features/execution-environments.md)
