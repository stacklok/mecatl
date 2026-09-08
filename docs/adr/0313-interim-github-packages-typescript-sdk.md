# ADR 0313 — Interim GitHub Packages distribution and 0.0.x versioning for the TypeScript SDK

- Status: Proposed
- Date: 2026-09-08
- Scope: the interim registry, release authority, provenance, version line, and consumer installation contract for `@stacklok/mecatl-sdk`
- Supersedes: ADR 0304 Decisions 8–9, in part
- Superseded by: None

## Context

[ADR 0304](./0304-typescript-sdk-public-surface-and-release.md) selected npmjs
trusted publishing and npm-native provenance for the first TypeScript SDK
release. The repository is currently internal. npmjs does not generate
provenance for a private source repository, even when the package itself is
public, so that design would either publish without the required evidence or
block the release indefinitely.

GitHub Packages can publish the existing owner-scoped name
`@stacklok/mecatl-sdk` from the internal repository. It does not support npm's
native provenance attestations, and its npm registry requires authentication
even to read a public package. GitHub artifact attestations can instead bind the
exact verified tarball to the workflow run without introducing a long-lived npm
credential.

The interim registry also needs a version line that cannot consume the first
npmjs compatibility release. npm versions are permanent in practice: unpublish
is limited to 72 hours and a deleted version cannot be reused. GitHub Packages
versions are deletable, making it the appropriate place for the preview period.

## Decision

**1. Publish the interim SDK to GitHub Packages without renaming it.** The
package remains `@stacklok/mecatl-sdk` and publishes to
`https://npm.pkg.github.com`. Its manifest declares that registry in
`publishConfig.registry`. It does not declare `publishConfig.access` and the
publish command does not pass `--access`; GitHub Packages visibility follows
repository and organization policy.

**2. Authenticate publication with only the job's ephemeral GitHub token.** The
`publish` job receives `NODE_AUTH_TOKEN: ${{ secrets.GITHUB_TOKEN }}` and exact
permissions `contents: read`, `packages: write`, `id-token: write`, and
`attestations: write`. No other job receives those write permissions. In
particular, `verify` retains only `contents: read`. `GITHUB_TOKEN` is the
ephemeral token GitHub creates for the job, not a stored Actions secret. No
long-lived `NPM_TOKEN`-shaped secret may exist at repository, environment, or
organization scope.

**3. Attest and re-verify the exact tarball with GitHub artifact attestations.**
The publish job downloads the sole tarball built and inspected by `verify`,
re-verifies its npm-format SHA-512 integrity, and invokes a SHA-pinned
`actions/attest-build-provenance` action over that tarball before publishing
the tarball by path. It then runs
`gh attestation verify <tgz> --repo stacklok/mecatl`. The job summary records
the SHA-512 integrity and attestation URL. GitHub Packages has no npm-native
`dist.attestations` output; its absence is expected and is not a publication
failure.

**4. Preserve the release controls that are independent of the registry.** The
dedicated two-job `verify` → `publish` workflow retains the
`sdk/typescript/vX.Y.Z` tag grammar, root/SDK tag-trigger isolation, exact tag
checkout, tag-to-package-version parity, `origin/main` ancestry, frozen install
and generation cleanliness, exact tarball inventory, and fail-closed licence
provenance gate. `workflow_dispatch` remains dry-run-only: its `verify` job can
reach pack and inspection, while its tag-only `publish` job—and therefore its
OIDC and package-write authority—is never instantiated.

**5. Reserve separate version lines for the interim registry and npmjs.**
GitHub Packages releases use `0.0.x`, beginning with `0.0.1`. They are never
republished to npmjs under the same version, and cross-registry integrity
equality is not a goal. The first npmjs release is `0.1.0`, cut from a fresh
tag after the cutover requirements are met. The semver range `^0.0.1` admits
only `0.0.1`, so interim consumers are patch-pinned by design and must opt into
each preview release explicitly.

**6. Flip the canonical registry at the npmjs cutover.** Cutover requires the
repository to be public, confirmed publish rights in the npm `@stacklok`
organization, and an npm trusted-publisher record naming
`stacklok/mecatl`, `.github/workflows/release-sdk-typescript.yml`, and its
release environment. The release workflow and manifest then flip from GitHub
Packages to npmjs for versions `0.1.0` and later; they do not dual-publish.
Maintaining one canonical registry per version line avoids two authentication
contracts, conflicting `latest` views, and a false expectation of
cross-registry artifact identity. Experimentation and deletions happen only on
GitHub Packages. npmjs releases are treated as permanent: its 72-hour
unpublish window does not make a version reusable.

**7. Document the authenticated interim consumer contract.** While the
repository is internal, consumers are organization members and configure:

```ini
@stacklok:registry=https://npm.pkg.github.com
```

Their npm client authenticates with a personal access token carrying
`read:packages`. The first install is
`pnpm add @stacklok/mecatl-sdk@0.0.1`. This registry-specific setup disappears
when the canonical registry flips to npmjs.

ADR 0313 remains Proposed while the workflow is being adapted. The `v0.0.1`
release close-out changes its status to Accepted after the GitHub Packages
artifact, attestation, integrity, and inventory have been verified.

## Consequences

- The SDK can release from an internal repository with evidence over the exact
  inspected tarball and without a long-lived publishing credential.
- The publish job needs narrowly scoped package and attestation write
  authority. Its environment remains the human approval boundary.
- Interim installation is less convenient because every reader needs GitHub
  Packages authentication, even if package visibility later becomes public.
- Preview consumers deliberately opt into each `0.0.x` release. No preview
  version or tarball is promoted across registries.
- Cutover requires one explicit registry/authentication change, but leaves a
  single canonical source for all `0.1.0+` releases.

## See also

- [ADR 0304 — TypeScript SDK public surface completeness and v0.1.0 release](./0304-typescript-sdk-public-surface-and-release.md)
- [ADR 0093 — Provider modules and path-qualified tags](./0093-provider-modules.md)
- [TypeScript SDK release acceptance plan](../acceptance/sdk-typescript-release.md)

