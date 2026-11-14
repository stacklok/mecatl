# Tracked tagged digests are updated by Renovate's native Dockerfile manager.
ARG GO_IMAGE=docker.io/library/golang:1.27.4@sha256:5e8afd493a7124a8586e134d9b532731161d4b9becf9dee5a5956b107426f93d
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
