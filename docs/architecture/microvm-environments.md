# Local microVM execution environments

> Part of the [mecatl architecture guide](../architecture.md) and listed in
> [`docs/READING.md`](../READING.md).

**What this covers:** the Linux amd64 repository-scoped microVM MVP: singleton VM/rootfs
identity, logical worktree routing, artifact admission, Linux ownership, networking, and
attachment.

**Prerequisites:** [the ports](ports.md), especially `tool.Environment`, `Workspace`, bound
`CommandRunner`, and durable `EnvironmentRef` reattachment.

**Follow-on:** [the operator runbook](../usage/microvm-environments.md).

The backend is opt-in and lives outside the engine. Root composition talks to local
microvmd; go-microvm/libkrun and artifact machinery do not enter the importable engine.
Providers, permissions, hooks, MCP, memory, identity, and credentials remain on the host.

> **MVP status:** the repository-scoped backend is implemented for Linux amd64 KVM.
> Compile/static coverage on other platforms is not a live-support claim.

## Repository identity and singleton lifecycle

The reuse key is `(authenticated local operator, canonical Git common directory)`.
Canonicalization resolves linked worktrees and symlinks before deriving an opaque,
owner-confined state identity. Caller-controlled names and paths never become registry
selectors directly.

Microvmd owns an inter-process-locked durable registry. Each key admits one VM generation
and one private rootfs materialization. Concurrent first use converges on that record.
Different operators or Git common directories receive different records and mutable state.

Exact reattachment is permitted only while every owned dependency remains live in the
current daemon process. The go-microvm hosted network provider is in-process and cannot be
safely reconstructed after daemon restart, so restart readiness and resolve fail promptly
with an actionable phase-specific error while preserving the VM record, rootfs, and logical
worktrees. Missing, foreign, or otherwise inconsistent VM/process/endpoint state follows the
same fail-closed path. The MVP never silently creates a replacement or destroys an uncertain
resource.

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

The placement binding also carries an internal host-composition root that is distinct from
the guest execution root. MicroVM Bind and exact Reattach supply the configured source checkout
only after the daemon resolves the binding against that repository. Host-side project
instructions, rules, slash commands, permission learning, and authorization use that root;
Read/Edit/Bash continue to use guest `/workspace`. The composition root is not snapshot data,
public metadata, or model-visible content. Missing MicroVM composition context fails closed,
and no-FS deliberately supplies none.

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
resource without mistaking the host composition root for tool authority. Absolute, empty,
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
the same repository VM.

Closing a session or child releases process-local handles and unregisters the attachment;
it does not stop the repository VM or remove its rootfs, caches, or another logical
worktree. The MVP uses the existing isolated-child merge path: a non-conflicting change
applies and a conflict preserves the child. Daemon-wide multi-client merge serialization
and crash-durable merge recovery are explicitly not claimed.

## Base image, guest agent, and Linux ownership

Brood `latest` is discovery only. Controlled admission resolves Linux amd64 platform bytes
to an immutable digest and records a downstream mecatl endorsement. Runtime verifies that
endorsement in process through `toolhive-core/container/verifier` and fails before boot for
wrong, stale, missing, or corrupted evidence.

Mecatl neither rebuilds Brood nor publishes a derived guest-tools image. It independently
verifies and injects the guest agent into the one private repository rootfs. Static setup
establishes workload UID/GID 65532, `HOME`, `PATH`, default workdir, writable home, and
declared caches. Session and child creation do not copy the rootfs.

On Linux, the backend passes `WithUserNamespaceUID(65532, 65532)` to go-microvm.
Those arguments name the namespace-side UID/GID; go-microvm maps them to the daemon's
host UID/GID when it creates the unprivileged user namespace. Host worktrees retain their
private modes; the backend never widens them to world-readable, world-writable, or
world-traversable. Model commands run unprivileged. Linux arm64 live support and macOS
ownership parity are deferred, regardless of compile/static coverage.

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
`EnsureReady` flow from the signed Linux-amd64 release binary; service startup and explicit
`no-fs` creation bypass it. A readiness failure occurs before placement creation and session
persistence, with no host-local fallback. Composition attaches one bounded readiness observer
used by diagnostics and the embedded TUI; the first-session connecting screen therefore reports
verified download/install/start stages without a second prewarm or policy path. The permissive
IPv4 default is disclosed before readiness starts. Embedded failures expose only a stable stage,
`mecated microvm doctor`, and the diagnostics-log location; detailed manager errors remain in the
operator log. Ordinary source builds do
not embed authenticated release defaults and fail closed. Before release download or
repository provisioning, readiness checks Git, Python 3, read-write KVM access, the Linux
user-namespace controls, and an actual ephemeral namespace creation to detect disabled or
exhausted quota. Doctor repeats those non-destructive host checks. Neither path changes
ACLs, groups, sysctls, or quota. The manager lock serializes concurrent startup across
sessions and host processes. Only genuinely fresh state is installed and started; compatible
callers reuse the repository daemon. Desired release or egress conflicts and unhealthy or
incompatible runtime state fail without rewriting active configuration, stopping the daemon,
deleting state, or replacing repository runtime. There is no dedicated activation flag, init
command, or recover command.

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
current OS principal; mecatui and remote connect expose no administration surface. A fresh host
with satisfied prerequisites is successfully reported as ready to configure on first use.
Same-process reattachment requires every dependency to remain live; daemon
restart fails loudly and preserves state because hosted networking cannot be reconstructed safely.

## Required live journey and limits

The automated `task e2e:microvm` Linux-amd64 KVM gate uses the deterministic mock provider and
does not contact OpenRouter. Separately, a manual qualification executed on 2026-09-10 used
OpenRouter `openai/gpt-5-mini` through public HTTP session creation and prompting. Normal Write,
Read, and Bash ran in the Wolfi guest as UID 65532, with a proof marker absent from the source
checkout. The same session reattached after mecated restarted while microvmd remained alive;
doctor and status were healthy. No credential, private placement ref, socket, or host path was
retained in the evidence. This proves first-use readiness, direct artifact admission and in-process verification, guest
filesystem and Bash execution, source isolation from the guest namespace, and exact harness
restart reattachment. It does not prove microvmd restart recovery: that remains fail-closed as
described above. Deterministic composition tests prove repository-VM reuse across multiple
sessions, daemon-restart failure behavior, and delegation routing. Optional fail-closed network
tightening is proven by AC6.2's deterministic app/profile and network-enforcement tests rather
than a second live VM.

Deferred after the MVP: repository-VM deletion UX,
sophisticated retention, crash-orphan reconciliation, crash-durable and cross-process
merge, Linux arm64 and macOS live support, upstream Brood signing, independent refresh
channels, per-session fairness and quotas, dashboards, and exhaustive cache-poisoning
controls. Non-Git, remote, multi-user, and cross-principal placement remain out of scope.
Scheduled tasks are supported through the existing server-owned placement contract: origin-backed
schedules borrow their exact logical attachment, while independent schedules allocate one logical
attachment and persist a trusted ownership bit. Every fire reattaches that ref. Delete disables
first, preserves a claimed/running record for scheduler recovery, and removes only the owned
logical attachment; dirty state remains exact-reattachable and the repository VM/rootfs and
siblings are never deleted. See [ADR 0342](../adr/0342-microvm-execution-environments.md).
