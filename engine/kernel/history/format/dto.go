package format

import (
	"bytes"
	"slices"

	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
)

// Commit is a mutable persistence DTO for an atomic, nonempty ordered group of
// records. It is not an accepted-state aggregate or an immutable snapshot.
// Assignment shares record and payload storage; use Clone for a detached copy.
// Construction, cloning, and equality do not validate the format contract.
type Commit struct {
	// FormatVersion is the positive commit/record envelope version, initially 1.
	FormatVersion int64
	// HistoryID is valid for a root commit and unset for a successor.
	HistoryID historyid.HistoryID
	// Parent is unset for a root commit and names the predecessor for a successor.
	Parent CID
	// Records is the nonempty group in authoritative order.
	Records []Record
}

// Clone returns a mutable copy with detached record and nested payload storage.
// It preserves nil versus empty slices and does not validate or freeze the DTO.
// The caller must not mutate the source or its slices during cloning.
func (c Commit) Clone() Commit {
	c.Records = slices.Clone(c.Records)
	for i := range c.Records {
		c.Records[i] = c.Records[i].Clone()
	}
	return c
}

// Equal compares envelope fields and ordered records without validating them.
// Nil and empty slices compare equally, including nested record payloads.
// Neither operand's reachable storage may be mutated during comparison.
func (c Commit) Equal(other Commit) bool {
	return c.FormatVersion == other.FormatVersion &&
		c.HistoryID == other.HistoryID &&
		c.Parent == other.Parent &&
		slices.EqualFunc(c.Records, other.Records, Record.Equal)
}

// Record is a mutable persistence DTO carrying an owner-defined payload schema.
// Unknown kind/version pairs remain opaque; preservation does not establish safe
// interpretation, replay, or display. Assignment shares payload storage.
// Construction, cloning, and equality do not validate the format contract.
type Record struct {
	// Kind identifies the payload's record kind.
	Kind string
	// KindVersion is the positive payload schema version, not an instance revision.
	KindVersion int64
	// Payload is required opaque bytes. Nil and empty both represent empty bytes.
	Payload []byte
}

// Clone returns a mutable copy with detached payload storage, preserving nil
// versus empty slices. It does not validate or freeze the DTO. The caller must
// not mutate the source payload during cloning.
func (r Record) Clone() Record {
	r.Payload = bytes.Clone(r.Payload)
	return r
}

// Equal compares kind, version, and exact payload bytes without interpreting or
// validating them. Nil and empty payloads compare equally. Neither operand's
// payload may be mutated during comparison.
func (r Record) Equal(other Record) bool {
	return r.Kind == other.Kind && r.KindVersion == other.KindVersion && bytes.Equal(r.Payload, other.Payload)
}
