# goccy/go-yaml migration — acceptance plan

**Phase:** dependency and parser migration
**Status:** draft
**ADR:** [ADR 0246](../adr/0246-goccy-yaml-parser.md) — migrate direct YAML parsing to goccy/go-yaml while preserving parser contracts and safe diagnostics.
**Branch:** `feat/goccy-yaml-migration`.

## Outcome

Replace every **direct** root- and engine-module use of `go.yaml.in/yaml/v3` with
`github.com/goccy/go-yaml`. Preserve the existing YAML contracts: strict readers
still fail closed, the intentionally lenient settings reader stays intentionally
lenient, fail-safe readers keep their documented skip/fallback behavior, and the
accepted/rejected YAML scalar, null, numeric, timestamp, tag, merge-key, and
alias forms are captured during this migration rather than deferred as a future
syntax policy.

A parser failure is untrusted input. Its externally visible diagnostic may contain
only harness-authored context plus the goccy token line and column. It must not
contain the parser's rendered error, a source snippet, mapping key, scalar,
credential, or YAML body. This replaces—not reproduces—the temporary yaml.v3
parser-string/indentation heuristic from PR #836.

The root module and `engine/` module migrate together. The engine remains a
standalone module with a small closure, as required by [ADR 0036](../adr/0036-engine-module.md).

## Scope

### In scope

- Every direct root- and engine-module import, module requirement/sum entry, and
  dependency allowlist entry for yaml.v3, replaced with goccy/go-yaml.
- The parser adapters and command/editor paths, including
  `internal/adapter/permconfig/permconfig.go`,
  `internal/adapter/authfile/authfile.go`,
  `internal/adapter/daemonconfig/daemonconfig.go`,
  `internal/adapter/toolhivellm/detect.go`,
  `internal/adapter/workspacetrust/registry.go`,
  `cmd/mecated/configvalidate.go`, `cmd/mecatui/learning_settings.go`,
  `cmd/mecatui/state.go`, `cmd/mecatui/keymap_wiring_yaml.go`, and the engine
  frontmatter adapters under `engine/adapter/{agentfs,skillfs,rulesfs}/`.
- A small, local safe-diagnostic boundary that consumes goccy token location
  metadata; it must not forward parser strings or create a second generic parser
  abstraction.
- Contract tests for diagnostics, parser semantics, document editing, frontmatter,
  lenient readers, and module/lint gates.
- Living-document updates that replace yaml.v3 with goccy in the engine closure
  and adapter dependency descriptions once code lands.

### Out of scope

- YAML schema changes, new configuration keys, or new accepted syntax beyond the
  migration's captured current accepted/rejected behavior.
- Moving YAML parsing into `engine/governance`, `engine/port`, `engine/agent`, or
  a repository-wide parser framework.
- Changing the behavior of indirect dependencies that happen to use YAML.
- Reformatting configuration files generally; only the existing learning-settings
  document editor may rewrite the document as it already does.
- Parser migration implementation, dependency edits, generated-file changes, or a
  commit in this drafting change.

## Scenarios

### Scenario 1 — parser failures are structured, located, and value-free

Every migrated parser classifies malformed YAML using goccy token line/column
metadata. The diagnostic reports only a stable operation/category and bounded
location; it never includes goccy's formatted parser message or text derived from
the YAML document. Safe harness-authored schema guidance remains allowed. This is
the replacement for PR #836's yaml.v3 parser-string/indentation heuristic, not a
port of it.

**Acceptance:**

- AC1.1: malformed YAML reported by strict root readers includes the goccy token line and column when available, and contains neither the offending line nor any attacker-controlled key or scalar.
  - verify: `TestGoccyYAMLMigration_Scenario1_StrictDiagnosticsUseTokenLocationWithoutSource`
- AC1.2: malformed YAML reported by lenient/fail-safe root readers is value-free even when the input contains credential-shaped values, quotes, comments, indentation traps, or parser-looking text.
  - verify: `TestGoccyYAMLMigration_Scenario1_FailSafeDiagnosticsNeverEchoYAML`
- AC1.3: the PR #836 parser-string/indentation heuristic is absent after migration; the safe diagnostic boundary uses goccy typed/token data and no production parser path inspects `error.Error()` text or source snippets.
  - verify: inspection — `TestGoccyYAMLMigration_Scenario1_NoParserStringOrIndentationHeuristic` statically inspects the migrated diagnostic boundary because deletion and non-use are implementation properties, not externally observable behavior.
- AC1.4: absent token location has a stable value-free diagnostic without invented coordinates; no error path panics or falls back to a parser string.
  - verify: `TestGoccyYAMLMigration_Scenario1_MissingTokenLocationFailsSafe`
