---
id: 11-compatibility-security-matrix
title: Configuration-source compatibility and security matrix
blocked_by: [09-permconfig-compatibility-security-matrix, 10-lenient-readers]
status: done
branch: "plan-goccy-yaml-migration/11-compatibility-security-matrix"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/goccy-yaml-migration
---

# Task brief

Add the cross-source regression matrix after the migrated configuration readers
are available. Exercise equivalent YAML through explicit CLI files, user-global
XDG settings, shared-project settings, and local-project settings. Prove the
existing rule tier/order, project-trust admission, operator-only handling, and
safe error/diagnostic/log-attribute boundary remain intact.

The matrix is a security proof, not an opportunity to refactor resolver or parser
architecture. Explicit or known-location paths may be emitted as identifiers even
when credential-shaped; YAML-derived keys, values, snippets, and parser messages
never may. Reuse the task-01 typed location contract and cover returned errors as
well as structured diagnostics.

## Acceptance criteria

- AC1.5: the source matrix covers explicit CLI files, user-global XDG settings, shared-project settings, and local-project settings. Returned errors and diagnostics/log attributes remain value-free and preserve each source's tier, trust-gating, and operator-only handling. A path may be emitted only when it was supplied externally or derived from a known configuration location; a credential-shaped externally supplied pathname is still an allowed identifier, while YAML-derived names/values are never emitted.
  - verify: `TestGoccyYAMLMigration_Scenario1_SourceMatrixSafeErrorsDiagnosticsAndPaths`
- AC6.1: the explicit CLI, user-global XDG, shared-project, and local-project fixture matrix preserves rule tier/order, project allow trust-gating, and operator-only subtree ignore/warn behavior after migration.
  - verify: `TestGoccyYAMLMigration_Scenario6_SourceTierTrustAndOperatorOnlyMatrix`
- AC6.2: for every source in the matrix, returned errors and diagnostics/log attributes contain only safe harness context, token location, and a permitted source path; they contain no YAML-derived key, scalar, snippet, parser string, or credential.
  - verify: `TestGoccyYAMLMigration_Scenario6_SourceMatrixSafeErrorsAndLogAttributes`
- AC6.3: a credential-shaped explicit pathname and an equivalent known-location-derived pathname may appear as identifiers in errors/diagnostics, while the same credential-shaped text in YAML content cannot appear there.
  - verify: `TestGoccyYAMLMigration_Scenario6_CredentialShapedPathnameIsPermittedButContentIsNot`

## Worker notes

Keep this focused on fixtures and integration assertions. Do not change parser
behavior, trust policy, source precedence, or logging architecture. Run the matrix
and relevant permission resolver tests.
