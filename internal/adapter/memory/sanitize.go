package memory

import "github.com/stacklok/mecatl/engine/prompt"

// Compile-time assertion that *Store satisfies the prompt-defined UserModelSource
// port. *Store also satisfies the structurally identical MemoryIndexSource via
// Index; composition chooses whether an instance is project- or user-scoped.
var _ prompt.UserModelSource = (*Store)(nil)