- AC1.5: the source matrix covers explicit CLI files, user-global XDG settings, shared-project settings, and local-project settings. Returned errors and diagnostics/log attributes remain value-free and preserve each source's tier, trust-gating, and operator-only handling. A path may be emitted only when it was supplied externally or derived from a known configuration location; a credential-shaped externally supplied pathname is still an allowed identifier, while YAML-derived names/values are never emitted.
  - verify: `TestGoccyYAMLMigration_Scenario1_SourceMatrixSafeErrorsDiagnosticsAndPaths`

---

### Scenario 2 — strict configuration and YAML semantics remain compatible

The migration records a reader-by-reader fixture matrix before changing parser
behavior, then proves goccy accepts and rejects the same inputs. The matrix covers
plain and quoted implicit scalar forms, null/empty values, decimal/hex/octal-like
numeric forms, timestamp-like forms, explicit tags, merge keys, anchors, aliases,
and aliases hidden through merges. It identifies the reader contract rather than
assuming all readers share one YAML policy.

Strict documents keep their existing single-document, schema, duplicate-key, and
anchor/alias boundaries. This covers daemon configuration, credentials, and the
operator validation path, while `permconfig` retains its intentional top-level
leniency plus targeted removed-key rejection. The relevant existing seams are
`internal/adapter/daemonconfig/daemonconfig.go` (`parse`),
`internal/adapter/authfile/authfile.go` (`Load`),
`internal/adapter/permconfig/permconfig.go` (`parseYAML`), and
`cmd/mecated/configvalidate.go` (`validateSettingsInMemory`).

**Acceptance:**

- AC2.1: the committed semantic fixture matrix records and preserves each reader's accepted/rejected behavior for implicit and quoted bool/string forms, null/empty values, numeric forms, and timestamp forms; a semantic drift names the reader and fixture case.
  - verify: reader-owned `TestGoccyYAMLMigration_SemanticMatrixAuthfile`, `TestGoccyYAMLMigration_SemanticMatrixDaemonConfig`, and `TestGoccyYAMLMigration_SemanticMatrixPermconfig`, plus `TestGoccyYAMLMigration_SemanticMatrixConfigValidate`; each loads only its assigned fixtures and invokes its real reader.
- AC2.2: the matrix records and preserves each reader's handling of explicit tags, merge keys, anchors, aliases, and aliases reached through merges; readers that currently prohibit aliases or ambiguous mapping inputs continue to reject them.
  - verify: the same reader-owned tests above; frontmatter fixtures are separately invoked by `TestGoccyYAMLMigration_SemanticMatrixAgentFrontmatter`, `TestGoccyYAMLMigration_SemanticMatrixSkillFrontmatter`, and `TestGoccyYAMLMigration_SemanticMatrixRuleFrontmatter`.
- AC2.3: daemon configuration rejects unknown fields, wrong types, malformed syntax, and a second document; its diagnostic preserves the existing distinction between schema and syntax without exposing YAML content.
  - verify: `TestGoccyYAMLMigration_Scenario2_DaemonConfigStrictContract`
- AC2.4: a whole-file auth.yaml schema/decode failure rejects the complete credential snapshot with a value-free warning, while entry-local invalid OAuth/API material found during provider validation drops only that entry/material and retains valid sibling entries.
  - verify: `TestGoccyYAMLMigration_Scenario2_AuthFileWholeFileVsEntryLocalFailure`
- AC2.5: `mecated config validate` retains ADR-0225's read-only safety boundary: it opens only a final-component no-follow, nonblocking regular file, bounded-reads it, never writes either input or prints configuration values, and permits a missing base only when an explicit learning patch preflights a prospective new file.
  - verify: `TestGoccyYAMLMigration_Scenario2_ConfigValidateADR0225Safety`
- AC2.6: config validation still rejects aliases, anchors, duplicate mapping keys, non-mapping roots, multiple documents, and invalid learning-only patches before it claims a document is valid.
  - verify: `TestGoccyYAMLMigration_Scenario2_ConfigValidationSafeDocumentContract`
- AC2.7: permissions/settings decoding remains lenient at the top level, preserves the targeted `output-economy` rejection, and keeps nested strict section validation and lost-rule counting behavior.
  - verify: `TestGoccyYAMLMigration_Scenario2_PermconfigStrictLenientBoundary`

---

### Scenario 3 — engine frontmatter retains its tolerant contract

The engine's agent, skill, and rule filesystem adapters remain standalone and
preserve their compatible frontmatter forms: unknown fields remain forward
compatible where documented, scalar-or-sequence coercions continue to normalize,
and malformed frontmatter fails or skips exactly as each adapter currently
requires. The implementation stays in `engine/adapter/agentfs/discover.go`,
`engine/adapter/skillfs/discover.go`, and
`engine/adapter/rulesfs/discover.go`; no YAML dependency enters an engine domain
or port package.

**Acceptance:**

