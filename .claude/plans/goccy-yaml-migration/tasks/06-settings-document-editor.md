---
id: 06-settings-document-editor
title: Mecated validation and mecatui settings document editor
blocked_by: [03-root-ast-document-foundation]
status: done
branch: "plan-goccy-yaml-migration/06-settings-document-editor"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/goccy-yaml-migration
---

# Task brief

Migrate together the document-node paths in `cmd/mecated/configvalidate.go`
(`parseSettingsDocument`, `replaceMappingValue`, and in-memory learning-patch
validation) and `cmd/mecatui/learning_settings.go`
(`operatorLearningSettings.withLockedDocument`) using the task-03 root AST/document
foundation. Add and use the named preservation fixtures
`settings-preserve-top-level.yaml` and `settings-preserve-learning.yaml`. The
editor may change only the intended learning value and any fixture-declared
normalization directly required by that rewrite.

Preserve ADR-0225's read-only validation boundary: final-component no-follow,
nonblocking regular-file open; bounded read; no write or configuration-value
output; and a missing base only for an explicit learning patch that preflights a
prospective new file. Preserve single-document, duplicate, anchor, alias, symlink,
lock, and atomic-write boundaries. Validation patches remain in-memory and
value-free; do not broaden this task into strict `permconfig` decoding or UI
state/keymap readers.

## Acceptance criteria

- AC2.5: `mecated config validate` retains ADR-0225's read-only safety boundary: it opens only a final-component no-follow, nonblocking regular file, bounded-reads it, never writes either input or prints configuration values, and permits a missing base only when an explicit learning patch preflights a prospective new file.
  - verify: `TestGoccyYAMLMigration_Scenario2_ConfigValidateADR0225Safety`
- AC2.6: config validation still rejects aliases, anchors, duplicate mapping keys, non-mapping roots, multiple documents, and invalid learning-only patches before it claims a document is valid.
  - verify: `TestGoccyYAMLMigration_Scenario2_ConfigValidationSafeDocumentContract`
- AC2.7: malformed config validation input exposes only its available line and column, never YAML-derived text.
  - verify: `TestRunConfigValidateSyntaxLocationIsValueFree`
- AC4.1: a `--learning-patch` replaces or inserts only the top-level learning mapping in memory, validates the complete proposal, never writes or displays the proposed document, and preserves the unrelated semantic content specified by `settings-preserve-top-level.yaml`.
  - verify: `TestGoccyYAMLMigration_Scenario4_LearningPatchPreservesUnrelatedDocument`
- AC4.2: mecatui's learning mode and sensitivity edits match the expected outputs for `settings-preserve-top-level.yaml` and `settings-preserve-learning.yaml`: unrelated values, comments, order, and supported styles survive; every permitted normalization is asserted explicitly.
  - verify: `TestGoccyYAMLMigration_Scenario4_LearningEditorPreservationFixtures`
- AC4.3: the settings editor retains one-document/duplicate/alias protections, does not write on parse or validation failure, and keeps its existing symlink rejection, cross-process lock, and atomic-write behavior; parser migration does not widen its write authority.
  - verify: `TestGoccyYAMLMigration_Scenario4_LearningEditorWriteSafetyUnchanged`

## Worker notes

Keep the slice to the two document-node consumers, fixtures, and focused tests.
Never silently accept a formatting loss: write each allowed normalization into
expected fixture output. Do not alter public flags or general configuration
formatting behavior.
