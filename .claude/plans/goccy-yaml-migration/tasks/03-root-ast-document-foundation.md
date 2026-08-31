---
id: 03-root-ast-document-foundation
title: Root goccy AST and safe-document foundation
blocked_by: [02-dependency-engine-frontmatter]
status: done
branch: "plan-goccy-yaml-migration/03-root-ast-document-foundation"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/goccy-yaml-migration
---

# Task brief

Add the narrow root-adapter helper required by the remaining goccy document-node
consumers. It parses exactly one YAML document, exposes only safe token locations,
and provides the explicit checks those consumers need: document/root shape,
duplicate mapping keys, anchors, and aliases. It must decode only caller-selected
AST nodes; it must not become a generic repository parser framework, a schema
layer, or a cross-layer service.

Keep parser diagnostics on the task-01 value-free typed/token boundary. The helper
must not inspect or expose parser-rendered errors, source snippets, mapping keys,
scalars, or YAML bytes. It is a root-adapter implementation detail for the
subsequent `permconfig` and settings-document tasks, not an engine dependency.
Do not migrate a reader, alter module requirements, or change an acceptance-plan
or ADR in this task.

## Completion checks

- Focused tests prove one-document parsing and safe available/absent token
  locations without parser text.
- Focused tests prove the helper rejects or reports the requested non-mapping root,
  duplicate-key, anchor, and alias conditions before a caller decodes a selected
  node.
- Focused tests prove selected-node decode is explicit and cannot accidentally
  turn the helper into a whole-document schema decoder.

## Worker notes

Keep this a small foundation (roughly 200–400 LoC including tests) beside the
existing root diagnostic seam. Do not add an interface, registry, or package meant
for arbitrary YAML consumers. Run its focused tests before handoff; tasks 04–06
own the reader migrations and their acceptance proofs.
