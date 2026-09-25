# ADR 0319 — Signed release archives and Homebrew tap distribution

- Status: Accepted
- Date: 2026-09-11
- Scope: host-native executable distribution for `mecated` and `mecatui` — GitHub Release archives and the Homebrew formula
- Supersedes: none
- Superseded by: none

## Context

A root `vX.Y.Z` tag published only container images and a Helm chart to GHCR. There
was no GitHub Release object and no binary archive, so every documented way to
obtain Mecatl was "clone the repository and run `task build`". Operators on macOS
had to install a Go toolchain to run a terminal client.

Stacklok already runs one Homebrew tap, `stacklok/homebrew-tap`, fed by
`stacklok/toolhive` and `stacklok/modelith`. Both use GoReleaser's `brews:` block to
commit a generated formula directly to the tap's default branch, authenticated by a
per-repository GitHub App. The tap's formulas carry GoReleaser's
`# DO NOT EDIT` header. Reusing that mechanism was therefore a question of matching
an established org pattern, not of inventing one.

Two constraints shaped the result. First, Homebrew's downloader does not
authenticate, so a formula pointing at a private repository's release assets fails
for every user — this repository was internal when the work landed, and the
resulting 404 window was accepted deliberately rather than worked around. Second,
`internal/buildinfo` is the only stamped symbol in this repository; there is no
`main.version`, so GoReleaser's default ldflags would have silently produced
`dev+<vcs-revision>` in every published binary.

**Process note, recorded rather than implied:** the implementation shipped before
this ADR. The directing human explicitly waived the acceptance-plan spine for the
work, so it landed as a series of ordinary PRs and was verified in production at
tags `v0.0.31` and `v0.0.32`. This ADR records the durable decision after the fact.
An earlier, unmerged proposal occupied this ADR number with a narrower decision —
Darwin arm64 only, assembled by a hand-written script — and is described under
Rejected alternatives.

## Decision

Publish host-native executables from every root `vX.Y.Z` tag, using GoReleaser
**alongside** ko rather than in place of it. The two tools own disjoint artifact
classes: ko owns every container image and the Helm chart; GoReleaser owns the
binary archives, the GitHub Release they attach to, and the Homebrew formula.
`.goreleaser.yaml` states that boundary, and neither tool builds the other's
artifacts.

Ship `mecated` and `mecatui`, mirroring the existing `install` target in
`Taskfile.yml`, as one tarball per platform containing both executables. Cover
`darwin` and `linux` on `amd64` and `arm64`. Exclude `mecademo`, `mecatequi`, and
`mecak8s`: they remain source-only or image-only. Windows is excluded because
Homebrew does not serve it, **not** because it fails to compile; Windows packaging
would be a separate scoop or winget decision.

Keep the archive flat, with both executables at its root. The formula's generated
`bin.install` expects that layout, so a versioned top-level directory is not an
option.

Stamp both executables with `-X github.com/stacklok/mecatl/internal/buildinfo.BuildID=v{{ .Version }}`.
The `v` prefix is re-added deliberately: GoReleaser strips it from `.Version`,
whereas `.ko.yaml` and `Taskfile.yml` both stamp the `git describe` form with it, so
without the prefix a Homebrew install and a container image would disagree about
their own version. Three independent checks pin this — the formula's `test` block,
the `release:verify` target in `Taskfile.yml`, and a native Apple Silicon smoke job
in `.github/workflows/ci.yml`.

Give the archives the same supply-chain treatment the images already receive: a
`checksums.txt`, a keyless cosign signature bundle and an SPDX SBOM per archive, and
one build-provenance attestation whose subjects are taken from `checksums.txt` rather
than from a glob. One attestation therefore covers every published artifact with the
digests GoReleaser already computed.

Commit the generated formula directly to `stacklok/homebrew-tap`, matching the two
existing publishers, authenticated by a GitHub App installation token scoped to that
one repository with `contents: write` as its only permission. The release job holds
`contents: write` for this repository alone; the cross-repository write never uses
`GITHUB_TOKEN`. A prerelease tag publishes archives but does **not** move the
formula.

