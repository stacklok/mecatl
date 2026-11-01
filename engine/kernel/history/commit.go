package history

import "github.com/stacklok/mecatl/engine/kernel/history/format"

// Commit pairs a decoded mutable persistence DTO with its verified accepted
// reference. Ref describes the bytes read, not later mutations to the DTO.
type Commit struct {
	// Ref identifies the accepted canonical bytes from which Commit was decoded.
	Ref CommitRef
	// Commit owns the decoded DTO, records, and nested payload storage.
	Commit format.Commit
}
