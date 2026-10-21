# Tracked tagged digests are updated by Renovate's native Dockerfile manager.
ARG GO_IMAGE=docker.io/library/golang:1.27.3@sha256:f20d83a83e18dbd2a4714b8ffd4b30b287b6243962dd75608e86abcc63ba4efc
ARG RUNTIME_IMAGE=cgr.dev/chainguard/static:latest@sha256:fe55470f22d3259488d9d3739168d8f04da67755f0b69382bc26eda4a7d3d327
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false -o /out/mecatl-execution-provider ./cmd/mecatl-execution-provider

FROM ${RUNTIME_IMAGE}
COPY --from=build --chown=65532:65532 /out/mecatl-execution-provider /mecatl-execution-provider
USER 65532:65532
ENTRYPOINT ["/mecatl-execution-provider"]
