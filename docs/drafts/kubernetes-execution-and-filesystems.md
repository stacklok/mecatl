# Kubernetes execution and filesystems

Mecatl proposes a cloud-native disaggregated architecture for an AI agent harness. Disaggregation means that different
components/modules of the harness are separated by contract boundaries which may imply even process boundaries. One flagship
example of this is that the agent loop may run in a different process than the execution environment (where tools like `Shell`
actually run). This provides an interesting security property: the loop can keep credentials and permission checks outside
the shell. But, as mentioned, this separation needs clear contracts between services. The [cloud-native harness
explanation](../../user-docs/building/cloud-native-harness.md) describes the broader design. The [agent
identity](../agent-identity-model.md) and [scoped resource grants](../scoped-resource-grants.md) drafts describe a
possible future identity system for agents that takes into account the delegation chain.

We use Kubernetes Pods so deployments can reuse the isolation mechanisms their cluster already provides. The operator
selects an installed runtime through Kubernetes
[RuntimeClass](https://kubernetes.io/docs/concepts/containers/runtime-class/). This could be a standard container runtime,
gVisor, or a VM-backed runtime such as Kata Containers. The executor remains a Pod from Kubernetes' perspective, even
when its programs run inside a VM. Mecatl already selects a RuntimeClass through its execution profile; each runtime
and storage combination still needs testing. The goal is to reuse this support rather than manage every isolation
technology ourselves.

The immediate question is how file tools and shell commands can use one live, persistent workspace across Pods.
Persistent files belong under a documented mount point, currently `/workspace`; paths outside it are outside the
persistence guarantee and may be ephemeral or read-only. Executors are disposable: the design requires a replacement
to start fresh and reattach the same authorized storage. Process restoration is optional; whole-Pod snapshots are not
required.

After a successful shell write and close, a fresh file-tool read should see the change without waiting for command
completion. After a file-tool Edit completes, a fresh shell read should see it. Files should survive executor Pod loss
and remain accessible without an executor. Keeping mounts out of the agent-loop Pod is a preference, not a requirement.
The alternatives below compare loop access through a service interface with loop access through a mount; the executor
uses an ordinary filesystem mount in both.

## Context, files, and commands are different

`HarnessContext` supplies the instructions, rules, skills, custom slash commands, and agent definitions given to the
model. Slash commands here are prompt shortcuts, **not** shell commands. These instruction rules also cannot replace
the permission rules that control tool use.

The operator chooses where this content comes from. A source can read an API, host files, or explicitly selected
workspace files. The code combines these sources; it does not require a separate context service or Pod.
[Model][context-model] · [Context sources][context-source]

`Workspace` gives file tools access to files. `CommandRunner` runs programs. An `Environment` groups these together with
its identity and a `ReadLedger`: a record of the file versions the agent has read. That record helps prevent an Edit
from overwriting changes made since the Read. It does not store file contents. A workspace can exist without a command
runner. Shell programs use the mounted filesystem directly, rather than calling the `Workspace` API.
[Environment][link-2] · [Workspace operations][link-3]

The operator selects context sources separately from the execution environment. They can use the same storage, but
access to those files does not make their contents trusted instructions. For example, two sessions can use the same
company instruction source while using different workspaces and executors. Each session keeps its own record of file
reads. A session with no file tools can still receive company instructions from an independent source.
[Source selection][context-binding] · [Model][context-model]

Files in a workspace or container image must not automatically become trusted instructions. A deployment may explicitly
select a workspace-backed context source using the appropriate adapter. Project content still needs the project-trust
check. Context reads do not count as file-tool reads, even when they access the same file: they neither satisfy
read-before-edit nor update the `ReadLedger`. A context choice cannot grant filesystem or shell access or weaken
permissions. A virtual Redis workspace root must not be reopened as a host path to load instructions. [Project
admission][context-admission] · [Model][context-model]

Today the native Kubernetes path keeps operator-configured sources that do not depend on project files. It does not
load project instructions from either the host repository or the executor's PVC, even if the host project is trusted.
The existing local microVM backend can read repository instructions and prompt shortcuts from the guest, but only when
that source is selected and project trust permits it. This local implementation is separate from RuntimeClass selection.
Choosing a shell image or execution template must not silently change the agent's instructions. Selectable, versioned
templates are [proposed, not implemented][templates].
[Local microVM sources][context-microvm] · [Project trust checks][context-admission]

## What the Kubernetes provider does today

The current proof of concept uses one operator-selected execution profile. The loop sends file and command requests
through an authenticated gRPC connection to the execution provider. The provider uses Kubernetes Pod exec to start
`/mecatl-executor`. Its file operations use
`osfs`; commands use `/bin/sh`. Both see the same persistent volume claim (PVC), Kubernetes storage mounted at
`/workspace` in the executor Pod. The PVC is `ReadWriteOnce`, which limits where it can be mounted. `/tmp` is temporary
Pod storage. The loop mounts neither volume. [Remote binding][link-4] · [Pod exec][link-5] · [Executor][link-6] ·
[Pod storage][link-7]

The PVC outlives the executor and session references. Retirement retains it; deletion is a separate administrator
operation tied to the recorded PVC identity. A missing PVC cannot quietly be replaced under the same allocation. If an
executor's termination is uncertain or its lease expires, access stops rather than allowing an automatic takeover.
This is a draft provider, **not** an approved boundary for hostile multi-tenant workloads. Its operation grants and
run claim also do not implement the full proposed identity system. [Lifecycle][k8s-lifecycle] · [Run claim][link-8]

So the gap is not basic persistence or shared files between the current file and shell tools. It is independent file
access when compute is absent, and a defined consistency contract when independently placed clients use one filesystem.
Current `Workspace` operations include versioned reads, create-only writes, conditional replacement, and search.
Compare-and-swap (CAS) means replacing a file only if its version still matches the version previously read. Existing
adapter guarantees do not establish atomic CAS against a shell process writing through a separate mount.
[Workspace operations][link-3]

Mecatl also has a Redis-backed workspace shared among sessions of the same authenticated user identity. It has no
command runner or ordinary Unix filesystem. Its hash-based files and Lua CAS work for file tools, but do not provide
normal Unix file operations (POSIX), such as open handles, symlinks, and permissions. It cannot become a runnable mount
just by adding a thin wrapper; durability also depends on the Redis deployment. [Redis placement][link-9] ·
[Redis operations][link-10]

## Filesystem requirements

### Instructions and storage identity

The loop receives instructions through selected `HarnessContext` sources. Mounting files must not turn them into
trusted instructions; shell requests still need execution permission. The server selects storage before starting an
executor and records its stable identity with the `EnvironmentRef`. Reattachment must fail closed if storage is absent
or replaced. Clients cannot choose raw mount paths or storage credentials. Retention is independent of executor or
session deletion; deleting retained files requires a separate explicit authorized operation. An execution template,
an environment identity, and a file version identify different things.

### Live files and recovery

Ordinary, unmodified shell programs, Git, and representative builds must operate on the persistent tree. Qualification
must exercise their actual operations, not demand every POSIX feature. After a successful write and close, a fresh
read from another client must see the change before the writing command finishes. The same holds for a completed file
Edit followed by a fresh shell open. Already-open descriptors may retain the old file after replacement; hard links,
writable mappings, cache behavior, and delayed writes remain qualification points. A batch of shell writes is not a
single transaction; job-start/end snapshots cannot meet live two-way visibility. [inotify][inotify] is not a reliable
cross-host change feed.

Completed, closed files must remain available to an independent file client after **executor Pod** loss, with no
executor running. An interrupted write may be incomplete, but unrelated completed files remain retained. If the
filesystem client runs inside that Pod, its loss is part of the same failure and cannot be excluded from this guarantee.
Separate filesystem-client, node, and storage-cluster failures need their own documented durability guarantees.
Qualify close and `fsync` behavior at the actual client placement, including writeback errors and server failures;
no candidate integration is claimed to meet these guarantees without qualification.

### Concurrent edits

Preserve Mecatl’s existing read-before-edit behavior: file tools reject detected changes since the preceding Read and
require a fresh Read. Background shell commands may overlap with file tools. Conditional replacement does not guarantee
atomicity against arbitrary shell writers, and content-based versions do not detect changes that restore identical bytes.

This concerns live shared visibility, not the [grants draft's][grant-draft] `exclusive-write` mode, which publishes at
explicit commit boundaries.

### Agent identity and withdrawal

Workload authentication and user authentication are inputs, not substitutes for the acting agent's identity. Mecatl
would issue logical agent identities in its own SPIFFE trust domain using those and any additional authenticators.
The user -> agent -> subagent chain can continue through further hops under policy; each hop may preserve or narrow,
never widen, delegated authority. Ancestors provide provenance, not extra rights. The verifier must enforce the
acting agent's *current* delegated authority from Mecatl's session and permission checks, not treat a signed history
as authority. A trusted client authenticates its workload while acting for the agent; both identities stay distinct.
The [identity draft][identity-draft] is a proposal, not current support; this requirement does not freeze token format,
require per-process keys, or ask SPIRE to attest goroutines.

Native filesystem enforcement, a concrete trusted adapter, or an upstream change can provide the checks on *both*
file-tool and shell paths. Mecatl selects each agent's permitted files and operations, including read-only inputs and
writable outputs. Sharing files requires explicit authorization; neither a storage identifier nor starting an executor
grants access. A restricted agent must not bypass its view through backing-storage endpoints or another agent's mount.
Keep broad filesystem, database, mount-control, and harness credentials outside the shell.
Ordinary programs should use the mount without handling grant tokens, SDKs, or an auth service. Scope enforcement
applies to the persistent tree, not every filesystem operation in the entire shell Pod. Shell approval is not a
permission prompt for every open or write.

Withdrawal must have a bounded effect through nonrenewal of short-lived grants, including already-mounted clients
and delayed writes. An expired grant or replaced executor must not keep publishing writes. Define and test the exact
cutoff and in-flight operation semantics; immediate push revocation is not mandatory, and bytes already read cannot
be recalled. Do not automatically retry a modifying command whose outcome is unknown. These are proposed integration
requirements, not guarantees of the current provider.

## Filesystem integration proposals

File tools and shell commands should work on the same files. A change made through either
must be visible to the other. The difference is how the agent loop and executor reach those files. Either option can
use the runtime selected by the Kubernetes installation; filesystem design should not require a particular isolation
technology. The diagrams show file-access paths, not every runtime component.

### Option 1: The agent uses an API; the executor mounts the filesystem

The agent loop sends file requests through its `Workspace` adapter to a service interface. For example, `Read` asks
for a file, and `Edit` asks the service to update it. The loop does not mount the filesystem.

Programs inside the executor expect normal filesystem access. A trusted mount client exposes the same stored files,
not a separate copy to synchronize. This could be a FUSE client speaking to a service, or a native filesystem client
with a service frontend for the loop. In the latter case the backend or integration must enforce authorization across
both routes; a lock at the frontend alone cannot protect native mount writes.

```text
Agent file tools -> Workspace adapter -> API ---+
                                               +--> Filesystem service -> Storage
Executor programs -> FUSE client ------> API ---+
```

The diagram illustrates a shared-service implementation, not a requirement to build a complete storage protocol.
An API frontend over a mature filesystem can reuse its existing clients. Any distinct mount route still needs common
enforcement. Mecatl's current Redis filesystem would need more functionality to serve programs.

Each API request can carry the acting agent's identity and permitted operations and files. The service must verify
that identity and check the requested action; using RPC does not provide those checks automatically. Access through
the shell's mount needs equivalent checks without giving the shell broad credentials.

Programs such as Git and compilers need more than reading and writing whole files. They also keep files open, change
parts of a file, rename files, and use permissions and symbolic links. We need to test these operations before relying
on the mount for real workloads. If using FUSE, its client needs a safe place to run: putting it in a sidecar does not,
by itself, keep its credentials or control socket away from the shell.

If this route uses FUSE, it needs access to `/dev/fuse` and a mount mechanism permitted by the runtime. This does
not automatically require making the whole Pod privileged. Trusted infrastructure should perform the mount, not the
untrusted shell. We must also test where the client can run and how its mount reaches the executor with runtimes such
as gVisor or Kata. [FUSE][fuse]

A FUSE client does not need to run inside the executor. A VM-backed runtime may expose a host-mounted filesystem through
[virtio-fs](https://virtio-fs.gitlab.io/design.html). The runtime provides the VM-to-host connection; when using a
remote filesystem service, the host client connects to it.

### Option 2: Both use an existing shared filesystem

The agent loop and executor mount the same shared filesystem, such as NFS, CephFS, or JuiceFS. The agent's `Workspace`
adapter accesses files through its mount. Programs in the executor access them through theirs. A runtime-provided
VM-to-host connection can also expose shared storage to a VM-backed executor.

```text
Agent file tools -> Workspace adapter -> Mount ---+
                                                 +--> Shared filesystem
Executor programs --------------------> Mount ---+
```

This reuses an existing filesystem and its clients instead of building a new filesystem API and custom FUSE client.
The choice between these proposals is whether the loop uses a service interface or a mounted filesystem.
Option 2 requires a mount in the agent-loop Pod, unlike option 1 and the mount-free preference described above.
A filesystem that enforces agent scopes natively need not put every write through a separate gateway; neither option
assumes that mounts bypass enforcement.

Mounted access sees processes and Unix IDs, not logical agents. A trusted adapter or mount broker must map verified
permissions to a restricted view for file tools and shell, without unintentionally combining scopes. Two goroutines
in one process with one Unix ID are not separately authorized by directory or mount names. NFS has identity controls,
but still needs this mapping.

#### Deploying shared storage on Kubernetes

##### Can the loop and executor access the same storage?

The loop and executor may run on different nodes. The chosen filesystem and Kubernetes storage driver must support
access from both, and both mounts must reach the same files.

A persistent volume claim (PVC) requests storage, but does not make it shareable across nodes. The current provider
uses `ReadWriteOnce`, which allows read-write mounts on one node. Multiple Pods on that node may use it. Cross-node
access needs a backend and driver that support it, commonly exposed through `ReadWriteMany`.

##### How do we limit access for each agent?

Kubernetes mounts volumes into Pods. Mecatl must decide which files and operations each agent may access.

For example, an agent may have read-only access to an input directory and write access to an output directory. Both
its file tools and executor must respect those permissions. Mounting a shared volume does not enforce them
by itself, especially when one loop process serves several agents. Kubernetes RBAC does not supply these file-level
agent permissions either.

We could use separate volumes or restricted views of shared storage. In either case, access must follow the agent's
verified permissions, not merely a supplied directory name.

##### Does each new session require a Pod restart?

Not necessarily. If the agent-loop Pod already mounts a shared filesystem, Mecatl can assign a directory to a new
session without changing the Pod's volumes.

Adding a separate volume to an existing Pod normally requires replacing that Pod. This makes one-volume-per-session
harder to manage in a long-running, multi-session loop. [Pod updates][pod-updates]

A trusted node service can also make mounts available after a Pod starts. That requires explicit runtime and
mount-propagation support; it is not something we should assume every installation provides. A mount made in one
container is not automatically visible in another.
[Mount namespaces][mount-ns] · [Mount propagation][mount-propagation]

## Filesystem candidates

These candidates can support either integration option and must meet the filesystem requirements above.

Source review baseline: 2026-10-07. The implementation links below identify reviewed commits or releases; general
manuals may change independently. These are existing capabilities and proposed integration points, not tested Mecatl
integrations. A missing feature may be supplied by a concrete adapter, extension, or upstream change; it is not by
itself grounds to reject a candidate.

| Candidate | Existing capability | Integration to research | Status |
| --- | --- | --- | --- |
| NFS / Ganesha | Standard Linux NFS clients; close-to-open coherence on fresh opens [NFS coherence][nfs]; [Ganesha FSAL][ganesha-fsal] has write, commit, rename, close, and access hooks. | Server state/auth plus FSAL or other backend integration for agent scope and writer fencing; hooks alone do not guarantee it. | Source reviewed; untested. |
| CephFS | Shared filesystem with coordinated clients [CephFS behavior][ceph]; [scoped client access][ceph-client-auth] and [client eviction/blocklisting][ceph-eviction]. | Bind clients to agent scopes, grant expiry, and writer fencing. Eviction is not an agent TTL. Qualify close durability at the actual client placement. | Source reviewed; untested. |
| JuiceFS Community | FUSE client, [metadata transactions][juice-meta] and [CSI mount pods][juice-csi]; [cache behavior][juice-cache] requires fresh-open qualification. | Grant epochs across all writes, not just file-tool API calls; isolate mount credentials and test CSI/runtime paths. Do not conflate Community and Enterprise behavior. | Source reviewed; untested. |
| Purpose-built service / go-fuse | [go-fuse hooks][go-fuse] provide a reusable mount interface over a service. | Build or integrate distributed protocol, authentication, consistency, and recovery; the mount library supplies none of these guarantees. | Source reviewed; untested. |

Client placement matters for durability: the reviewed Ceph userspace client's [close path][ceph-close] may start an
asynchronous flush. Losing only the executor is different from losing that client too. JuiceFS's metadata schema is
not Mecatl's Redis-hash workspace, so it is not a drop-in replacement for that adapter. Its cache and writeback settings
also need qualification against the requirements above.

Reusing a filesystem saves implementation work, but isolation, availability, durability, cache behavior, and
conditional Edit still need qualification. Choose the filesystem separately from the controller that starts and
stops executor Pods.

## Alternatives: adopting an upstream execution platform

The question is whether each platform can connect ordinary programs to the **same live, retained files** that our
`Workspace` API serves independently. All four candidates can keep Mecatl's loop outside the executor. Pod recreation
is normal; a replacement starts fresh against the same authorized workspace. Process resumption is not required.

### Shared integration work

For the API option, a trusted file service could reuse `osfs` over a mounted NFS, CephFS, or JuiceFS filesystem. The
executor mounts the same files through the platform's storage support. The mounted-loop option uses that filesystem
directly instead of a service. Neither requires copying files between commands or building a new filesystem protocol.

Both options need a recorded storage identity and verified agent scopes: for example, read-only inputs and writable
outputs. File-tool checks alone cannot restrict shell writes. Mount credentials stay in trusted infrastructure, and
the chosen backend or mount broker must enforce withdrawal; read-only mount flags alone do not provide grant expiry.
Keep the existing Edit semantics described above, not stronger atomicity against shell writers.

The evaluations below describe concrete platform-specific work in addition to this shared work. They are source-backed
integration proposals, not tested support or delivery estimates.

### agent-sandbox

[agent-sandbox][agent-sandbox] is a Kubernetes controller. A `Sandbox` describes a Pod and its lifecycle. Templates are
reusable Pod specifications; claims request sandboxes, optionally from a pool started in advance.

- **Workspace connection:** A [direct Sandbox][sandbox-lifecycle] accepts an existing PVC through its Pod specification.
  An independent file-service Pod can mount the same shared storage. Cross-node access needs a suitable backend and
  driver, normally with `ReadWriteMany`. Separate read-only and writable mounts can expose the authorized input/output view.
- **Pros:** Standard Kubernetes volume configuration supports this layout without an identified agent-sandbox source
  change. We can retain our executor helper and RuntimeClass selection.
- **Limits:** Use an independently owned PVC, not [Sandbox-owned claim templates][link-12] that can delete or recreate
  storage. The controller does not supply our file service or agent-level mount authorization.
- **Work:** Generate the restricted Pod mounts from verified authority, bind the Sandbox to the recorded storage identity,
  and adapt executor discovery after replacement. The shared Workspace service and mount enforcement remain Mecatl
  integration work. Router adoption is optional; its [authorization hook][router-authorizer] would need explicit wiring.

### OpenSandbox

[OpenSandbox][opensandbox] offers a sandbox-management API and `execd`, a guest command/file helper. Its Kubernetes
`BatchSandbox` controller creates Pods from templates; we can use it directly or through the server API.

- **Workspace connection:** The server's template mode accepts [existing PVCs][link-17], subdirectories, and per-mount
  read-only flags. Mount the same retained filesystem in our independent Workspace service. Set `createIfNotExists=false`
  and `deleteOnSandboxTermination=false`; Mecatl must still check the recorded PVC identity.
- **Pros:** Its [mount construction][opensandbox-mounts] supports read-only inputs and writable outputs from one PVC.
  Direct [BatchSandbox templates][batchsandbox-template] also support our own helper and ordinary Kubernetes volumes.
- **Limits:** The guest file API requires a running sandbox, so it cannot replace the independent Workspace route.
  Warm-pool and FastSandbox routes restrict dynamic volume attachment; use template mode for this proposal.
- **Work:** Map approved storage views to volume requests and connect current-agent authorization to command access.
  If adopting the server, adapt our command runner to its [injected execd service][opensandbox-bootstrap]; that helper
  does not require moving the harness inside. No platform patch was identified for basic shared-PVC attachment.

### Agent Substrate and env

[Substrate][substrate] schedules *actors*: isolated workloads, not logical agents. Its [env service][substrate-env]
provides commands and guest file access to an external harness. Workers attach storage through CSI, the standard
container-storage driver interface, without Kubernetes PVC objects.

- **Workspace connection:** Reuse its worker-side mount path to attach an independently allocated shared filesystem.
  Our Workspace service accesses that same allocation directly, not through env's guest file API.
- **Pros:** Its internal mount path already passes a volume handle and driver context to the worker. We can extend that
  path rather than implement a new storage transport.
- **Limits:** The [current volume API][substrate-volumes] provisions storage per actor and [deletes it with the actor][substrate-delete].
  It lacks an explicit existing-volume source. The reviewed [CSI adapter][substrate-csi] publishes mounts read-write
  and lacks the secret plumbing required by some CephFS/JuiceFS configurations.
- **Work:** Add an externally owned volume source that attaches/detaches but never creates/deletes retained storage.
  Carry read-only intent and required credentials through trusted CSI and runtime components. Bind actor mounts to verified
  agent scopes and secure the [env API][env-api]. These are upstream extensions or maintained changes, not configuration alone.

### E2B

[E2B][e2b] manages Firecracker microVMs. Its guest helper, `envd`, runs commands, while an NFS proxy mounts persistent
volumes into the guest. This is a different compute backend from Kubernetes RuntimeClass, but the harness can stay external.

- **Workspace connection:** In a self-hosted deployment, configure E2B's volume roots on a shared filesystem and mount
  that same filesystem in our Workspace service. Its [directory builder][e2b-volume-root] maps volume type, team, and volume
  ID to a host directory used by the NFS proxy and host file operations. All eligible nodes must reach the same files,
  not different local directories with matching names.
- **Pros:** We can supply the independent Workspace API ourselves. The separate `belt` content service is **not required**
  for this route, and the ordinary guest NFS mount can remain.
- **Limits:** Current [mount authorization][link-18] uses sandbox/team volume bindings, not Mecatl's agent scopes.
  The reviewed mount configuration lacks scoped read-only/subtree grants. This proposal requires self-hosted access to
  the backing mounts and NFS implementation; hosted support is not established.
- **Work:** Bind each export to the acting agent's permitted view and enforce read/write rights and grant withdrawal
  through NFS operations, including existing handles. Add the independent Workspace adapter, exact volume binding, and
  E2B command adapter. This extends a public mount path rather than depending on an unavailable content-service implementation.

Keeping our provider remains an option. Compare which platform best supports the shared workspace and reduces maintenance;
do not select one only for its compute lifecycle features.

## Choosing an integration

Select a candidate using documented capabilities, deployment fit, and implementation and operational cost. Prototype
only unresolved behavior that could change the choice. Validate the selected integration against these requirements
before claiming support; a full qualification plan belongs with its implementation.

[ganesha-fsal]: https://github.com/nfs-ganesha/nfs-ganesha/blob/952fb93373a6f9f9e187bf9bc35c41a9fc25efa6/src/include/fsal_api.h
[ceph-eviction]: https://github.com/ceph/ceph/blob/v19.2.3/doc/cephfs/eviction.rst
[ceph-client-auth]: https://github.com/ceph/ceph/blob/v19.2.3/doc/cephfs/client-auth.rst
[ceph-close]: https://github.com/ceph/ceph/blob/v19.2.3/src/client/Client.cc#L10444-L10476
[juice-meta]: https://github.com/juicedata/juicefs/blob/adcca1cc61bb4d668a945d64b2e176b44ac8e5b5/pkg/meta/redis.go#L3132-L3175
[juice-csi]: https://github.com/juicedata/juicefs-csi-driver/blob/2d2bd8a9ceb6233a9dc84a18ea169d8cacede157/docs/en/introduction.md
[go-fuse]: https://github.com/hanwen/go-fuse/blob/efadbedbc68e9f5781e3cc04e8be7217e89bf775/fs/api.go
[grant-draft]: ../scoped-resource-grants.md
[identity-draft]: ../agent-identity-model.md
[link-2]: https://github.com/stacklok/mecatl/blob/e731897077c75b020190f0d7b6e0b78af15082f1/engine/tool/environment.go#L9-L43
[link-3]: https://github.com/stacklok/mecatl/blob/e731897077c75b020190f0d7b6e0b78af15082f1/engine/tool/tool.go#L427-L505
[link-4]: https://github.com/stacklok/mecatl/blob/e731897077c75b020190f0d7b6e0b78af15082f1/internal/adapter/executionclient/client.go#L753-L792
[link-5]: https://github.com/stacklok/mecatl/blob/e731897077c75b020190f0d7b6e0b78af15082f1/internal/adapter/executioncontroller/podexec.go#L33-L73
[link-6]: https://github.com/stacklok/mecatl/blob/e731897077c75b020190f0d7b6e0b78af15082f1/internal/executionexecutor/executor.go#L26-L47
[link-7]: https://github.com/stacklok/mecatl/blob/e731897077c75b020190f0d7b6e0b78af15082f1/internal/adapter/executioncontroller/controller.go#L252-L327
[link-8]: https://github.com/stacklok/mecatl/blob/e731897077c75b020190f0d7b6e0b78af15082f1/internal/adapter/executioncontroller/handler.go#L642-L672
[link-9]: https://github.com/stacklok/mecatl/blob/e731897077c75b020190f0d7b6e0b78af15082f1/internal/app/redis_workspace.go#L23-L80
[link-10]: https://github.com/stacklok/mecatl/blob/e731897077c75b020190f0d7b6e0b78af15082f1/adapters/redisstore/workspace.go#L129-L179
[link-11]: https://github.com/agent-substrate/substrate/blob/0b91488d79637052458a29a6ba6c757bcf7a9292/docs/architecture.md#terminology-actor
[link-12]: https://github.com/kubernetes-sigs/agent-sandbox/blob/58707de4f339615645724deae3898ef65360192a/controllers/sandbox_controller.go#L1711-L1786
[link-13]: https://github.com/kubernetes-sigs/agent-sandbox/blob/58707de4f339615645724deae3898ef65360192a/site/content/docs/volumes/volume-claim-template/_index.md#L8-L23
[link-14]: https://github.com/kubernetes-sigs/agent-sandbox/blob/58707de4f339615645724deae3898ef65360192a/examples/latebind-storage-gke-sandbox/README.md#L1-L20
[link-15]: https://github.com/agent-substrate/substrate/blob/0b91488d79637052458a29a6ba6c757bcf7a9292/docs/csi-volumes.md#L7-L12
[link-16]: https://github.com/agent-substrate/substrate/blob/0b91488d79637052458a29a6ba6c757bcf7a9292/docs/authentication.md#L21-L31
[link-17]: https://github.com/opensandbox-group/OpenSandbox/blob/c7dc78a4090e5de2b9119e9bd93952cae24f87bd/docs/examples/kubernetes-pvc-volume-mount.md#L6-L18
[link-18]: https://github.com/e2b-dev/infra/blob/47096f195aca4a545a9079b706efb3a18f51cf3f/packages/orchestrator/pkg/nfsproxy/chroot/nfs.go#L122-L193
[link-19]: https://github.com/e2b-dev/infra/blob/47096f195aca4a545a9079b706efb3a18f51cf3f/docs/ARCHITECTURE.md#L511-L546
[link-20]: https://github.com/daytonaio/docs/blob/f6ff62bc27fb7701e04c51a1869aa8d016573cb5/src/content/docs/volumes.mdx#L114-L121
[context-model]: ../architecture/mecatl.modelith.yaml
[context-source]: ../../internal/app/harness_context.go
[context-binding]: ../../internal/app/harness_context_binding.go
[context-admission]: ../../internal/app/project_ingestion.go
[context-microvm]: ../../internal/app/execution.go
[templates]: https://github.com/stacklok/mecatl/issues/2109
[k8s-lifecycle]: ../../user-docs/features/execution-environments.md#native-kubernetes-lifecycle-and-retention
[inotify]: https://man7.org/linux/man-pages/man7/inotify.7.html
[nfs]: https://man7.org/linux/man-pages/man5/nfs.5.html#DATA_AND_METADATA_COHERENCE
[ceph]: https://docs.ceph.com/en/latest/cephfs/posix/
[juicefs]: https://juicefs.com/docs/community/architecture/
[juice-cache]: https://juicefs.com/docs/community/guide/cache/
[pod-updates]: https://kubernetes.io/docs/concepts/workloads/pods/#pod-update-and-replacement
[mount-ns]: https://man7.org/linux/man-pages/man7/mount_namespaces.7.html
[mount-propagation]: https://kubernetes.io/docs/concepts/storage/volumes/#mount-propagation
[router-authorizer]: https://github.com/kubernetes-sigs/agent-sandbox/blob/58707de4f339615645724deae3898ef65360192a/sandbox-router/authz/authorizer.go
[router-modes]: https://github.com/kubernetes-sigs/agent-sandbox/blob/58707de4f339615645724deae3898ef65360192a/sandbox-router/cmd/main.go#L194-L276
[router-tokenreview]: https://github.com/kubernetes-sigs/agent-sandbox/blob/58707de4f339615645724deae3898ef65360192a/sandbox-router/authz/tokenreview.go#L83-L95
[router-proxy]: https://github.com/kubernetes-sigs/agent-sandbox/blob/58707de4f339615645724deae3898ef65360192a/sandbox-router/proxy/proxy.go#L243-L276
[substrate-authn]: https://github.com/agent-substrate/substrate/blob/0b91488d79637052458a29a6ba6c757bcf7a9292/cmd/ateapi/internal/apiauthn/apiauthn.go
[substrate-authz]: https://github.com/agent-substrate/substrate/blob/0b91488d79637052458a29a6ba6c757bcf7a9292/cmd/ateapi/internal/authz/registry.go#L69-L83
[substrate-authz-pass]: https://github.com/agent-substrate/substrate/blob/0b91488d79637052458a29a6ba6c757bcf7a9292/cmd/ateapi/internal/authz/interceptor.go#L25-L54
[env-api]: https://github.com/agent-substrate/env/blob/f73ddaab0b2c18e97ca9466d4202e2e24e9902e2/cmd/ate-env-api/main.go#L58-L83
[substrate-delete]: https://github.com/agent-substrate/substrate/blob/0b91488d79637052458a29a6ba6c757bcf7a9292/cmd/ateapi/internal/controlapi/workflow_delete.go#L73-L106
[fuse]: https://docs.kernel.org/filesystems/fuse/fuse.html
[agent-sandbox]: https://github.com/kubernetes-sigs/agent-sandbox/tree/58707de4f339615645724deae3898ef65360192a
[substrate]: https://github.com/agent-substrate/substrate/tree/0b91488d79637052458a29a6ba6c757bcf7a9292
[substrate-env]: https://github.com/agent-substrate/env/blob/f73ddaab0b2c18e97ca9466d4202e2e24e9902e2/README.md#L147-L200
[opensandbox]: https://github.com/opensandbox-group/OpenSandbox/tree/c7dc78a4090e5de2b9119e9bd93952cae24f87bd
[e2b]: https://github.com/e2b-dev/infra/tree/47096f195aca4a545a9079b706efb3a18f51cf3f
[daytona]: https://github.com/daytonaio/docs/tree/f6ff62bc27fb7701e04c51a1869aa8d016573cb5
[sandbox-lifecycle]: https://github.com/kubernetes-sigs/agent-sandbox/blob/58707de4f339615645724deae3898ef65360192a/controllers/sandbox_controller.go#L1260-L1563
[batchsandbox-template]: https://github.com/opensandbox-group/OpenSandbox/blob/c7dc78a4090e5de2b9119e9bd93952cae24f87bd/kubernetes/apis/sandbox/v1alpha1/batchsandbox_types.go#L84-L120
[opensandbox-bootstrap]: https://github.com/opensandbox-group/OpenSandbox/blob/c7dc78a4090e5de2b9119e9bd93952cae24f87bd/server/opensandbox_server/services/k8s/provider_common.py#L116-L232
[opensandbox-mounts]: https://github.com/opensandbox-group/OpenSandbox/blob/c7dc78a4090e5de2b9119e9bd93952cae24f87bd/server/opensandbox_server/services/k8s/volume_helper.py#L25-L105
[substrate-volumes]: https://github.com/agent-substrate/substrate/blob/0b91488d79637052458a29a6ba6c757bcf7a9292/pkg/proto/ateapipb/ateapi.proto#L1253-L1316
[substrate-csi]: https://github.com/agent-substrate/substrate/blob/0b91488d79637052458a29a6ba6c757bcf7a9292/internal/volume/csi/plugin.go#L214-L319
[e2b-volume-root]: https://github.com/e2b-dev/infra/blob/47096f195aca4a545a9079b706efb3a18f51cf3f/packages/orchestrator/pkg/chrooted/builder.go#L25-L50
[daytona-status]: https://github.com/daytonaio/daytona/blob/ec4c21b2d597091ac09ecc278f3bcc172575a987/README.md
[daytona-license]: https://github.com/daytonaio/daytona/blob/01c502bb1f1ff8f2885d0cd490e043736083dca8/LICENSE
