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
FROM golang:1.27.4-bookworm@sha256:ce59b41415e2524ae99926fe1f37ce48ba8c194dcbd2a56a65436cdcefa2bd34 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mecated ./cmd/mecated

FROM debian:bookworm-slim@sha256:e67eeead736ff7bbe258ef088bff683635c561b1fcfefad0806c1df4ab49ce0e
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/mecated /usr/local/bin/mecated
ENTRYPOINT ["mecated"]
