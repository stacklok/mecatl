## Summary

See the [contribution guide](https://github.com/stacklok/mecatl/blob/main/CONTRIBUTING.md) for contribution and verification expectations.

<!--
REQUIRED. Explain:
1. WHY this change is needed (the problem or motivation)
2. WHAT changed (concise bullet points)

The diff shows the code — your summary must give reviewers the context needed
to understand the change without reading the diff first.
-->

-

<!--
Link related issues. Use "Closes" or "Fixes" to auto-close on merge.
Remove this line if there is no related issue.
-->

Fixes #

## Type of change

<!-- REQUIRED. Check exactly one. -->

- [ ] Bug fix
- [ ] New feature
- [ ] Refactoring (no behavior change)
- [ ] Dependency update
- [ ] Documentation
- [ ] Other (describe):

## Test plan

<!--
Baseline verification is REQUIRED for every repository change. Check all three
baseline items after running them. The remaining checks are conditional; check
each one that applies and that you ran. Describe any manual testing below.
-->

### Required baseline

- [ ] Linting (`task lint`)
- [ ] Offline test suite (`task test`)
- [ ] Offline demo (`go run ./cmd/mecademo`)

### Conditional checks

- [ ] Markdown changed: documentation generation and checks (`task docs`)
- [ ] User docs or user-facing behavior changed: user-docs site build (`task site:build`)
- [ ] Guarded engine API surface affected: compatibility check (`task api:check`)
- [ ] Intentional guarded engine API change: baselines regenerated (`task api:update`) and `engine/CHANGELOG.md` updated
- [ ] Manual testing (describe below)

## Changes

<!--
Optional — include for PRs touching more than a few files to help reviewers
navigate the diff. Remove this entire section for small PRs.
-->

| File | Change |
|------|--------|
|      |        |

## Does this introduce a user-facing change?

<!--
If yes, describe the change from the user's perspective. This helps with release notes.
If no, write "No". Remove this section entirely if not applicable.
-->

## Implementation plan

<!--
Optional — include when this PR was planned with an AI assistant or has an
approved implementation plan. Paste it inside the details block. Remove this
section entirely when it is not useful.
-->

<details>
<summary>Approved implementation plan</summary>

<!-- Paste the plan here -->

</details>

## Special notes for reviewers

<!--
Optional — call out non-obvious logic, known limitations, areas needing extra
scrutiny, or follow-up work. Remove this section if not needed.
-->