- AC3.1: agent frontmatter continues to accept documented scalar/list and inline-MCP forms, ignores unknown fields, and reports malformed frontmatter without parser-derived source text.
  - verify: `TestGoccyYAMLMigration_Scenario3_AgentFrontmatterCompatibility`
- AC3.2: skill frontmatter preserves string-or-list `allowed-tools`, metadata normalization, unknown-field tolerance, and malformed-frontmatter rejection/skip behavior.
  - verify: `TestGoccyYAMLMigration_Scenario3_SkillFrontmatterCompatibility`
- AC3.3: rule frontmatter preserves scalar-or-list `paths`, unknown-field tolerance, and existing per-file failure isolation.
  - verify: `TestGoccyYAMLMigration_Scenario3_RuleFrontmatterCompatibility`
- AC3.4: the three adapters continue to satisfy their source conformance coverage after the parser replacement.
  - verify: `task test` — runs the engine adapter/source conformance suites; the scenario-specific tests above pin parser behavior.

---

### Scenario 4 — settings document editing preserves unaffected content

The mecated validation patch and mecatui learning editor continue to parse one
safe top-level document, mutate only the intended `learning` node, and reject
ambiguous documents before write. The migration fixtures are named
`settings-preserve-top-level.yaml` and `settings-preserve-learning.yaml`: they
contain an unrelated `posture: trusted` scalar, `permissions.deny: [Write]`, a
comment before an unrelated top-level mapping, a comment before
`learning.skills.activation`, explicit flow/block style examples, and deliberately
ordered sibling mappings. After a learning mode or sensitivity edit, unrelated
values, comments, sibling order, and styles represented by goccy's node API must
survive; only the edited learning scalar and formatting directly required by
rewriting that scalar may change. Any unsupported style normalization must be
listed in the fixture's expected output, not silently accepted. The migration must
not weaken symlink/lock/atomic-write safeguards. Relevant seams are
`cmd/mecated/configvalidate.go` (`replaceMappingValue`) and
`cmd/mecatui/learning_settings.go` (`operatorLearningSettings.withLockedDocument`).

**Acceptance:**

- AC4.1: a `--learning-patch` replaces or inserts only the top-level learning mapping in memory, validates the complete proposal, never writes or displays the proposed document, and preserves the unrelated semantic content specified by `settings-preserve-top-level.yaml`.
  - verify: `TestGoccyYAMLMigration_Scenario4_LearningPatchPreservesUnrelatedDocument`
- AC4.2: mecatui's learning mode and sensitivity edits match the expected outputs for `settings-preserve-top-level.yaml` and `settings-preserve-learning.yaml`: unrelated values, comments, order, and supported styles survive; every permitted normalization is asserted explicitly.
  - verify: `TestGoccyYAMLMigration_Scenario4_LearningEditorPreservationFixtures`
- AC4.3: the settings editor retains one-document/duplicate/alias protections, does not write on parse or validation failure, and keeps its existing symlink rejection, cross-process lock, and atomic-write behavior; parser migration does not widen its write authority.
  - verify: `TestGoccyYAMLMigration_Scenario4_LearningEditorWriteSafetyUnchanged`

---

### Scenario 5 — lenient and fail-safe readers keep their availability posture

Readers intentionally designed to skip bad optional input remain safe and
available: ToolHive detection, workspace-trust state, mecatui state/keymaps, and
permission-config reloads retain their existing bounded parsing, warning, and
fallback contracts. A parser change must not turn optional malformed input into a
crash, accidentally accept a strict input, or leak values through a warning.

**Acceptance:**

- AC5.1: ToolHive detection keeps its forward-compatible unknown-field behavior and malformed/no-config/untrusted fallback, while `Config.BaseURL` remains `http://127.0.0.1:<port>/v1` and never derives a request host from `gateway_url` or another YAML value; diagnostics expose no parser-derived content.
  - verify: `TestGoccyYAMLMigration_Scenario5_ToolHiveUnknownFieldsFallbackAndLoopbackBaseURL`
- AC5.2: malformed mecatui state and keymap YAML preserves the existing local fallback/error behavior without breaking startup or exposing YAML content.
  - verify: `TestGoccyYAMLMigration_Scenario5_MecatuiReadersRetainFallbacks`
- AC5.3: a malformed permission config still follows its documented bounded skip/report path, including strict nested sections and lost-rule counts, rather than silently applying a partial policy.
  - verify: `TestGoccyYAMLMigration_Scenario5_PermissionReloadFailsSafeWithoutPartialPolicy`
- AC5.4: malformed workspace-trust YAML retains the documented untrusted fallback and emits no parser-derived content.
  - verify: `TestGoccyYAMLMigration_Scenario5_WorkspaceTrustFailsSafeAndValueFree`

---

### Scenario 6 — configuration sources preserve trust and diagnostic boundaries

