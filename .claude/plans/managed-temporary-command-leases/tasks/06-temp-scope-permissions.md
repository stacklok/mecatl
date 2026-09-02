---
id: 06-temp-scope-permissions
title: Bash temporary scope policy and guidance
blocked_by: [01-temporary-storage-config, 04-command-environment-overlay, 05-foreground-managed-leases]
status: done
branch: "plan-managed-temporary-command-leases/06-temp-scope-permissions"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/managed-temporary-command-leases
---

# Bash temporary scope policy and guidance

Extend the existing Bash tool with the `temp_scope` field, its independent tool-wide `BashSystemTemp` capability decision, safe approval projection, and real-factory prompt guidance. Do not invent compound permission syntax.

## Acceptance criteria

- AC2.1: A Bash call without `temp_scope`, or with `temp_scope: managed`, receives the managed lease overlay when managed mode is enabled; an unknown scope is rejected before command execution.
  - verify: `TestADR_0281_BashManagedScopeDefaultAndValidation`
- AC2.2: A `temp_scope: system` request requires both the ordinary Bash decision and `BashSystemTemp`; allowing `Bash(go test:*)` alone never permits the system overlay, while a deny on `BashSystemTemp` dominates every allow.
  - verify: `TestADR_0281_SystemScopeRequiresIndependentCapability`
- AC2.3: `BashSystemTemp` is a deliberately tool-wide v1 capability: its allow permits the system overlay only for Bash commands independently allowed by normal policy, never Bash execution or arbitrary filesystem paths.
  - verify: `TestADR_0281_SystemTempCapabilityIsGlobalButNotBashAllow`
- AC2.4: A system-scope approval and audit record disclose the scope and safe command summary but contain neither raw temporary path nor environment values.
  - verify: `TestADR_0281_SystemTempApprovalDoesNotLeakPath`
- AC2.5: The real built engine's system prompt explains that managed storage is disposable, `temp_scope: system` is the declared escape for host-shared or longer-lived temporary state, and the field is not a filesystem sandbox.
  - verify: `TestADR_0281_EngineSystemPromptContainsTempScopeContract`
- AC2.6: When the operator selects `mode: system`, every Bash call retains the configured/inherited system temporary-directory behavior, no managed allocation is created, and per-call `temp_scope` cannot re-enable management.
  - verify: `TestADR_0281_SystemModeIsRollbackSwitch`
