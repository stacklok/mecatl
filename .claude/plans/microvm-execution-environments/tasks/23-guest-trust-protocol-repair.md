---
id: 23-guest-trust-protocol-repair
title: Drop guest workload privilege and converge on one negotiated protocol
blocked_by: []
status: done
branch: "plan-microvm-execution-environments/23-guest-trust-protocol-repair"
worktree: ".scratch/task-microvm-23"
issue: "530"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Panel repair brief

Security blocker: model commands run as guest root/PID-neighbour of the privileged agent, can read the HMAC key, alter IPv6/sysctls/mounts, and signal the agent. Reuse blocker: the production multiplex protocol and an unused second capability handshake drift; capability-negotiation tests cover the unused path.

Run model commands under a dedicated unprivileged UID/GID with no ambient capabilities and no read access to guest-agent capability material. Keep privileged configuration outside its readable namespace and enforce IPv6/mount policy beyond workload control. Remove or fold the dead duplicate handshake so the single production multiplex handshake negotiates actual services/capabilities and task-04 fail-closed tests exercise that path. Preserve transferable generation binding, replay rejection, stream bounds, cancellation, and no host fallback.

Protects AC2.5, AC4.1–AC4.5, AC8.2.

## Verification

- Live/fake guest Bash cannot read capability key, change IPv6 sysctls, signal agent, or mount; ordinary workspace/toolchain use remains possible.
- Production multiplex path rejects missing required capability/version.
- Duplicate unused handshake is removed or demonstrably one shared implementation.
- Existing guest protocol/exec/network tests plus lint/test pass.
