# ADR 0372 - Publish the existing persistence adapters as independent Go modules

- Status: Accepted
- Date: 2026-09-29
- Scope: publication, module layout, and compatibility of the existing concrete persistence and gRPC driver packages
- Supersedes: [ADR 0005](0005-driver-seams.md), only the deferred public promotion of driver clients and server wrappers

## Context

An external Go application needs to consume the built-in stores without importing
the host's internal packages or reimplementing their persistence behavior. ADR
0005 deferred public driver promotion because every exported identifier would
become a compatibility commitment. The directing human explicitly authorized this
publication and its documentation without a separate plan PR, with no behavioral
changes. This record does not authorize new runtime design.

## Decision

Publish the complete existing `jsonlstore`, `redisstore`, and `grpcdriver`
packages in `github.com/stacklok/mecatl/adapters`. Retain their concrete APIs,
including writes, schedules, content sources, and learning drivers; do not create
a reader-only fork.

Place the generated driver package in the independently versioned parent module
`github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver`. Preserve its existing
`/v1` Go import path and the `mecatl.driver.v1` protocol. Keep shared watcher and
soul-validation implementation private in
`github.com/stacklok/mecatl/internal/adaptersupport`. Watcher ownership, cleanup,
and restart behavior remain with the existing callers.

Leave engine source and its dependency graph unchanged. Adapters depend on the
real engine baseline `v0.15.1-0.20260929125653-6142f5252a09`; root and workspace
replacements serve only checkout development. External applications must resolve
the independently published dependencies without a root-module dependency.

Cover all adapter exports with a distinct compatibility policy and classified
changelog. Pre-v1 minor releases carry additive or explicitly classified breaking
API changes; patch releases contain fixes without API additions or breaks. The
engine's eight-package API guard does not guard these packages. Protobuf wire
compatibility remains separate from generated Go module versions.

Release support and driver before adapters, using independent nested tags:
`internal/adaptersupport/v0.1.0`,
`contracts/gen/go/mecatl/driver/v0.1.0`, then `adapters/v0.1.0`. These initial tags
remain subject to human publication. Ordinary standalone builds and tidy do not
wait for tags: the adapter manifest pins both dependencies to the public,
checksum-verified `v0.0.0-20260929205240-3fd4c343ca56` revision and commits its
real standalone sums.

## Consequences

External applications can reuse the existing implementations and conformance
suites. Maintainers take on compatibility review for the complete concrete API
and a dependency-first release sequence. Standalone module tests use the pinned
dependencies without workspace replacements, but cannot prove publication or
backend ACL compatibility. No storage format, protocol behavior, authorization boundary,
constructor side effect, or resource lifetime changes as part of this extraction.

## See also

- [Adapter compatibility and release policy](../../adapters/COMPATIBILITY.md)
- [Use built-in stores and remote drivers](../../user-docs/building/extension-points/session-store.md)
- [Persistence and driver architecture](../architecture/observability.md)
- [Maintained resource inventories](0027-cloud-native.md#list-1-resource-inventory)
