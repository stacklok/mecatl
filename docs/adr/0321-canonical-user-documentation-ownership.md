# ADR 0321 — Canonical user documentation ownership

- Status: Accepted
- Date: 2026-09-09
- Scope: user-facing guides, operator reference, API reference, and contributor guidance
- Supersedes: ADR 0002's assignment of user-facing usage truth to `docs/usage.md`
- Superseded by: —

## Context

Mecatl accumulated two substantial user-facing documentation trees.
`docs/usage.md` and `docs/usage/` were the living operator reference, while
`user-docs/` was rendered as the public Docusaurus site. Both trees described
deployment, configuration, permissions, providers, APIs, and troubleshooting.
Readers left the site for raw Markdown, and contributors had to decide which
copy to update.

The public site already contains task, feature, deployment, and extension-point
pages. It can also render lookup-oriented reference pages. Keeping a separate
usage tree no longer provides a useful ownership boundary.

## Decision

Make `user-docs/` the single source for user-facing guidance and reference
material. Organize content by reader need:

- task and concept guidance stays in `mecatui/`, `building/`, and `features/`;
- exact configuration and client API contracts live in `reference/`;
- the configuration generator writes directly to
  `user-docs/reference/configuration.md`;
- gRPC and HTTP/SSE references render from `user-docs/reference/` and link to
  the task-oriented transport guide.

Keep short files at the former `docs/usage` paths only as compatibility pointers
for historical ADR links. They contain no normative behavior and are not an
authoring surface. Active instructions, skills, help text, and internal living
docs must point to the owning `user-docs/` page or its rendered URL.

The protobuf contracts remain the gRPC wire source of truth. Changes to a gRPC
service or message must update the rendered reference in the same change until
the reference is generated directly from protobuf descriptors. The handwritten
HTTP/SSE router remains the current wire source. A future OpenAPI source should
be generated from or checked against that router and rendered in Docusaurus;
SSE framing, reconnect behavior, and stream-control semantics that OpenAPI cannot
express stay in the adjacent handwritten reference.

## Consequences

Readers can complete a journey and consult exact reference material without
leaving the site. Contributors have one user-facing destination, and generated
configuration facts retain one schema-backed source.

The public content tree becomes larger and its build is the required validation
for operator and API documentation. The gRPC and HTTP/SSE references remain
manual until their planned generators exist, so contract changes still require
an explicit documentation review. Historical links continue through small
compatibility files, which must never regain substantive content.

## See also

- User documentation authoring contract: `user-docs/_README.md`
- [Reference index](https://mecatl.dev/docs/reference/)
- [ADR 0002](./0002-documentation-lifecycle.md)
- [Issue 1029](https://github.com/stacklok/mecatl/issues/1029)
