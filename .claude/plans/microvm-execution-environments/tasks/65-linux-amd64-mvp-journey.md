---
id: 65-linux-amd64-mvp-journey
title: Deliver the usable Linux amd64 repository-microVM journey
blocked_by: [59-manager-ensure-ready-surface, 60-brood-admission-inprocess-verifier, 61-repository-rootfs-guest-contract, 64-session-attachment-delegation]
status: done
branch: "plan-microvm-execution-environments/65-linux-amd64-mvp-journey"
worktree: ""
issue: "526"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Linux amd64 MVP journey

Complete only the operator-visible Linux amd64 path. Preserve private host ownership with
an unprivileged user namespace, default to honest unrestricted IPv4 guest egress while
leaving the guest IPv6 stack enabled but externally unrouted/unsupported, and retain optional
fail-closed tightening. Prove permissive networking in the live journey; prove optional tightening
through AC6.2's production app/profile and network enforcement tests. The live journey also proves
ordinary first use, direct admitted Brood and in-process
verification, one repository VM/rootfs, two sessions sharing a declared cache but not a
worktree, confined filesystem/exec, close-detach, network behavior, and a prompt loud daemon-
restart failure that preserves the generation record, rootfs, and worktrees without replacement. Keep documentation concise and list every deferred surface.

## Acceptance criteria

- AC6.1: On Linux amd64, guest workload UID/GID 65532 maps to the daemon user through an unprivileged user namespace, model commands run unprivileged, and session or child worktrees are never widened to world-readable, world-writable, or world-traversable modes.
  - verify: `TestMicroVMMVP_Scenario6_LinuxUserNamespaceAvoidsWorldModeWidening`
- AC6.2: The built-in hosted profile provides unrestricted IPv4 guest egress by default and reports that the IPv6 stack remains enabled while external IPv6 is unrouted and unsupported; optional deny-all or allowlist tightening filters IPv4, disables IPv6, and aborts readiness rather than falling back when either enforcement step fails.
  - verify: `TestMicroVMMVP_Scenario6_PermissiveIPv4WithOptionalFailClosedTightening`
- AC7.1: Linux amd64 KVM enters through production profile/session composition and proves ordinary first use, direct admitted Brood consumption with in-process verification, one guest-backed session, confined filesystem/Bash execution, and source-checkout isolation. Deterministic production-composition tests separately prove two-session logical-worktree isolation, exact harness-restart reattachment, fail-closed daemon-state loss, and direct-write versus isolated-child routing. Repository-VM/rootfs singleton, networking, close-detach, and merge/conflict behavior remain proven by their focused AC2–AC6 tests; the live journey does not overclaim those observations.
  - verify: `task e2e:microvm` runs `TestMicroVMDefaultPlacementDailyHarnessJourney` plus `TestMicroVMOperatorJourneyIsLazyIsolatedAndRestartExact`; focused evidence is listed by AC2–AC6
- AC7.2: Concise architecture, operator, and public documentation describes profile selection, the repository sharing boundary, distinct worktrees, direct Brood admission, Linux ownership, permissive networking, optional tightening, restart failure behavior, and the deferred surfaces without claiming Linux arm64 or macOS live support.
  - verify: inspection — `task docs` and `task site:build` prove the linked documentation surfaces build
