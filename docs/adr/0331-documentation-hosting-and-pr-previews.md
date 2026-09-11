# ADR 0331 — Host public documentation and pull request previews on Vercel

- Status: Accepted
- Date: 2026-09-10
- Scope: production hosting for `mecatl.dev`, hosted previews for pull requests
  that change `website/` or `user-docs/`, redirect ownership, and the security
  boundary for external contributions
- Supersedes: None
- Superseded by: None

## Context

Mecatl builds its public documentation from `website/` and `user-docs/` with
Docusaurus. Pull request CI verifies that the site builds, but reviewers must
run it locally to inspect layout, navigation, and redirects.

Production uses a private S3 bucket behind CloudFront Origin Access Control.
The deployment workflow builds from `main`, assumes an AWS role through GitHub
OIDC, synchronizes the build into the production bucket, and invalidates the
production distribution. A CloudFront Function provides the temporary
pre-public password gate and rewrites directory requests to `index.html`.

The password gate was the main reason for selecting the current host. It is not
a requirement once the site becomes public, and project ownership does not
require S3 and CloudFront to remain the long-term host. Stacklok's existing
documentation site uses Vercel, including repository-owned HTTP redirects in
`vercel.json` and hosted pull request previews.

Preview output is untrusted active content. A contributor can change the
Docusaurus configuration, npm dependencies, HTML, and JavaScript. Any host must
keep forked pull request builds away from production credentials and must not
publish them beneath the `mecatl.dev` same-site boundary.

## Proposed decision

Move the public documentation site and its pull request previews to one Vercel
project. Keep the current AWS deployment until production behavior, redirects,
external-fork controls, rollback, and DNS cutover have been verified.

Connect the project to `stacklok/mecatl`. Production deployments build `main`
and serve `mecatl.dev`. Pull requests that change `website/` or `user-docs/`
receive Vercel preview deployments and GitHub deployment links. Use
provider-generated preview domains. Do not add preview aliases beneath
`mecatl.dev`.

Keep Vercel Git Fork Protection enabled. Vercel requires authorization by a
team member before it deploys a pull request from a fork; disabling that
control is outside this decision. The Preview environment contains no project
secrets because Docusaurus needs none to build. Production-only values, if any
are introduced later, remain scoped to Production. A maintainer authorization
precedes deployment of a forked pull request.

Treat every preview as arbitrary contributor-controlled HTML and JavaScript.
Keep Vercel Deployment Protection disabled for previews so contributors and
other non-members can inspect an authorized deployment. Vercel automatically
marks Preview deployments `noindex` and `nofollow`. Preview URLs must not
receive production cookies, credentials, analytics secrets, or privileged API
access.

Store published URL redirects in repository-owned Vercel configuration so
Vercel returns HTTP redirect responses before serving the static build. The
existing `/docs/intro` redirect is represented in `vercel.json`. Future page
moves update the same repository-owned redirect registry.

After the DNS cutover and an agreed rollback window, remove the production S3
bucket, CloudFront distribution and functions, Route 53 aliases that target
CloudFront, and the GitHub OIDC deployment role through the `stacklok/infra`
process. Retain the Route 53 zone and manage the Vercel domain records there.

## Alternatives considered

### Keep production on AWS and add an isolated AWS preview plane

One private preview bucket and one CloudFront distribution could route pull
request hostnames to isolated S3 prefixes. A secretless GitHub workflow would
build an artifact, and a trusted default-branch publisher with a preview-only
AWS role would validate and copy it.

This option keeps production unchanged and provides explicit AWS authority
boundaries. It also adds a bucket, distribution, certificate, preview domain,
GitHub environment, IAM role, artifact handoff, cleanup process, and edge
configuration. The team would own machinery that Vercel already provides for
the existing Stacklok documentation site.

### Use Vercel for previews only

Vercel could build pull request previews while AWS continues to serve
production. This reduces the migration scope, but previews would not exercise
the production host's clean-URL rewrites, redirects, caching, or response
headers. It also leaves redirect ownership split between Docusaurus and
infrastructure. Use this only as a time-bounded migration stage.

### Keep local previews

The existing `task site:dev` workflow avoids another public surface. It does
not give reviewers a shared URL and makes navigation and responsive-layout
review dependent on each reviewer's local environment.

## Vercel project requirements

The prepared Vercel project uses the same baseline as the Stacklok
documentation site:

- The project root directory is `website/`.
- The Ignored Build Step is
  `git diff HEAD^ HEAD --quiet -- . ../user-docs`, so Vercel builds only commits
  that change `website/` or `user-docs/`.
- Git Fork Protection is enabled.
- Deployment Protection is disabled for previews.
- Protected Sourcemaps is enabled.
- Build Logs and Source Protection is enabled.
- The Preview environment contains no project secrets.

These settings are part of the production and fork trust boundaries. Changes
to them require security review against the constraints in this ADR.

## Migration requirements

The production cutover and AWS decommissioning work must define and verify:

- any additional production and preview response headers, analytics, and
  observability;
- redirect migration and verification;
- DNS cutover and rollback criteria; and
- the AWS decommissioning sequence and rollback window.

## Consequences

Reviewers receive the same hosted preview workflow used by other Stacklok
documentation. Production and previews exercise one hosting platform, and
published URL redirects become repository-owned HTTP responses. Mecatl no
longer needs a bespoke AWS content-deployment role or CloudFront request logic
after migration.

Vercel becomes the availability, build, cache, redirect, and preview security
provider for `mecatl.dev`. The team must preserve the project requirements in
this ADR, especially Git Fork Protection and the secret-free Preview
environment. Preview deployments still publish untrusted active content and
require maintainer judgment before a fork build goes live.

The migration changes production DNS and removes recoverable AWS
infrastructure only after a rollback window. Running both hosts during cutover
temporarily adds operational complexity.

## See also

- [Issue 1009: Decide post-launch hosting and pull request previews for mecatl.dev](https://github.com/stacklok/mecatl/issues/1009)
- [Issue 1008: Define link validation and post-launch redirect policy](https://github.com/stacklok/mecatl/issues/1008)
- [Vercel deployment authorization for forks](https://vercel.com/docs/git/vercel-for-github#deployment-authorizations-for-forks)
- [Vercel redirects](https://vercel.com/docs/routing/redirects)
- [Stacklok documentation Vercel configuration](https://github.com/stacklok/docs-website/blob/main/vercel.json)
- [ADR 0002: Documentation lifecycle](./0002-documentation-lifecycle.md)
- [ADR 0321: Canonical user documentation ownership](./0321-canonical-user-documentation-ownership.md)
- Rollback site deployment during the cutover window:
  `.github/workflows/deploy-site.yml`
