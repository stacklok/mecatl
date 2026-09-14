# GitHub Actions workflows

The workflow YAML files in this directory are the source of truth for CI and
release behavior. This page is only a stable index to operational guidance.

- [CI race-shard maintenance](../scripts/root-race-packages.sh)
- [Live end-to-end testing](../../e2e/README.md)
- [Performance tracking rationale](../../docs/adr/0019-perf-tracking.md)
- [Deslop advisory duplication analysis](deslop.yml) (not a required CI gate)
- [Mecatequi CI adoption](https://mecatl.dev/docs/building/deployment/mecatequi) and its
  [design rationale](../../docs/adr/0028-mecatequi.md)

## Cutting a root release

Releases go through a pull request; nothing pushes a commit to `main`.

1. A maintainer dispatches [Create Release PR](create-release-pr.yml) and picks
   `patch`, `minor`, or `major`. A bot opens `Release vX.Y.Z` on branch
   `release/vX.Y.Z`, bumping [`VERSION`](../../VERSION) — the whole diff.
2. A maintainer reviews and squash-merges that PR.
3. [Create Release Tag](create-release-tag.yml) sees `VERSION` change on `main`,
   re-verifies the commit is a merged release PR, and pushes the annotated tag.
4. [Release](release.yml) publishes images, the Helm chart, the GitHub Release,
   and the Homebrew formula bump.

Both release workflows draw the release GitHub App from a `release` GitHub Environment
restricted to `main`, so the credential is not reachable from a branch. The full procedure,
including post-release verification, is in the
[cut-release skill](../../.claude/skills/cut-release/SKILL.md).

`VERSION` is the single authored source of the release version, and the only file a
release changes. Mecatequi's sibling actions use `$/` self-repository refs, which
resolve to this repository at the ref the workflow is running from — so there is no
version pin to bump and no skew to guard against.

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
