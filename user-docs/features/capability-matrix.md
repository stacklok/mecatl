---
sidebar_position: 2
title: Capability and deployment matrix
description: Check which mecatl capabilities are available in each deployment shape.
---

# Capability and deployment matrix

This page answers a practical question: **where can I use a capability today?**
It describes reachable product behavior, not every port or adapter in the
repository.

## Legend

- **✓** — available
- **Opt** — available when configured, enabled, or supported by the model
- **Srv** — supplied by the connected server
- **Host** — available when an embedding application supplies the adapter
- **—** — not exposed by this surface
- **Gap** — a portable capability with a missing implementation or client path

A capability marked **Portable** has a shared engine, composition seam, or wire
contract. Portable does not mean enabled by default everywhere.

## At a glance

| Deployment | Best understood as | State and boundary |
| --- | --- | --- |
| `mecated` | Full server | gRPC + HTTP/SSE; optional storage, MCP, scheduling, and ACP. |
| `mecak8s` | Cloud server | Redis-backed state, Kubernetes leases, gRPC + HTTP/SSE; headless by default. |
| `mecatequi` | One-shot local runner | Text prompt → local Git patch/summary; optional store directory. |
| `mecatui` embedded | Local terminal product | Builds a server over a private Unix gRPC socket. |
| `mecatui connect` | Remote terminal client | Uses the external server’s capabilities; embedded-only settings do not apply. |
| Embedded `engine` | Library | Loop only; the host supplies model, workspace, persistence, and adapters. |

## Product capabilities

These tables intentionally use fewer columns than a single all-purpose matrix.
Read the deployment-specific notes below when a row is `Opt` or `—`.

### Execution and safety

| Capability | Classification | `mecated` | `mecak8s` | `mecatequi` | `mecatui` embedded | `mecatui connect` |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| Agent loop and core tools | Portable | ✓ | ✓ | ✓ | ✓ | Srv |
| Filesystem tools | Portable; harness namespace | ✓ | ✓ | ✓ | ✓ | Srv |
| Bash / command execution | Portable runner; namespace-local today | Opt | Opt | Opt | Opt | Srv |
| Permissions, approvals, posture | Portable | ✓ | ✓ | ✓ | ✓ | Srv |
| `no-fs` session profile | Portable; TUI client gap | ✓ | ✓ | — | Gap | Gap |
| Provider and model selection | Portable | ✓ | ✓ | Launch-time | ✓ | Srv |
| Multimodal input | Portable; model-gated | Opt | Opt | — | Opt | Srv |
| Mid-run steering | Portable; HTTP client gap | ✓ | ✓ | — | Opt | Srv |

### Project and model context

| Capability | Classification | `mecated` | `mecak8s` | `mecatequi` | `mecatui` embedded | `mecatui connect` |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| `AGENTS.md` / `CLAUDE.md` | Portable over `Workspace`; trust-gated | Opt | Opt | Opt | Opt | Srv |
| Project and user rules | Portable source seam; remote-source gap | Opt | Opt | — | Opt | Srv |
| Skills | Portable source seam | Opt | Opt | — | Opt | Srv |
| Slash commands | Portable source seam | Opt | Opt | — | Opt | Srv |
| Soul/persona | Portable source seam | Opt | Opt | ✓ user tier | Opt | Srv |
| Project memory | Portable store seam | Opt | Opt | — | Opt | Srv |
| User model | Portable source/store seam | Opt | Opt | ✓ | Opt | Srv |
| Named agent definitions | Portable source seam | Opt | Opt | — | Opt | Srv |
| Streaming-HTTP MCP | Portable host capability | Opt | Opt | Opt | Opt | Srv |
| MCP OAuth login/provisioning | Local operator flow | ✓ local CLI | — | — | ✓ local/operator | — |

### State and delegation

| Capability | Classification | `mecated` | `mecak8s` | `mecatequi` | `mecatui` embedded | `mecatui connect` |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| Durable sessions, event log, resume | Portable; backend-dependent | Opt | ✓ Redis | Opt `--store-dir` | ✓ by default | Srv |
| Schedule tools and APIs | Portable; backend-dependent | Opt | Opt | Opt `--store-dir` | Opt | Srv |
| Automatic schedule firing | Portable; ticker-dependent | Opt | Opt | — | Opt | Srv |
| Subagent | Portable | ✓ | ✓ | ✓ | ✓ | Srv |
| Parallel delegation | Portable; optional composition | Opt | Opt | — | Opt | Srv |
| Teams | Portable; optional composition | Opt | Opt | — | Opt | Srv |

### Interfaces and local integrations

