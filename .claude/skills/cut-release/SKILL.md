---
name: cut-release
description: >-
  Cut a tagged Mecatl application release, or route an independently published Go module
  release to its bounded publication workflow and guide. For application releases, dispatch
  the Create Release PR workflow, review and merge the release PR, then verify its images,
  Helm charts, GitHub Release, signed archives, and Homebrew cask bump. Do not use for
  general git tagging unrelated to a Mecatl release.
metadata:
  author: stacklok
---

# Cut a mecatl release

For an application release, a release is a `vX.Y.Z` git tag. Pushing that tag triggers
`.github/workflows/release.yml`, which publishes signed images (`mecated`, `mecatui`,
`mecak8s`, execution provider/workload, Studio, and the Slack-bot example), Helm
charts, and versioned microVM and Brood Box artifacts. It also publishes a **GitHub
Release** carrying `darwin`/`linux` x
`amd64`/`arm64` CLI archives, a `checksums.txt`, cosign bundles, SBOMs, and build
provenance, and pushes a `mecatl` cask bump to the public `stacklok/homebrew-tap`
repository. There is **no** version baked into the Go code — the tag IS the release.

Two consequences of the Homebrew half, before you start:

- **A published tag is immutable in practice.** The cask records the release archives'
  SHA256 checksums for a specific tag. Deleting and re-pushing a `vX.Y.Z` that already produced
  a release and a tap commit leaves the tap pointing at checksums that no longer match, which
  breaks `brew install` for everyone. If a release goes wrong after the tap commit lands,
  **fix forward with the next patch version.** Re-tagging is only an option when the run failed
  before publishing anything.
- **Validate release-only inputs BEFORE tagging**, not by pushing a throwaway tag.
  The release-PR workflow checks execution-image digest pins before it opens the PR;
  CI checks the Brood Box build and microVM defaults handoff. A local
  `task release:snapshot && task release:verify` only checks CLI archives and
  Homebrew packaging; it does not prove the container publishing jobs work.

**Nothing pushes a commit to `main`.** The release runs through an ordinary pull request:
you dispatch a workflow, a bot opens the PR, a human merges it, and a bot tags the merge
commit. You never run `git push origin main`, and you never create the tag by hand.

`VERSION` (repo root, **bare** semver — `0.0.34`, not `v0.0.34`) is the source of truth
for the release version. The release PR propagates it to the `mecak8s` and
`mecatl-studio` chart versions and `appVersion`s, and to the `mecak8s` default image
tag. The workflows verify those values directly rather than maintaining a fixed list of
files that a release PR may change.

It did not used to be. `mecatequi-reusable.yml` referenced its three sibling composite actions
by a hardcoded `@vX.Y.Z` literal, so every release had to bump those pins in the same tagged
commit or ship version skew. That self-reference — a file naming a tag that does not exist yet —
is why a release needed a commit on `main` at all. The pins are now `$/` self-repository refs,
which resolve to this repo at the exact ref the workflow is running from, so there is nothing
left to bump and skew is impossible rather than merely policed.

## Steps

Run from the repo root.

1. **Confirm what you're shipping.** The release tags whatever is on `main` when the release PR
   merges. Review what has landed since the last tag:
   ```sh
   git tag --sort=-v:refname --list 'v*' | head -1   # e.g. v0.0.33
   git log <last-tag>..origin/main --oneline
   ```
   Confirm the bump type with the operator before dispatch; `patch` is the established
   release cadence even when the range includes additive changes. Do not infer a
   `minor` bump from commit subjects alone.

2. **Dispatch the release-PR workflow.** This is the only step that starts a release:
   ```sh
   gh workflow run create-release-pr.yml -f bump_type=patch
   gh run watch "$(gh run list --workflow=create-release-pr.yml --limit 1 --json databaseId --jq '.[0].databaseId')"
   ```
   The workflow validates the tracked `ARG` defaults in
   `build/execution-provider/Dockerfile` and `build/execution-workload/Dockerfile`
   on `main`: the Go builder, static provider runtime, and Brood Box workload
   runtime must have tagged digest pins available for Linux amd64 and arm64.
   It does not build the images: `execution-images.yml` builds both for amd64
   and arm64, without publishing, on every push to `main`. The tag workflow repeats the
   pin and availability validation on the merged commit before tagging. Renovate
   updates the pins through its native Dockerfile manager; no image repository
   variables are required.
   If validation fails, fix the tracked pins through review (or retry a transient
   registry failure); do not bypass the check. The publishing job validates the
   tagged checkout again. The workflow bumps `VERSION`, the `mecak8s` and
   `mecatl-studio` chart versions and app versions, and the `mecak8s` chart's default
   image tag. It opens `Release vX.Y.Z` from branch `release/vX.Y.Z`, then asserts that
   the required values are synchronized. **If that verification step fails, do not merge the PR**;
   close it, delete the branch, and read the job log.

