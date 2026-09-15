---
matlatl: orphan-intentional
paths:
  - "engine/tool/**"
  - "internal/app/learned_skills.go"
---
# Catalog metadata

Catalog ordering and filtering must use registration keys, not `Tool.Spec()`: specification may refresh durable state. Keep live `Spec()` calls at real advertisement/specification boundaries and preserve `Execute()` freshness; do not replace either with a stale spec cache. Counting-tool regressions must detect extra specification calls during enumeration or filtering.
