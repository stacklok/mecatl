# Tracked tagged digests are updated by Renovate's native Dockerfile manager.
ARG GO_IMAGE=docker.io/library/golang:1.27.3@sha256:e519d3b0a5b285e5aa53ab9eef02bc6bbb9cb04608389b743a136a9ce75f0d1e
ARG RUNTIME_IMAGE=ghcr.io/stacklok/brood-box/base:latest@sha256:934dae12a8f55e4a83403a48ae3f788131cc236dbe494a4caa94055882273afa
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false -o /out/mecatl-executor ./cmd/mecatl-executor \
    && mkdir -p /out/workspace

FROM ${RUNTIME_IMAGE}
COPY --from=build --chown=65532:65532 /out/mecatl-executor /mecatl-executor
# Prepare the writable workspace without executing the target architecture.
COPY --from=build --chown=65532:65532 /out/workspace /workspace
USER 65532:65532
WORKDIR /workspace
ENV HOME=/workspace TMPDIR=/tmp GOTMPDIR=/tmp
ENTRYPOINT ["/bin/sh", "-c", "trap : TERM INT; sleep infinity & wait"]
