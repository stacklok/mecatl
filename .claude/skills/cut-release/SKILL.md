---
name: cut-release
description: >-
  Cut a tagged release of mecatl — dispatch the Create Release PR workflow, review and
  merge the release PR, then verify the tag and the artifacts it publishes (ko images +
  Helm chart to GHCR, plus a GitHub Release with signed archives and a Homebrew formula
  bump). Use when asked to cut/ship/tag/publish a release or bump the version. NOT for
  general git tagging unrelated to a mecatl release.
metadata:
  author: stacklok
---

# Cut a mecatl release

A release is a `vX.Y.Z` git tag. Pushing that tag triggers `.github/workflows/release.yml`,
which does two things. It builds, signs, and attests the `mecated`, `mecatui`, `mecak8s`, and
slack-bot images plus the `mecak8s` Helm chart to GHCR (jobs `publish`, `publish-mecatui`,
`publish-mecak8s`, `publish-slack-bot`, `publish-helm-chart`). It also publishes a **GitHub
Release** carrying `darwin`/`linux` x `amd64`/`arm64` archives, a `checksums.txt`, cosign
bundles, SBOMs, and build provenance — and pushes a `mecatl` formula bump to the public
`stacklok/homebrew-tap` repository, which is what makes `brew install stacklok/tap/mecatl`
resolve (job `publish-cli`). There is **no** version baked into the Go code — the tag IS the
release.

Two consequences of the Homebrew half, before you start:

- **A published tag is immutable in practice.** The formula records the release archives'
  SHA256 checksums for a specific tag. Deleting and re-pushing a `vX.Y.Z` that already produced
  a release and a tap commit leaves the tap pointing at checksums that no longer match, which
  breaks `brew install` for everyone. If a release goes wrong after the tap commit lands,
  **fix forward with the next patch version.** Re-tagging is only an option when the run failed
  before publishing anything.
- **Verify a release build BEFORE tagging**, not by pushing a throwaway tag — a pushed tag is a
  public release and a tap commit. `task release:snapshot && task release:verify` builds the
  archives and the formula locally, with no tag, no upload and no tokens.

**Nothing pushes a commit to `main`.** The release runs through an ordinary pull request:
you dispatch a workflow, a bot opens the PR, a human merges it, and a bot tags the merge
commit. You never run `git push origin main`, and you never create the tag by hand.

`VERSION` (repo root, **bare** semver — `0.0.34`, not `v0.0.34`) is the source of truth
for the release version. The release PR propagates it to the `mecak8s` chart version,
`appVersion`, and default image tag. The workflows verify those values directly rather
than maintaining a fixed list of files that a release PR may change.

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
   Pick the bump type from that: `patch` for fixes, `minor` for additive behavior, `major` for
   a break. Releases so far have all been `patch`.

2. **Dispatch the release-PR workflow.** This is the only step that starts a release:
   ```sh
   gh workflow run create-release-pr.yml -f bump_type=patch
   gh run watch "$(gh run list --workflow=create-release-pr.yml --limit 1 --json databaseId --jq '.[0].databaseId')"
   ```
   It bumps `VERSION`, the `mecak8s` chart version and app version, and the chart's default
   image tag. It opens `Release vX.Y.Z` from branch `release/vX.Y.Z`, then asserts that the
   required values are synchronized. **If that verification step fails, do not merge the PR**;
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

6. **Confirm the release run started, then wait for it.** The archive/Homebrew job is the one
   that reaches outside this repository, so it is the one to watch:
   ```sh
   gh run list --workflow=release.yml --limit 3
   gh run watch "$(gh run list --workflow=release.yml --limit 1 --json databaseId --jq '.[0].databaseId')"
   ```

7. **Verify the GitHub Release carries every artifact.** It must not be a draft, and it must
   have four archives plus a checksum file, with a cosign bundle and an SBOM alongside each:
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

8. **Verify the Homebrew tap got the formula bump:**
   ```sh
   gh api repos/stacklok/homebrew-tap/commits --jq '.[0].commit.message'
   gh api repos/stacklok/homebrew-tap/contents/Formula/mecatl.rb --jq '.content' \
     | base64 -d | grep -E 'version|url|sha256' | head
   ```
   The top commit must name the version you just cut, and the formula's `url` and `sha256`
   values must match the release assets from step 7.

   **While `stacklok/mecatl` is private, `brew install stacklok/tap/mecatl` fails** even after a
   correct tap commit: Homebrew's downloader does not authenticate, so it cannot fetch a release
   archive from a private repository. The tap commit landing is the whole verification until the
   repository goes public; this is known and accepted. Once it is public, run the real
   end-to-end check once:
   ```sh
   brew update && brew install stacklok/tap/mecatl && mecatui --version
   ```