Retain GoReleaser's deprecated `brews:` block rather than migrating to
`homebrew_casks`. `stacklok/homebrew-tap` is a `Formula/` tap and casks must live in
`Casks/`; migrating would fork the tap's layout and is an org-wide decision across
three publishing repositories, not a Mecatl one.

## Consequences

`brew install stacklok/tap/mecatl` installs both executables on macOS and Linux, and
every release carries artifacts an operator can verify independently — the
certificate identity binds to this repository's release workflow at the exact tag,
with no long-lived key.

The costs are real. The published archives are large: roughly 86 MB compressed per
platform and about 302 MB installed, because the root module's dependency cone is
heavy by design and neither `-s -w` nor any other flag reduces the resulting
`__gopclntab`. Reducing it requires removing linked code, which is separate work.

A release now writes to a second, public repository, so a failure mode exists where
images publish and the formula does not, or the reverse. GoReleaser's brew pipe
continues on error and the Release is created before the formula is pushed, so a bad
tap token loses the formula but not the Release; recovery is to re-run.

A published tag is effectively immutable. The formula records archive checksums for a
specific version, so re-tagging after a tap commit breaks `brew install` for
everyone. Releases are fixed forward, never re-cut. `.claude/skills/cut-release/SKILL.md`
carries that rule and the post-release verification steps.

Cross-compiling eight targets is disk-hungry: about 2.0 GB of Go build cache per
platform, with effectively no sharing between platforms, plus roughly 1.6 GB of
build output. A GitHub-hosted runner starts with about 14 GB free, so
`.github/workflows/release.yml` reclaims unused preinstalled toolchains before
building.

Retaining a deprecated GoReleaser block means `goreleaser check` exits 2 rather than
0, so both the CI validation step and the `release:check` target in `Taskfile.yml`
accept that exit code and fail only on genuine configuration errors.

## Rejected alternatives

**A new private tap plus a credential shim.** The original plan created a separate
private tap repository and a user-owned credential mechanism for downloading private
release assets, to be deleted at public launch. Rejected: the organisation already
runs one tap for two projects, a second tap repository was not wanted, and building
then deleting a credential workaround cost more than accepting a short window in
which the formula 404s.

**Darwin arm64 only, assembled by a hand-written script.** The earlier proposal under
this ADR number scoped distribution to a single platform, packaged by a dedicated
shell script with a versioned top-level directory and a per-archive `.sha256` file,
built on a macOS runner. Rejected on scope — four platforms cost one configuration
block once GoReleaser is present — and on mechanism, since the org's two existing tap
publishers are GoReleaser-driven and the tap's formulas are GoReleaser-generated.
Its offline contract test for the release workflow was **not** rejected and remains
worth porting; nothing currently tests `.github/workflows/release.yml`.

**Reusable-workflow-driven or hand-rolled matrix builds.** Splitting the build across
runners would reduce per-runner disk to about 2.4 GB, and is the correct axis
(platforms, not binaries). Rejected because GoReleaser's `--split`/`continue` and its
`prebuilt` builder are Pro-only, so no OSS job could reassemble a correct
multi-platform formula from separately built binaries without hand-rolling the
archives, checksums, signing and formula templating. Reclaiming runner disk solved
the constraint instead.

**A tag-only release with no pre-tag commit.** Not available. The reusable workflow
references its sibling composite actions by hardcoded literal tag because an
expression is illegal in `uses:`, and `.github/actions/check-reusable-pins.sh` fails
the release unless those pins equal the tag. The pin bump must therefore be in the
commit the tag points at. See [ADR 0028](./0028-mecatequi.md).

## See also

- [Install Mecatl](../../user-docs/install.md) — the canonical user-facing install page.
- [Prerequisites and build reference](../usage/install.md) — the from-source path.
- [ADR 0028](./0028-mecatequi.md) — why the reusable workflow's action pins are literal tags.
- [ADR 0002](./0002-documentation-lifecycle.md) — the documentation lifecycle this record follows.
