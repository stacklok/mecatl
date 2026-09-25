---
sidebar_position: 7
title: Local microVM environments
description: Run filesystem and shell tools in a repository-scoped local microVM.
---

# Local microVM environments

Use the released `microvm-local` path on Linux amd64 with KVM to run model-controlled
filesystem and shell tools in a local microVM. Providers, MCP, hooks, credentials, memory,
and the Mecatl server remain on the host. It is for one local operator and Git repository.
The unmerged Darwin arm64 implementation admits Apple Silicon macOS 15 or newer with
Hypervisor.framework, but it has not completed a native real-VM or signed-release
qualification and is not released support. Linux arm64, remote placement, multi-user
sharing, and non-Git sources are not available. Recurring and one-shot schedules are
supported on the same repository-scoped VM and use durable logical worktrees.

Install and verify published, release-stamped `mecatui` **and** `mecated` binaries before
use. `mecatui` runs the embedded server; `mecated` supplies the local `microvm doctor`,
`status`, and `delete` administration commands and does not need to remain running.
Source builds are for the separate repository-developer workflow, not ordinary local
installation. Linux requires Git, Python 3, read-write `/dev/kvm`, and enabled
unprivileged user namespaces. The experimental Darwin path requires a non-root Apple
Silicon host on macOS 15 or newer with Hypervisor.framework. Follow the
[Darwin source qualification procedure](#qualify-the-experimental-darwin-source-path)
on this page. The [microVM architecture](https://github.com/stacklok/mecatl/blob/main/docs/architecture/microvm-environments.md)
describes the placement and isolation boundaries.

> **Evidence boundary:** `task e2e:microvm` is the opt-in deterministic
> production-composed journey for Linux amd64 KVM and experimental Darwin arm64
> HVF. It uses the mock provider and never contacts OpenRouter. Linux has
> automated gate evidence. The Darwin command is executable but has not yet run
> on physical Apple Silicon. Separately, on 2026-09-10, a manual qualification
> used OpenRouter `openai/gpt-5-mini` through the public
> HTTP create and prompt APIs. Write, Read, and Bash ran in the Wolfi guest as UID 65532, the
> marker stayed out of the source checkout, and the same session reattached after restarting
> only mecated while microvmd remained alive. Doctor and status were healthy. No credential,
> private placement ref, socket, or host path was retained; this is not a microvmd-restart claim.
>
> The Darwin unit, ownership-xattr, and launch-lifecycle tests have only been cross-compiled
> locally on Linux for this unmerged implementation. The macOS CI job has not run for the
> change, and no native Apple Silicon real-VM or signed-candidate journey has run.

## Qualify the experimental Darwin source path

Repository developers can exercise the implemented Darwin path on a non-root Apple Silicon
host running macOS 15 or newer with Hypervisor.framework. Artifact preparation uses the
current-platform development descriptor and pinned go-microvm v0.0.41 runtime and firmware.
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

This procedure is qualification work, not an installation path. Released support still
requires the native journey and a non-publishing signed-candidate journey. If the Darwin
launch supervisor dies while its runner retains the ownership lock, replacement fails closed
and reports that operator recovery is required. Mecatl does not signal a stored PID or fall
back to host execution.

Install and verify both host binaries (set `VERSION` to the release tag):

```sh
VERSION=vX.Y.Z
PLATFORM=linux-amd64
mkdir -p "$HOME/.local/bin" .scratch/mecatl-host-release
cd .scratch/mecatl-host-release
for BINARY in mecatui mecated; do
  gh release download "$VERSION" --repo stacklok/mecatl \
    --pattern "${BINARY}-${VERSION}-${PLATFORM}" \
    --pattern "${BINARY}-${VERSION}-${PLATFORM}.sha256" \
    --pattern "${BINARY}-${VERSION}-${PLATFORM}.sigstore.json"
  cosign verify-blob \
    --bundle "${BINARY}-${VERSION}-${PLATFORM}.sigstore.json" \
    --certificate-identity "https://github.com/stacklok/mecatl/.github/workflows/release.yml@refs/tags/${VERSION}" \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    "${BINARY}-${VERSION}-${PLATFORM}"
  gh attestation verify "${BINARY}-${VERSION}-${PLATFORM}" --repo stacklok/mecatl
  sha256sum --check "${BINARY}-${VERSION}-${PLATFORM}.sha256"
  install -m 0755 "${BINARY}-${VERSION}-${PLATFORM}" "$HOME/.local/bin/${BINARY}"
done
export PATH="$HOME/.local/bin:$PATH"
cd ../..
```

## Embedded mecatui journey

Bare `mecatui` uses the host-local placement default unless operator settings override
`execution.default_placement`. To return to host-local execution after using this guide,
remove that override or set it explicitly:

```yaml
execution:
  default_placement: host-local
```

Set the server-owned MicroVM placement once in the XDG operator settings file, then use bare
`mecatui`. This command uses `$XDG_CONFIG_HOME` when set and the standard fallback otherwise:

```sh
CONFIG_HOME="${XDG_CONFIG_HOME:-$HOME/.config}"
mkdir -p "$CONFIG_HOME/mecatl"
cat >"$CONFIG_HOME/mecatl/settings.yaml" <<'YAML'
execution:
  default_placement: microvm-local
harness_context:
  enabled_sources: [repository, local, skills]
  kinds:
    instructions:
      sources: [repository, local]
      mode: combine
    commands:
      sources: [repository, local, skills]
      mode: combine
    rules:
      sources: [local]
      mode: combine
    skills:
      sources: [local]
      mode: combine
    agent_defs:
      sources: [local]
      mode: combine
YAML
mecated microvm doctor
mecatui
```

This policy selects repository `AGENTS.md` or `CLAUDE.md` files and slash commands
from each session's exact guest worktree. Selection does not grant project admission:
separately trust the checkout through operator `trustedWorkspaces` settings or
`--trust-project`. Headless posture alone does not trust a project. MicroVM placement
alone does not select those files.
The `local` registration is the source-bound compatibility view for local instructions,
commands, rules, skills, and agent definitions. `skills` exposes resolved skills as commands.
Configured remote command or customization services register as `driver`, and enabled MCP
prompts register as `mcp`. Include only registered IDs in `enabled_sources`; every explicit
policy must provide all five kind mappings.

`microvm doctor` is read-only. With prerequisites satisfied, a fresh home reports
`ready to configure on first use` and succeeds. Bare mecatui hosts its in-process server;
you do not start a separate `mecated serve` process. During the first session, the UI
shows bounded download, verification, installation, and daemon-start progress. A failure
names a bounded preparation stage, category, and actionable cause, then directs you to
`mecated microvm doctor` and the mecatui diagnostics log. Inspect and resume without
reselecting placement:

```sh
mecated microvm status
mecatui --resume SESSION_ID
```

A microVM session remains on its exact server-owned placement for its lifetime; it is
never moved to host execution. Harness context follows the separately configured source
policy. Independent `local`, `driver`, `skills`, and `mcp` sources remain available without
a guest attachment, including for no-filesystem sessions. A selected `repository` source
requires the session's exact guest files and reports an error if they cannot be acquired.
If `microvmd` restarts or the host reboots, ordinary session
resume starts a fresh VM boot around the retained rootfs and logical worktrees. The logical
`EnvironmentRef`, including its revision, stays unchanged. Installed packages, guest home,
caches, branches, indexes, and dirty or untracked files remain available. A command that was
running when the process stopped is interrupted and is never replayed automatically.
On the experimental Darwin path, an ordinary daemon restart retains the exact ref.
If readiness reports that the launch owner is orphaned, Mecatl has no automated
self-service operation that can prove the surviving runner's identity safely. Preserve the
MicroVM state and collect diagnostics before recovery:

```sh
mecated --version
mecated microvm doctor
mecated microvm status
```

Save the command output and a redacted copy of the reported error. If the process that owns
the local deployment is still available, stop it through its normal service control, such as
exiting embedded `mecatui` or stopping the managed `mecated` service, then retry the same
saved session. Do not signal a numeric PID and do not delete the VM or its state. If the
launch-owner supervisor was lost while the runner retained the ownership lock, no in-process
recovery can prove that runner safe to signal. After saving other work, restart macOS. The
restart releases the surviving processes and lock; ordinary session resume then starts a new
boot around the retained rootfs and logical worktrees. If a host restart is not acceptable,
keep the state unchanged and provide the collected diagnostics to the deployment operator.
`mecatui connect ADDRESS` is a pure remote client and never resolves, starts, or forwards
local MicroVM placement.

## Scheduled tasks

A schedule created from a running session borrows that session's exact logical worktree.
Deleting or updating the schedule never deletes or replaces the originating session's
placement. A schedule created independently provisions one logical worktree at creation and
reuses that exact ref for every recurring or one-shot fire, including after a harness restart;
there is no current-default or fresh-worktree fallback.

Deleting an independently placed schedule first disables it. A claimed or running fire must
settle through scheduler recovery before deletion can be retried. Before the first claim, deletion
removes a clean schedule-owned worktree and retains a dirty one under its exact ref. The first atomic
claim hands placement lifetime to the fire-session lineage: after that point, deleting the schedule
removes only the schedule record and retains the worktree, clean or dirty, for historical or resumable
fire sessions. Cleanup never deletes the repository VM, its shared rootfs, an originating session, or
sibling logical worktrees. Legacy schedule records without explicit ownership metadata are treated as
borrowed and are never destructively cleaned up.

## Headless mecated-only journey

In one terminal:

```sh
mecated microvm doctor
mecated serve --headless --mock --default-placement microvm-local
```

In another terminal, create a session on that deployment default, then
prompt and inspect it:

```sh
curl -sS -X POST http://127.0.0.1:8081/v1/sessions \
  -H 'Content-Type: application/json' \
  -d '{}'

SESSION_ID=copy-from-create-response
curl -sS -N -X POST "http://127.0.0.1:8081/v1/sessions/${SESSION_ID}/prompt" \
  -H 'Content-Type: application/json' \
  -d '{"text":"Run pwd and report the execution workspace."}'
mecated microvm status
```

`--headless` is for unattended API use, so configure main-agent permissions for
autonomous work. `--mock` is only for offline smoke testing; configure a real provider
for model work. Session details expose bounded placement metadata only; host paths and
exact environment refs remain private.

## Guest egress and isolation

Guest IPv4 egress is permissive by default. External IPv6 is unrouted and unsupported.
The host operator restricts guest egress in the same settings file:

```yaml
execution:
  default_placement: microvm-local
  microvm:
    guest_egress:
      mode: allowlist
      allow:
        - api.example.com:443/tcp
```

Allowlist rules use `HOST:PORT/tcp|udp`; at least one valid rule is required. Invalid
rules or unavailable enforcement stop readiness. HTTP/gRPC requests and project
configuration cannot set or weaken this policy. Host provider, MCP, web, hook, artifact,
and telemetry traffic is outside guest egress policy.

Sessions and isolated children receive separate Git worktrees in a repository VM.
The guest workload identity is fixed at UID/GID 65532. Linux maps it through an
unprivileged user namespace. The experimental Darwin path prepares go-microvm VirtioFS
ownership xattrs for the same identity on rootfs writable trees, logical worktrees, and the
read-only Git-object snapshot; it does not copy the host account identity or widen owner-only
host modes. Host-side child merge-back applies the patch before refreshing ownership, so a
reported ownership-refresh failure means the patch was already applied and must be inspected
before retrying.

Creating those worktrees captures repository state under fixed host-safety ceilings. At most
two captures run at once per daemon process. An initial capture has a cumulative 256 MiB
accounting budget across Git path listings, tracked and untracked content, staged and
unstaged binary patches, and the committed archive, plus 100,000 cumulative
tracked/untracked/archive entry records; each verification scan has the same limits.
Because content represented in multiple phases is counted each time, this is not a 256 MiB
checkout-size guarantee. If creation reports `source capture limit exceeded`, reduce the
repository or dirty working-tree size (for example, remove unnecessary untracked artifacts)
and retry; no session placement is registered and temporary capture data is removed.
Worktrees separate Git state and routing, not mutually hostile processes in the same VM.
Different repositories receive different VMs. Recovery verifies the retained rootfs,
guest agent, admitted artifacts, release policy, and guest egress policy before it starts a
replacement boot. Missing or changed retained data fails with the preserved state left in
place. Mecatl never falls back to host filesystem or shell execution and never creates an
empty replacement environment.

Use `mecated microvm doctor` and `mecated microvm status` for read-only inspection
local to the execution host and current OS principal. Status calls daemon rows
`attachment_id`; they are placement attachments, not public mecatl session IDs. To remove
one exact retained logical attachment, copy its `backend`, `attachment_id`, `ref`, and
`generation` from the same status row and run:

```sh
mecated microvm delete --backend microvm-local \
  --attachment-id ATTACHMENT_ID --ref REF --generation GENERATION
```

The command confirms before deletion, preserves dirty worktrees, and never deletes or resets
the repository VM. If placement creation or child forking returns invalid metadata and automatic
cleanup cannot confirm removal, run `mecated microvm status` first. Delete only the exact matching
row with the command above; do not infer missing identifiers from the failed response.
Administration exists only in local `mecated`, not mecatui or remote
connect mode. One repository-scoped daemon is shared across sessions and host processes. First use installs
and starts only genuinely fresh state. A compatible configured daemon that is stopped starts
again during ordinary MicroVM use; `microvm doctor` is optional for diagnosis. While the daemon
is stopped, `microvm status` reports that inventory is unavailable because durable attachment
records can still exist. A conflicting requested release or egress policy, corrupt
configuration, live prior daemon with an unavailable socket, or uncertain process identity
fails without rewriting active configuration, deleting state, or replacing repository data.
