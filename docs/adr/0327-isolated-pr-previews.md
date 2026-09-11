# ADR 0327 — Isolated pull request previews for the documentation site

- Status: Proposed
- Date: 2026-09-10
- Scope: hosted previews for pull requests that change `website/` or
  `user-docs/`, including their publication authority, domain isolation,
  retention, and routing
- Supersedes: None
- Superseded by: None

## Context

Mecatl builds its public documentation from `website/` and `user-docs/` with
Docusaurus. Pull request CI verifies that the site builds, but reviewers must
run it locally to inspect layout, navigation, and generated redirects. A hosted
preview would give each affected pull request a stable review URL without
moving production hosting to another provider.

Production uses a private S3 bucket behind CloudFront Origin Access Control.
The deployment workflow builds from `main`, assumes an AWS role through GitHub
OIDC, synchronizes the build into the production bucket, and invalidates the
production distribution. Extending that workflow or role to pull requests
would combine untrusted pull request code with production publication
authority.

The preview output is also untrusted active content. A contributor can change
the Docusaurus configuration, npm dependencies, HTML, and JavaScript. Publishing
that output beneath `mecatl.dev` would give it a sibling origin but the same
registrable site as production. It could set parent-domain cookies or interact
with future same-site services even though it could not read production origin
data directly.

## Proposed decision

Host documentation previews on an isolated S3 and CloudFront deployment plane.
Do not reuse the production bucket, distribution, GitHub environment, or AWS
role.

Use one private preview bucket and one CloudFront distribution for all pull
requests. Give the distribution a wildcard certificate and DNS record on a
dedicated registrable preview domain. A host such as
`pr-1009.<PREVIEW_DOMAIN>` maps to the S3 prefix `pr-1009/`. The exact preview
domain is an infrastructure-owned choice and must be resolved before this ADR
is accepted.

A CloudFront viewer-request function validates the host, derives only a numeric
pull request identifier, prefixes the origin request, and applies the same
directory-to-`index.html` rewrite required by the production S3 REST origin.
The distribution serves only `GET` and `HEAD`, uses Origin Access Control, and
adds preview-specific response headers. These include `X-Robots-Tag: noindex`,
a restrictive Content Security Policy, `frame-ancestors 'none'`, and
`worker-src 'none'`. Preview responses use a short cache lifetime so an update
does not require broad distribution invalidations.

Separate building from publication:

1. A `pull_request` workflow checks out and builds the pull request without
   secrets or AWS authority. It uploads `website/build/` as an untrusted
   artifact associated with the workflow run and exact head commit.
2. A trusted default-branch workflow receives the completed run, verifies that
   it belongs to the current pull request head and changed `website/` or
   `user-docs/`, then downloads the artifact into a fixed directory. It does not
   check out or execute pull request code.
3. Before assuming AWS credentials, the publisher accepts only regular files
   within configured file-count and byte limits. It rejects links and special
   files. Publication does not follow symbolic links or interpolate
   contributor-controlled values into shell commands.
4. The publisher assumes a preview-only role through a dedicated GitHub
   environment. The role can list the preview bucket, replace objects under
   pull request prefixes, and perform no operation on production resources.

Organization members and collaborators may publish automatically after the
build succeeds. External-contributor previews require approval of the GitHub
preview environment for each workflow run. Approval applies to one exact head
commit; a later push produces a new build and approval request.

A trusted close workflow removes the pull request prefix without checking out
pull request code. A scheduled reconciler removes orphaned prefixes, and S3
lifecycle rules expire noncurrent object versions and provide a final retention
bound. The retention period remains an open operational choice for the
infrastructure plan.

Docusaurus client redirects remain build artifacts declared in
`website/docusaurus.config.ts`. They work unchanged on production and preview
hosts. True HTTP redirect status codes remain a separate infrastructure
decision and are not part of preview hosting.

## Discussion before acceptance

This ADR is a design proposal. It authorizes no infrastructure or workflow
change while its status is Proposed. Discussion should resolve:

- the dedicated preview domain and its ownership;
- the preview retention period and scheduled cleanup policy;
- whether every internal pull request publishes automatically or publication
  requires an explicit label; and
- whether access logging or a web application firewall is required at launch.

If the proposal proceeds, an Architectural acceptance plan must define the
exact GitHub workflow events, environment protections, IAM resources, artifact
validation bounds, cache behavior, security headers, cleanup behavior, and
verification evidence before implementation.

## Consequences

Reviewers receive a stable hosted URL for each documentation pull request while
production remains on S3 and CloudFront. The build stage can run for forked pull
requests without receiving secrets, and the publisher treats the result as data
rather than executable code. A separate registrable domain prevents preview
content from sharing the production site's cookie and same-site boundary.

The design adds a bucket, CloudFront distribution, certificate, DNS records,
GitHub environment, AWS role, two-stage workflow, and cleanup process. External
previews need a human publication decision. The team must operate another
public content surface and respond to abuse or unexpected cost even though its
AWS authority is isolated from production.

Client redirects return an HTML page rather than an HTTP 301 or 302 response.
Adding server-side redirects later requires its own validated data path and
edge-deployment decision.

## See also

- [Issue 1009: Evaluate PR previews separately from docs authoring](https://github.com/stacklok/mecatl/issues/1009)
- [Issue 1008: Define link validation and post-launch redirect policy](https://github.com/stacklok/mecatl/issues/1008)
- [ADR 0002: Documentation lifecycle](./0002-documentation-lifecycle.md)
- [ADR 0321: Canonical user documentation ownership](./0321-canonical-user-documentation-ownership.md)
- Production site deployment: `.github/workflows/deploy-site.yml`
