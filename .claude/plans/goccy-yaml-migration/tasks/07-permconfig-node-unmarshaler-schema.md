---
id: 07-permconfig-node-unmarshaler-schema
title: Permconfig schema and provider NodeUnmarshaler migration
blocked_by: [05-permconfig-local-ast-foundation]
status: done
branch: "plan-goccy-yaml-migration/07-permconfig-node-unmarshaler-schema"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/goccy-yaml-migration
---

# Task brief

Migrate `internal/adapter/permconfig`'s custom schema decoders and provider
sections from yaml.v3 `Node` decoding to goccy `NodeUnmarshaler` handling, using
the package-local foundation from task 05. Convert every inventoried custom node
decoder; preserve its strict node-shape and nested-section behavior rather than
substituting a broad untyped decode.

Keep goccy AST handling inside `permconfig`, not `internal/adapter/yamldiag`. This task is limited
to schema/provider decoding mechanics. Do not change `parseYAML`, top-level
removed-key policy, lost-rule accounting, reload error handling, editor behavior,
or the cross-source matrix.

## Completion checks

- Every yaml.v3 `Node` custom decoder identified by task 05, including provider
  configuration decoders, is migrated to the goccy `NodeUnmarshaler` path.
- Focused schema tests retain each decoder's strict nested mapping/sequence/type
  rejection and provider-specific validation behavior.
- Package-local helpers do not leak raw parser errors or YAML-derived content into
  returned errors or diagnostics.

## Worker notes

Work decoder-by-decoder against the characterization suite. Keep the production
change confined to `permconfig` schema/provider paths; task 08 owns parse entry
points and fail-safe reload semantics.
