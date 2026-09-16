# ADR 0346 — Repository-scoped local microVM execution environments

- Status: Accepted
- Date: 2026-08-19
- Scope: Linux amd64 local microVM MVP; repository VM identity; logical worktree and schedule placement lifecycle; artifact admission; guest ownership and networking
- Supersedes: the earlier branch-only session-per-VM, derived guest-tools image, deny-default networking, explicit init/recover, external cosign, and per-session rootfs decisions recorded in prior revisions of this unmerged ADR
- Superseded by: none

## Context

ADRs 0208, 0211, and 0214 already define the engine contract: a
`tool.Environment` affines one `Workspace` and optional bound `CommandRunner`, isolated
children receive complete Environments, and a durable open `EnvironmentRef` reattaches a
non-in-tree backend without local fallback. The unmerged microVM implementation proved the
seam, but its session-per-VM design made ordinary coding expensive and grew lifecycle,
operations, and platform ambitions beyond the first usable release.

The useful MVP reuse boundary is one local operator and one Git repository. Sessions need
separate writable Git state but can intentionally share packages, guest home, declared
caches, and a rootfs. Different repositories must not share that mutable state.

Brood provides the base. Its mutable `latest` reference is discovery rather than execution
identity, so controlled admission resolves and endorses immutable platform bytes. Linux
amd64 KVM is the sole live platform in this decision. Linux arm64 and macOS live support are
future work, not implied by compile coverage.

## Decision

### 1. Keep the core Environment contract unchanged

The backend implements the existing `tool.Environment`, `Workspace`, bound
`CommandRunner`/`CommandStreamer`, `EnvironmentForker`, `EnvironmentMerger`, and the
server-owned `PlacementProvider`/exact `PlacementReattacher` contracts. Repository identity,
OCI admission, and VM lifecycle remain outside `engine/`; go-microvm/libkrun and artifact
dependencies remain in the opt-in module and composition.

Providers, permissions, hooks, MCP, memory, identity, and credentials remain on the host.
Only execution tools and the independently admitted guest agent run in the VM.

### 2. Use one durable VM/rootfs record per operator and canonical Git repository

The daemon canonicalizes the Git common directory and combines it with the authenticated
local operator identity. That tuple is the only repository key. Repository-controlled
display names, linked-worktree paths, symlinks, and path components cannot choose a state
directory directly.

A durable, inter-process-locked registry admits one VM generation and one private rootfs
materialization for that key. Concurrent first use converges on that singleton. Exact
reattachment is allowed only while the VM, capability issuer, guest transport, and hosted
network provider are all still owned and live in the current daemon process. Daemon restart
loses the in-process hosted network provider, so readiness and resolve fail promptly with a
phase-specific inconsistent/unavailable error while preserving the durable record, rootfs,
and worktrees. Missing, foreign, or otherwise inconsistent runtime state follows the same
safe behavior: the MVP does not mint an empty replacement, destroy an orphan, or claim
comprehensive crash reconciliation.

The repository VM shares its rootfs, package installation, guest home, and declared caches
among logical environments in that repository. This is an intentional trust boundary.
Another operator or canonical Git common directory receives another registry entry, VM,
rootfs, home, and cache boundary.

This registry task does not itself add guest multiplexing. Logical worktree registration
and routing are the next layer over the singleton lifecycle.

### 3. Route authenticated logical worktrees inside the repository VM

The daemon creates a distinct host Git worktree, branch, and index for each session or
isolated child and registers its guest-visible root. Each opaque `EnvironmentRef` binds the
owner, repository VM generation, and assigned root. Before the host sends a binding
capability, registration, request, or secret on a newly accepted vsock/Unix connection, the
peer must answer an unpredictable challenge with the repository generation's boot
authority. The proof binds owner, repository identity, VM identity, generation, and channel
purpose (`control` or `data`); connection arrival order conveys no identity. Guest handshake
and every filesystem or exec request then authenticate the complete logical binding before
dispatch.

A protocol request cannot choose an arbitrary host path, switch its `EnvironmentRef` to a
sibling worktree, replay a stale binding, or fall back to host execution. Workspace and bound
runner observe the same assigned root and cwd and preserve the existing versioned
filesystem and streaming/cancellation contracts. This logical RPC confinement is not a
kernel boundary between same-repository processes: arbitrary Bash in the repository VM may
address another worktree through its guest-visible path. That is the accepted
operator+repository trust boundary, analogous to same-principal agents using Git worktrees;
worktrees isolate Git working/index state and routing, not mutually hostile processes.
Different canonical repositories remain isolated in different VMs and do not expose one
another's host paths or guest roots.

