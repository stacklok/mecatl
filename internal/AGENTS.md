# AGENTS.md: internal

## Where to change what

- A `settings.yaml` key is a field on a `*Section` struct in
  `adapter/permconfig/schema.go`. Its doc comment becomes the row in the
  [configuration reference](../user-docs/reference/configuration.md); the generator
  reads comments from that file only. A new top-level section also needs a subtree with a pinned `Tier` in
  `configgen/build.go`. Regenerate with `task docs:configref`.
- `Service.appendEvent` in `adapter/server/service.go` is the only place that stamps
  `session.Event.Actor`, from the verified caller in the request context, not from
  ownership. Engine emit sites leave `Actor` nil; do not add a second stamping path.

## Invariants

- Placement is server-owned (`app/placement.go`): exact
  `EnvironmentRef{Kind, ID, Revision}`, fail-closed reattachment, no public paths or
  cwd-based inference. A binding's governance root (the host checkout) is not its
  execution root; see [placement](../docs/architecture/microvm-environments.md#exact-reattachment).
- Operator `command_runner.environment.inherit` grants reach built-in main runners and
  direct-write children, not hardened children or internal Git. Reserved harness
  credential names stay scrubbed even when listed.
