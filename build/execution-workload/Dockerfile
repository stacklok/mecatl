# Tracked tagged digests are updated by Renovate's native Dockerfile manager.
ARG GO_IMAGE=docker.io/library/golang:1.27@sha256:e0174e51e81218523251d85d248a90d24c3d5e81543b4f07a5d66229397db190
ARG RUNTIME_IMAGE=ghcr.io/stacklok/brood-box/base:latest@sha256:b84a29171a5bfb673a223c2408e278cbc92fdf152e7eeb17727e5379a3bd83b1
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