3. **Review the release PR like any other PR** and confirm that all changes belong to the
   release update:
   ```sh
   gh pr list --head "release/vX.Y.Z" --json number,url,files
   gh pr diff <number>
   ```
   Wait for CI to go green. The PR is opened by the release GitHub App, so it triggers checks
   normally.

4. **Squash-merge it.** A human does this — it is the approval gate, and it is the only way
   `VERSION` changes on `main`:
   ```sh
   gh pr merge <number> --squash
   ```
   The tagging workflow does not read the commit subject — it asks GitHub which PR produced
   the commit and requires a merged, bot-opened PR from branch `release/vX.Y.Z` with matching
   version values. The squash title and the number of updated files do not affect the tag gate.

5. **Watch the tag get created.** Merging fires `create-release-tag.yml`, which re-verifies the
   commit and pushes the annotated tag. That push fires `release.yml` on its own — the tag is
   pushed by a GitHub App installation token precisely so the cascade happens, where a
   `GITHUB_TOKEN`-pushed tag would trigger nothing:
   ```sh
   gh run watch "$(gh run list --workflow=create-release-tag.yml --limit 1 --json databaseId --jq '.[0].databaseId')"
   git fetch --tags && git tag --sort=-v:refname --list 'v*' | head -1
   ```

6. **Confirm the release run started, then wait for every publishing job.** A
   successful CLI archive or one green image is not a complete release. In
   particular, check the `mecated`, execution provider/workload, Brood Box
   resolution, microVM, mecak8s/execution/Studio chart, and CLI/Homebrew jobs
   before reporting success:
   ```sh
   gh run list --workflow=release.yml --limit 3
   gh run watch "$(gh run list --workflow=release.yml --limit 1 --json databaseId --jq '.[0].databaseId')"
   ```
   The Studio chart job skips with a notice when the tag has no chart, so confirm the
   charts actually landed in GHCR (bare `X.Y.Z`, no `v`):
   ```sh
   helm show chart oci://ghcr.io/stacklok/mecatl/charts/mecak8s --version X.Y.Z
   helm show chart oci://ghcr.io/stacklok/mecatl/charts/mecatl-studio --version X.Y.Z
   ```

7. **Verify the GitHub Release carries every artifact.** It must not be a draft, and it must
   have four archives plus a checksum file, with a cosign bundle and an SBOM alongside each.
   Also require `brood-base-index.json`, `brood-platforms.json`, and the
   `microvm-default-*.json` completion assets for each platform. Missing assets
   mean the release is incomplete even if the CLI archives are available:
   ```sh
   gh release view vX.Y.Z --json isDraft,assets --jq '{draft: .isDraft, assets: [.assets[].name]}'
   ```
   Then prove one archive is actually usable rather than trusting the asset list. Use the
   repo-local `.scratch/` dir, never `/tmp` (AGENTS.md):
   ```sh
   mkdir -p .scratch/release-vX.Y.Z
   gh release download vX.Y.Z -p 'checksums.txt' -p '*darwin_arm64*' -D .scratch/release-vX.Y.Z
   (cd .scratch/release-vX.Y.Z \
     && shasum -a 256 -c checksums.txt --ignore-missing \
     && tar -xzf mecatl_*_darwin_arm64.tar.gz \
     && ./mecatui --version && ./mecated --version)
   ```
   Both `--version` lines must print the tag you just cut. A `dev+<revision>` output means the
   release build lost its `BUILD_ID` linker stamp — a release bug, not a cosmetic one, because
   the public install docs claim a released binary reports its tag.

   **If the run died between "release created" and "assets uploaded"** it leaves a DRAFT, which
   a lookup by tag does not return, so a naive re-run fails trying to create the release again.
   Recover with `gh release delete vX.Y.Z --cleanup-tag=false --yes`, then re-dispatch.

8. **Verify the Homebrew tap got the cask bump:**
   ```sh
   gh api repos/stacklok/homebrew-tap/commits --jq '.[0].commit.message'
   gh api repos/stacklok/homebrew-tap/contents/Casks/mecatl.rb --jq '.content' \
     | base64 -d | grep -E 'version|url|sha256' | head
   ```
   The top commit must name the version you just cut, and the cask's `url` and `sha256`
   values must match the release assets from step 7.

   **While `stacklok/mecatl` is private, `brew install stacklok/tap/mecatl` fails** even after a
   correct tap commit: Homebrew's downloader does not authenticate, so it cannot fetch a release
   archive from a private repository. The tap commit landing is the whole verification until the
   repository goes public; this is known and accepted. Once it is public, run the real
   end-to-end check once:
   ```sh
   brew update && brew install stacklok/tap/mecatl && mecatui --version
   ```