The same bytes reach parsers from distinct authority sources: an explicit CLI
`--permission-config`, user-global XDG settings, shared-project settings, and
local-project settings. Migration must preserve their configured tier, project
trust admission, and operator-only subtree handling. Error returns and diagnostics
(including structured log attributes) are a separate output surface and must remain
value-free. An externally supplied path or a path derived from a known configuration
location is an allowed identifier—even when the path text looks credential-shaped;
a mapping key, scalar, snippet, or parser string recovered from YAML is not.

**Acceptance:**

- AC6.1: the explicit CLI, user-global XDG, shared-project, and local-project fixture matrix preserves rule tier/order, project allow trust-gating, and operator-only subtree ignore/warn behavior after migration.
  - verify: `TestGoccyYAMLMigration_Scenario6_SourceTierTrustAndOperatorOnlyMatrix`
- AC6.2: for every source in the matrix, returned errors and diagnostics/log attributes contain only safe harness context, token location, and a permitted source path; they contain no YAML-derived key, scalar, snippet, parser string, or credential.
  - verify: `TestGoccyYAMLMigration_Scenario6_SourceMatrixSafeErrorsAndLogAttributes`
- AC6.3: a credential-shaped explicit pathname and an equivalent known-location-derived pathname may appear as identifiers in errors/diagnostics, while the same credential-shaped text in YAML content cannot appear there.
  - verify: `TestGoccyYAMLMigration_Scenario6_CredentialShapedPathnameIsPermittedButContentIsNot`

---

### Scenario 7 — both module boundaries and repository gates prove the replacement

The direct dependency replacement is complete only when neither root nor engine
retains yaml.v3 as a direct dependency or direct production import, and all
existing layering and standalone proofs remain green. Goccy is admitted only in
the adapter/command packages that already parse YAML. The root and engine module
files, `.golangci.yml` allowlists, and the living dependency documentation move in
one change.

**Acceptance:**

- AC7.1: root and engine direct production imports use only `github.com/goccy/go-yaml` for YAML parsing; yaml.v2, yaml.v3, `sigs.k8s.io/yaml`, and any other YAML parser are denied by a repository lint rule. Transitive dependencies are out of scope.
  - verify: `TestGoccyYAMLMigration_Scenario7_OnlyGoccyYAMLParserIsDirectlyImported`
- AC7.2: goccy is allowed only where the current parser dependency belongs, and engine domain/port/agent layering remains unchanged.
  - verify: inspection — `task lint` runs depguard, vet, and the engine layering checks.
- AC7.3: the engine independently resolves, builds, and tests with goccy in its small standalone closure and no root-module dependency.
  - verify: inspection — `task test:engine-standalone` is the standalone module proof.
- AC7.4: root and engine tests, API compatibility, and the offline demo remain green after module tidy/sync.
  - verify: inspection — `task test`, `task api:check`, and `go run ./cmd/mecademo` cover the repository gates and offline demo.

## Deferred decisions

- Whether goccy's formatter should become a general settings-file preservation
  guarantee, beyond the existing learning-editor behavior.
- Any YAML 1.2 compatibility expansion beyond the migration's captured current
  acceptance/rejection matrix.
- A reusable parser-error package or cross-repository diagnostic convention.
- Upgrading goccy after this migration; version changes require their own
  compatibility assessment and test pass.
- Removing yaml.v3 if it remains transitively required by an unrelated dependency;
  this plan prohibits only direct root/engine production use and direct module
  requirements.

## Documentation

When implementation lands, update the living dependency references in
`AGENTS.md`, `docs/architecture.md`, `docs/architecture/extensibility.md`, and
`docs/design/IMPLEMENTATION-NOTES.md` from yaml.v3 to goccy where they describe
current behavior. Update parser-specific comments that name yaml.v3 or its error
format, plus `.golangci.yml` allowlists. Keep this plan and ADR as the decision
record; do not edit the accepted ADR except for a later supersession pointer.

No public flags, YAML schema, or deployment steps change, so no user-docs change
is expected unless implementation reveals an observable compatibility difference.

## Definition of done

- All seven scenarios are implemented with every named proof present and green.
- The semantic fixture matrix proves current accepted/rejected scalar, null, numeric,
  timestamp, tag, merge-key, anchor, and alias behavior; no such policy is deferred.
- Root and engine direct YAML parsing, module requirements/sums, and allowlists use
goccy; no yaml.v3 parser-string/indentation heuristic remains.
- Parser diagnostics are token-located where available and value-free on every
migration path, including credential-shaped fixtures.
- Strict, lenient, editor, and fail-safe contracts are proven by their scenario
tests.
- `task lint`, `task test`, `task test:engine-standalone`, `task api:check`,
  `go run ./cmd/mecademo`, `task ac-trace-strict`, and `task docs` pass.
- The documentation updates above and regenerated `llms.txt` are included in the
implementation change.
