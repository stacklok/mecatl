---
id: 55-brood-base-lineage-provenance
title: Derive guest-tools image from immutable Brood Box base
blocked_by: []
status: done
branch: "b0d75fbe"
worktree: ".scratch/task-microvm-55"
issue: "526"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Usability/supply-chain repair

Make guest-tools mechanically derive from an immutable per-platform Brood Box base image or verified filesystem at a pinned revision/digest—not a label over independent Wolfi. Carry actual base manifest/build provenance into the mecatl image attestation and daemon admission. Retain UID65532 setup, guest-agent injection, no agent runtime/config/credentials, and multi-arch tooling smoke. If Brood Box lacks signed immutable base publication, add the minimal upstream release dependency rather than pretending lineage.

Verification: FROM/input digest is Brood base, provenance correlates exact platform manifest/revision, package/tool compatibility and secret absence, tag rejection/full gates.
