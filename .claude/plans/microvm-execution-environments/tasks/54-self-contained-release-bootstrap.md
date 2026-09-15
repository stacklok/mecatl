---
id: 54-self-contained-release-bootstrap
title: Make the release bootstrap self-contained on every host platform
blocked_by: []
status: done
branch: "1cc9a74c"
worktree: ".scratch/task-microvm-54"
issue: "526"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Usability/security repair

Use the installer from the digest-verified, safely extracted release bundle; remove the external installer requirement from normal init and bind any development override to explicit opt-in/digest. Publish signed/provenanced host `mecatui` binaries with embedded matching bootstrap defaults for Linux amd64/arm64 and Darwin arm64; keep OCI mecatui separate. Provide a copy-paste release download/bootstrap path that verifies manifest/binary before execution.

Verification: empty host needs only downloaded signed mecatu binary/init; unrelated installer env ignored/rejected; platform release assets/provenance; action/full gates.
