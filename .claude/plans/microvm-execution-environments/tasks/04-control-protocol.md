---
id: 04-control-protocol
title: Local control socket and guest protocol handshake
blocked_by: [01-module-contract]
status: done
branch: "plan-microvm-execution-environments/04-control-protocol"
worktree: ".scratch/task-microvm-04"
issue: "530"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Task brief

Define the local microvmd control service, Linux/macOS peer authentication, environment owner/ref/generation binding, and guest protocol handshake over go-microvm's vsock-to-UDS primitive. This task establishes framing/capabilities and test fakes, not full Workspace or exec methods.

## Acceptance criteria

- AC2.4: The local control socket authenticates the configured Unix account using the
  platform peer-credential mechanism; possession or guessing of an environment ID,
  socket path, or session ID alone authorizes no operation.
  - verify: `TestMicroVMEnvironments_Scenario2_LocalPeerCredentialsBindOwner`
- AC2.5: Protocol negotiation rejects a missing required filesystem, streaming,
  cancellation, generation-binding, or message-bound capability; it never degrades to
  SSH or host-local tool execution.
  - verify: `TestMicroVMEnvironments_Scenario2_CapabilityNegotiationFailsClosed`
