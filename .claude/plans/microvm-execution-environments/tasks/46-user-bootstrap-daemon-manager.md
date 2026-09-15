---
id: 46-user-bootstrap-daemon-manager
title: Add safe user-local bootstrap and daemon management defaults
blocked_by: []
status: done
branch: "plan-microvm-execution-environments/46-user-bootstrap-daemon-manager"
worktree: ".scratch/task-microvm-46"
issue: "526"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Usability round

Implement a root-module user-local bootstrap/manager that keeps daemon absolute-path validation intact but supplies safe XDG defaults: state 0700, short runtime socket, config 0600, verified artifacts data root, and alias `microvm-local`. Explicit interactive init preflights platform/KVM/HVF/Git, downloads and bootstrap-verifies a release bundle, runs the existing installer, generates strict daemon config plus user-global host alias, starts/reuses microvmd, waits for owner-only socket, runs doctor, and enables the alias only after success.

Never auto-run sudo/change KVM ACL/groups/user namespaces; never write project config; never auto-enable by detecting KVM/daemon; failure leaves profile disabled and recovery instructions. Add manager status/doctor/recover/delete seams around existing lifecycle.

Verification: empty-XDG bootstrap, path modes/symlink refusal/Darwin socket, unsupported/preflight/download/evidence/doctor failures, idempotent daemon reuse/no duplicate, enabled-only-after-doctor, no-host-fallback.
