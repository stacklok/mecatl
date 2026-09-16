---
id: 21-root-composition-lifecycle-repair
title: Wire production environment client and durable lifecycle ownership
blocked_by: []
status: done
branch: "plan-microvm-execution-environments/21-root-composition-lifecycle-repair"
worktree: ".scratch/task-microvm-21"
issue: "533"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Panel repair brief

Cross-confirmed Spec/Standards/Security/Architecture blockers: root `app.Build` exposes environment profiles but wires no microvmd lifecycle client, provisioner, resolver, detach, or delete path; daemon restart uses only in-memory runtime handles and never starts reconciliation; admission is unwired and leases are discarded.

Implement the thin authenticated UDS client/adapter in the root module without importing go-microvm. Wire create to `PreparedEnvironment`, resolve to a complete `tool.Environment`, failed-create reconciliation, exact detach, and permanent delete through Service lifecycle paths. Make daemon startup reconcile durable registry/runtime state before serving and make concrete runtime reopening verify exact runner/process/endpoint identity instead of consulting only a fresh map. Persist/reconstruct admission reservations and release only after durable destruction.

Protects AC1.1, AC5.1–AC5.6, AC6.3.

## Verification

- Real `app.Build` offline integration: configured profile create → persist → close/detach → fresh Build/daemon resolve → run → delete.
- Fresh concrete backend instance reopens or safely rejects exact persisted generations; startup reconciliation converges partial cleanup.
- Admission is production-wired, restart-reconstructed, and released on durable delete.
- Existing named AC proofs remain green; lint/test/docs pass.
