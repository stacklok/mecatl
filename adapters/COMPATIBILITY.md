# Adapter compatibility and release policy

This policy covers `github.com/stacklok/mecatl/adapters`. For construction,
capability checks, read semantics, and resource ownership, use the
[SessionStore and EventLog guide](../user-docs/building/extension-points/session-store.md).
The [driver architecture](../docs/architecture/observability.md#remote-store--source-drivers-adaptersgrpcdriver)
owns protocol integration details. [ADR 0372](../docs/adr/0372-public-persistence-adapters.md)
records the publication decision.

## Public Go API

All exported identifiers, fields, and methods in `jsonlstore`, `redisstore`, and
`grpcdriver` are public API. This includes the retained write, schedule,
content-source, and learning-driver APIs, not only snapshot and log readers.
The private `github.com/stacklok/mecatl/internal/adaptersupport` module holds
shared watcher and soul-validation implementation; its exports are not an
application extension API.

While these modules are pre-v1:

- Minor releases (`v0.Y+1.0`) carry additive or breaking API changes. Record every
  exported API change in [CHANGELOG.md](CHANGELOG.md): `Added` is additive;
  `Changed`, `Deprecated`, and `Removed` entries explicitly identify breaks.
- Patch releases (`v0.Y.Z+1`) carry fixes without API additions or breaks.
- At v1, breaking Go API changes require a major version and the corresponding
  Go module import-path change.

This follows the engine/provider pre-v1 versioning discipline, but it is a
separate compatibility commitment. The engine's eight-package API snapshot gate
**does not cover adapters**. Review adapter exports and changelog classifications
explicitly; backend conformance and integration tests exercise behavior, not a
complete exported-API diff.

## Modules and protocol versions

|Module path after `github.com/stacklok/mecatl/`|Repository directory|Independent tag prefix|
|-|-|-|
|`internal/adaptersupport`|`internal/adaptersupport/`|`internal/adaptersupport/v`|
|`contracts/gen/go/mecatl/driver`|`contracts/gen/go/mecatl/driver/`|`contracts/gen/go/mecatl/driver/v`|
|`adapters`|`adapters/`|`adapters/v`|

The generated package remains
`github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1`; `v1` there names
the protobuf protocol package, not the Go module's major version. Its parent
module can therefore have a `v0.1.0` release while serving `mecatl.driver.v1`.
Root `v*` tags do not publish these nested modules. Their versions are independent
of root and engine releases.

Go module versioning and protobuf wire compatibility are separate obligations.
Preserve field numbers, message/service identities, and existing RPC semantics
within `mecatl.driver.v1`; reserve removed fields and use compatible additions.
A breaking wire contract needs an explicit protocol-version decision, not merely
a Go minor bump. Conversely, a generated Go API change can need a module version
bump even when the wire encoding stays compatible. Exact capability-contract
markers and optional capability negotiation still govern runtime interoperability;
matching module versions alone do not prove that a backend supports an operation.
Edit protobuf sources under `contracts/proto/mecatl/driver/v1/` and regenerate
through `task generate`, never by editing generated Go output.

The adapter manifest requires the real engine baseline
`v0.15.1-0.20260929125653-6142f5252a09` and the independently tagged
`v0.1.0` support and driver modules. Engine-only applications do not acquire
Redis, gRPC, the root host, or adapters. Root `go.mod` replacements and
`go.work` are checkout conveniences, not external installation requirements.

## Release verification

The `adapters/v0.1.0` tag retains dependency pins to a revision that the Go
module resolver cannot fetch. Use `adapters/v0.1.1` or later for standalone
installation; the existing `v0.1.0` tag is immutable.

Release dependency-first from reviewed commits:

1. For each dependency module, run `GOWORK=off go mod tidy -diff` and
   `GOWORK=off go test ./...` before publishing its independent tag.
2. In `adapters/`, run `GOWORK=off go mod tidy -diff` and
   `GOWORK=off go test ./...` against the pinned public dependencies. Review
   `go.sum` and the dependency tags.
3. A human publishes the adapters tag after confirming installation without
   workspace or replace directives against the public checksum-backed proxy.

## Module tests

From `adapters/`, run:

```sh
GOWORK=off go test ./...
GOWORK=off go test -race ./...
```

These commands include the cross-adapter tests in `integration/`, using pinned
module dependencies and Go's normal download and build caches. The repository's
`task test` and `task test:race` already include this module.

Tests use local fixtures, not live services. They do not verify public tag
availability, real Redis ACL behavior, or read-only filesystem access.
