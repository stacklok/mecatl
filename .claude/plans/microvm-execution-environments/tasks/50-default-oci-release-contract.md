---
id: 50-default-oci-release-contract
title: Publish and install the default digest-pinned OCI guest image
blocked_by: [45-oci-default-guest-image]
status: done
branch: "plan-microvm-execution-environments/50-default-oci-release-contract"
worktree: ".scratch/task-microvm-50"
issue: "526"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Usability repair

Connect the task45 guest-tools OCI image to the release/bootstrap contract. Release publishing must produce the multi-arch image first, capture immutable per-platform manifest digests, and include the correct digest-pinned OCI reference plus Sigstore/provenance/tree policy in each platform release manifest. Installer must project OCI artifacts without requiring a local execution-image archive/path. `mecatui microvm init` must have a versioned default release channel/platform manifest and `microvm-local` guest image, with explicit override support and no mutable tags.

Verification: workflow ordering/digest capture; installer OCI projection; init defaults nonempty and strict; tag rejection; release fixture bootstrap and local live pull; full gates.
