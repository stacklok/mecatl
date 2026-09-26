---
id: 58-final-first-run-usability-e2e
title: Prove self-contained first-run usability and documentation
blocked_by: [53-daemon-identity-managed-restart, 54-self-contained-release-bootstrap, 55-brood-base-lineage-provenance, 56-paginated-lifecycle-inventory, 57-first-run-dead-end-repair]
status: done
branch: "5db272d8"
worktree: ".scratch/task-microvm-58"
issue: "526"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Final usability proof

From empty host/XDG using only published signed mecatu binary/release defaults: init, confirmation, Brood-derived OCI pull/admission, daemon identity match, doctor, daily --microvm session, edits/paths, detach/resume, restart convergence, paginated status, recover, clean/dirty delete. Exercise stale daemon, bad installer/evidence/base provenance, host-session resume, and >256 records. Ensure docs exactly match commands.

Verification: local KVM full production journey, CI contracts, UX/security reviewers zero blockers, all docs/site/full gates.
