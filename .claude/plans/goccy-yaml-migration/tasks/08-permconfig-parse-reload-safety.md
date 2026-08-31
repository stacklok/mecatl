---
id: 08-permconfig-parse-reload-safety
title: Permconfig parse entry point, lenient top level, and reload safety
blocked_by: [07-permconfig-node-unmarshaler-schema]
status: done
branch: "plan-goccy-yaml-migration/08-permconfig-parse-reload-safety"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/goccy-yaml-migration
---

# Task brief

Migrate `internal/adapter/permconfig`'s `parseYAML` entry point after the local
NodeUnmarshaler schema conversion. Preserve the lenient top-level contract,
including the targeted `output-economy` removed-key rejection, while retaining
strict nested-section validation. Preserve lost-rule counting and the bounded,
value-free reload skip/report path: malformed configuration must never apply a
partial policy.

Do not move AST handling into `internal/adapter/yamldiag`, alter providers' schema mechanics, or
broaden this task into the compatibility matrix, editors, or other readers.

## Acceptance criteria

- AC2.7: permissions/settings decoding remains lenient at the top level, preserves the targeted `output-economy` rejection, and keeps nested strict section validation and lost-rule counting behavior.
  - verify: `TestGoccyYAMLMigration_Scenario2_PermconfigStrictLenientBoundary`
- AC5.3: a malformed permission config still follows its documented bounded skip/report path, including strict nested sections and lost-rule counts, rather than silently applying a partial policy.
  - verify: `TestGoccyYAMLMigration_Scenario5_PermissionReloadFailsSafeWithoutPartialPolicy`

## Worker notes

Treat the entry point and reload boundary as fail-safe security behavior. Do not
log raw parser errors or make a partial policy apply after malformed input. Run
the targeted permission configuration and reload tests before handoff.
