package resourcegroup

// Completion tracks one instance's remaining obligations, including partial
// construction failure. Done is terminal regardless of diagnostic Err; terminal
// instances may acquire no new dependencies or restart work. Completion does not
// prove all external effects ceased or release dependencies still used by
// uncooperative work. Methods must be safe for concurrent coordination.
type Completion interface {
	// Done returns a stable nonnil channel closed when instance obligations are terminal.
	Done() <-chan struct{}
	// Err returns ErrNotComplete before Done, then immutable diagnostic failure
	// or nil published before Done closes. It never gates terminality.
	Err() error
}

// Loaded transfers a constructed value and its lifetime accounting to the Group.
// On success Value must be usable and nonzero (including no typed-nil pointer),
// and Completion is required. An error with Completion still transfers
// partial-instance accounting; an error without it promises completed cleanup.
type Loaded[T any] struct {
	// Value is the constructed resource; it is usable only after successful publication.
	Value T
	// Completion tracks the instance, including cleanup after partial failure.
	Completion Completion
}
