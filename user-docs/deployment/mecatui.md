---
sidebar_position: 6
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

---

## Building locally

```sh
task ko:build:mecatui      # build into the local daemon (tagged under ko.local)
KO_DOCKER_REPO=ghcr.io/stacklok/mecatl/mecatui task ko:publish:mecatui
```

The `mecatui` build entry in `.ko.yaml` overrides the distroless base with the
brood-box wolfi base (`baseImageOverrides`) — brood-box connects over SSH and
needs a shell, which the distroless static base lacks. The build version is
stamped into the welcome splash via `-X main.version` (fed from the `VERSION`
env var through ko's `{{.Env.VERSION}}` ldflag template).

---

## What's next

- [Run mecated standalone](mecated.md) — the server `mecatui` embeds in-process or dials over `--server`.
- [Drive via gRPC / HTTP](grpc-http.md) — the wire protocol `mecatui` speaks as a client.
- [Permissions & guardrails](/what-you-get/permissions.md) — the posture ladder and workspace trust behave identically inside the container.
