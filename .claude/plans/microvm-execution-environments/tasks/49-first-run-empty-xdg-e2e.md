---
id: 49-first-run-empty-xdg-e2e
title: Prove an empty-XDG first-run microVM journey
blocked_by: [45-oci-default-guest-image, 47-mecatui-microvm-first-run, 48-microvm-lifecycle-cli-ux, 50-default-oci-release-contract]
status: done
branch: "plan-microvm-execution-environments/49-first-run-empty-xdg-e2e"
worktree: ".scratch/task-microvm-49"
issue: "526"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Usability round

Extend real-hypervisor E2E from an empty XDG home: explicit init/confirmation, verified OCI default image install, config generation, daemon start/doctor, `mecatui --microvm` profile session, guest edit visible only in prepared worktree, detach, daemon restart, clean delete, dirty delete retention, status/recover outputs. On daemon restart, prove the documented exact-generation convergence: securely reattach when all runtime/network identity is recoverable, otherwise identity-check and destroy the orphan while retaining the session worktree and returning an actionable no-replacement recovery result. Never require or permit silent empty-VM recreation. Include negative trust/preflight/no-fallback controls.

Verification: local Linux KVM full first-run covers the actual convergence branch, retained edits/recovery instructions, and a subsequent explicitly-created session when destruction was required; CI platform contracts; ordinary tests remain offline; all gates/ac-trace.
