# Storage and recovery

*This chapter describes the intended design, not current implementation status.*

A session's files live on storage that outlives its executor. This chapter describes how that storage is identified,
what happens when the executor or the storage fails, and what Mecatl guarantees in each case. Durability beyond these
cases depends on the executor implementation and the storage itself, as the
[overview](README.md#live-files-and-recovery) explains.

## Storage identity

Each execution environment has one PersistentVolumeClaim (PVC), created from the template's storage class and size with
the `ReadWriteOnce` access mode. The PVC has no owner reference, so deleting the environment's Kubernetes resource does
not delete it; it is removed only by the separate administrative delete operation. The environment records the PVC's
name and UID when it is created. If that PVC later disappears, the provider never creates a replacement under the same
environment, because a new empty volume would silently discard the session's work.

The session refers to its environment through its `EnvironmentRef`, which names the environment, not the PVC. The PVC
UID is checked by administrative operations but is not part of the reference.

This behavior exists today and does not change. The
[lifecycle and retention](../../../user-docs/features/security-and-execution/execution-environments.md#native-kubernetes-lifecycle-and-retention)
documentation describes it from the operator's side.

## When the executor fails

Executor failures fall into three cases, distinguished by whether Kubernetes can prove that the old executor has
stopped. Only the kubelet on the Pod's node can report that a Pod's containers have stopped; the API server records what
the kubelet reports.

### The executor process crashes

When the executor's server process exits but its Pod is healthy, the kubelet restarts the container in place: same Pod,
same node, same PVC. This needs the server to be the container's main process, instead of today's `sleep infinity`, and
the Pod's restart policy to change from `Never` to restarting failed containers. Because every process in the container
stops when its main process exits, no command from the crashed server survives the restart.

Calls in flight when the server exits fail when their connection drops. The agent loop reports their outcome as
unknown and does not retry a command.

### The Pod is gone and its termination is proven

A Pod can be evicted, deleted, or fail as a whole while the kubelet still reports its containers as terminated. A new
executor Pod can then safely use the same PVC.

In the first implementation, an administrator starts the replacement with `ReplaceExecutor`, as today. The replacement
keeps the PVC, starts a new Pod, and gives the environment a new epoch. The new epoch invalidates the session's current
run claim, so the current run fails with an error and the next prompt acquires a new claim. The `EnvironmentRef` does
not change, so the session stays bound to the same environment.

Automatic replacement in this case is a later evolution. It reuses the same replacement sequence, started by the
controller instead of an administrator, and needs a limit on repeated attempts so that a broken template is not
replaced forever, and guards that skip revoked templates and environments with a lifecycle operation in progress.

### Termination cannot be proven

The kubelet cannot report when the node is unreachable (a network partition, a frozen or powered-off node, or a dead
kubelet), when the container runtime is stuck, or when someone force-deletes the Pod. The old executor might still be
running and writing to the PVC, and starting a second executor would put two writers on the same files.
`ReadWriteOnce` does not prevent this on its own: it limits a volume to one node, not to one Pod, and a volume attached
to an unreachable node usually cannot attach elsewhere until it is detached.

The environment stays fenced until an administrator confirms that the old node is dead and fences it externally, for
example with the `node.kubernetes.io/out-of-service` taint or by deleting the node. Kubernetes then detaches the volume,
and the administrator replaces the executor onto the same PVC. The storage is not treated as lost: on network-attached
storage the files survive a node failure.

A user who does not want to wait can start a new session, which gets a fresh environment through normal
[admission](admission.md). The fenced environment stays until it is recovered or deleted.

## When the storage fails

Mecatl is not a storage system. It relies on the storage class for durability and availability, and guarantees only
that storage failures are visible and never silently papered over.

| Failure | What the executor sees | What Mecatl does |
| --- | --- | --- |
| Storage temporarily unavailable | File operations return errors or block | File operations time out and return an error to the agent. A command blocked on storage may not stop when killed; if its Pod cannot terminate, the environment is fenced as in [termination cannot be proven](#termination-cannot-be-proven). |
| Storage full | Writes fail with `ENOSPC` | The error reaches the agent as an ordinary tool error. |
| Volume cannot be attached | A new or replacement Pod does not start | The environment stays not ready. |
| Volume lost | The PVC or its volume is reported lost | The environment is fenced; it is never given a new volume automatically. |

Across these failures:

- a storage error reaches the agent as a tool error, never as a success, and the executor never falls back to other
  storage;
- the executor's readiness check touches `/workspace`, so storage that is down shows the environment as not ready;
- silent corruption is not detected.

## Operator requirements

The template's storage class must provide what Mecatl does not:

- durability and availability, including replication, backups, and recovery from data loss; Mecatl takes no backups
  of its own;
- network-attached storage, for files to survive the loss of a node; with node-local storage, a node failure loses the
  session's files;
- enough capacity, sized with the template's storage size and namespace quotas.

## Session forks

Forking a session so that the new session starts from a copy of the source's files will likely need Mecatl to implement
its own snapshot and restore mechanism. That mechanism could use the storage layer's snapshots or clones where the
storage class supports them, or something as simple as an archive of the workspace. It is deferred and out of scope for
this work.

## What changes from today

- The executor server becomes the container's main process, and the Pod restarts failed containers instead of using
  `RestartPolicyNever`.
- Fencing caused by an expired operation lease, and by a command that returns no terminal receipt, is removed along
  with Pod exec; see the [executor protocol](executor-protocol.md#why-pod-exec-goes).
- The executor gains a readiness check on `/workspace`.
- A lost volume fences the environment.

Fencing for unprovable termination, administrator replacement and recovery, and the rule against recreating a PVC stay
as they are. The [execution provider lifecycle](../../../user-docs/operating/mecak8s/execution-provider-lifecycle.md)
documentation describes the current administrative operations.

## Open questions

- **Automatic replacement.** When to add it, and how many attempts to allow before leaving the environment for an
  administrator.
- **PVC identity in the reference.** Whether the PVC UID should be part of the `EnvironmentRef`.
