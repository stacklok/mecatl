---
id: 17-concrete-runtime
title: Concrete microvmd and guest-agent runtime
blocked_by: [03-artifact-verification, 04-control-protocol, 06-virtiofs-assets, 07-workspace-rpc, 08-exec-rpc, 09-network-egress, 10-lifecycle-create, 18-guest-multiplex, 19-daemon-lifecycle-rpc]
status: done
branch: "plan-microvm-execution-environments/17-concrete-runtime"
worktree: ".scratch/task-microvm-17"
issue: "530"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Task brief

Repair the decomposition gap found by task 16. Compose the already-landed artifact, worktree, virtio-fs, control, Workspace, exec, network, and lifecycle packages into a concrete go-microvm/libkrun runtime, a `mecatl-microvmd` executable, and a minimal guest-agent executable/image entrypoint. The daemon must expose the local control socket, own VMRuntime provisioning/stop/resolve hooks, wire verified runtime/firmware/image sources, bind the guest protocol over vsock, and use the explicit network provider. Add build/package entry points needed by the later real-hypervisor E2E, while keeping ordinary tests offline and the standard root/engine binaries free of libkrun linking.

This is an enabling repair task: it owns no new numbered plan AC. It must preserve and integrate the already-pinned behavior for AC2.1–AC5.5 rather than duplicate their proofs. Its completion proof is that a deterministic fake-backed daemon composition test exercises create → ready → Workspace/exec → detach/delete through the concrete composition root, and the nested module builds both executables with `GOWORK=off` on supported build targets.

## Verification obligations

- `TestMicroVMRuntime_ComposesVerifiedEnvironmentEndToEnd` proves the concrete daemon composition wires the existing verified artifact, worktree, guest protocol, Workspace, exec, network, and lifecycle seams without a host fallback.
- `TestMicroVMRuntime_StandardBinariesDoNotLinkLibkrun` preserves the nested-module boundary while the microvmd/guest-agent build targets compile.
- `task test:microvm-standalone`, `task lint`, and `task test` pass; ordinary tests use fakes and never require KVM/HVF.
