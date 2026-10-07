---
sidebar_position: 130
title: Local microVM environments
description: Run filesystem and shell tools in a repository-scoped local microVM.
---

# Local microVM environments

Use the qualified `microvm-local` path on Linux amd64 with KVM to run model-controlled
filesystem and shell tools in a local microVM. Providers, MCP, hooks, credentials, memory,
and the Mecatl server remain on the host. It is for one local operator and Git repository.
The experimental Darwin arm64 implementation admits Apple Silicon macOS 15 or newer with
Hypervisor.framework, but it has not completed a native real-VM or signed-release
qualification and is not released support. Linux arm64, remote placement, multi-user
sharing, and non-Git sources are not available. Recurring and one-shot schedules are
supported on the same repository-scoped VM and use durable logical worktrees.

Linux requires Git, Python 3, read-write `/dev/kvm`, and enabled unprivileged
user namespaces. `mecatui` runs the embedded server; `mecated` supplies the local
`microvm doctor`, `status`, and `delete` commands and does not need to remain
running for that embedded journey. The deployment must have the matching
release artifacts described below.

## Verify the installation

The runtime needs release-stamped host binaries and the matching signed microVM
artifact descriptor and bundles. A normal CLI archive alone does not establish
that the release supplies this execution path. Before configuring placement,
check the selected release's artifacts and run:

```sh
mecated microvm doctor
```

Doctor checks prerequisites and artifact admission without booting a repository
VM. Continue only when it reports a ready installation. If the release does not
include the required artifacts, use a release that does; source-build
qualification belongs to the contributor workflow.

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
never moved to host execution. You can run multiple local `mecatui` or `mecated` processes
against the same checkout without extra configuration. Each process receives its own logical
worktree for a newly created session. When processes intentionally use the same persisted ref,
closing one process releases only its own attachment; the other process can continue reading,
writing, and running commands. The repository VM, rootfs, and durable worktrees remain shared
at the repository boundary described above. This does not allow two terminals to drive the
same conversation concurrently: its existing single-writer lease still applies and may remain
held while the owning terminal is idle. Close that session in the owning instance before
resuming it elsewhere, or start a separate session for another coding task.

Harness context follows the separately configured source
policy. Independent `local`, `driver`, `skills`, and `mcp` sources remain available without
a guest attachment, including for no-filesystem sessions. A selected `repository` source
requires the session's exact guest files and reports an error if they cannot be acquired.
If `microvmd` restarts or the host reboots, ordinary session
resume starts a fresh VM boot around the retained rootfs and logical worktrees. The logical
`EnvironmentRef`, including its revision, stays unchanged. Installed packages, guest home,
caches, branches, indexes, and dirty or untracked files remain available. A command that was
running when the process stopped is interrupted and is never replayed automatically.
A daemon restart also invalidates bindings cached in an already-running harness; Mecatl does
not automatically reconnect or reuse that cached repository context. Exit the affected
harness, then run `mecatui --resume SESSION_ID` to reacquire its retained exact placement and
sources under current authorization. Reattachment fails rather than substituting another
worktree if the daemon or placement is unavailable.

If one terminal reports an incompatible release or daemon identity after an upgrade, Mecatl
leaves the existing daemon and other terminals' work unchanged. Compare `mecatui --version`
and `mecated --version`, run `mecated microvm doctor`, and use the release matching the existing
local deployment. A version mismatch is not an orphan-owner error and does not by itself
require restarting the host. Mecatl does not automatically replace incompatible retained
runtime state; do not delete a worktree or VM to clear the mismatch.

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

## Next steps

- [Operate mecated](/operating/mecated.md) to configure the host service.
- [Execution environments](/features/security-and-execution/execution-environments.md) explains placement and reattachment.
