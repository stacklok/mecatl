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

Workspace and CommandRunner remain affined to the same root and cwd. Existing opaque file
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
unsupported rather than a dual-stack claim. At the two host-local composition roots,
operators select `--microvm-guest-egress=permissive|deny-all|allowlist`; allowlist mode
uses repeatable `--microvm-guest-allow=HOST:PORT/tcp|udp` rules. That means
`mecated serve --headless ...` for the local server, or local embedded
`mecatui --default-placement microvm-local ...`. Rules normalize hostnames and reject
IP literals, wildcards, malformed values, and duplicates. The selection is host-only:
no engine port, HTTP/gRPC request, project setting, or release-default field carries it.
No API client selects this placement; `profile` remains limited to the default tool surface
or explicit `no-fs` attenuation.

A selected tightening mode filters IPv4 and disables IPv6, aborting readiness if either
enforcement step fails; it never falls back to permissive operation. The validated
selection overlays only the guest-egress fields after authenticated release/resource
defaults are built. Because daemon config identity includes policy, changing the selection
causes the existing mismatch/restart convergence rather than reusing a daemon with stale
policy. Omitting the flags leaves the prior permissive request byte-compatible.

Guest egress policy does not govern host providers, WebFetch, WebSearch, MCP, hooks, OCI
discovery, or telemetry.

## Readiness and bounded operations

Ordinary `--default-placement microvm-local` deployment selection calls the completed idempotent
`EnsureReady` flow from the signed Linux-amd64 release binary. Ordinary source builds do
not embed authenticated release defaults and fail closed. Before release download or
repository provisioning, readiness checks Git, Python 3, read-write KVM access, the Linux
user-namespace controls, and an actual ephemeral namespace creation to detect disabled or
exhausted quota. Doctor repeats those non-destructive host checks. Neither path changes
ACLs, groups, sysctls, or quota. The manager lock converges concurrent startup, and errors never rewrite
desired configuration. There is no dedicated activation flag, init command, or recover
command.

Status reads the durable repository logical-attachment inventory as the sole `microvm-local`
inventory authority; it survives daemon restart and shows the shared repository generation with each
exact session/ref/worktree and health, in deterministic owner-scoped pages of at most 64 entries
plus an opaque continuation token. Exact delete remains available after restart, removes only the
selected logical attachment and a clean worktree, and records a dirty worktree as retained stale
recovery state. It never deletes the repository VM.
Doctor remains diagnostic. Repository-VM deletion UX and a broad reconciliation state machine
are deferred. Same-process reattachment requires every dependency to remain live; daemon
restart fails loudly and preserves state because hosted networking cannot be reconstructed safely.

## Required live journey and limits

Linux amd64 KVM is the only required live platform. Its journey proves ordinary first use,
direct admitted Brood boot and in-process verification, one VM/rootfs, two sessions sharing
a declared cache but not a worktree, confined filesystem and exec, unrestricted IPv4
networking with the external-IPv6 limitation explicit, detach, and prompt daemon-restart
failure with preserved state and no replacement. Optional fail-closed tightening is proven
by AC6.2's production app/profile and network enforcement tests rather than a second live VM.

Deferred after the MVP: repository-VM deletion UX,
sophisticated retention, crash-orphan reconciliation, crash-durable and cross-process
merge, Linux arm64 and macOS live support, upstream Brood signing, independent refresh
channels, per-session fairness and quotas, dashboards, and exhaustive cache-poisoning
controls. Non-Git, scheduled, remote, multi-user, and cross-principal placement also remain
out of scope. See [ADR 0326](../adr/0326-microvm-execution-environments.md).
