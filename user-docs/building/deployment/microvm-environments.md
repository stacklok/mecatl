---
sidebar_position: 7
title: Local microVM environments
---

# Local microVM environments

Use `microvm-local` on Linux amd64 with KVM to run model-controlled filesystem and
shell tools in a local microVM. Providers, MCP, hooks, credentials, memory, and the
mecatl server remain on the host. It is for one local operator and Git repository;
Linux arm64, macOS, remote placement, multi-user sharing, and non-Git
sources are not available. Recurring and one-shot schedules are supported on the
same repository-scoped VM and use durable logical worktrees.

Install and verify published, release-stamped `mecatui` **and** `mecated` binaries before
use. `mecatui` runs the embedded server; `mecated` supplies the local `microvm doctor`,
`status`, and `delete` administration commands and does not need to remain running.
Source builds are for the separate repository-developer workflow, not ordinary local
installation. Git, Python 3, read-write `/dev/kvm`, and unprivileged user namespaces are
required. The full [operator runbook](https://github.com/stacklok/mecatl/blob/main/docs/usage/microvm-environments.md)
includes verification and developer workflow instructions.

> **Evidence boundary:** `task e2e:microvm` is the opt-in automated Linux amd64 KVM gate
> and uses the deterministic mock provider; it never contacts OpenRouter. Separately, on
> 2026-09-10, a manual qualification used OpenRouter `openai/gpt-5-mini` through the public
> HTTP create and prompt APIs. Write, Read, and Bash ran in the Wolfi guest as UID 65532, the
> marker stayed out of the source checkout, and the same session reattached after restarting
> only mecated while microvmd remained alive. Doctor and status were healthy. No credential,
> private placement ref, socket, or host path was retained; this is not a microvmd-restart claim.

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

Set the server-owned placement once in the XDG operator settings file, then use bare
`mecatui`. This command uses `$XDG_CONFIG_HOME` when set and the standard fallback otherwise:

```sh
CONFIG_HOME="${XDG_CONFIG_HOME:-$HOME/.config}"
mkdir -p "$CONFIG_HOME/mecatl"
cat >"$CONFIG_HOME/mecatl/settings.yaml" <<'YAML'
execution:
  default_placement: microvm-local
YAML
mecated microvm doctor
mecatui
```

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
never moved to host execution. `mecatui connect ADDRESS` is a pure remote client and never
resolves, starts, or forwards local MicroVM placement.

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
  -d '{"text":"Inspect this repository and report its test command."}'
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
Different repositories receive different VMs. A daemon restart may leave records,
rootfs, and worktrees intact while live hosted dependencies are unavailable; affected
sessions report that condition. Mecatl never falls back to host filesystem or shell
execution and never creates an empty replacement environment.

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
the repository VM. Administration exists only in local `mecated`, not mecatui or remote
connect mode. One repository-scoped daemon is shared across sessions and host processes. First use installs
and starts only genuinely fresh state. A conflicting requested release or egress policy,
corrupt configuration, process identity mismatch, stopped daemon, or unhealthy runtime
fails without restarting the daemon, rewriting active configuration, deleting state, or
replacing repository runtime.
