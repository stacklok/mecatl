---
id: 45-oci-default-guest-image
title: Reuse Brood Box base through a verified OCI guest-tools image
blocked_by: []
status: done
branch: "plan-microvm-execution-environments/45-oci-default-guest-image"
worktree: ".scratch/task-microvm-45"
issue: "526"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Usability round

Add an OCI execution-image resolver to microvmd that accepts digest-pinned `repo@sha256` only, reuses go-microvm's hardened platform-specific OCI pull/extract/cache, and returns the extracted tree through mecatl's existing Sigstore/tree-digest/launch-snapshot admission. Retain both OCI manifest digest and materialized-tree identity.

Define and publish a thin mecatl guest-tools image derived from Brood Box's `images/base`—not an agent-specific image and never mutable `:latest`. Add UID/GID 65532 home/cache setup and inject/use the separately verified mecatl guest agent. Do not place LLM credentials, MCP config, SSH credentials, or another coding-agent harness in the image.

Verification: tag rejection, platform mismatch, cold/warm/concurrent cache, manifest/tree/signature mismatch, tool smoke under UID65532, guest secret absence, live KVM/HVF contract, release image digest/provenance.
