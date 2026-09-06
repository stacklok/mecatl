## Summary

<!-- REQUIRED: why this is needed and what changed. -->

-

## Development stage

<!-- REQUIRED. Check exactly one. -->

- [ ] **Plan / Interface** — behavioral and exact-interface contract; no implementation
- [ ] **Implementation** — based on an approved, merged Plan / Interface PR
- [ ] **Combined** — compact one-task exception; no separate plan PR, and the in-PR plan
      declares `**Expected tasks:** 1`, a non-placeholder `**Combined rationale:**`, and no
      runtime/public/operator/persistence/trust-boundary interface change (`None — rationale`;
      workflow-only meta-changes may review process docs/skills here)
- [ ] **Trivial / mechanical** — acceptance-plan spine exempt

### Contract linkage

<!--
Plan / Interface: link the acceptance plan and write the non-closing issue reference.
Implementation: link the Plan / Interface PR and full approved commit.
Combined: link the in-PR plan, confirm there was no separate plan PR, and quote its narrow
one-task eligibility rationale.
Trivial/mechanical: write N/A with rationale.
-->

- Acceptance plan:
- Plan / Interface PR:
- Approved commit baseline:
- Combined/exemption rationale:

### Interface conformance

<!--
Implementation: state "Matches approved contract" or list deviations and link the
human-approved amendment PR + full merged commit. Material drift must not be approved in
this PR; orchestration cannot author the amendment, which requires a separately authorized
Split Plan / Interface PR.
Plan / Interface and Combined: summarize the exact interface contract under review.
-->

-

## Issue relationship

<!--
GitHub has no native "Related-to" keyword.
Plan / Interface PRs: use ordinary non-closing text: "Relates to #N" or "Tracking: #N".
Implementation/Combined PRs: use "Closes #N" or "Fixes #N" ONLY when this PR fully
completes the issue; otherwise use a non-closing reference. Never put closing keywords
in commit messages, and never make the Plan / Interface PR the sidebar closing PR.
-->

Relates to #

## Type of change

<!-- REQUIRED. Check all that apply. -->

- [ ] Behavioral/interface plan
- [ ] Bug fix
- [ ] New feature
- [ ] Refactoring (no behavior change)
- [ ] Dependency update
- [ ] Documentation/process
- [ ] Other (describe):

## Test plan

### Baseline checks

<!-- Check applicable commands; explain intentionally skipped runtime gates. -->

- [ ] Acceptance-plan checker
- [ ] Linting (`task lint`)
- [ ] Offline test suite (`task test`)
- [ ] Offline demo (`go run ./cmd/mecademo`)
- [ ] Markdown changed: docs generation/link checks (`task docs`)
- [ ] User docs/user-facing behavior changed: site build (`task site:build`)
- [ ] Guarded engine API affected: compatibility check (`task api:check`)
- [ ] Intentional engine API change: `task api:update` + `engine/CHANGELOG.md`
- [ ] Landed plan: strict acceptance trace (`task ac-trace-strict`)
- [ ] Final implementation review: `/panel-review`

## Changes

<!-- Optional navigation table for larger diffs. -->

| File | Change |
|---|---|
| | |

## User-facing change

<!-- Describe from the user's perspective, or write "None". -->

## Special notes for reviewers

<!-- Call out risks, limitations, or areas needing scrutiny. -->
