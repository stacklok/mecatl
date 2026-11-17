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
FROM golang:1.27.4-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mecated ./cmd/mecated

FROM debian:bookworm-slim@sha256:734a72b3d6142a71993b4bc580ca01d8ca687f77a2c422d96a961d3376b179bc
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/mecated /usr/local/bin/mecated
ENTRYPOINT ["mecated"]
