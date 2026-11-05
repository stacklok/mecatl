package session

import (
	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
)

// prepared owns a canonical proposal bound to its original accepted head.
// Copies share private immutable storage. It proves no acceptance. SessionCommit
// retains identical bytes and identity across uncertain retries.
type prepared struct {
	commit format.Commit
	ref    history.CommitRef
}
