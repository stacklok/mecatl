# Both bases are mandatory digest references supplied by the release workflow.
ARG GO_IMAGE
ARG RUNTIME_IMAGE
FROM ${GO_IMAGE} AS build
ARG GO_IMAGE
ARG RUNTIME_IMAGE
WORKDIR /src
COPY . .
RUN case "${GO_IMAGE}" in *@sha256:*) ;; *) echo "GO_IMAGE must be digest-pinned" >&2; exit 1;; esac \
    && case "${RUNTIME_IMAGE}" in *@sha256:*) ;; *) echo "RUNTIME_IMAGE must be digest-pinned" >&2; exit 1;; esac \
    && CGO_ENABLED=0 go build -trimpath -buildvcs=false -o /out/mecatl-execution-provider ./cmd/mecatl-execution-provider

FROM ${RUNTIME_IMAGE}
COPY --from=build --chown=65532:65532 /out/mecatl-execution-provider /mecatl-execution-provider
USER 65532:65532
ENTRYPOINT ["/mecatl-execution-provider"]
