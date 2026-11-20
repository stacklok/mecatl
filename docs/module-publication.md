# Publish selected Go modules

Use this procedure to release independently versioned Go modules after their source
changes reach `main`. For source development and metadata repair, use
[Develop changes across Go modules](module-development.md).

Module publication uses the bounded workflow below, which creates lightweight tags
such as `engine/vX.Y.Z`. Root application releases use a separate procedure and
annotated `vX.Y.Z` tags. Create module tags through the workflow, not by hand.

## Select the release

1. Follow each selected module's compatibility and changelog policy, including the
   [engine](../engine/COMPATIBILITY.md) or [adapter](../adapters/COMPATIBILITY.md)
   policy where applicable. Obtain approval for the exact module versions.
2. Choose the full SHA of a reviewed commit reachable from `main` as `source`.
   Supply a `versions` JSON object mapping module directories to exact versions,
   for example `{"engine":"v0.12.0","provider/openai":"v0.8.0"}`. Only listed
   modules are released; unselected dependencies keep their published pins.
   Include required prerequisite releases explicitly.
3. Confirm that [`publish-modules.yml`](../.github/workflows/publish-modules.yml)
   is present on `main`, the `release` environment has the release GitHub App
   configured, and repository settings allow merge commits. Dispatch with your
   normal workflow credentials; the workflow supplies the App credential only
   to isolated writer jobs.

## Preflight and publish

Dispatch a read-only preflight from `main`, substituting the approved inputs:

```sh
gh workflow run publish-modules.yml --ref main \
  -f source='<SOURCE_SHA>' \
  -f versions='<VERSIONS_JSON>' \
  -f preflight_only=true
```

Wait for preflight to validate the source, versions, and module inventory and pass
workspace integration. This does not prove that every selected module can publish:
standalone dependencies are checked immediately before each module's release.

After reviewing preflight, dispatch the same inputs with `preflight_only=false` to
publish. Monitor the run through the adoption PR:

- Each selected module is prepared in dependency order, updating sibling pins to
  versions already published earlier in the batch. Preparation tidies metadata,
  builds, and tests with `GOWORK=off`. Missing prerequisites, new direct sibling
  requirements, or changes to unselected sibling pins stop the chain.
- Writers create metadata-only release commits on a temporary branch and tag each
  module. Modules can tag different commits; a module needing no metadata change
  tags its incoming parent. Implementation source stays at the reviewed revision.
- Public proof downloads each exact tagged version through the public proxy and
  checksum database and compiles a minimal import before the next module proceeds.
- After every public proof passes, reconciliation updates selected version pins
  in the remaining modules, preserves development replacements, and opens one
  metadata-only adoption PR. Those remaining modules receive no release tags.

Preparation and reconciliation can add indirect requirements, remove obsolete
requirements, and update Go/toolchain directives. Review those changes in adoption.

## Review and adopt the releases

Review the adoption PR's metadata diff and normal checks. Record the exact module
versions and distinguish workspace, standalone, and public-download results.

**A human must merge the adoption PR with a merge commit.** Squash and rebase merges
lose the tagged release commits from `main`'s ancestry. The workflow opens the PR
but never merges it. The prospective-merge check validates the proposed ancestry;
it cannot enforce the human's chosen merge method.

Publication is complete when every selected module has passed public proof and the
adoption merge has preserved its tagged commit in `main`'s ancestry. The post-merge
ancestry check detects a merge that lost those commits.

## Recover from a partial publication

If preparation, writing, or public proof fails, the remaining chain stops and no
adoption PR is created. Existing tags and metadata commits remain valid. Inspect
the workflow logs and release branch, then obtain explicit approval for a recovery
action. Preserve published tags; do not move or recreate them, or assume a rerun
will resume a partial publication.

## Maintain the publication workflow

When adding or reordering modules, follow the registration checklist in
[`publish-modules.yml`](../.github/workflows/publish-modules.yml). Job ownership,
metadata-artifact boundaries, and credential handling live there and in
[`publish-module.yml`](../.github/workflows/publish-module.yml).
