---
name: cut-release
description: >-
  Cut a tagged release of mecatl — bump the reusable-workflow version pins, commit,
  annotate and push a vX.Y.Z tag, which triggers the Release workflow (ko images + Helm
  chart to GHCR, plus a GitHub Release with signed archives and a Homebrew formula bump).
  Use when asked to cut/ship/tag/publish a release or bump the version. NOT for general
  git tagging unrelated to a mecatl release.
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

The one fragile part: the `mecatequi-reusable.yml` workflow references its three first-party
sibling composite actions by a **hardcoded** `@vX.Y.Z` literal (expressions are illegal in
`uses:`). Every release MUST bump those pins in the same tagged commit, or the release ships
pins pointing at the previous tag — the version skew that `.github/actions/check-reusable-pins.sh`
fails the release on. The bundled script does this bump for you.

## Steps

Run from the repo root.

1. **Pick the next version.** Find the latest tag and increment per semver (releases so far are
   `v0.0.x` patch bumps).
   ```sh
   git tag --sort=-v:refname | head -1     # e.g. v0.0.5  ->  next is v0.0.6
   ```

2. **Confirm what you're tagging.** A release tags the current committed `HEAD` plus the one
   pin-bump commit on top. Check what's been added since the last tag, and make sure `HEAD` is
   what you intend to ship (if another agent has uncommitted work, it stays out — you only stage
   the two pin files):
   ```sh
   git log <last-tag>..HEAD --oneline
   git status -sb
   ```

3. **Bump the pins** with the bundled script (edits the two gated files and proves the gate passes):
   ```sh
   .claude/skills/cut-release/scripts/bump-release-pins.sh vX.Y.Z
   ```

4. **Commit ONLY the two pin files** by explicit path — never `git add -A`, never sweep in another
   agent's working-tree changes:
   ```sh
   git add .github/actions/check-reusable-pins.sh .github/workflows/mecatequi-reusable.yml
   git commit -m "chore(release): bump reusable-workflow pins vOLD -> vNEW"
   ```

5. **Create an annotated tag** with concise release notes (group the commits since the last tag
   into a few bullet lines):
   ```sh
   git tag -a vX.Y.Z -m "vX.Y.Z — <one-line summary>

   - <bullet>
   - <bullet>"
   ```

6. **Push the commit, then the tag** (tag push is what fires the Release workflow):
   ```sh
   git push origin main && git push origin vX.Y.Z
   ```

7. **Confirm the release run started, then wait for it.** The archive/Homebrew job is the one
   that reaches outside this repository, so it is the one to watch:
   ```sh
   gh run list --workflow=release.yml --limit 3
   gh run watch "$(gh run list --workflow=release.yml --limit 1 --json databaseId --jq '.[0].databaseId')"
   ```

8. **Verify the GitHub Release carries every artifact.** It must not be a draft, and it must
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

9. **Verify the Homebrew tap got the formula bump:**
   ```sh
   gh api repos/stacklok/homebrew-tap/commits --jq '.[0].commit.message'
   gh api repos/stacklok/homebrew-tap/contents/Formula/mecatl.rb --jq '.content' \
     | base64 -d | grep -E 'version|url|sha256' | head
   ```
   The top commit must name the version you just cut, and the formula's `url` and `sha256`
   values must match the release assets from step 8.

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

**IMPORTANT — an engine tag fires NO image build, NO GitHub Release, and NO Homebrew formula bump.** `release.yml` triggers on `v*` (the root tag
glob), which does **not** match `engine/v*`, so cutting an engine tag runs none of the ko build /
cosign / SBOM / SLSA pipeline. It only publishes the module version, making it resolvable for
`go get github.com/stacklok/mecatl/engine@engine/vX.Y.Z` consumers (ADR 0036/0037). There is no pin
bump and no `release.yml` run to confirm — the push of the tag is the whole release.

## Notes

- **Two publishing destinations, one tag.** A run can succeed on the GHCR images and still fail
  on the release or the tap (or vice versa). GoReleaser's brew pipe continues on error and the
  Release is created before the formula is pushed, so a bad tap token loses the formula but NOT
  the Release. Steps 8 and 9 are not optional: a green `gh run list` line is not proof that
  `brew install` works.
- **Never hand-edit `stacklok/homebrew-tap`.** The formula is generated from the tag by the
  release workflow and carries a `DO NOT EDIT` header. A manual edit is overwritten by the next
  release and desynchronizes the checksums in the meantime.
- **Tag and commit must match.** The pushed tag must point at the commit that carries the bumped
  pins, or the pin gate fails the release. Step 4 → 5 ordering guarantees this.
- **Annotated tags only** (`git tag -a`), matching prior releases — they carry a tagger + message.
- **Don't bump illustrative documentation refs** unless asked — the `@vX.Y.Z` examples in
  `docs/usage/mecatequi-ci.md` are illustrative and do NOT gate the release. The script
  deliberately leaves them alone.
- **Commit trailer:** end the commit message with the repo's `Co-Authored-By` trailer (see AGENTS.md).
- **If the release run fails on the pin gate**, the tag's commit didn't have the pins bumped — the
  commit/tag ordering in steps 4–6 was broken. Re-tag the correct commit.
