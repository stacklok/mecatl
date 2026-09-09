---
id: 22-artifact-release-ci-repair
title: Bind artifact evidence to bytes and publish verifiable runtime artifacts
blocked_by: []
status: done
branch: "plan-microvm-execution-environments/22-artifact-release-ci-repair"
worktree: ".scratch/task-microvm-22"
issue: "528"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Panel repair brief

Cross-confirmed Spec/Security blockers: artifact admission signs the claimed digest but never binds it to the materialized bytes. DevOps blockers: ordinary CI omits nested microVM module gates, and the mecatl release publishes neither microvmd nor a signed/attested guest artifact.

Cryptographically bind materialized runtime/firmware/image content to the signed subject and reject valid evidence paired with altered bytes. Add normal CI build/race/standalone/lint/vet/cache coverage for every microVM subpackage. Extend release output with deterministic microvmd/guest artifacts or OCI payloads, immutable digests, SBOM, signature, and provenance consistent with documented operator verification. Keep go-microvm v0.0.40 runtime/firmware reuse.

Protects AC2.1–AC2.3, AC8.1, and Definition of done supply-chain claims.

## Verification

- Valid evidence + altered source bytes fails admission.
- Ordinary PR CI fails on a planted nested-package test/lint error.
- Release workflow dry-run/fixture produces reproducible digest/SBOM/signature/provenance references consumable by strict config.
- Lint/test/docs/action lint pass.
