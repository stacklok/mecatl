# Placement and local microVM environments

_Placement_ decides which `tool.Environment` (Workspace, CommandRunner, and read ledger)
backs a session's tools. This page covers the server-owned placement contract, then the
local microVM runtime that implements it with one VM per repository. Operator steps are in
the [local microVM guide](../../user-docs/operating/microvm-environments.md); contributors
can [qualify local source builds](#qualify-local-microvm-source-builds).

## Server-owned placement

Trusted composition installs exactly one `server.PlacementProvider` and an authorization
scope before listeners serve; startup validates it but never binds. Clients never send a
workspace path, cwd, placement ID, or `EnvironmentRef`. A create picks only the deployment
`default` or explicit `no-fs`. A path in a request would be filesystem authority that a
remote caller could aim at the host. ACP's required cwd is only checked against the
binding already chosen. Current providers:

| Provider | Default placement |
| --- | --- |
| Local (`internal/app/placement.go`) | The operator's private `--workspace` root; no root means no-FS |
| MicroVM (`internal/adapter/microvm`) | A new logical worktree in the repository VM |
| Redis filesystem (`internal/app/redis_workspace.go`) | A principal-scoped Redis workspace |
| Remote execution (mecak8s) | The execution service's binding; no-FS stays local |

### Exact reattachment

Each persisted session carries one opaque `session.EnvironmentRef{Kind, ID, Revision}`.
Every later run, load, or schedule fire reauthorizes the owner and reattaches that exact ref.
`server.PlacementBinder` rejects any binding whose ref differs, and a provider without
reattachment fails. Bind is never used as a fallback: following the current default after
a restart or config change would silently move a session onto a different tree.
Failures surface as stable errors (not found, unavailable, changed, invalid binding);
provider detail goes to diagnostics with path-like words redacted.

A binding also carries a private _governance root_: the host directory that partitions
permission rules, learning, and authorization. It is separate from the execution root. For
local placements the two coincide. For microVM it is the source checkout, while tools run
at guest `/workspace`; a guest path is never reopened on the host. No-FS has none.

### Alternate worktrees, schedules, and delegation

Switching to another Git worktree requires an owned source session. `ListWorktrees`
reattaches the source and returns display metadata plus an opaque selector: an HMAC over
caller, source session, and choice, keyed by a random per-process key. `ClearSession` and
`ForkSession` accept that selector. The provider re-enumerates before binding and reports
"changed" if the choice moved. Selectors are never persisted, so clients relist after a
restart. Only the local provider offers discovery; the microVM provider rejects worktree
selectors.

Schedules persist a resolved ref plus scope, and every fire reattaches it. A schedule
created without an origin session owns its placement: deleting it before the first claimed
fire cleans the placement up, and afterwards keeps it for fire sessions that may resume.

Delegated children never choose a placement; they share or fork the parent's Environment.

## Local microVM runtime

`microvm-local` is opt-in operator configuration that the engine never imports. Root
composition (`internal/app/execution.go`) talks to a user-local `microvmd` daemon over an
owner-only socket; hypervisor code lives in the nested `environment/microvm` module. Only
the Workspace and runner cross into the guest. Providers, permissions, hooks, MCP, memory,
identity, credentials, and the TUI stay on the host. Linux amd64 with KVM is the qualified
platform; Darwin arm64 (macOS 15+, Hypervisor.framework) is admitted but experimental.

### Repository identity and lifecycle

The reuse key is (operator, canonical Git common directory). Linked worktrees and symlinks
resolve to the same key, and the key is hashed into an opaque state directory, so no
repository-controlled name or path becomes a selector. Concurrent first use converges on one
durable record under a per-repository lock.

The record separates a stable placement generation from replaceable boot state (VM ID,
endpoint, runner, and boot authority), so persisted refs survive daemon restarts and reboots.
The rootfs, packages, guest home, and caches are deliberately shared within one key, so one
session can influence a later one in the same repository. Nothing is shared across keys.

### Logical worktrees and routing

For each session the daemon captures the source checkout, including dirty and untracked
state, into a new host Git worktree on its own `mecatl/` branch, and registers it with the
guest as one logical environment. A capture has a 256 MiB and 100,000-entry budget and fails
with `source capture limit exceeded` before registration.

Each new guest connection must answer a challenge with the current boot authority, for its
`control` or `data` purpose, before any capability or payload is sent. Every filesystem or
exec request carries the full logical binding, and the guest resolves paths through its
registration table. Stale generations, sibling refs, and path escapes fail before dispatch;
transport failure never falls back to host files or shell.

This confines the protocol, not processes: worktrees in one VM are not sandboxed from each
other's Shell. The trust boundary is operator plus repository, like same-user Git worktrees.

### Sessions, delegation, and merge

Sessions in one repository share the VM generation but hold separate refs and worktrees.
Direct-write Subagents use the parent's Environment. Read-only Subagents, Parallel branches,
and Team members get daemon-forked worktrees in the same VM. Merge-back runs `git apply
--check` and then applies the child's patch: a conflict leaves the child intact. The merge is
not serialized against concurrent Shell in the parent.

Each create, reattach, or fork holds a daemon-issued acquisition. A logical environment
detaches only after every holder releases it and in-flight operations drain, so closing one
session never detaches a ref another process uses. Detach keeps the worktree and the VM.

### Image, guest agent, and credentials

The guest image is admitted, not built: discovery of the Brood base image resolves to an
immutable digest whose signed provenance is verified in process
(`environment/microvm/artifact_sigstore.go`) before boot. Mecatl injects its own verified guest
agent into the one private rootfs. Commands run as fixed unprivileged UID/GID 65532; Linux maps
it through a user namespace, and Darwin projects ownership through VirtioFS.

Guest processes get a fixed environment (home, path, caches, isolated Git config), and exec
requests carry only a command, so no host variable or credential reaches the guest.

### Networking

Guest egress defaults to permissive IPv4, which readiness discloses; external IPv6 is not
routed. `deny-all` or a hostname allowlist also disables guest IPv6, and readiness fails
rather than falling back if either step fails. The policy is operator-tier
[configuration](../../user-docs/reference/configuration.md) that no API request or project
setting can change. It governs guest processes only, not host providers, WebFetch, MCP,
hooks, or telemetry.

### Readiness, administration, and recovery

Readiness runs inside the provider's Bind and Reattach, never at startup or for no-FS. Under
a manager lock it checks Git, Python 3, and on Linux read-write KVM plus a real user-namespace
probe, then installs a fresh verified release or reuses (restarting if stopped) a compatibly
configured daemon. Policy conflicts and unrecognized state fail without rewriting or deleting
anything. Source builds lack the link-time release defaults and fail closed.

Runtime-directory selection budgets the canonical repository socket paths, including the
hosted-network suffix and macOS's `/tmp` expansion to `/private/tmp`. If the XDG runtime
path is too long, the manager uses the owner-only `/tmp/mv-<UID>` directory and checks that
fallback against the same bound. Existing ownership, symlink, and daemon-identity checks
still apply.

`mecated microvm doctor|status|delete` is the local, current-user administration surface;
remote clients have none. Doctor and status never boot or repair a VM. Delete removes one exact
logical attachment and its clean worktree; dirty worktrees and the VM stay.

Recovery is lazy: an exact reattach to a cold VM revalidates the retained rootfs and artifacts,
checks that configured artifact and egress policy still match what was admitted, and reconciles
the previous runner before booting with a rotated authority. Linux proves the old runner dead
through a pidfd; Darwin waits for its supervising helper's lock and never signals a stored PID.
A live or uncertain prior runner, or missing or mismatched state, fails and asks for operator
recovery. It never deletes user data or falls back to host execution.

## Qualify local microVM source builds

Repository developers can exercise the implemented Darwin path on a non-root Apple Silicon
host running macOS 15 or newer with Hypervisor.framework. Artifact preparation uses the
current-platform development descriptor and the go-microvm runtime and firmware pinned
in `environment/microvm/go.mod`.
The following agent run is offline and uses no provider credential or paid model.

The provider-offline production-composed journey prepares the current-platform
release fixture and runs the scripted mock-provider checks:

```sh
task e2e:microvm
```

This journey requires artifact-network access during preparation. It exercises guest Read,
Write, and Shell operations, fixed UID 65532, host-path and source isolation, isolated-child
merge and conflict handling, graceful microvmd restart, and exact reattachment. It does not
contact an LLM provider.

For an interactive developer check through the public HTTP API, prepare the artifacts and
source binaries:

```sh
task microvm:dev:prepare
task microvm:dev:build
QUAL_ROOT="$(pwd)/.scratch/microvm-darwin-check"
mkdir -p "$QUAL_ROOT/config" "$QUAL_ROOT/runtime" "$QUAL_ROOT/state"
cat >"$QUAL_ROOT/mock.json" <<'JSON'
{"turns":[
  {"tool_calls":[{"id":"shell-uid","name":"Shell","args":{"command":"id -u && pwd"}}]},
  {"tool_calls":[{"id":"write-proof","name":"Write","args":{"path":"darwin-vm-proof.txt","content":"darwin microvm proof\n"}}]},
  {"tool_calls":[{"id":"read-proof","name":"Read","args":{"path":"darwin-vm-proof.txt"}}]},
  {"text":"offline Darwin microVM check complete"}
]}
JSON
```

Start the development server from the repository root:

```sh
QUAL_ROOT="$(pwd)/.scratch/microvm-darwin-check"
export XDG_STATE_HOME="$QUAL_ROOT/state"
export XDG_CONFIG_HOME="$QUAL_ROOT/config"
export XDG_RUNTIME_DIR="$QUAL_ROOT/runtime"
.scratch/microvm-dev/bin/mecated serve --headless --posture auto \
  --store-dir="$QUAL_ROOT/sessions" \
  --default-placement microvm-local \
  --mock-script="$QUAL_ROOT/mock.json" \
  --microvm-dev-release="$(pwd)/.scratch/microvm-dev/darwin-arm64/release.json" \
  --microvm-dev-acknowledge-untrusted-local-artifacts
```

In a second terminal, create and prompt a session through the public HTTP API. Set the same
private XDG roots so local administration inspects the server's state:

```sh
QUAL_ROOT="$(pwd)/.scratch/microvm-darwin-check"
export XDG_STATE_HOME="$QUAL_ROOT/state"
export XDG_CONFIG_HOME="$QUAL_ROOT/config"
export XDG_RUNTIME_DIR="$QUAL_ROOT/runtime"
curl -sS -X POST http://127.0.0.1:8081/v1/sessions \
  -H 'Content-Type: application/json' -d '{}'
SESSION_ID=copy-from-create-response
curl -sS -N -X POST \
  "http://127.0.0.1:8081/v1/sessions/${SESSION_ID}/prompt" \
  -H 'Content-Type: application/json' \
  -d '{"text":"Run the scripted offline Darwin microVM check."}'
test ! -e darwin-vm-proof.txt
.scratch/microvm-dev/bin/mecated microvm doctor
.scratch/microvm-dev/bin/mecated microvm status
```

Confirm that Shell reports UID 65532, Write and Read use the logical worktree, the marker is
absent from the source checkout, and doctor/status are healthy. To check exact normal restart
reattachment, replace the mock script before restarting:

```sh
cat >"$QUAL_ROOT/mock.json" <<'JSON'
{"turns":[
  {"tool_calls":[{"id":"read-after-restart","name":"Read","args":{"path":"darwin-vm-proof.txt"}}]},
  {"text":"offline Darwin microVM restart check complete"}
]}
JSON
```

Stop and restart only the `mecated serve` command with the same XDG roots and store directory,
then prompt the same `SESSION_ID` to read `darwin-vm-proof.txt`.

This procedure is qualification work, not an installation path. If the Darwin
launch supervisor dies while its runner retains the ownership lock, replacement fails closed
and reports that operator recovery is required. Mecatl does not signal a stored PID or fall
back to host execution.

## Related

- [Ports](ports.md): `tool.Environment`, Workspace, and CommandRunner
- [Subagents and teams](subagents-and-teams.md): child environments and merge-back
- [Deployment and hardening](deployment-and-hardening.md): caller identity and deployment shapes
- [Governance](governance.md): permissions keyed by the governance root
- [Local microVM guide](../../user-docs/operating/microvm-environments.md)
