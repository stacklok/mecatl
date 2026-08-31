# Plan: goccy-yaml-migration

Migrate direct YAML parsing in the root and engine modules from `go.yaml.in/yaml/v3`
to `github.com/goccy/go-yaml`, preserving the existing strict, tolerant, and
fail-safe contracts. Parser diagnostics are a security boundary: expose only
harness context and typed token locations, never parser-rendered YAML content.

- **Plan:** [docs/acceptance/goccy-yaml-migration.md](../../../docs/acceptance/goccy-yaml-migration.md)
- **ADR:** [ADR 0246](../../../docs/adr/0246-goccy-yaml-parser.md)
- **Accumulator:** `acc/goccy-yaml-migration` (off `feat/goccy-yaml-migration`)

## Tasks (dependency ordered)

| # | Task | blocked_by |
|---|---|---|
| 01 | [Parser capability baseline and safe diagnostics](tasks/01-parser-capability-baseline.md) | — |
| 02 | [Dependency replacement and engine frontmatter](tasks/02-dependency-engine-frontmatter.md) | 01 |
| 03 | [Root goccy AST and safe-document foundation](tasks/03-root-ast-document-foundation.md) | 02 |
| 04 | [Strict direct readers and mecatui YAML readers](tasks/04-strict-direct-readers.md) | 03 |
| 05 | [Permconfig-local goccy AST foundation and characterization](tasks/05-permconfig-local-ast-foundation.md) | 03 |
| 06 | [Mecated validation and mecatui settings document editor](tasks/06-settings-document-editor.md) | 03 |
| 07 | [Permconfig schema and provider NodeUnmarshaler migration](tasks/07-permconfig-node-unmarshaler-schema.md) | 05 |
| 08 | [Permconfig parse entry point, lenient top level, and reload safety](tasks/08-permconfig-parse-reload-safety.md) | 07 |
| 09 | [Permconfig compatibility and security matrix](tasks/09-permconfig-compatibility-security-matrix.md) | 08 |
| 10 | [Remaining lenient and fail-safe reader migration](tasks/10-lenient-readers.md) | 04, 08 |
| 11 | [Configuration-source compatibility and security matrix](tasks/11-compatibility-security-matrix.md) | 09, 10 |
| 12 | [Documentation and aggregate repository proof](tasks/12-docs-aggregate.md) | 04, 06, 09, 10, 11 |

Tasks 04 and 06 remain independent direct-reader/editor migrations after task 03;
their stable task IDs keep completed work valid. The `permconfig` path is
intentionally sequential: task 05 establishes package-local AST characterization,
task 07 converts schema/provider `NodeUnmarshaler` handling, task 08 migrates the
parse and fail-safe reload boundary, and task 09 proves that package's compatibility
and security matrix. Task 10 follows the completed parse/reload migration, task 11
then exercises the broader configuration-source matrix, and task 12 aggregates the
repository proof.
