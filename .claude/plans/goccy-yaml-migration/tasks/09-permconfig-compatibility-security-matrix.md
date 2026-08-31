---
id: 09-permconfig-compatibility-security-matrix
title: Permconfig compatibility and security matrix
blocked_by: [08-permconfig-parse-reload-safety]
status: done
branch: "plan-goccy-yaml-migration/09-permconfig-compatibility-security-matrix"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/goccy-yaml-migration
---

# Task brief

Add focused `permconfig` compatibility and security regression coverage after the
schema and parse/reload migration. Exercise the package's supported strict and
lenient configuration forms, provider sections, removed-key behavior, malformed
nested sections, lost-rule counts, and value-free error/diagnostic boundary.

This is a matrix/proof task, not a parser or resolver rewrite. Keep goccy AST
handling package-local to `internal/adapter/permconfig`; do not introduce a
`internal/adapter/yamldiag` schema API, change source precedence, or duplicate the later
cross-source configuration matrix.

## Completion checks

- Fixtures cover compatible top-level input, provider configuration, strict nested
  rejection, targeted `output-economy` rejection, and malformed reload behavior.
- Matrix assertions prove no malformed input yields a partial effective policy and
  no returned error or diagnostic contains YAML-derived keys, scalars, snippets,
  or parser-rendered text.
- The focused matrix reuses the migrated package behavior rather than introducing
  another decode path or parser abstraction.

## Worker notes

Keep this task test-focused. The broad explicit/XDG/project source-tier matrix
belongs to task 11; make only small fixes exposed by this package-level proof.
