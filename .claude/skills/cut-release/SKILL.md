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
which builds, signs, and attests the `mecated` image to GHCR. There is **no** version baked
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

- **No `engine/vX.Y.Z` tag has been cut yet.** Cutting the first one is a deliberate maintainer
  decision (deferred per ADR 0037) — do NOT cut it as part of a routine root release unless asked.
- When you DO cut an engine tag, first run **`task api:release-check`** (advisory `gorelease`) to
  preview the SemVer classification of the surface change, and confirm `engine/CHANGELOG.md` has an
  entry for everything since the last engine tag. The `api-compat` gate already guarantees the
  committed `engine/api/*.txt` snapshots match the surface being tagged.

## Notes

- **Tag and commit must match.** The pushed tag must point at the commit that carries the bumped
  pins, or the pin gate fails the release. Step 4 → 5 ordering guarantees this.
- **Annotated tags only** (`git tag -a`), matching prior releases — they carry a tagger + message.
- **Don't bump the illustrative doc refs** unless asked — the `@vX.Y.Z` examples in `docs/usage.md`
  and `.github/workflows/README.md` are illustrative and do NOT gate the release. The script
  deliberately leaves them alone.
- **Commit trailer:** end the commit message with the repo's `Co-Authored-By` trailer (see AGENTS.md).
- **If the release run fails on the pin gate**, the tag's commit didn't have the pins bumped — the
  commit/tag ordering in steps 4–6 was broken. Re-tag the correct commit.
