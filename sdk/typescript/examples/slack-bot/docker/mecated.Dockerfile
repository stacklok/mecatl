# Local dev/demo image for mecated, built for `docker compose up` in this
# example only. NOT the project's official release artifact — that's built
# via `ko` from ../../../../.ko.yaml (see docs/adr/ for the release
# pipeline). This Dockerfile exists purely so the Slack bot example is
# runnable end to end without a separately-installed Go toolchain.
#
# Build context is the repository root (see docker-compose.yml) since
# mecated needs the whole Go workspace (go.work references ./engine and the
# other in-repo modules).
FROM golang:1.26.6-bookworm AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mecated ./cmd/mecated

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/mecated /usr/local/bin/mecated
ENTRYPOINT ["mecated"]
