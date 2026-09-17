# GO_IMAGE is deliberately mandatory and must include a digest, for example:
# docker build --build-arg GO_IMAGE=golang:1.27@sha256:<verified-digest> ...
ARG GO_IMAGE
FROM ${GO_IMAGE} AS build
ARG GO_IMAGE
WORKDIR /src
COPY . .
RUN case "${GO_IMAGE}" in *@sha256:*) ;; *) echo "GO_IMAGE must be digest-pinned" >&2; exit 1;; esac \
    && go build -trimpath -buildvcs=false -o /out/mecatl-executor ./cmd/mecatl-executor

FROM ${GO_IMAGE}
COPY --from=build --chown=65532:65532 /out/mecatl-executor /mecatl-executor
RUN mkdir -p /workspace /tmp && chown 65532:65532 /workspace /tmp
USER 65532:65532
WORKDIR /workspace
ENV HOME=/workspace TMPDIR=/tmp GOTMPDIR=/tmp
ENTRYPOINT ["/bin/sh", "-c", "trap : TERM INT; sleep infinity & wait"]
