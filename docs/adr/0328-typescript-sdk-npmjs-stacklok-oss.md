# ADR 0328 — Publish the TypeScript SDK to npmjs as `@stacklok-oss/mecatl-sdk`

- Status: Proposed
- Date: 2026-09-11
- Scope: the canonical npm name, registry, trusted-publisher environment, provenance, and consumer installation contract for the TypeScript SDK
- Supersedes: [ADR 0313](./0313-interim-github-packages-typescript-sdk.md); [ADR 0279](./0279-typescript-sdk-architecture.md) Decision 6; [ADR 0304](./0304-typescript-sdk-public-surface-and-release.md) Decisions 8–9 package identity and organization name
- Superseded by: None

## Context

[ADR 0304](./0304-typescript-sdk-public-surface-and-release.md) selected npmjs
trusted publishing, the `npm-publish` GitHub Environment, path-qualified
`sdk/typescript/vX.Y.Z` tags, public `publishConfig.access`, and npm-native
provenance for the first TypeScript SDK release. [ADR 0313](./0313-interim-github-packages-typescript-sdk.md)
temporarily published `@stacklok/mecatl-sdk@0.0.x` to GitHub Packages because
the source repository was internal and npmjs does not generate provenance for
private source.

The repository is now public. GitHub Packages was a deletable preview line;
npmjs versions are permanent. The npm trusted-publisher record must name the
exact workflow file and GitHub Environment. The `@stacklok` npm organization is
not the publishing home; the public package lives under `@stacklok-oss`. There
is no supported npm alias from the preview name, and automating deletion of
`@stacklok/mecatl-sdk` on GitHub Packages would race a failed first npm
release.

## Decision

**1. The published package is `@stacklok-oss/mecatl-sdk`.** Active code,
examples, tests, and user documentation use that name. `@stacklok/mecatl-sdk`
remains only in historical records that explain the GitHub Packages preview.
There is no compatibility alias or transition package.

**2. The canonical registry is public npmjs.** The manifest sets
`publishConfig.registry` to `https://registry.npmjs.org` and
`publishConfig.access` to `public`. Verify both values from the packed tarball
before publication. The first supported npmjs version is `0.1.0`. GitHub
Packages `0.0.x` versions are never republished to npmjs.

**3. Bootstrap the npm package record without consuming the supported version
line.** npm cannot attach a trusted publisher to a package that does not yet
exist. Before the release tag, an authorized maintainer publishes an
intentionally minimal `0.0.0-bootstrap.0` placeholder under the non-default
`bootstrap` dist-tag using interactive authentication or a one-time granular
token with 2FA. The placeholder contains no SDK build. npm also assigns
`latest` to the only version of a newly created package and refuses to remove
that tag while no other version exists; the placeholder README marks the
version unsupported, and publishing `0.1.0` moves `latest` to the supported
release. Immediately after the package record exists, configure trusted
publishing, revoke the bootstrap credential, and set publishing access to
disallow tokens. `0.1.0` remains the first supported release and the first
version staged by the release workflow.

**4. Release staging stays behind the protected `npm-publish` environment.** Rename
the GitHub Environment from `github-packages-publish` to `npm-publish` and
require manual approval for the publish job. The npm trusted-publisher
configuration must use that exact environment name, repository
`stacklok/mecatl`, and workflow
`.github/workflows/release-sdk-typescript.yml`. Permit staged publishing only;
leave direct `npm publish` disabled. A successful tag workflow creates a
private npm release candidate and cannot make it public.

**5. Authenticate supported releases with npm trusted publishing only.** The
tag-gated `publish` job receives `contents: read` and `id-token: write`. It does not receive
`packages: write`, `attestations: write`, `NODE_AUTH_TOKEN`, `NPM_TOKEN`, or
any other long-lived npm credential. `verify` remains `contents: read` only.
`workflow_dispatch` remains dry-run-only. The one-time package-record bootstrap
is the bounded exception described by Decision 3; its credential is never
stored in GitHub Actions.

**6. Stage the inspected artifact, then require npm approval.** The publish job
uses an exactly pinned npm CLI at or above 11.15.0 and runs `npm stage publish`
against the downloaded tarball path. It validates npm's JSON result against the
expected package name, version, and SHA-512 integrity, then records the opaque
stage ID. An authorized maintainer reviews the candidate in npm or with
`npm stage view` / `npm stage download` and either approves or rejects it with
2FA. The workflow's short-lived trust token cannot approve its own candidate.

**7. Prove provenance on the approved package, not a second GitHub
attestation.** After approval, use `npm view` to verify `dist.attestations` and
`dist.integrity` against the packed SHA-512 recorded with the stage ID. Do not
run `actions/attest-build-provenance` or `gh attestation verify` on this
workflow. Provenance is asserted on npm's output, not by passing `--provenance`
or `--access` on the staging command.

**8. Automate reviewed version bumps and path-qualified tags.**
`sdk/typescript/VERSION` is the SDK release trigger and must equal the
`package.json` version. A maintainer dispatches the SDK release-PR workflow and
selects a semantic bump; the release GitHub App opens a PR changing exactly
those two files. After a human merges that PR, a separate workflow verifies the
App-authored branch, exact diff, monotonic version, previous tag, and package
identity before the App pushes `sdk/typescript/vX.Y.Z`. Using the App rather
than `GITHUB_TOKEN` is load-bearing: its tag push starts the npm staging
workflow. The first tag is `sdk/typescript/v0.1.0`. Root `v*` image releases
and provider-module tags stay isolated.

**9. Delete the GitHub Packages preview manually after an approved npm
release.** Do not automate unpublish or deletion in the release workflow.
Approve and verify `@stacklok-oss/mecatl-sdk@0.1.0` first.

## Consequences

- Consumers install from the public registry with no GitHub Packages
  authentication or scope remap.
- The permanent registry contains one explicitly unsupported bootstrap
  prerelease. `bootstrap` and `latest` point to it only until `0.1.0` publishes;
  the supported release then becomes `latest`.
- Operators must create the `npm-publish` environment, trusted-publisher
  record with staged-publish-only authority, and npm `@stacklok-oss` package
  permissions before the first tag. Package publishing access requires 2FA and
  disallows tokens.
- A tag stages `0.1.0`; it does not move `latest` or expose the SDK until an npm
  maintainer approves it with 2FA.
- Later releases require only a reviewed version-bump PR plus the GitHub
  Environment and npm approvals; automation creates the immutable tag.
- A failed or rejected npm stage still leaves the GitHub Packages preview
  intact until a human deletes it.
- In-repository examples depend on the SDK via `file:` until
  `@stacklok-oss/mecatl-sdk@0.1.0` exists on npmjs.

## See also

- [ADR 0304 — TypeScript SDK public surface completeness and v0.1.0 release](./0304-typescript-sdk-public-surface-and-release.md)
- [ADR 0313 — Interim GitHub Packages distribution](./0313-interim-github-packages-typescript-sdk.md)
- [ADR 0093 — Provider modules and path-qualified tags](./0093-provider-modules.md)
- [TypeScript SDK release acceptance plan](../acceptance/sdk-typescript-release.md)