| Capability | Classification | `mecated` | `mecak8s` | `mecatequi` | `mecatui` embedded | `mecatui connect` |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| Git patch and JSON summary | Local-only by design | — | — | ✓ | — | — |
| ACP editor-buffer environment | Local-only by design | ACP only | — | — | — | — |
| Terminal UI and attachment picker | Local-only by design | — | — | — | ✓ | ✓ |
| Production remote Workspace/environment | Local-only gap | — | — | — | — | — |

## Direct clients and embedded engine

| Surface | Agent loop | Persistence | Delegation | Steering | Important boundary |
| --- | ---: | ---: | ---: | ---: | --- |
| gRPC client | Srv | Srv | Srv | ✓ | Typed server capabilities and approval/resume APIs. |
| HTTP/SSE client | Srv | Srv | Srv | Gap | No equivalent HTTP route for mid-run steering. |
| Embedded `engine` | Host | Host | Host | Host | No listener, credentials, filesystem, store, auth, or UI is supplied by the importable module. |

## The important exceptions

### `mecatequi` is deliberately smaller than `mecated`

`mecatequi` projects a deliberately small configuration surface into
`app.Build`. Its current one-shot behavior is:

| Capability | Current `mecatequi` behavior |
| --- | --- |
| Skills | Not configured; no conventional or remote skill source. |
| Slash commands | Not configured; no file, skill, driver, or MCP command expansion. |
| Soul | User-tier conventional soul is loaded when present; custom and remote soul sources are not exposed. |
| Project memory | Not configured; `--trust-project` does not create a memory store. |
| User model | Available through the conventional XDG/home location when available. |
| Named agents | No named-agent discovery source; generic Subagent remains distinct. |
| Scheduling | With `--store-dir`, schedule tools can be present for manual use; no scheduler tick loop runs. |
| Persistence | `--store-dir` persists the session; `--out-events` is an event artifact, not a resume interface. |
| Multimodal input | Not exposed; the command accepts a text prompt or prompt file. |

These are current command boundaries, not claims that the shared composition
layer cannot support the features.

### `mecak8s` is storage-free locally

`mecak8s` has no local `--store-dir`. Session snapshots and the durable event log
use Redis, while Kubernetes leases coordinate ownership. Source-backed content
must still use the configured pod filesystem, mounted storage, or a configured
remote source; it should not be assumed to survive pod replacement merely
because the feature is available in the process.

ACP is intentionally excluded from `mecak8s`; it is a `mecated`-only local
stdio/editor integration.

### `mecatui` has two different modes

Bare `mecatui` embeds the server, uses a private Unix socket, and can apply local
provider, posture, trust, storage, and resilience settings. `mecatui connect`
dials an external gRPC server and relies on that server’s configuration and
capability snapshot. It does not fall back to embedding.

The server and wire API support `profile: "no-fs"`, including its empty-workspace
validation. The shipped TUI session-creation wrapper does not expose that
profile, so this is a concrete client gap rather than an unknown server
capability.

### ACP is separate from `no-fs`

ACP `session/new` currently creates a default filesystem profile using the
client’s validated working directory. With editor read/write capabilities, it
can install a shell-less editor-buffer environment for that session. That is
not the `no-fs` profile. ACP resume restores the conversation but does not yet
restore the editor-buffer override.

## What “local” means

Filesystem and Bash run where the harness runs. In `mecak8s`, that normally means
the pod’s workspace and command environment, not the client’s machine. The
abstract `Workspace`, `Environment`, and `CommandRunner` ports permit remote
implementations, but no production remote Workspace/environment driver is
currently shipped. The `internal/adapter/remoteenv` implementation is a
contract fake, not a deployment backend.

MCP OAuth login/provisioning is a local operator/browser workflow. Clients that
connect to a server use already-configured MCP tools; they do not perform the
login flow through the HTTP/SSE or gRPC session API.

## Current gaps

1. **Production remote Workspace/environment** — portable ports exist, but no
   production remote driver/service is wired.
2. **Remote rules source** — filesystem rules exist, but there is no production
   gRPC RulesSource driver.
3. **HTTP/SSE steering** — steering exists on the gRPC surface, not HTTP/SSE.
4. **TUI `no-fs` selection** — server and wire support exist; the TUI client does
   not expose the selector.

The remaining `Opt` cells are configuration or deployment questions, not claims
that the feature is available in every installation. Runtime optional capability
claims should be checked against `Service.capabilities` and the session-creation
capability snapshot, rather than inferred from a constructor or proto alone.

## Related information

- [Features](./index.md)
- [Project instructions and rules](./project-instructions-and-rules.md)
- [Deployment decision](../getting-started/deployment-decision.md)
- [Workspace trust](https://github.com/stacklok/mecatl/blob/main/docs/usage/workspace-trust.md)
- [gRPC API](https://github.com/stacklok/mecatl/blob/main/docs/usage/grpc-api.md)
- [HTTP/SSE API](https://github.com/stacklok/mecatl/blob/main/docs/usage/http-sse-api.md)
