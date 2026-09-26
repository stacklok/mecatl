---
sidebar_position: 130
title: mecatui container image (brood-box)
description:
  Import the signed mecatui container image into brood-box or build it locally.
---

# mecatui container image (brood-box)

Every release includes a multi-architecture `mecatui` image for `linux/amd64`
and `linux/arm64`:

```text
ghcr.io/stacklok/mecatl/mecatui:<VERSION>
```

The image also includes a
[brood-box](https://github.com/stacklok/brood-box) agent manifest, so you can
import it without writing a Dockerfile or wrapper. Use a release version for a
repeatable deployment; `latest` tracks the latest release.

It is signed with keyless cosign, includes an SPDX SBOM attestation, and carries
SLSA build provenance. See
[the release workflow docs](https://github.com/stacklok/mecatl/blob/main/.github/workflows/README.md)
for how to verify a signed image.

## Import into brood-box

```sh
bbox agents import ghcr.io/stacklok/mecatl/mecatui:latest
```

The included manifest configures:

- `command: ["mecatui"]` as the image entry point.
- `env_forward` of `OPENROUTER_API_KEY`, `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`,
  `OPENCODE_API_KEY`, and `TYPESAFE_API_KEY`. Provider keys enable their matching
  LLM provider. The Typesafe key remains inert unless operator settings select
  `models.router.backend: jev`.
- `mcp.mode: env` and `egress_profile: standard` with egress allowed to
  `api.anthropic.com`, `openrouter.ai`, `api.openai.com`, `opencode.ai`, and
  `api.typesafe.ai` on port 443.

Customize the manifest when you need to pin one provider or apply a narrower
egress policy.

### ChatGPT Codex subscription

Embedded `mecatui` can bill inference to a ChatGPT Plus or Pro subscription
through the `openai-codex` provider. Sign in inside the container:

```sh
mecatui providers login openai-codex --device
mecatui providers set-default openai-codex <MODEL_ID>
```

`--device` prints a verification URL and a code, and completes without a browser
or a callback port on this host, so it works in a container. The grant is written
to the credential store under `$XDG_CONFIG_HOME`; mount that path if the sign-in
should outlive the container. `openai-codex` has no built-in default model, so
name one the account is entitled to.

The included brood-box manifest neither forwards a grant nor allows the
provider's hosts. Allow HTTPS egress to `chatgpt.com` and `auth.openai.com`.

Mounting an owner-only `auth.yaml` with a manual token and passing
`--api-key-file` and `--default-provider openai-codex` is an alternative. That
snapshot never refreshes, so relaunch the container after replacing the token.
Read the
[operator setup and same-UID plaintext boundary](./settings.md#configure-provider-credentials)
before adding the mount.

## Runtime and sensitive local administration

Each embedded instance creates a private runtime directory. With `--perf`, it
uses an owner-private `admin.sock` in that directory. Multiple containers or
local instances therefore do not compete for a fixed port.

`--perf-mcp` requires a streaming HTTP URL. Set `--perf-addr` to an explicit
loopback address, or let `mecatui` choose an ephemeral loopback port and log the
endpoint. There is no standard input/output transport. Keep performance output
private because it can contain prompts, paths, and runtime details.

## Building locally

```sh
task ko:build:mecatui      # build into the local daemon (tagged under ko.local)
KO_DOCKER_REPO=ghcr.io/stacklok/mecatl/mecatui task ko:publish:mecatui
```

The image uses the brood-box Wolfi base because brood-box connects over SSH and
requires a shell. Taskfile builds stamp the welcome screen with a version from
Git. Set `BUILD_ID` to supply an explicit build identifier. A direct ko build
without a version uses embedded VCS metadata when available, or `dev` otherwise.

## Next steps

- [Run mecated standalone](mecated.md) to connect `mecatui` to a separate
  server.
- [Connect with gRPC or HTTP](grpc-http.md) to understand the client transport.
- [Permissions and guardrails](/building/what-you-get/permissions.md) to
  configure posture and workspace trust.
