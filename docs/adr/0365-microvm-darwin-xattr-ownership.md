# ADR 0365 - Prepare Darwin VirtioFS ownership and supervise the direct runner

- Status: Proposed
- Date: 2026-09-23
- Scope: Darwin arm64 VirtioFS ownership preparation and exact launch ownership
- Supersedes on acceptance: ADR 0368 section 4 only for the macOS platform deferral; the Linux amd64 mechanism remains authoritative
- Superseded by: none

## Context

ADR 0368 fixes the guest workload identity at UID/GID `65532:65532`. Linux maps
that identity to the daemon account with an unprivileged user namespace. Darwin
arm64 has no equivalent namespace mapping in this integration, but go-microvm
v0.0.41 can project a prepared VirtioFS ownership xattr as the fixed guest
identity.

The repository backend must prepare more than the initial worktree. The private
rootfs home and workspace, each logical root, copied Git objects, and files
created by host-side merge-back all need the same projected ownership. The Git
object snapshot must remain host-private and read-only after preparation.

Darwin also cannot reuse Linux pidfd reconciliation. Reading a PID from a durable
record and signaling it after a separate identity check would leave a PID-reuse
window. Replacement must remain blocked whenever the current daemon cannot prove
that a historical runner has stopped.

## Decision

### Prepare fixed guest ownership with VirtioFS xattrs

Both platforms keep guest workload UID/GID `65532:65532`. Linux keeps its
unprivileged user-namespace mapping and does not prepare ownership xattrs.
Darwin calls go-microvm's ownership preparation for the fixed guest identity and
starts both read-write and read-only VirtioFS mounts with strict ownership
preparation enabled.

Preparation covers the private rootfs home and workspace before publication,
each logical subtree before guest registration, and the Git object snapshot.
The object snapshot is copied into private files with mode `0600`, prepared by
go-microvm, then sealed to `0400`; directories remain owner-only and traversable.
This preserves host administration while exposing the prepared guest identity.

Merge-back continues to use host `git apply`. After the patch is applied, the
backend runs the same upstream ownership preparation over the parent logical
root. This labels new host-created inodes and preserves host-derived modes,
including a surviving guest `chmod`. There is no custom capture/replay path.
If ownership preparation fails after `git apply`, the error states that the
patch was already applied. Merge-back is not transactional with concurrent
Bash, and this decision makes no cache-invalidation guarantee.

### Use a safe direct-child supervisor on Darwin

Darwin launch ownership uses a private launch-owner helper as the runner's direct
parent. The helper validates the inherited attempt descriptors and immutable
launcher and runner identities, starts the runner, publishes the exact receipt,
and owns the only `Wait4(..., WNOHANG, ...)` loop and all TERM/KILL signals for
that child. A naturally exited runner remains a zombie until that loop reaps it,
so its PID cannot be reused between the exit observation and a signal.

The daemon holds the write end of a liveness pipe and the supervisor holds the
read end. Pipe EOF asks the supervisor to stop the runner with bounded TERM and
KILL phases and reap it. The runner inherits the exact attempt flock, so the
lock remains held if the supervisor dies. A restarted daemon waits boundedly for
historical attempt locks; it never signals a stored PID. A lock that remains
busy returns an explicit operator-recovery-required ownership error and blocks a
duplicate boot.

Microvmd and its manager use the same service-flock lifecycle on Darwin and
Linux. Linux launch ownership remains the existing pidfd, boot-ID, process-start,
executable-identity, and inherited-lock implementation.

### Keep authority and compatibility boundaries unchanged

The initial repository registry remains version 1. There is no shipped older
format to migrate and no host UID/GID binding in the record. Unknown registry
formats fail closed while preserving stored data.

The development descriptor and artifact preparation select the current host
platform. Normal platform admission accepts Linux amd64 with KVM and Darwin
arm64 with macOS 15 or newer and Hypervisor.framework. Linux arm64 remains
outside the admitted set. Runtime artifacts stay pinned to go-microvm v0.0.41,
including platform-specific runtime and firmware SHA-256 values.

Providers and credentials remain host-private. The guest receives no provider
credential, and a MicroVM failure never widens placement or falls back to host
filesystem or shell execution. Exact repository refs survive an ordinary daemon
restart. A runner orphaned by supervisor death remains fail-closed and requires
operator recovery.

## Consequences

Darwin can retain owner-only host modes while presenting fixed UID/GID 65532 to
the guest. Read-write logical roots and read-only object snapshots use one
upstream ownership mechanism, and Linux behavior does not change.

The direct-child supervisor closes the PID-reuse signaling window without
adding pidfd emulation. Supervisor death can leave a live runner holding the
attempt lock; refusing replacement is safer than guessing ownership.

The code path is implemented in the unmerged delivery, but released support
still depends on native Apple Silicon and release qualification. Cross-compiled
Darwin tests and CI unit tests are implementation evidence, not a real-VM
qualification result.

## Alternatives considered

### Host numeric UID/GID convergence

Persisting and projecting the host account's numeric identity would create a
second workload-identity contract and identity-drift lifecycle. The selected
xattr mechanism keeps the fixed guest identity and avoids a registry migration.

### Widened host modes or recursive chown

World/group mode widening and host ownership mutation weaken the private host
boundary. Ownership xattrs preserve owner-only host modes.

### Signaling a recorded Darwin PID

A separate PID identity check cannot make a later signal atomic against PID
reuse. The direct parent signals only its unreaped child; restart reconciliation
uses the inherited flock instead.

## See also

- [ADR 0368](./0368-microvm-execution-environments.md)
- [MicroVM execution-environment acceptance plan](../acceptance/microvm-execution-environments.md)
- [MicroVM architecture](../architecture/microvm-environments.md)
