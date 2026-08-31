---
id: 02-dependency-engine-frontmatter
title: Dependency replacement and engine frontmatter migration
blocked_by: [01-parser-capability-baseline]
status: done
branch: "plan-goccy-yaml-migration/02-dependency-engine-frontmatter"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/goccy-yaml-migration
---

# Task brief

Replace direct module/parser dependency plumbing with goccy, then migrate only
the standalone engine frontmatter adapters: `engine/adapter/agentfs`,
`engine/adapter/skillfs`, and `engine/adapter/rulesfs`. Preserve their tolerant
contracts and source conformance behavior. Use the task-01 safe diagnostic
approach where an adapter exposes malformed-frontmatter diagnostics, but do not
move YAML into engine domain, port, or agent packages.

Update root and engine module metadata/sums and parser-specific engine allowlist entries as needed for this vertical slice. Do not migrate root configuration adapters, settings editors, or lenient root readers here. Keep the engine
standalone closure small and verify with commands run from the prescribed
Taskfile workflow.

## Acceptance criteria

- AC3.1: agent frontmatter continues to accept documented scalar/list and inline-MCP forms, ignores unknown fields, and reports malformed frontmatter without parser-derived source text.
  - verify: `TestGoccyYAMLMigration_SemanticMatrixAgentFrontmatter`
- AC3.2: skill frontmatter preserves string-or-list `allowed-tools`, metadata normalization, unknown-field tolerance, and malformed-frontmatter rejection/skip behavior.
  - verify: `TestGoccyYAMLMigration_SemanticMatrixSkillFrontmatter`
- AC3.3: rule frontmatter preserves scalar-or-list `paths`, unknown-field tolerance, and existing per-file failure isolation.
  - verify: `TestGoccyYAMLMigration_SemanticMatrixRuleFrontmatter`
- AC3.4: the three adapters continue to satisfy their source conformance coverage after the parser replacement.
  - verify: `task test` — runs the engine adapter/source conformance suites; the scenario-specific tests above pin parser behavior.

## Worker notes

Target a 200–400 LoC-ish vertical change plus generated module sums. Do not
relax unknown-field or malformed-file behavior, and do not run a broad parser
migration merely to satisfy the import scan; coordinate any remaining root
imports through tasks 03–07. Run focused engine adapter tests and
`task test:engine-standalone`.
