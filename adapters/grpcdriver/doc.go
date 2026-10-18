// Package grpcdriver provides public clients and server wrappers for the
// mecatl.driver.v1 store-driver protocol. Its gRPC client adapters satisfy the
// engine's store seams over a remote, operator-run driver process; the matching
// server wrappers let external Go drivers mount an in-process store.
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
// SnapshotFormat ("sessnap-json/2"). Opaque drivers store and return bytes and
// tag verbatim, without interpreting provider-private replay. Decoding wrappers
// accept /1 and /2 but return /2; the local identity schema must match the tag.
// Unknown or contradictory versions are infrastructure errors, never not-found.
//
// Drain active writers and upgrade decoding snapshot AND independently configured
// event drivers before the harness/client. Old decoding wrappers reject /2 writes
// before backend mutation. Upgraded decoding wrappers are unsupported with old
// readers; mixed writers and rollback after new writes are unsupported. The opaque
// session-store/1 capability marker is unchanged and does not negotiate formats;
// event drivers have no handshake. Redis's private outer tag stays /1, so it does
// not protect old local readers from schema-2 inner events.
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
