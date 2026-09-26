---
id: 26-production-e2e-ci-repair
title: Exercise production daemon boundaries in a viable platform matrix
blocked_by: [21-root-composition-lifecycle-repair, 22-artifact-release-ci-repair, 23-guest-trust-protocol-repair, 24-worktree-git-repair, 25-delegation-operations-repair]
status: done
branch: "plan-microvm-execution-environments/26-production-e2e-ci-repair"
worktree: ".scratch/task-microvm-26"
issue: "535"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Panel repair brief

Spec/DevOps blockers: live E2E bypasses production artifact admission and Unix peer/control daemon, omits deny-all/UDP/port/IPv6 probes, and uses GitHub-hosted macOS where nested HVF is unavailable.

Make acceptance E2E launch the production `mecatl-microvmd` and client over a real private UDS with peer credentials, strict digest/signature/attestation inputs, create/resolve/detach/delete lifecycle, guest Git, Workspace/exec, quotas/doctor, delegation, and host-secret/sibling isolation. Exercise deny-all, allowed/denied hostname+port+protocol, UDP as supported, and explicit IPv6 unavailability. Move HVF execution to a controlled self-hosted Apple Silicon runner with a real hypervisor preflight; keep GitHub-hosted macOS for compile/static gates only. Preserve Linux amd64/arm64 cells.

Protects AC4.5, AC8.1–AC8.2 and the live Definition-of-done gate.

## Verification

- Local Linux KVM production-daemon journey passes with strict artifacts and real UDS.
- Wrong peer and altered/unsigned artifact fail.
- CI matrix uses a viable HVF runner and explicit preflight.
- `task e2e:microvm`, action lint, lint/test/docs/site gates pass.
