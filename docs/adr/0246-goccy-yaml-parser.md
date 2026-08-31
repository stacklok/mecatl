# ADR 0246 — goccy/go-yaml parser migration

- Status: Accepted
- Date: 2026-08-28
- Scope: root and engine YAML parsing, YAML diagnostics, and the engine standalone dependency closure
- Supersedes: none
- Superseded by: none

## Context

Mecatl directly parses YAML in the root module and in the importable engine. The
root parsers cover operator configuration, credentials, daemon configuration, and
mecatui-maintained settings documents; engine adapters parse agent, skill, and rule
frontmatter. The current direct dependency is `go.yaml.in/yaml/v3`.

Parser errors are attacker- or operator-controlled input. Returning a parser's
formatted error can disclose source snippets, mapping keys, scalar values, or
credentials. This applies to returned errors and diagnostic/log attributes, not
only command output. A path is safe to identify only when it was supplied outside
the YAML document or derived from a known configuration location; a path-looking
YAML value remains untrusted content. A temporary yaml.v3 parser-string/indentation
heuristic from PR #836 tries to distinguish safe schema errors from unsafe syntax
errors. That policy is parser-format-dependent, incomplete by construction, and
cannot be a durable security boundary.

A parser replacement can also silently change YAML interpretation. The existing
readers intentionally differ: strict configuration rejects ambiguous documents;
operator settings are top-level lenient with targeted rejections; ToolHive ignores
unknown fields for upstream compatibility; authfile distinguishes a malformed
whole document from entry-local invalid credential material. Those contracts, plus
implicit scalars, nulls, numeric/timestamp forms, tags, merge keys, anchors, and
aliases, need migration-time compatibility coverage rather than a later policy
choice.

Replacing direct dependencies in both modules is costly to reverse: it changes
parsing behavior, document editing, the root dependency graph, and the small
standalone closure promised by [ADR 0036](./0036-engine-module.md). The decision
therefore needs a frozen record rather than an implementation-local choice.

## Decision

Migrate every direct root- and engine-module use of `go.yaml.in/yaml/v3` to
`github.com/goccy/go-yaml` now. Keep YAML parsing in its existing adapters and
commands; do not introduce a shared parser service or move parser dependencies
into domain, port, or agent packages.

Derive malformed-document diagnostics only from goccy token line and column
metadata. Diagnostics are structured and value-free: they may identify the
operation, bounded location, and a path that was supplied externally or derived
from a known config location, but must never include a parser-formatted error,
source snippet, mapping key, scalar, document body, credential, or YAML-derived
path. Apply this rule to returned errors and diagnostic/log attributes.

Preserve the current strict-versus-lenient contracts and verify them with a
reader-by-reader fixture matrix during migration. The matrix includes implicit and
quoted scalar forms, nulls, numeric and timestamp forms, explicit tags, merge
keys, anchors, aliases, and aliases through merges. Strict readers continue to
reject unknown fields, wrong shapes, duplicate/multiple documents, anchors, and
aliases where their existing contract requires it. Lenient settings readers remain
lenient except for their targeted named rejections. Fail-safe readers retain their
current skip/fallback behavior without promoting parser detail into warnings.

Keep [ADR 0225](./0225-operator-settings-validation.md)'s config-validate boundary:
use a final-component no-follow, nonblocking open; bounded-read only a regular
file; validate without writing or printing configuration values; and permit a
missing base only when an explicit learning patch preflights a prospective file.
Preserve authfile's distinction between a whole-document schema/decode rejection
(which rejects the snapshot) and entry-local invalid OAuth/API material (which
removes only the bad material while valid siblings remain). Preserve ToolHive's
forward-compatible unknown-field decoding and its hardcoded loopback request base
URL; malformed ToolHive input remains a fail-soft detection miss. Preserve the
mecatui learning editor's documented unrelated values, comments, mapping order,
and supported styles; any node-format normalization goccy cannot preserve must be
an explicit fixture expectation, never an untested side effect.

Replace the temporary yaml.v3 parser-string/indentation heuristic from PR #836;
do not port, emulate, or retain it. Use goccy's typed/token location data instead.
The engine remains independently buildable with its deliberately small dependency
closure, now containing goccy rather than yaml.v3.

## Consequences

The root and engine modules gain a new direct dependency and must update their
module files, sums, dependency allowlists, and the living dependency-closure
documentation together. YAML node/document-editor code must be adapted and tested
for preservation rather than assumed source-compatible.

Diagnostics gain reliable line/column information for parser failures without
leaking YAML content. They will no longer inherit incidental wording from yaml.v3;
callers and tests must assert the stable structured contract rather than a parser
string. The source matrix makes the CLI, user-global, shared-project, and
local-project error/log surface part of the security contract, including existing
tier, trust-gating, and operator-only rules.

The migration deliberately preserves behavior rather than widening YAML syntax or
redesigning configuration schemas. The semantic matrix and editor preservation
fixtures add migration cost, but prevent an accidental parser-version policy
change. A later parser upgrade, alternate parser, or syntax-policy expansion is a
separate decision.

## See also

- [ADR 0036](./0036-engine-module.md) — engine module and standalone closure.
- [ADR 0225](./0225-operator-settings-validation.md) — safe operator-settings validation.
- [Acceptance plan: goccy YAML migration](../acceptance/goccy-yaml-migration.md).
- `internal/adapter/daemonconfig/daemonconfig.go` (`parse`)
- `internal/adapter/permconfig/permconfig.go` (`parseYAML`)
- `cmd/mecated/configvalidate.go` (`parseSettingsDocument`)
- `cmd/mecatui/learning_settings.go`
- `engine/adapter/agentfs/discover.go` (`parseAgentDef`)
- `engine/adapter/skillfs/discover.go` (`ParseSkill`)
- `engine/adapter/rulesfs/discover.go` (`parseRule`)
