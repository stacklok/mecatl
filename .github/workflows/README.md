# GitHub Actions workflows

The workflow YAML files in this directory are the source of truth for CI and
release behavior. This page is only a stable index to operational guidance.

- [Live end-to-end testing](../../e2e/README.md)
- [Performance tracking rationale](../../docs/perf-tracking.md)
- [Deslop advisory duplication analysis](deslop.yml) (not a required CI gate)
- [Mecatequi CI adoption](../../user-docs/building/ci/mecatequi.md)
- [microVM environment operations](../../user-docs/operating/microvm-environments.md)

## Continuous builds from main

[Continuous main build](continuous.yml) runs after [CI](ci.yml) succeeds on a push
to `main`. It publishes the `mecak8s` image, the Studio image, and the `mecak8s` and
`mecatl-studio` charts, all built from that commit and sharing one version,
`X.Y.(Z+1)-main.<run>.<attempt>.g<sha8>`, derived from the commit's `VERSION`. Images
go to `ghcr.io/stacklok/mecatl/main/{mecak8s,studio}` (tagged `v<version>`) and charts
to `oci://ghcr.io/stacklok/mecatl/main/charts`; each chart pins its image by digest.
Release artifacts and their paths are never touched.

The charts publish last, after both images are signed and verified, so a follower
that selects the newest chart with a prerelease range such as `>=0.0.0-0` always gets
images from one commit. A commit publishes only when it is newer on `main` than the
last published `mecatl-studio` chart; an older or already published commit ends with
a notice, so followers never move back. To publish by hand, dispatch the workflow from
`main`; it publishes the dispatched commit. Set `force` only to republish a commit or
to deliberately move followers to an older one.

Signatures and attestations name `.github/workflows/continuous.yml@refs/heads/main`,
so verify a main build with that certificate identity. Main builds ship without the
product-metrics ingest key. GitHub creates the four `main/` packages as private on
their first publication; pull them with a GHCR credential or make them public.

## Cutting a root release

Releases go through a pull request; nothing pushes a commit to `main`.

1. A maintainer dispatches [Create Release PR](create-release-pr.yml) and picks
   `patch`, `minor`, or `major`. A bot opens `Release vX.Y.Z` on branch
   `release/vX.Y.Z`, bumping [`VERSION`](../../VERSION), the `mecak8s` and
   `mecatl-studio` chart versions and app versions, and the `mecak8s` chart's
   default image tag.
2. A maintainer reviews and squash-merges that PR.
3. [Create Release Tag](create-release-tag.yml) sees `VERSION` change on `main`,
   re-verifies the commit is a merged release PR, and pushes the annotated tag.
4. [Release](release.yml) publishes images, the Helm charts, the GitHub Release,
   and the Homebrew formula bump.

Both release workflows draw the release GitHub App from a `release` GitHub Environment
restricted to `main`, so the credential is not reachable from a branch. The full procedure,
including post-release verification, is in the
[cut-release skill](../../.claude/skills/cut-release/SKILL.md).

`VERSION` is the source of truth for the release version. The release PR propagates it
to `deploy/helm/mecak8s/Chart.yaml`, `deploy/helm/mecak8s/values.yaml`, and
`deploy/helm/mecatl-studio/Chart.yaml`, and both release workflows verify the synchronized
values without relying on a fixed list of files.
Mecatequi's sibling actions use `$/` self-repository refs, which resolve to this repository
at the ref the workflow is running from, so they need no version pin.

## Cutting a TypeScript SDK release

The SDK uses the same release GitHub App and keeps its version independent from
the root binaries:

1. Dispatch [Create TypeScript SDK Release PR](create-sdk-typescript-release-pr.yml)
   from `main` and select `patch`, `minor`, or `major`.
2. Review and merge the bot-authored PR. Its entire diff contains the generated
   [`sdk/typescript/CHANGELOG.md`](../../sdk/typescript/CHANGELOG.md) entry,
   [`sdk/typescript/VERSION`](../../sdk/typescript/VERSION), and the matching
   `package.json` version. The changelog lists package changes since the previous
   SDK tag.
3. [Create TypeScript SDK Release Tag](create-sdk-typescript-release-tag.yml)
   verifies the merged PR and pushes `sdk/typescript/vX.Y.Z` with the release App.
4. [Release TypeScript SDK](release-sdk-typescript.yml) builds and inspects the
   npm artifact, waits for GitHub Environment approval, and stages it on npm.
5. An npm maintainer reviews the candidate, approves it with 2FA, and verifies
   its public integrity and provenance.

The App-authored tag means a maintainer who merged the release PR can approve the
`npm-publish` environment even when prevent-self-review is enabled. The workflow
and npm approvals remain separate gates.
