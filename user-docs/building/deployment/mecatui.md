---
sidebar_position: 7
title: mecatui container image (brood-box)
---

# mecatui container image (brood-box)

`mecatui` ships as a container image on every release, alongside `mecated`:
`ghcr.io/stacklok/mecatl/mecatui` (tagged `<version>` and `latest`, multi-arch
`linux/amd64` + `linux/arm64`). It is built with [ko](https://ko.build/) from
`./cmd/mecatui` and carries a [brood-box](https://github.com/stacklok/brood-box)
agent manifest, so it can be imported as a brood-box agent without a separate
Dockerfile or wrapper.

It is signed with cosign (keyless, via the release workflow's OIDC identity),
ships an SPDX SBOM as a signed attestation, and carries SLSA build provenance —
the same supply-chain story as the `mecated` image. See
[the release workflow docs](https://github.com/stacklok/mecatl/blob/main/.github/workflows/README.md)
for how to verify a signed image.

---

## Import into brood-box

```sh
bbox agents import ghcr.io/stacklok/mecatl/mecatui:latest
```

brood-box locates the agent manifest via the OCI config label
`org.stacklok.broodbox.agent` (set to `/var/run/ko/agent.yaml` at build time),
falling back to `/usr/share/broodbox/agent.yaml`. ko's per-package `kodata/` dir
lands `cmd/mecatui/kodata/agent.yaml` at `/var/run/ko/agent.yaml` in the image.

The manifest (`cmd/mecatui/kodata/agent.yaml`) declares:

- `command: ["mecatui"]` — the in-image entrypoint.
- `env_forward` of `OPENROUTER_API_KEY`, `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`,
  and `OPENCODE_API_KEY` — mecatl auto-detects the provider from whichever key
  is set.
- `mcp.mode: env` and `egress_profile: standard` with egress allowed to
  `api.anthropic.com`, `openrouter.ai`, `api.openai.com`, and `opencode.ai` on
  port 443.

It is operator-tunable: edit the manifest for a deployment that pins a single
provider or applies a stricter egress profile.

### Experimental ChatGPT Codex subscription

Embedded mecatui can use provider `openai-codex` with a manual subscription
token, but the shipped brood-box manifest does not mount that secret or allow
`chatgpt.com` egress. Customize the manifest to mount owner-only `auth.yaml`,
pass `--auth-file` and `--default-provider openai-codex`, and permit HTTPS to
`chatgpt.com`. This is not public OpenAI API credit: it uses an undocumented
private backend, has no refresh flow, and requires relaunch after token
replacement. Read the [exact operator setup and same-UID plaintext
boundary](https://github.com/stacklok/mecatl/blob/main/docs/usage/mecated.md#openai-codex-subscription-manual-token-experimental)
before adding the mount.

## Runtime and sensitive local administration

Each embedded instance creates its own private runtime directory. With `--perf`, ordinary
runtime administration uses an owner-private `admin.sock` in that directory, so multiple
containers or local instances do not compete for a fixed port. `--perf-mcp` instead needs
a streaming-HTTP URL: without an explicit loopback `--perf-addr`, it chooses ephemeral
loopback TCP and logs the endpoint. No stdio transport exists. Keep all perf output private;
it may contain prompts, paths, and runtime details.

---

## Building locally

```sh
task ko:build:mecatui      # build into the local daemon (tagged under ko.local)
KO_DOCKER_REPO=ghcr.io/stacklok/mecatl/mecatui task ko:publish:mecatui
```

The `mecatui` build entry in `.ko.yaml` overrides the distroless base with the
brood-box wolfi base (`baseImageOverrides`) — brood-box connects over SSH and
needs a shell, which the distroless static base lacks. The build ID is stamped
into the welcome splash via
`-X github.com/stacklok/mecatl/internal/buildinfo.BuildID`. Taskfile-driven ko
builds set it at build time from
`git describe --tags --match 'v[0-9]*' --always --dirty` (for example,
`v0.0.22-28-g40a6b3fc6-dirty`); `BUILD_ID` preserves an explicit stamp verbatim.
A direct ko build may instead leave `VERSION` unset: its binary uses embedded VCS
metadata as `dev+<12-char-vcs-revision>[.dirty]`, or `dev`, without invoking git
at runtime.

---

## What's next

- [Run mecated standalone](mecated.md) — the server that a bare `mecatui` embeds in-process — or dials via `mecatui connect ADDRESS`.
- [Drive via gRPC / HTTP](grpc-http.md) — the wire protocol `mecatui` speaks as a client.
- [Permissions & guardrails](/building/what-you-get/permissions.md) — the posture ladder and workspace trust behave identically inside the container.
