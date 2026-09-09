---
id: 02-profile-paths
title: Environment profiles and source/worktree/guest path roles
blocked_by: [01-module-contract]
status: done
branch: "plan-microvm-execution-environments/02-profile-paths"
worktree: ".scratch/task-microvm-02"
issue: "533"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Task brief

Add the operator-only environment-profile surface and the explicit source checkout, prepared host worktree, guest root, and EnvironmentRef projections. Wire host-side discovery to the correct tier without enabling VM provisioning yet. Project configuration must not control privileged environment policy.

## Acceptance criteria

- AC1.1: Selecting an enabled operator microVM profile creates a session whose source
  checkout, prepared host worktree, guest `/workspace`, and EnvironmentRef are four
  explicit values with non-overlapping meanings; the response/UI displays the first
  three accurately without exposing driver credentials or control endpoints.
  - verify: `TestMicroVMEnvironments_Scenario1_PathRolesAreExplicit`
- AC1.2: `environment_profile` selects execution placement independently from the
  existing `profile`; default and `no-fs` retain their existing tool-surface meaning.
  - verify: `TestADR_0108_EnvironmentProfileIsIndependentOfToolProfile`
- AC1.3: Project-tier configuration and model input cannot set or weaken the driver
  endpoint, image, mounts, resources, egress, seccomp, lifecycle, quotas, signer, or
  attestation policy; an unknown, disabled, or unavailable alias fails loudly without
  local fallback.
  - verify: `TestMicroVMEnvironments_Scenario1_OperatorProfilesAreAuthoritative`
- AC1.4: Project AGENTS/CLAUDE instructions, rules, commands, skills, and agent
  definitions are resolved from the prepared session worktree, while user-global
  settings, soul, memory, skills/agents, MCP configuration, identity, and provider
  credentials remain host-side.
  - verify: `TestMicroVMEnvironments_Scenario1_DiscoveryUsesCorrectPathTier`
