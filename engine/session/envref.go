// Package session — EnvironmentRef.
//
// EnvironmentRef is the sole durable identity of a session's execution
// environment. It names a provider family, an opaque provider-owned ID, and
// the exact provider revision required for reattachment. Live workspaces and
// command runners remain runtime capabilities in tool.Environment and are
// never persisted here.
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
// is the complete durable placement identity: all three fields are required
// and compared exactly during reattachment. The session package stores but
// never interprets the values.
type EnvironmentRef struct {
	// Kind names the backend family (local / mem / nofs / …).
	Kind EnvironmentKind
	// ID is the opaque backend identity the adapter that minted the
	// Environment owns. It is never parsed by the session package.
	ID string
	// Revision pins the exact provider inventory generation.
	Revision string
}

// Valid reports whether every component required for exact reattachment is
// present. The session domain deliberately does not interpret any component.
func (r EnvironmentRef) Valid() bool {
	return r.Kind != "" && r.ID != "" && r.Revision != ""
}
