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
explicitly; backend conformance tests and the standalone consumer proof exercise
behavior and packaging, not a complete exported-API diff.

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
`v0.15.1-0.20260929125653-6142f5252a09`. Publication leaves engine source and its
dependency graph unchanged: an engine-only application does not acquire Redis,
gRPC, the root host, or adapters. Root `go.mod` replacements and `go.work` are
checkout conveniences, not external installation requirements.

## Initial release sequence

The initial support, driver, and adapter `v0.1.0` tags are **pending human
publication**. Support and driver already have real dependency sums; the final
standalone `adapters/go.sum` is absent until those dependencies are published.
Do not invent published checksums or commit candidate-proxy sums as release sums.

From reviewed commits, release dependency-first:

1. Run `task release:persistence-support-preflight` and
   `task release:persistence-driver-preflight`. Each runs
   `scripts/preflight-persistence-module-release.sh` for its module with
   `GOWORK=off`, the public Go proxy, checksum verification, and `go mod tidy -diff`.
   A human then publishes `internal/adaptersupport/v0.1.0` and
   `contracts/gen/go/mecatl/driver/v0.1.0`. Neither depends on the other.
2. After both versions resolve from the public proxy, generate the real adapter
   sums with `cd adapters && GOWORK=off go mod tidy`. Review and commit the actual
   sums through the normal human-directed release process.
3. Run `task release:persistence-adapters-preflight`. It first downloads both
   published dependencies through the public checksum-backed proxy, refuses an
   absent adapter sum file, and requires standalone `go mod tidy -diff` to pass.
   Run standalone adapter tests with `cd adapters && GOWORK=off go test ./...`.
4. A human publishes `adapters/v0.1.0` from the later reviewed commit containing
   those real sums. Confirm external installation without workspace or replace
   directives before announcing availability.

The preflight script is a networked manual gate, not a publisher or an offline
CI check. It uses a checkout-local release module cache and never creates or
pushes tags. A local tag or workspace build is not proof of public availability.

## Offline candidate verification

Run this from the repository root, choosing a fresh output directory:

```sh
sh scripts/prove-external-persistence.sh .scratch/external-proof-N
# Optional race verification, also with a fresh directory:
sh scripts/prove-external-persistence.sh .scratch/external-proof-race-N -race
```

The helper builds a file-only module proxy from candidate archives and cached
third-party dependencies. Only staged manifests use candidate `v0.1.0-dev`
versions. It runs standalone module tests and an unrelated external consumer with
`GOWORK=off`, no replacements, an isolated module cache, and no network fallback.
It checks the external and engine-only graphs and leaves the checkout's release
requirements unchanged. Missing cached artifacts fail the proof.

This proves candidate packaging and the exercised integration contracts, **not
live tag publication**, real Redis ACL behavior, or read-only filesystem access.
See the [external consumer fixture](../integration/external-persistence-consumer/README.md)
for its scope and limitations.
