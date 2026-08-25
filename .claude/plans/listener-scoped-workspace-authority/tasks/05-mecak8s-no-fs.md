---
id: 05-mecak8s-no-fs
title: Make mecak8s server-assigned and no-FS by default
blocked_by: [01-service-authority, 02-internal-authority-paths, 03-mecated-composition]
status: done
branch: "plan-listener-scoped-workspace-authority/05-mecak8s-no-fs"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/listener-scoped-workspace-authority
---

# Task brief

Configure mecak8s as an always server-assigned, file-less deployment. Map the wire's empty profile to no-FS, permit explicit no-FS, reject other profiles and non-empty workspaces, and prove the offline fixture cannot use the container root. Preserve documented no-FS tools and schedule behavior.

## Acceptance criteria

- AC4.1: A new `mecak8s` session that omits a profile uses the no-FS profile and succeeds with an empty workspace.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario4_Mecak8sDefaultsToNoFS`
- AC4.2: The default mecak8s session catalog excludes `Read`, `Edit`, `Write`, `Grep`, `Glob`, `Bash`, `Parallel`, and `SkillDraft`, while retaining the documented no-FS-safe tools.
  - verify: `TestNoFSCatalogProfile`
- AC4.3: mecak8s accepts an explicit no-FS profile and treats the wire's empty (omitted/default) profile as no-FS. It rejects every non-no-FS profile and every non-empty workspace, so a caller cannot bypass the file-less deployment default.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario4_Mecak8sRejectsFilesystemProfileAndWorkspace`
- AC4.4: The offline mecak8s fixture creates and runs a default session without using the container root as its agent workspace.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario4_Mecak8sFixtureRunsNoFS`
