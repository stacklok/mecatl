# Local microVM execution environments

> Part of the [mecatl architecture guide](../architecture.md) and listed in
> [`docs/READING.md`](../READING.md).

**What this covers:** the repository-scoped local microVM backend on qualified Linux amd64 and the experimental Darwin arm64 implementation: singleton VM/rootfs identity, logical worktree routing, artifact admission, platform ownership, networking, and attachment.

**Prerequisites:** [the ports](ports.md), especially `tool.Environment`, `Workspace`, bound
`CommandRunner`, and durable `EnvironmentRef` reattachment.

**Follow-on:** [the operator runbook](../usage/microvm-environments.md).

The backend is opt-in and lives outside the engine. Root composition talks to local
microvmd; go-microvm/libkrun and artifact machinery do not enter the importable engine.
Providers, permissions, hooks, MCP, memory, identity, and credentials remain on the host.

> **Platform status:** Linux amd64 KVM has automated and manual qualification evidence. The
> Darwin arm64 code path admits macOS 15+ with Hypervisor.framework, and the same deterministic
> mock-provider `task e2e:microvm` production-composed journey is executable there. Only
> cross-compilation evidence is available in this unmerged change; no physical Apple Silicon run
> has completed. Native Apple Silicon and signed-release qualification remain pending, so Darwin
> is experimental rather than released support. Linux arm64 is not admitted.

## Repository identity and singleton lifecycle

The reuse key is `(authenticated local operator, canonical Git common directory)`.
Canonicalization resolves linked worktrees and symlinks before deriving an opaque,
owner-confined state identity. Caller-controlled names and paths never become registry
selectors directly.

Microvmd owns an inter-process-locked durable registry. Each key admits one stable placement
generation and one private rootfs materialization. Concurrent first use converges on that
record. Different operators or Git common directories receive different records and mutable
state.

The placement generation remains stable across VM boots, so persisted session and schedule
refs do not change after daemon restart or host reboot. Replaceable boot state has its own
generation, VM ID, endpoint, runner identity, and fresh authority. Recovery first validates
the retained rootfs and immutable launch artifacts, confirms that configured artifact and
egress policy still match the admitted values, and reconciles every historical launch
attempt through its durable ownership lock and exact process receipt. Only then does it
publish a new boot intent and start hosted networking and the VM around the retained rootfs.
A live or uncertain prior runner blocks replacement. Missing, corrupt, or incompatible state
fails without deleting user data, creating empty state, or falling back to host execution.
On Linux, exact reconciliation uses the existing pidfd path. On Darwin, a private helper is
the runner's direct parent and owns the only nonblocking `Wait4` loop plus bounded TERM/KILL
signaling. The manager and microvmd use real service flocks on both Unix platforms; the shared
Unix implementations replace the former Linux-only lock files. Manager-initiated stop keeps
Linux's pidfd signaling. On Darwin it instead authenticates the owner-only control socket,
requires the serving daemon's exact installed identity, sends that identity in a private
graceful-shutdown request, and waits for the acknowledgement before microvmd cancels its server
and removes the socket. No Darwin manager path signals a recorded PID.
Daemon-pipe EOF stops and reaps the runner. The runner inherits the attempt flock,
so supervisor death leaves replacement fail-closed; a restarted daemon waits on the lock,
never signals a stored PID, and reports that operator recovery is required while it remains
busy. The repository's exact logical ref is retained through an ordinary restart. The Darwin
implementation is `environment/microvm/launch_ownership_darwin.go` (`ownDirectChild`); Linux
remains in `environment/microvm/launch_ownership_linux.go` (`Reconcile`).

The VM's rootfs, installed packages, guest home, and declared caches are deliberately
shared within the repository key. Code in one session can influence a later same-repository
session through that shared state. No equivalent sharing crosses the repository key.

## Logical worktree routing

The lifecycle registry does not expose an arbitrary host root through the logical RPC
protocol. For each session or isolated child, the daemon creates a host Git worktree with
its own branch and index, registers one guest-visible root, and mints an opaque
`EnvironmentRef` binding:

1. local owner;
2. repository VM generation;
3. logical environment identity;
4. assigned worktree root.

A newly accepted vsock/Unix connection has no identity from arrival order. Before sending a
binding capability, registration, request, or secret, the host sends an unpredictable
challenge; only the repository guest agent can answer with the generation boot authority.
The proof covers owner, repository and VM identity, generation, and `control`/`data` purpose.
A failed connector is closed without receiving protocol payload, and the host may authenticate
the next connection.

