---
id: 05-permconfig-local-ast-foundation
title: Permconfig-local goccy AST foundation and characterization
blocked_by: [03-root-ast-document-foundation]
status: done
branch: "plan-goccy-yaml-migration/05-permconfig-local-ast-foundation"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/goccy-yaml-migration
---

# Task brief

Establish the narrow goccy AST seam owned by `internal/adapter/permconfig` and
characterize the current yaml.v3-node behavior before migrating a decoder. This
AST handling stays package-local: do not move it into `internal/adapter/yamldiag`,
which remains limited to its root diagnostic/document contract.

Inventory the custom yaml.v3 `Node` decoders, their node-shape expectations, and
the source-location/error behavior the later migration must retain. Add only the
local helpers necessary to inspect goccy nodes safely and focused characterization
tests. Do not switch production decoders, parse entry points, reload behavior, or
configuration policy in this task.

## Completion checks

- The complete inventory of `permconfig` custom yaml.v3 `Node` decoders is covered
  by focused characterization tests for its accepted and rejected node shapes.
- The local AST helper exposes only the information required by `permconfig`; no
  goccy AST API or schema helper is added to `internal/adapter/yamldiag`.
- Tests characterize safe location/error context without asserting or exposing
  YAML-derived keys, scalar values, snippets, or parser-rendered messages.

## Worker notes

Keep this as a small package-local foundation. It is deliberately sequentially
before the NodeUnmarshaler conversion: do not fold schema migration or reload
semantics into it. Run the focused `permconfig` characterization tests before
handoff.
