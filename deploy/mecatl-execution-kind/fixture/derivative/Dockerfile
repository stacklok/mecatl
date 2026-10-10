# The build argument is a run-unique local tag verified against the built executor ID.
# Only this utility is added; the executor, UID, entrypoint and mounts are inherited.
ARG EXECUTOR_BASE
FROM ${EXECUTOR_BASE} AS compatible
COPY --chown=65532:65532 --chmod=755 operator-utility /usr/local/bin/mecatl-operator-utility

# Negative qualification control: valid image, incompatible executor wire protocol.
FROM compatible AS incompatible
COPY --chown=65532:65532 --chmod=755 incompatible-protocol /mecatl-executor

FROM compatible AS qualified