Guest handshake and every filesystem or exec request then authenticate the complete logical
binding. The guest resolves Workspace requests through its registration table rather than
accepting an arbitrary host path. Stale generations, replayed credentials, sibling refs, and
path escapes fail before dispatch. Transport failure never falls back to host filesystem or
shell.

The placement binding also carries an internal host governance root for permission,
learning, and authorization partitioning. It is distinct from both harness-context sources
and the guest execution root. MicroVM Bind and exact Reattach supply the configured source
checkout as that governance root only after the daemon resolves the binding against the
repository. Read, Edit, and Shell continue to use guest `/workspace`. No-FS supplies no
governance root.

Harness context is selected independently by operator policy. The MicroVM composition
registers `repository` instruction and command sources, but registration does not enable
them. Selection also requires separate project trust and ingestion admission. Each admitted
principal-scoped binding lazily borrows the exact authorized guest workspace through the
placement reattachment capability. The source
view supports reads and command discovery without a runner or execution `ReadLedger`,
and refuses mutations; its cleanup releases only that source borrow. Each successful create,
reattach, or isolated-child fork keeps one authenticated lifecycle socket and one daemon-issued
acquisition ID. Microvmd retains a logical guest registration until every harness process has
released its acquisition and all in-flight operations have drained. Closing one source, session,
schedule fire, or whole harness therefore cannot detach another local process that still uses the
same ref. Acquisition IDs remain private daemon memory and never enter session state, logs, or
model-visible content. The guest root string
`/workspace` is never reopened on the host or used as an identity. Independent `local`,
`driver`, `mcp`, and `skills` sources attach no guest placement. A required selected
repository source fails when its exact guest files are unavailable instead of using the host
checkout or another session. Root instructions retain AGENTS.md-first and CLAUDE.md-fallback
behavior, and directory commands remain live between observations.

Two source-capture transactions may run concurrently in one daemon process. Each initial
capture has a cumulative 256 MiB I/O accounting budget across tracked and untracked working
content, staged and unstaged binary patches, Git path listings, and the committed-tree tar,
plus 100,000 cumulative tracked/untracked/archive entry records. Each exact-state verification
scan is independently subject to the same 256 MiB and 100,000-entry ceilings. Git output and
untracked payloads stream through owner-private temporary files, which are removed before
placement registration. Exceeding either ceiling fails before registration with
`source capture limit exceeded`; reduce the repository or dirty working-tree size and retry.
The accounting budget intentionally counts bytes represented in more than one capture phase,
so it is not a promise that every 256 MiB checkout is admissible.

Workspace and CommandRunner remain affined to the same root and cwd. The MicroVM Workspace's
`tool.AuthorityResourceResolver` projects relative tool paths onto the same confined guest
`/workspace` identity used by filesystem RPCs; this lets authorization evaluate the guest
resource without mistaking the host governance root for tool authority. Absolute, empty,
NUL-containing, and escaping paths fail closed. Existing opaque file
versions, create-only and conditional writes, ordered stdout/stderr, exit status, bounded
output, and process-group cancellation remain the data-plane contract. This confines
protocol requests; it does not create a kernel sandbox between worktrees in one repository
VM. Arbitrary Bash can address a sibling's guest-visible path. The accepted trust boundary
is operator+repository, like same-principal Git worktrees: branches, indexes, working state,
and logical routing are distinct, but the Bash processes are not mutually hostile.
Different canonical repositories use different VMs and cannot address each other's guest
roots or host paths.

## Session and delegation attachment

Two sessions in one repository attach to one VM generation but hold different refs and
worktrees. Direct-write Subagents use the parent's complete Environment. Read-only
Subagents, Parallel branches, and Team members use new daemon-created worktrees and refs in
the same repository VM. All four delegation paths retain the parent's admitted harness
source binding and its source borrow for as long as the child reference needs it. A child
worktree never replaces that source anchor.

Closing a session or child releases that caller's acquisition. Microvmd unregisters the
logical attachment only after all local harness processes and in-flight operations release it.
Final cleanup preserves the durable worktree, repository VM, rootfs, caches, and sibling logical
environments. The MVP uses the existing isolated-child merge path: a non-conflicting change
applies and a conflict preserves the child. Daemon-wide multi-client merge serialization
and crash-durable merge recovery are explicitly not claimed.

## Base image, guest agent, and platform ownership

