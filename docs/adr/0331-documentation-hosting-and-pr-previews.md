# ADR 0331 — Host public documentation and pull request previews on Vercel

- Status: Proposed
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
precedes deployment of a forked pull request. The team must confirm whether a
later commit requires a new authorization before this ADR is accepted.

Treat every preview as arbitrary contributor-controlled HTML and JavaScript.
Apply `X-Robots-Tag: noindex` and appropriate security headers to preview
responses, and decide whether previews also require Vercel Deployment
Protection before this ADR is accepted. Preview URLs must not receive
production cookies, credentials, analytics secrets, or privileged API access.

Move published URL redirects into repository-owned Vercel configuration so
Vercel returns HTTP redirect responses before serving the static build. The
current Docusaurus client redirect remains in place until the production host
migrates. During cutover, represent it in `vercel.json`, verify the old and new
URLs against the Vercel deployment, then remove the duplicate client redirect.
Future page moves update the same repository-owned redirect registry.

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

## Discussion before acceptance

This ADR is a design proposal. It authorizes no hosting, workflow, or DNS change
while its status is Proposed. Discussion must resolve:

- the owning Vercel team, project, plan, and cost center;
- confirmation of Git Fork Protection and environment-variable behavior under
  the Stacklok Vercel organization;
- preview Deployment Protection, retention, and access expectations;
- the exact build, output, ignored-build, and Node.js settings;
- production and preview response headers, analytics, and observability;
- redirect migration and verification;
- DNS cutover, rollback criteria, and the AWS decommissioning sequence; and
- whether another Stacklok site supplies a reusable Vercel project baseline.

If the proposal proceeds, an Architectural acceptance plan must define these
settings, the production and fork trust boundaries, migration proofs, rollback,
and cleanup before implementation.

## Consequences

Reviewers receive the same hosted preview workflow used by other Stacklok
documentation. Production and previews exercise one hosting platform, and
published URL redirects become repository-owned HTTP responses. Mecatl no
longer needs a bespoke AWS content-deployment role or CloudFront request logic
after migration.

Vercel becomes the availability, build, cache, redirect, and preview security
provider for `mecatl.dev`. The team must confirm organization settings rather
than assuming provider defaults, especially for fork authorization and preview
environment variables. Preview deployments still publish untrusted active
content and require maintainer judgment before a fork build goes live.

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
- Production site deployment: `.github/workflows/deploy-site.yml`
