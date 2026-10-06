## Summary

<!-- REQUIRED: why this is needed and what changed. -->

-

## Issue relationship

<!--
Use "Closes #N" or "Fixes #N" only when this PR fully completes the issue; otherwise
use a non-closing reference such as "Relates to #N". Never put closing keywords in
commit messages.
-->

Relates to #

## Type of change

<!-- REQUIRED. Check all that apply. -->

- [ ] Bug fix
- [ ] New feature
- [ ] Refactoring (no behavior change)
- [ ] Dependency update
- [ ] Documentation
- [ ] Other (describe):

## Test plan

### Baseline checks

<!-- Check applicable commands; explain intentionally skipped runtime gates. -->

- [ ] Linting (`task lint`)
- [ ] Fast offline test suite (`task test`)
- [ ] Pre-review race suite (`task test:race`)
- [ ] Offline demo (`go run ./cmd/mecademo`)
- [ ] Markdown changed: docs generation/link checks (`task docs`)
- [ ] User docs/user-facing behavior changed: site build (`task site:build`)
- [ ] Guarded engine API affected: compatibility check (`task api:check`)
- [ ] Intentional engine API change: `task api:update` + `engine/CHANGELOG.md`

## Changes

<!-- Optional navigation table for larger diffs. -->

| File | Change |
|---|---|
| | |

## User-facing change

<!-- Describe from the user's perspective, or write "None". -->

## Special notes for reviewers

<!-- Call out risks, limitations, or areas needing scrutiny. -->
