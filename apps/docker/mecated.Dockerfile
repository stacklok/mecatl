# SPDX-License-Identifier: Apache-2.0
#
# Local dev/demo image for mecated, built for `docker compose up` in the Studio
# workspace (apps/docker-compose.yml) only. NOT the project's official release
# artifact — that's built via `ko` from ../../.ko.yaml (see
# .github/workflows/release.yml for the release pipeline). This Dockerfile exists purely so Studio is runnable end to
# end against a real daemon without a separately-installed Go toolchain. It is a
# Studio-owned copy of the Slack bot example's
# sdk/typescript/examples/slack-bot/docker/mecated.Dockerfile, so the two demos
# can drift independently.
#
# Build context is the repository root (see docker-compose.yml: `context: ..`)
# since mecated needs the whole Go workspace (go.work references ./engine and the
# other in-repo modules).
FROM golang:1.27.3-bookworm@sha256:972b4110ba0f47539a975c31e52c64765e1ff19f2d74469ae4a595c6676e80a5 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mecated ./cmd/mecated

FROM debian:bookworm-slim@sha256:7ff6ba31527b9e336162fa061746c366c4b2032f4d2cff2078bea43f78efe3f8
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/mecated /usr/local/bin/mecated
ENTRYPOINT ["mecated"]
