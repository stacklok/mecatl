# Adapter changelog

Changes to the public adapter API follow [COMPATIBILITY.md](COMPATIBILITY.md).

## [Unreleased]

### Added

- Compatible additive `mecatl.driver.v1` protobuf wire and generated-Go API
  fields on `ProposalEvidence` and `LearnedSkillEvidenceRef`: `protocol` (7),
  optional `original_message` (8), `aggregate_digest` (9), and `manifest_index`
  (10).
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

### Changed

- **Breaking minor (`v0.Y+1.0`)**: `grpcdriver.SnapshotFormat` and
  `grpcdriver.EventLogFormat` now write the identity-aware `/2` envelopes
  (`sessnap-json/2` and `eventlog-json/2`). Decoding wrappers read shipped `/1`
  and `/2` data but write `/2`; opaque drivers preserve each tag and payload
  byte-for-byte. Drain and stop old harness readers and writers, then upgrade
  both independently configured snapshot and event decoding drivers; upgrade the
  harness and clients together, then resume writers. See the [storage upgrade
  runbook](../user-docs/operating/session-storage-operations.md#upgrade-to-api-major-2).
  Old readers reject `/2`; mixed writers and rollback after `/2` writes are
  unsupported. The `session-store/1` opaque driver contract marker is unchanged.

The initial module extraction otherwise has no runtime or protobuf wire-contract
change. The initial `v0.1.0` tags are pending human release; support and driver
must be published before adapters. Engine source and dependency graph are
unchanged.
