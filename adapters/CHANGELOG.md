# Adapter changelog

Changes to the public adapter API follow [COMPATIBILITY.md](COMPATIBILITY.md).

## [Unreleased]

### Added

- `jsonlstore.OpenReader` and `Reader` provide explicitly read-only access to
  existing JSONL snapshots, metadata, lineage, and event cursors without source
  initialization, repair, or mutation. Metadata paging requires a prepared
  current catalog at the same canonical path.
- Initial public `github.com/stacklok/mecatl/adapters` module containing the
  complete `jsonlstore`, `redisstore`, and `grpcdriver` packages, including their
  existing write, schedule, content-source, and learning-driver exports.
- Independent generated-driver module at
  `github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver`, preserving the
  existing `/v1` package import path, and private shared support module at
  `github.com/stacklok/mecatl/internal/adaptersupport`.

This is an extraction without runtime, storage-format, or wire-behavior changes.
The initial `v0.1.0` tags are pending human release; support and driver must be
published before adapters. Engine source and dependency graph are unchanged.
