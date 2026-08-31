---
id: 04-strict-direct-readers
title: Strict direct readers and mecatui YAML readers
blocked_by: [03-root-ast-document-foundation]
status: done
branch: "plan-goccy-yaml-migration/04-strict-direct-readers"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/goccy-yaml-migration
---

# Task brief

Migrate the direct reader paths in `internal/adapter/daemonconfig`,
`internal/adapter/authfile`, and mecatui's state/keymap YAML readers to goccy.
Use the task-01 diagnostic boundary and the task-03 document foundation where a
reader needs document checks. Preserve each reader's current strict or local
fallback contract; this task does not migrate `permconfig`'s custom node schema or
the settings-document editors.

Daemon failures must retain their schema-versus-syntax distinction, reject a
second document, and never expose YAML content. Authfile must keep its
whole-document rejection versus entry-local credential-material filtering. Mecatui
state and keymap failures must keep their current startup-safe local fallback/error
behavior. Do not move the ADR-0225 config-validation path into this task.

## Acceptance criteria

- AC1.1: malformed YAML reported by strict root readers includes the goccy token line and column when available, and contains neither the offending line nor any attacker-controlled key or scalar.
  - verify: `TestGoccyYAMLMigration_Scenario1_StrictDiagnosticsUseTokenLocationWithoutSource`
- AC2.3: daemon configuration rejects unknown fields, wrong types, malformed syntax, and a second document; its diagnostic preserves the existing distinction between schema and syntax without exposing YAML content.
  - verify: `TestGoccyYAMLMigration_Scenario2_DaemonConfigStrictContract`
- AC2.4: a whole-file auth.yaml schema/decode failure rejects the complete credential snapshot with a value-free warning, while entry-local invalid OAuth/API material found during provider validation drops only that entry/material and retains valid sibling entries.
  - verify: `TestGoccyYAMLMigration_Scenario2_AuthFileWholeFileVsEntryLocalFailure`
- AC5.2: malformed mecatui state and keymap YAML preserves the existing local fallback/error behavior without breaking startup or exposing YAML content.
  - verify: `TestGoccyYAMLMigration_Scenario5_MecatuiReadersRetainFallbacks`

## Worker notes

Keep this to the named direct readers and focused regression tests. Do not add a
partial policy path, log raw parser errors, or change reader availability posture.
Run the targeted adapter and mecatui reader tests before handoff.