Sessions in one repository reuse the same VM with distinct refs and worktrees. A
direct-write child reuses its parent's Environment; a read-only Subagent, Parallel branch,
or Team member receives a distinct daemon-created worktree and ref in that VM. Closing a
session or child detaches process-local handles; it does not destroy the repository VM,
rootfs, shared caches, or sibling logical environments. Mecated retains one exact attached
binding per live placement generation and shares it across session runs, discovery, ACP, and
team borrowers. `CloseSession`/`EndSession`, team cleanup, or service shutdown releases that
Service-owned binding only after the final borrower exits, so no borrower can detach another.
A placement provisioned for a session that fails before its first durable snapshot is instead
rolled back by exact generation deletion; dirty or failed cleanup remains in durable daemon
inventory for explicit recovery and never broadens deletion to the repository VM, rootfs, or
siblings. Once persistence succeeds, rollback authority is discarded and ordinary teardown is
detach-only.

Schedules resolve placement once at creation and persist its exact ref and scope. A schedule
created from a session borrows that session's logical worktree and never owns its lifecycle.
An independently created MicroVM schedule provisions one logical worktree and owns it until
the first fire claim. Updates preserve the placement and ownership relationship. Each fire
mints a fresh persisted `sched--` session but reauthorizes and attaches the same exact
schedule ref; neither a mecated/harness restart while microvmd remains live nor a changed
deployment default causes rebinding or host fallback. Reattachment succeeds only when that
exact repository generation and its owned runtime dependencies remain available under the
general rule above; restarting microvmd itself remains the loud fail-closed case.

Owned schedule deletion uses an atomic, durable deleting marker. Before any fire claim it
disables the schedule, removes only the exact schedule-owned logical placement, preserves a
dirty worktree according to provider deletion semantics, and conditionally removes the same
schedule incarnation; failures leave the marker and exact ref for retry. The first atomic
claim transfers placement lifetime to the persisted fire-session lineage. From then on,
schedule deletion removes only the schedule record and retains the worktree for historical
or resumable fire sessions. A claimed or running fire blocks deletion until recovery settles
it. Borrowed, no-FS, host-local, and legacy records without explicit ownership metadata are
never destructively cleaned up. No schedule operation deletes the repository VM, rootfs,
origin session, or sibling logical worktrees. This retention is the cost of preserving
already-published fire sessions without a crash-durable placement reference-counting or
lineage garbage-collection protocol.

The MVP uses the existing isolated-child merge behavior: non-conflicting changes apply and
conflicts preserve the child. It does not add crash-durable merge recovery, daemon-wide
cross-process serialization, or multi-client merge proofs.

### 4. Consume Brood directly and provide an explicit Linux guest

Brood `latest` is used only in controlled discovery. Release/admission resolves the Linux
amd64 platform manifest digest, records the discovery evidence, and creates a downstream
mecatl endorsement. Runtime accepts only admitted immutable bytes and fails before boot on
changed resolution, wrong platform, stale policy, missing endorsement, or corruption.

Mecatl does not rebuild Brood or publish a derived guest-tools image. The independently
built guest agent is verified separately and injected into the repository generation's one
private rootfs materialization. Guest setup explicitly establishes workload UID/GID 65532,
`HOME`, `PATH`, default workdir, writable home, and declared package/tool cache directories.
Creating a session or child does not clone the rootfs again.

Sigstore verification runs in-process through `toolhive-core/container/verifier`.
Production admission has no cosign executable/path, subprocess, or temporary-file protocol.
Upstream Brood signing is deferred; the downstream endorsement is the MVP trust root.

On Linux, an unprivileged user namespace maps guest workload UID/GID 65532 to the daemon
user. Host worktrees are not widened to world-readable, world-writable, or
world-traversable modes. Model commands run unprivileged. Live macOS ownership parity and
Linux arm64 are not part of this MVP.

The built-in hosted topology defaults to unrestricted IPv4 guest egress. The guest IPv6
stack remains enabled, but go-microvm hosted networking does not route external IPv6; external
IPv6 is unsupported and is reported that way rather than as dual-stack connectivity.
Operators may select deny-all or an allowlist; tightening filters IPv4 and disables guest
IPv6, and fails readiness closed if either enforcement step cannot start. Guest policy does
not govern host providers, WebFetch, WebSearch, MCP, hooks, OCI discovery, or telemetry.

