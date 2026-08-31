---
id: 01-parser-capability-baseline
title: Parser capability baseline and safe diagnostic boundary
blocked_by: []
status: done
branch: "plan-goccy-yaml-migration/01-parser-capability-baseline"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/goccy-yaml-migration
---

# Task brief

Establish the migration's local parser capability baseline before adapter work.
Add only the smallest local safe-diagnostic helper needed by root adapters to
classify goccy parse failures from typed/token location metadata. It may emit a
stable operation/category and available line/column; it must never inspect or
forward `error.Error()`, source snippets, mapping keys, scalars, or YAML bytes.
Do not create a generic repository parser framework and do not migrate callers
in this task.

Commit reader-labelled fixtures that record the current accepted/rejected scalar,
null, numeric, timestamp, tag, merge, anchor, and alias forms. Keep the fixture
and test seam usable by later strict, lenient, editor, and engine workers; the
fixtures describe each reader contract rather than imposing one policy. Include
the no-token case so future callers cannot invent coordinates or fall back to a
parser string.

## Acceptance criteria

- AC1.3: the PR #836 parser-string/indentation heuristic is absent after migration; the safe diagnostic boundary uses goccy typed/token data and no production parser path inspects `error.Error()` text or source snippets.
  - verify: inspection — `TestGoccyYAMLMigration_Scenario1_NoParserStringOrIndentationHeuristic` statically inspects the migrated diagnostic boundary because deletion and non-use are implementation properties, not externally observable behavior.
- AC1.4: absent token location has a stable value-free diagnostic without invented coordinates; no error path panics or falls back to a parser string.
  - verify: `TestGoccyYAMLMigration_Scenario1_MissingTokenLocationFailsSafe`
- AC2.1: the committed semantic fixture matrix records and preserves each reader's accepted/rejected behavior for implicit and quoted bool/string forms, null/empty values, numeric forms, and timestamp forms; a semantic drift names the reader and fixture case.
  - verify: `TestGoccyYAMLMigration_SemanticMatrixAuthfile`, `TestGoccyYAMLMigration_SemanticMatrixDaemonConfig`, `TestGoccyYAMLMigration_SemanticMatrixPermconfig`, and `TestGoccyYAMLMigration_SemanticMatrixConfigValidate`
- AC2.2: the matrix records and preserves each reader's handling of explicit tags, merge keys, anchors, aliases, and aliases reached through merges; readers that currently prohibit aliases or ambiguous mapping inputs continue to reject them.
  - verify: `TestGoccyYAMLMigration_SemanticMatrixAuthfile`, `TestGoccyYAMLMigration_SemanticMatrixDaemonConfig`, `TestGoccyYAMLMigration_SemanticMatrixPermconfig`, and `TestGoccyYAMLMigration_SemanticMatrixConfigValidate`

## Worker notes

Keep this to the diagnostic seam, fixtures, and focused tests (roughly 200–400
LoC). Do not edit module requirements, parser imports, production adapters, or
ADRs. Run focused tests for the new helper and fixture matrix before handing off.
