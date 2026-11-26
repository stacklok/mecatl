# Tracked tagged digests are updated by Renovate's native Dockerfile manager.
ARG GO_IMAGE=docker.io/library/golang:1.27.4@sha256:5f1b98fa02fe396ebce2a0227a0e08309e11792915b9dd2e1f71053d7e7e6666
ARG RUNTIME_IMAGE=ghcr.io/stacklok/brood-box/base:latest@sha256:7e4533084b85257d3244ad9dc2a1376c72b8699d5c8a2abb719f31eb022d8210
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
ENTRYPOINT ["/mecatl-executor", "serve"]
