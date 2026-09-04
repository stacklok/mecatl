package port

import (
	"context"
	"errors"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

// SessionLineageState is the closed persistence state of a lineage record.
type SessionLineageState string

const (
	// SessionLineageRetained identifies a session whose snapshot remains stored.
	SessionLineageRetained SessionLineageState = "retained"
	// SessionLineagePruned identifies a content-deleted lineage tombstone.
	SessionLineagePruned SessionLineageState = "pruned"
)

// MaxSessionLineageRecords is the largest direct-related result a reader may return.
const MaxSessionLineageRecords = 256

// ErrSessionLineageUnsupported marks a store without durable lineage support.
var ErrSessionLineageUnsupported = errors.New("port: store does not support session lineage")

// ErrInvalidSessionLineageQuery marks an invalid or over-limit lineage query.
var ErrInvalidSessionLineageQuery = errors.New("port: invalid session lineage query")

// SessionLineageRecord is the content-free durable identity of one session.
// Pruned records retain only a non-reversible owner-scope token, never Principal
// PII. Retained records are always snapshot-revalidated before authorization.
// There is no tombstone-expiry API: conforming stores retain them indefinitely
// (including across restart and later reuse of the same session ID).
type SessionLineageRecord struct {
	ID           session.SessionID
	Kind         session.SessionKind
	Relationship session.SessionRelationship
	OwnerScope   [32]byte
	Incarnation  string
	State        SessionLineageState
	DeletedAt    time.Time
}

// SessionLineageQuery asks for records in one exact root lifetime. Root records
// (including tombstones) are returned for audit; a direct child must name both
// RootID and RootIncarnation in Parent, Origin, or DebugTarget relationship fields.
// RecordID and RecordIncarnation are an optional pair selecting one exact direct
// child from that partition. Exact queries return at most one record and never
// enumerate sibling edges.
type SessionLineageQuery struct {
	RootID            session.SessionID
	RootIncarnation   session.IncarnationID
	RecordID          session.SessionID
	RecordIncarnation session.IncarnationID
	Limit             int
}

// SessionLineageResult contains the current retained root first when present,
// followed by historical root tombstones, then direct records ordered by session
// ID, retained incarnation before tombstones, and incarnation as the final tie
// breaker. Truncated reports that additional records existed beyond Limit.
type SessionLineageResult struct {
	Records   []SessionLineageRecord
	Truncated bool
}

// SessionLineageReader is the OPTIONAL bounded durable-lineage capability of a
// SessionStore. Implementations query only their content-free index; they never
// discover relationships by parsing session IDs or loading transcripts.
type SessionLineageReader interface {
	ReadSessionLineage(ctx context.Context, query SessionLineageQuery) (SessionLineageResult, error)
}

// ValidateSessionLineageQuery applies the shared query bounds.
func ValidateSessionLineageQuery(query SessionLineageQuery) error {
	exact := query.RecordID != "" || query.RecordIncarnation != ""
	if query.RootID == "" || !query.RootIncarnation.Valid() || query.Limit <= 0 || query.Limit > MaxSessionLineageRecords ||
		exact && (query.RecordID == "" || query.RecordID == query.RootID || !query.RecordIncarnation.Valid()) {
		return ErrInvalidSessionLineageQuery
	}
	return nil
}