## Publish an importable Go module

The root `vX.Y.Z` release above is the application release. It does not automatically
publish the independently versioned Go modules. Module tags use their module path,
such as `engine/vX.Y.Z` or `adapters/vX.Y.Z`, and are lightweight tags. Root application
tags remain annotated.

When a request concerns an independently published Go module, use the bounded workflow in
[the Go module publication procedure](../../../docs/module-publication.md).
Do not create an engine tag, or any other module tag, by hand. Do not add a module
release to the root application release flow.

Before dispatching, verify the release's compatibility policy and changelog. For
`engine`, retain the version and API preflight policy in `engine/COMPATIBILITY.md`:
update `engine/CHANGELOG.md`, run `task api:release-check` as the advisory SemVer
preview, and require the committed API snapshots checked by `task api:check`. Supply
a reviewed, `main`-reachable full commit SHA as `source` and choose every exact version
explicitly in the workflow's `versions` map; the workflow never infers versions or
releases additional modules.

The workflow must be present on `main`, and the repository needs a configured
`release` environment with the release App plus merge commits enabled before dispatch.
Those rollout prerequisites do not establish hosted verification. There is no evidence
here of a completed hosted module release or public-download proof.

A human alone merge-commits the final adoption PR. Agents never merge it, and squash
or rebase adoption breaks the required release ancestry. If a write or public proof
fails, preserve the existing tags and branch and recover manually through a new
explicitly approved action.

## Notes

- **Two publishing destinations, one tag.** A run can succeed on the GHCR images and still fail
  on the release or the tap (or vice versa). GoReleaser's cask pipe continues on error and the
  Release is created before the cask is pushed, so a bad tap token loses the cask but NOT
  the Release. Steps 7 and 8 are not optional: a green `gh run list` line is not proof that
  `brew install` works.
- **Never hand-edit `stacklok/homebrew-tap`.** The cask is generated from the tag by the
  release workflow and carries a `DO NOT EDIT` header. A manual edit is overwritten by the next
  release and desynchronizes the checksums in the meantime.
- **Never push to `main`, and never create a root `vX.Y.Z` tag by hand.** Both are the
  application workflows' job. A hand-pushed version bump skips code review, and a
  hand-created tag can point at a commit whose release metadata the gate rejects. If
  `VERSION` is edited on `main` outside a release PR, `create-release-tag.yml` refuses
  to tag it rather than cutting a release from it. `release.yml`'s `guard` job also
  refuses a root tag that is not an ancestor of `main`. Module tags follow the bounded
  module-publication workflow above.
- **Root tags are annotated.** `create-release-tag.yml` creates the root application
  tag with `git tag -a`; module publication creates lightweight module tags.
- **Don't bump illustrative documentation refs** unless asked — the `@vX.Y.Z` examples in
  `user-docs/building/deployment/mecatequi.md` are illustrative and do NOT gate the release. The
  release flow deliberately leaves them alone.
- **If the release run fails on the version gate**, the tagged commit has inconsistent release
  metadata. That should be impossible through the normal flow: `create-release-pr.yml` verifies
  the bump before the PR can merge, and `create-release-tag.yml` tags only the merge commit. It
  means someone tagged by hand or the chart metadata drifted from `VERSION`. Fix forward with a
  patch.
- **Rerunning is safe.** `create-release-tag.yml` makes one decision from the tag's state and
  the commit's provenance, so it is quiet when there is nothing to do (the tag already points
  here, or `VERSION` names an already-released tag this commit did not produce) and loud only
  when a tag should have been created and something is wrong. `release.yml` re-signs
  idempotently via its `workflow_dispatch` `tag` input.
- **Setup, once.** Both workflows read the release GitHub App from a `release` GitHub
  Environment (`vars.RELEASE_APP_CLIENT_ID`, `secrets.RELEASE_APP_PRIVATE_KEY`), whose
  deployment-branch policy must be restricted to `main`. The App needs exactly two repository
  permissions — Contents: write and Pull requests: write. It does NOT need Workflows: write,
  because a release no longer edits anything under `.github/workflows/`. Repo-level secrets would let anyone
  with push access dispatch a modified workflow from a branch and mint the App credential.
- **One release at a time.** If any `release/v*` PR is open, the next dispatch refuses and names
  it — merge or close it first. Once none is open, leftover `release/v*` branches from failed
  runs are deleted automatically before the new PR is cut.
- **The release gate checks synchronized values, not a fixed file list.** This lets the release
  automation add another version projection or generated file without requiring a second gate
  update. There is nothing to dry-run locally.