### 5. Make readiness part of ordinary use

Operator configuration selects `microvm-local` as the deployment's trusted default
placement provider. Startup or first use runs the completed idempotent `EnsureReady` operation under its
inter-process manager lock. Failure is actionable and never rewrites desired configuration;
ordinary use retries it.

There is no dedicated `--microvm`, required `init`, or `recover`. Existing nonduplicative
`status`, `doctor`, and `delete` commands remain, but the MVP adds no repository-VM deletion
UX. Status returns deterministic owner-scoped pages of at most 64 entries and prints an
opaque continuation token when another page exists. Restart supports exact reattachment only
for dependencies still live in-process; a daemon restart loses hosted networking and therefore
returns a loud safe failure without replacing or deleting the preserved generation.

### 6. Keep source-build release activation developer-only

Published binaries embed authenticated release defaults; ordinary source builds fail closed.
For local development only, the `microvm_dev` compile-time tag registers two local-root
flags: `--microvm-dev-release=ABSOLUTE_DESCRIPTOR_PATH` and
`--microvm-dev-acknowledge-untrusted-local-artifacts`. Both are required. Untagged
binaries do not expose them, and a release-stamped binary rejects the development path
regardless of its build tags.

The descriptor schema is `mecatl-microvm-development-release/v1`. Before readiness it
strictly binds source-build identity, Linux amd64, owner-only regular non-symlink local
bundle and public-key paths, bundle SHA-256, public-key identity, and policy revision.
Unknown, missing, unsafe, or mismatched values fail closed. The admitted local bundle then
uses the same manager transaction and verification as a downloaded release: safe
extraction, bundled installer, artifact signatures, provenance, immutable references, and
daemon policy checks remain mandatory.

This unsupported workflow exists only in local `mecated serve` and embedded local
`mecatui` composition. It adds no HTTP/gRPC field, settings key, environment-variable
control, arbitrary URL, or installer override. Preparation and tagged binaries stay under
`.scratch`; ordinary `task build` remains unchanged.

## Consequences

### Benefits

- Two sessions reuse one boot, rootfs, package set, home, and declared cache while retaining
  distinct Git worktrees.
- Repository and operator boundaries prevent accidental cross-repository mutable-state
  sharing.
- The existing Environment seam and merge behavior are reused rather than widened.
- Direct admitted Brood bytes and in-process verification keep the distribution and trust
  path small.
- One Linux amd64 first-use and restart journey can prove the complete MVP.

### Costs and limits

- Same-repository code can influence later sessions through shared packages, home, and
  caches, and arbitrary Bash can address sibling guest worktree paths; exhaustive
  cache-poisoning defenses and kernel isolation between same-repository worktrees are not
  claimed.
- A broken or orphaned recorded generation can require manual intervention because the MVP
  fails loudly rather than reconciling or deleting it.
- No new repository-VM deletion UX or sophisticated retention policy ships in the MVP.
- Merge correctness is the existing single-client behavior, not a crash-durable or
  cross-process transaction.
- Once a schedule fire is claimed, its shared logical worktree is retained with the
  fire-session lineage; automatic lineage-wide placement garbage collection is not claimed.
- Permissive egress is not network containment.

## Deferred decisions

The following require later ADRs or amendments after MVP evidence:

- repository-VM deletion UX and sophisticated retention;
- crash-orphan reconciliation beyond loud safe failure;
- crash-durable merge recovery and cross-process/multi-client merge proofs;
- macOS live ownership parity and Linux arm64 live support;
- upstream Brood signing and independent refresh channels;
- per-session fairness, quotas, dashboards, and exhaustive cache-poisoning controls.

Non-Git sources, remote or multi-user microvmd, cross-principal VM sharing,
unified host+guest egress containment, and guest provider/MCP credentials also remain out of
scope.

## Superseded branch-only decisions

This unmerged ADR replaces session-lifetime VMs, child VMs, rebuilt Brood and a derived
guest image, deny-default networking, explicit activation/recovery, external cosign, world
mode widening, and per-session rootfs copies. Tasks 59–61 completed readiness, admission,
and the static rootfs primitive. Tasks 62–65 implement only the MVP above.

## See also

- [Acceptance plan](../acceptance/microvm-execution-environments.md)
- [MicroVM architecture](../architecture/microvm-environments.md)
- [Operator guide](../usage/microvm-environments.md)