Brood `latest` is discovery only. Controlled admission resolves current-platform bytes
to an immutable digest and records a downstream mecatl endorsement. Runtime verifies that
endorsement in process through `toolhive-core/container/verifier` and fails before boot for
wrong, stale, missing, or corrupted evidence. Runtime and firmware archives are pinned to
go-microvm v0.0.41 with platform-specific SHA-256 values. The Darwin arm64 runtime
archive is pinned to `2641838c11064cd9b896825eeee263aab35e6cca9a2d7b3cfddd649ae4c5125d`
and firmware to `02ff1ca992c3b104cf6c4d0c2995fac5aa8646e7f7cafa0ee72b734b69fc5216`.

Mecatl neither rebuilds Brood nor publishes a derived guest-tools image. It independently
verifies and injects the guest agent into the one private repository rootfs. Static setup
establishes workload UID/GID 65532, `HOME`, `PATH`, default workdir, writable home, and
declared caches. Session and child creation do not copy the rootfs. The repository registry
uses initial schema version 1; unknown versions preserve state and fail closed.

On Linux, the backend passes `WithUserNamespaceUID(65532, 65532)` to go-microvm.
Those arguments name the namespace-side UID/GID; go-microvm maps them to the daemon's
host UID/GID when it creates the unprivileged user namespace. Linux does not prepare
ownership xattrs.

On Darwin, the backend uses go-microvm ownership preparation to project the same fixed
UID/GID 65532 through VirtioFS. Both read-write and read-only mounts require strict ownership
preparation. The private rootfs home and workspace are prepared before publication, and every
logical subtree is prepared before guest registration. Git objects are copied with private
`0600` file modes, prepared, and then sealed to `0400`; directories remain owner-only and
traversable. Host-side merge-back runs `git apply` and then refreshes ownership preparation
across the parent logical root. New inodes get host-derived modes, and surviving guest chmod
metadata remains intact. A preparation failure after apply explicitly reports that the patch
was already applied; merge-back is not transactional with concurrent Bash and does not promise
cache invalidation. Platform projection is selected in
`environment/microvm/repository_ownership_darwin.go` (`prepareRepositoryOwnership`), and
merge-back refresh is in `environment/microvm/operational.go` (`mergeRepositoryWorktreesWithOwnership`).

Host worktrees retain private modes on both platforms. The backend never widens them to
world-readable, world-writable, or world-traversable. Model commands run unprivileged.
Linux arm64 remains outside normal admission.

## Networking

`microvm-local` defaults to unrestricted guest IPv4 egress. The guest IPv6 stack remains
enabled, but go-microvm's hosted topology does not route external IPv6; external IPv6 is
unsupported rather than a dual-stack claim. The strict operator-tier `execution:`
settings select `default_placement: host-local|microvm-local` and
`microvm.guest_egress.mode: permissive|deny-all|allowlist`; allowlist mode uses
`allow: [HOST:PORT/tcp|udp]`. Both mecated and bare embedded mecatui consume the same
resolved policy, while explicit mecated serve flags remain higher-precedence overrides.
Rules normalize hostnames and reject
IP literals, wildcards, malformed values, and duplicates. The selection is host-only:
no engine port, HTTP/gRPC request, project setting, or release-default field carries it.
No API client selects this placement; `profile` remains limited to the default tool surface
or explicit `no-fs` attenuation.

A selected tightening mode filters IPv4 and disables IPv6, aborting readiness if either
enforcement step fails; it never falls back to permissive operation. The validated
selection overlays only the guest-egress fields after authenticated release/resource
default configuration is built. Omission resolves to permissive egress.

Guest egress policy does not govern host providers, WebFetch, WebSearch, MCP, hooks, OCI
discovery, or telemetry.

## Readiness and bounded operations

