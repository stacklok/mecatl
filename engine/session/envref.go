// Package session — EnvironmentRef.
//
// This file is part of issue #462 (ADR 0105): the in-process
// Environment seam. EnvironmentRef is the small, cycle-safe, stdlib-only
// identity value a tool.Environment carries. It names the backend FAMILY a
// Workspace/CommandRunner pair was minted against (local, mem, nofs, later
// remote) plus an opaque backend identity string, WITHOUT pulling any tool
// type into the session package. It is intentionally a plain struct (not a
// pointer) so it is comparable and never escapes to the heap on the hot
// dispatch path.
//
// PHASE 3 (ADR 0106): EnvironmentRef is now a snapshot field. It persists
// across a process restart as an inert exported field on session.Session
// (sessnap.Snapshot.EnvironmentRef), round-tripped through Of/Restore. A
// restored session with a non-zero ref reattaches a live Environment through
// composition's server.Config.EnvironmentResolver for a non-in-tree Kind;
// local/mem/nofs resolve through the existing factories. Legacy snapshots
// without the field restore the zero ref and remain backward-compatible
// (Workspace-derived local/nofs resolution stamps a fresh ref on the next
// save). Remote transport itself remains deferred — the ref is durable
// identity, not a transport contract.
package session

// EnvironmentKind names the backend family an EnvironmentRef was minted
// against. It is an open STRING label carried verbatim; the session package
// owns the well-known constants for the in-tree backends (below) but does not
// constrain the set — a future remote transport or out-of-tree backend adds
// its own label without widening this package. The empty string is the zero
// value and is treated as "unspecified".
type EnvironmentKind string

// Well-known EnvironmentKind labels. These are the labels the in-tree
// adapters mint; composition is the sole constructor site, so a future
// remote transport adds its own label without widening this package. The set
// is open: the session package defines the in-tree constants but does not
// enumerate every possible label.
const (
	// EnvKindLocal is a real-OS workspace backed by osfs + a local command
	// runner (the main session, a worktree or force-copy fork child).
	EnvKindLocal EnvironmentKind = "local"
	// EnvKindMem is an in-memory workspace (memfs) with no command runner.
	EnvKindMem EnvironmentKind = "mem"
	// EnvKindNoFS is the file-less profile (engine/adapter/nofs).
	EnvKindNoFS EnvironmentKind = "nofs"
)

// EnvironmentRef is the cycle-safe identity value an Environment carries. It
// names the backend family (Kind) and an opaque backend identity (ID) the
// adapter that minted the Environment owns. The session package owns it (not
// the tool package) so it CAN ride the session snapshot and event log without
// pulling tool types in — it is the identity half of the Environment seam,
// kept separate from the capability half (tool.Environment).
//
// PHASE 3 (ADR 0106): this is a DURABLE identity. It persists on the snapshot
// (sessnap.Snapshot.EnvironmentRef) so a restarted process can reattach a live
// Environment to the SAME backend. The session package interprets NEITHER
// field — it only stores and carries them. A zero ref restored from a legacy
// snapshot is stamped from the first successfully resolved live Environment so
// the next ordinary save persists it (no migration sweep).
//
// Kind is a backend FAMILY label (see EnvironmentKind); ID is opaque backend
// identity (a workspace root, a remote container id, …). Both are compared by
// plain equality; the session package interprets NEITHER — it only stores and
// carries them. The zero value {Kind:"", ID:""} is the "unspecified" ref and
// never names a real backend; it is what an Environment built without a ref
// (legacy/test paths) carries.
type EnvironmentRef struct {
	// Kind names the backend family (local / mem / nofs / …).
	Kind EnvironmentKind
	// ID is the opaque backend identity the adapter that minted the
	// Environment owns. It is never parsed by the session package.
	ID string
}
