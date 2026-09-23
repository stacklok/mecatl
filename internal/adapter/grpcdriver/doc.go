// Package grpcdriver implements the harness side of the mecatl.driver.v1
// store-driver protocol: gRPC client adapters that satisfy the engine's store
// seams over a remote, operator-run driver process, plus the matching server
// wrappers a Go driver (or a test fixture) mounts over an in-process store.
//
//   - SessionStore implements port.SessionStore over SessionStoreService.
//   - MemoryStore implements tool.MemoryStore over MemoryStoreService.
//   - NewSessionStoreServer / NewMemoryStoreServer wrap an in-process store
//     as the generated server interfaces; see docs/architecture/observability.md
//     for the remote-driver boundary.
//
// # Trust model
//
// A driver is OPERATOR-CONFIGURED INFRASTRUCTURE, sitting at the same trust
// tier as an on-disk store directory (the JSONL file the jsonlstore adapter
// writes): it holds whatever the harness persists, and a corrupt or hostile
// payload from it fails the harness-side decode loudly — it never reaches the
// model silently. Sanitization of model-written memory values stays in the
// harness's memory tools; a driver is never trusted to sanitize. The server
// wrappers deliberately install no authentication or TLS: mount them only on
// a separately protected operator network, or provide those controls in the
// driver host.
//
// # Wire format and versioning
//
// Session snapshots cross the wire as an OPAQUE, format-tagged envelope: the
// payload is exactly engine/adapter/sessnap's encoding and the format tag is
// SnapshotFormat ("sessnap-json/1"). The driver stores and returns the
// envelope verbatim and never decodes it. Snapshot schema evolution lives in
// sessnap (additive JSON fields); the envelope's format tag changes ONLY if
// the encoding itself is replaced. Load rejects an unknown format with an
// infrastructure error — never ErrNotFound.
//
// # Resilience posture
//
// Deadline passthrough only: the caller's ctx deadline rides the RPC, there
// are NO retries, NO default deadline, and NO transient/permanent error
// classification — store consumers already treat Save/Load errors as unit
// failures. If drivers ever need retries/breakers the house pattern is a
// resilience DECORATOR over these clients (the llmresilience precedent), not
// knobs here. Dial is lazy (grpc.NewClient): the first RPC surfaces a
// connect error.
package grpcdriver
