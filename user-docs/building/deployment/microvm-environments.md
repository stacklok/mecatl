---
sidebar_position: 7
title: Local microVM environments
---

# Local microVM environments

Use `microvm-local` on Linux amd64 with KVM to run model-controlled filesystem and
shell tools in a local microVM. Providers, MCP, hooks, credentials, memory, and the
mecatl server remain on the host. It is for one local operator and Git repository;
Linux arm64, macOS, remote placement, schedules, multi-user sharing, and non-Git
sources are not available.

Install a published, release-stamped `mecatui` or `mecated` binary and verify it before
use. Source builds are for the separate repository-developer workflow, not ordinary
local installation. Git, Python 3, read-write `/dev/kvm`, and unprivileged user
namespaces are required. The full [operator runbook](https://github.com/stacklok/mecatl/blob/main/docs/usage/microvm-environments.md)
includes verification and developer workflow instructions.

## mecatui-only journey

From the Git repository:

```sh
mecatui microvm doctor
mecatui --default-placement microvm-local
```

`microvm doctor` is read-only and can report an unconfigured backend on a fresh home.
Selecting the profile prepares the verified local runtime and creates the session. Bare
`mecatui` remains host-local.

Inspect and resume with the same profile:

```sh
mecatui microvm status
mecatui --default-placement microvm-local --resume SESSION_ID
```

A microVM session remains in the microVM profile for its lifetime; it is never moved to
host execution.

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
The host operator can restrict guest egress only at the local composition root:

```sh
mecatui --default-placement microvm-local --microvm-guest-egress=deny-all

mecated serve --headless \
  --microvm-guest-egress=allowlist \
  --microvm-guest-allow=api.example.com:443/tcp
```

Allowlist rules use `HOST:PORT/tcp|udp`; at least one valid rule is required. Invalid
rules or unavailable enforcement stop readiness. HTTP/gRPC requests and project
configuration cannot set or weaken this policy. Host provider, MCP, web, hook, artifact,
and telemetry traffic is outside guest egress policy.

Sessions and isolated children receive separate Git worktrees in a repository VM.
Worktrees separate Git state and routing, not mutually hostile processes in the same VM.
Different repositories receive different VMs. A daemon restart may leave records,
rootfs, and worktrees intact while live hosted dependencies are unavailable; affected
sessions report that condition. Mecatl never falls back to host filesystem or shell
execution and never creates an empty replacement environment.

Use read-only `microvm doctor` and `microvm status` to inspect the local environment.
`microvm delete --session ID --ref REF --generation N` removes one logical attachment,
retains dirty worktrees, and does not delete the shared repository VM.