Selecting the `execution.default_placement` value `microvm-local` (or the mecated serve flag override)
configures the provider without provisioning. Actual default session creation calls the completed
idempotent
`EnsureReady` flow from the release binary for the current admitted platform; service startup and explicit
`no-fs` creation bypass it. A readiness failure occurs before placement creation and session
persistence, with no host-local fallback. Composition attaches one bounded readiness observer
used by diagnostics and the embedded TUI; the first-session connecting screen therefore reports
verified download/install/start stages without a second prewarm or policy path. The permissive
IPv4 default is disclosed before readiness starts. Embedded failures expose only a stable stage,
`mecated microvm doctor`, and the diagnostics-log location; detailed manager errors remain in the
operator log. Ordinary source builds do
not embed authenticated release defaults and fail closed. Before release download or
repository provisioning, readiness checks Git and Python 3. Linux additionally requires
read-write KVM access, user-namespace controls, and an actual ephemeral namespace creation.
Darwin requires Apple Silicon macOS 15 or newer and available Hypervisor.framework. Doctor
repeats those non-destructive host checks. Neither path changes ACLs, groups, sysctls, or quota. The manager lock serializes concurrent startup across
sessions and host processes. Only genuinely fresh state is installed. Compatible callers
reuse the repository daemon. If its configured process has stopped, ordinary readiness starts
it again under the same manager lock; process identity checks refuse a duplicate while a
recorded daemon is live or uncertain. Desired release or egress conflicts and unhealthy or
incompatible retained state fail without rewriting active configuration, deleting state, or
replacing user data. There is no dedicated activation flag, init command, or recover command.

Status reads the durable repository logical-attachment inventory as the sole `microvm-local`
inventory authority; it survives daemon restart and shows the shared repository generation with each
exact attachment/ref/worktree and health, in deterministic owner-scoped pages of at most 64 entries
plus an opaque continuation token. Its JSON uses `backend` and `attachment_id`; an attachment ID
is not a public mecatl session ID. Stopped and unhealthy states still return a bounded status
object with `state`, stable `error`, and `remediation` before the command exits nonzero.
`mecated microvm delete` requires the exact backend,
attachment, ref, and generation from one owner-scoped status row plus confirmation. It removes
only that logical attachment and a clean worktree, preserves dirty worktrees, and never deletes
or resets the repository VM. Doctor, status, and delete are local to the execution host and
current OS principal; mecatui and remote connect expose no administration surface. Doctor and
status never boot or repair a repository. A fresh host with satisfied prerequisites is
successfully reported as ready to configure on first use. Exact admission and reattachment
perform recovery lazily when a retained runtime is unavailable.

## Required live journey and limits

The automated `task e2e:microvm` journey supports Linux-amd64 KVM and Darwin-arm64 HVF and
uses the deterministic mock provider without contacting OpenRouter. Linux is the automated
qualified gate; the Darwin path is executable but still awaits a physical Apple Silicon run.
Separately, a manual qualification executed on 2026-09-10 used
OpenRouter `openai/gpt-5-mini` through public HTTP session creation and prompting. Normal Write,
Read, and Bash ran in the Wolfi guest as UID 65532, with a proof marker absent from the source
checkout. The same session reattached after mecated restarted while microvmd remained alive;
doctor and status were healthy. No credential, private placement ref, socket, or host path was
retained in the evidence. This proves first-use readiness, direct artifact admission and in-process verification, guest
filesystem and Bash execution, source isolation from the guest namespace, and exact harness
restart reattachment. That manual run did not exercise a microvmd restart. Deterministic
composition and launch-ownership tests cover retained-state recovery, authority rotation,
exact process reconciliation, repository-VM reuse across sessions, and delegation routing.
Optional fail-closed network tightening is proven by AC6.2's deterministic app/profile and
network-enforcement tests rather than a second live VM.

The Darwin implementation has local Linux-host cross-compilation evidence for its unit,
ownership-xattr, and lifecycle tests. The proposed CI job runs the complete nested module with
`-race` on macOS, but this change has not been pushed and therefore has no CI result. No native
Apple Silicon VM journey or signed-candidate journey has run. Darwin remains an experimental
code path until those qualifications prove real guest filesystem/Shell behavior, source
isolation, normal restart reattachment, and release admission. A runner surviving supervisor
death remains fail-closed and requires operator recovery; it is not an automatic-restart case.

Deferred after the MVP: repository-VM deletion UX, sophisticated retention,
crash-durable and cross-process merge, Linux arm64 support, released Darwin support, upstream
Brood signing, independent refresh channels, per-session fairness and quotas, dashboards, and
exhaustive cache-poisoning controls. Non-Git, remote, multi-user, and cross-principal placement remain out of scope.
Scheduled tasks are supported through the existing server-owned placement contract: origin-backed
schedules borrow their exact logical attachment, while independent schedules allocate one logical
attachment and persist a trusted ownership bit. Every fire reattaches that ref. Delete disables
first, preserves a claimed/running record for scheduler recovery, and removes only the owned
logical attachment; dirty state remains exact-reattachable and the repository VM/rootfs and
siblings are never deleted. See [ADR 0367](../adr/0367-microvm-execution-environments.md).