## The engine module is tagged separately

The `vX.Y.Z` release above is the **root repo / `mecated` image** release. The importable
core, `github.com/stacklok/mecatl/engine`, is its **own Go module** (ADR 0036) with its own
tag grammar `engine/vX.Y.Z` (distinct from the root tags). It carries a public-API
compatibility contract (`engine/COMPATIBILITY.md`, ADR 0037).

- **The first `engine/vX.Y.Z` tag is `engine/v0.0.1`** — a deliberate "earliest, no stability
  promise" initial cut (the lowest pre-v1 patch, signalling zero stability commitment for the very
  first published surface). Cutting it is a deliberate maintainer decision (deferred per ADR 0037) —
  do NOT cut it as part of a routine root release unless asked. The grammar is `engine/vX.Y.Z`,
  **distinct** from the root `vX.Y.Z` tags; the two version lines are independent. SUBSEQUENT bumps
  follow `engine/COMPATIBILITY.md` (pre-v1: minor = additive, patch = fixes).

### Cutting an engine tag (mirrors the root flow)

Run from the repo root.

1. **Pick the engine version.** First cut = `engine/v0.0.1` (a deliberate "earliest, no stability
   promise" initial cut); thereafter increment per semver, classified per `engine/COMPATIBILITY.md`
   (pre-v1: Added = minor, Changed/Removed = minor too; patch = fixes). The latest engine tag (none
   yet on the first cut):
   ```sh
   git tag --sort=-v:refname --list 'engine/v*' | head -1
   ```

2. **Pre-flight.** Confirm `engine/CHANGELOG.md` has an `[Unreleased]` entry covering everything
   since the last engine tag (on the **first** cut that is the whole initial surface — the existing
   `[Unreleased]` baseline section). Then run the advisory `gorelease` check:
   ```sh
   task api:release-check
   ```
   **On the FIRST cut this is a no-op / uninformative:** `gorelease` can only classify the surface
   against a *prior* `engine/vX.Y.Z` base tag, and none exists yet — so it has nothing to compare
   to. That is expected. The authoritative guard is the `api-compat` gate (`task api:check`), which
   already guarantees the committed `engine/api/*.txt` snapshots match the surface being tagged.

3. **Create the annotated tag** with a concise summary:
   ```sh
   git tag -a engine/vX.Y.Z -m "engine/vX.Y.Z — <one-line summary>"
   ```

4. **Push the tag:**
   ```sh
   git push origin engine/vX.Y.Z
   ```

**This line is deliberately still manual.** An engine tag adds no commit to `main` and carries
no pin bump, so it never needed the release-PR flow the root `vX.Y.Z` line uses — pushing the
tag is the whole release.

**IMPORTANT — an engine tag fires NO image build, NO GitHub Release, and NO Homebrew formula bump.** `release.yml` triggers on `v*` (the root tag
glob), which does **not** match `engine/v*`, so cutting an engine tag runs none of the ko build /
cosign / SBOM / SLSA pipeline. It only publishes the module version, making it resolvable for
`go get github.com/stacklok/mecatl/engine@engine/vX.Y.Z` consumers (ADR 0036/0037). There is no pin
bump and no `release.yml` run to confirm — the push of the tag is the whole release.

## Notes

- **Two publishing destinations, one tag.** A run can succeed on the GHCR images and still fail
  on the release or the tap (or vice versa). GoReleaser's brew pipe continues on error and the
  Release is created before the formula is pushed, so a bad tap token loses the formula but NOT
  the Release. Steps 7 and 8 are not optional: a green `gh run list` line is not proof that
  `brew install` works.
- **Never hand-edit `stacklok/homebrew-tap`.** The formula is generated from the tag by the
  release workflow and carries a `DO NOT EDIT` header. A manual edit is overwritten by the next
  release and desynchronizes the checksums in the meantime.
- **Never push to `main`, and never create a root `vX.Y.Z` tag by hand.** Both are the
  workflows' job. A hand-pushed version bump skips code review, and a hand-created tag can point
  at a commit whose release metadata the gate rejects. If `VERSION` is edited on `main` outside a
  release PR, `create-release-tag.yml` refuses to tag it rather than cutting a release from it.
  `release.yml`'s `guard` job additionally refuses to publish anything from a tag that is
  not an ancestor of `main`, so a tag cut on a branch builds nothing. (This applies to the ROOT
  `v*` line only — the `engine/v*` tags below are still cut by hand, deliberately: they carry
  no pin bump and add no commit to `main`.)
- **Annotated tags only** (`git tag -a`), matching prior releases — they carry a tagger + message.
  `create-release-tag.yml` does this; the tagger is `github-actions[bot]`.
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
