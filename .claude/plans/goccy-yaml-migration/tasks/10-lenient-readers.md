---
id: 10-lenient-readers
title: Remaining lenient and fail-safe reader migration
blocked_by: [04-strict-direct-readers, 08-permconfig-parse-reload-safety]
status: done
branch: "plan-goccy-yaml-migration/10-lenient-readers"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/goccy-yaml-migration
---

# Task brief

Migrate the remaining intentionally tolerant root readers while retaining their
availability posture: ToolHive detection and workspace-trust registry state. Use
the safe diagnostic boundary for parser failures. The task follows the direct
mecatui readers and completed `permconfig` parse/reload migration so its
value-free fail-safe proof covers the complete lenient/fail-safe reader set without
duplicating their implementation work.

Malformed optional input must retain its documented fallback/skip behavior, never
crash startup, never reveal YAML-derived content, and never change ToolHive's
loopback-only `Config.BaseURL` derivation. Do not change `permconfig` decode
mechanics or cross-tier source security behavior here.

## Acceptance criteria

- AC1.2: malformed YAML reported by lenient/fail-safe root readers is value-free even when the input contains credential-shaped values, quotes, comments, indentation traps, or parser-looking text.
  - verify: `TestGoccyYAMLMigration_Scenario1_FailSafeDiagnosticsNeverEchoYAML`
- AC5.1: ToolHive detection keeps its forward-compatible unknown-field behavior and malformed/no-config/untrusted fallback, while `Config.BaseURL` remains `http://127.0.0.1:<port>/v1` and never derives a request host from `gateway_url` or another YAML value; diagnostics expose no parser-derived content.
  - verify: `TestGoccyYAMLMigration_Scenario5_ToolHiveUnknownFieldsFallbackAndLoopbackBaseURL`
- AC5.4: malformed workspace-trust YAML retains the documented untrusted fallback and emits no parser-derived content.
  - verify: `TestGoccyYAMLMigration_Scenario5_WorkspaceTrustFailsSafeAndValueFree`

## Worker notes

Keep the production change to the named readers and use the completed direct-reader
and permconfig tests only for the aggregate diagnostic assertion. Avoid turning
optional malformed files into hard failures or silently accepting strict content.
Run targeted reader tests and the complete lenient/fail-safe diagnostic proof.
