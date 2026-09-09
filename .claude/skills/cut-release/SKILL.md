---
name: cut-release
description: >-
  Cut a tagged release of mecatl — bump the reusable-workflow version pins, commit,
  annotate and push a vX.Y.Z tag, which triggers the Release workflow (ko build +
  cosign sign + SBOM + SLSA provenance to GHCR). Use when asked to cut/ship/tag/publish
  a release or bump the version. NOT for general git tagging unrelated to a mecatl release.
metadata:
  author: stacklok
---

# Cut a mecatl release

A release is a `vX.Y.Z` git tag. Pushing that tag triggers `.github/workflows/release.yml`,
which builds, signs, and attests the `mecated` and `mecatui` images to GHCR. There is **no** version baked
into the Go code — the tag IS the release.

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

7. **Confirm the release run started:**
   ```sh
   gh run list --workflow=release.yml --limit 3
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

**IMPORTANT — an engine tag fires NO image build.** `release.yml` triggers on `v*` (the root tag
glob), which does **not** match `engine/v*`, so cutting an engine tag runs none of the ko build /
cosign / SBOM / SLSA pipeline. It only publishes the module version, making it resolvable for
`go get github.com/stacklok/mecatl/engine@engine/vX.Y.Z` consumers (ADR 0036/0037). There is no pin
bump and no `release.yml` run to confirm — the push of the tag is the whole release.

## Notes

- **Tag and commit must match.** The pushed tag must point at the commit that carries the bumped
  pins, or the pin gate fails the release. Step 4 → 5 ordering guarantees this.
- **Annotated tags only** (`git tag -a`), matching prior releases — they carry a tagger + message.
- **Don't bump illustrative documentation refs** unless asked — the `@vX.Y.Z` examples in
  `user-docs/building/deployment/mecatequi.md` are illustrative and do NOT gate the release. The script
  deliberately leaves them alone.
- **Commit trailer:** end the commit message with the repo's `Co-Authored-By` trailer (see AGENTS.md).
- **If the release run fails on the pin gate**, the tag's commit didn't have the pins bumped — the
  commit/tag ordering in steps 4–6 was broken. Re-tag the correct commit.
