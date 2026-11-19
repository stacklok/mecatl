# The build argument is a run-unique local tag verified against the built executor ID.
# Only this utility is added; the executor, UID, entrypoint and mounts are inherited.
ARG EXECUTOR_BASE
FROM ${EXECUTOR_BASE} AS compatible
COPY --chown=65532:65532 --chmod=755 operator-utility /usr/local/bin/mecatl-operator-utility

# Negative qualification control: a working server, incompatible Pod exec protocol.
FROM compatible AS incompatible
COPY --from=compatible /mecatl-executor /usr/local/libexec/mecatl-executor-server
COPY --chown=65532:65532 --chmod=755 incompatible-protocol /mecatl-executor

FROM compatible AS qualified
