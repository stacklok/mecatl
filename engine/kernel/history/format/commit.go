package format

import (
	"errors"
	"fmt"
	"slices"

	"github.com/jcalabro/atmos/cbor"

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

// Validate checks the v1 commit envelope and every record, without mutation,
// cloning, freezing, or resource budgets. Keep the input stable during the call.
// It establishes neither ancestry, history membership, acceptance, authorization,
// nor payload schema validity.
func (c Commit) Validate() error {
	if c.FormatVersion != 1 {
		return errors.New("unsupported commit format version")
	}
	if (c.Parent == (CID{})) == (c.HistoryID == (historyid.HistoryID{})) {
		return errors.New("commit requires exactly one of history ID or parent")
	}
	if len(c.Records) == 0 {
		return errors.New("commit requires records")
	}
	for i, r := range c.Records {
		if err := r.Validate(); err != nil {
			return fmt.Errorf("record %d: %w", i, err)
		}
	}
	return nil
}

// EncodeCommit validates and encodes a v1 commit as canonical DAG-CBOR and hashes
// those exact bytes with CIDv1/DAG-CBOR/SHA-256. The handwritten codec is limited
// to this small, closed v1 envelope; use schema-driven generation before adding
// persisted types. The output owns its storage; keep the DTO stable during the
// call. Errors return nil bytes and an unset CID. Success establishes no ancestry,
// membership, acceptance, authorization, or payload schema validity.
func EncodeCommit(c Commit, limits Limits) ([]byte, CID, error) {
	if err := limits.validate(); err != nil {
		return nil, CID{}, err
	}
	if err := c.Validate(); err != nil {
		return nil, CID{}, err
	}
	size, err := c.encodedSize(limits)
	if err != nil {
		return nil, CID{}, err
	}
	out := make([]byte, 0, size)
	if c.Parent == (CID{}) {
		// Root fields are h, p, r, v; h is required and p is canonical null.
		out = cbor.AppendMapHeader(out, 4)
		out = cbor.AppendText(out, "h")
		out = cbor.AppendText(out, c.HistoryID.String())
		out = cbor.AppendText(out, "p")
		out = cbor.AppendNull(out)
	} else {
		// Successor fields are p, r, v; p is required and h is omitted.
		out = cbor.AppendMapHeader(out, 3)
		out = cbor.AppendText(out, "p")
		out = append(out, 0xd8, 0x2a)
		var link [37]byte
		copy(link[1:], c.Parent.binary[:])
		out = cbor.AppendBytes(out, link[:])
	}
	out = cbor.AppendText(out, "r")
	out = cbor.AppendArrayHeader(out, uint64(len(c.Records)))
	for _, r := range c.Records {
		out = r.appendCBOR(out)
	}
	out = cbor.AppendText(out, "v")
	out = cbor.AppendInt(out, c.FormatVersion)
	return out, contentCID(out), nil
}

// encodedSize checks commit budgets and computes exact size before allocation.
// This metadata-only pass avoids buffer growth without scanning payload contents.
// Subtraction-based accounting avoids overflowing int even with maximal limits.
func (c Commit) encodedSize(l Limits) (int, error) {
	if len(c.Records) > l.MaxRecords {
		return 0, errors.New("record count exceeds limit")
	}
	total := 0
	add := func(n int) bool {
		if n > l.MaxCommitBytes-total {
			return false
		}
		total += n
		return true
	}
	// Root overhead is the map, four keys, null, and version (11 bytes).
	// Successor overhead includes the 41-byte tagged CID link (49 bytes).
	if c.Parent == (CID{}) {
		h := c.HistoryID.String()
		if !add(11) || !add(headerSize(len(h))) || !add(len(h)) {
			return 0, errors.New("commit bytes exceed limit")
		}
	} else if !add(49) {
		return 0, errors.New("commit bytes exceed limit")
	}
	if !add(headerSize(len(c.Records))) {
		return 0, errors.New("commit bytes exceed limit")
	}
	for i, r := range c.Records {
		size, err := r.encodedSize(l)
		if err != nil {
			return 0, fmt.Errorf("record %d: %w", i, err)
		}
		if !add(size) {
			return 0, errors.New("commit bytes exceed limit")
		}
	}
	return total, nil
}

// DecodeCommit accepts only the closed, canonical v1 DAG-CBOR schema and hashes
// the exact accepted input. Root fields are h, p, r, v; successor fields are p,
// r, v. Every record has k, p, v in that order, and payload remains opaque.
// Strict decoding preserves canonical wire bytes and pre-allocation budgets. Decoded
// strings and bytes own their storage. Keep input stable during the call. Every
// error returns a zero Commit and CID. Success establishes no ancestry, membership,
// acceptance, authorization, or payload schema validity.
func DecodeCommit(data []byte, limits Limits) (Commit, CID, error) {
	if err := limits.validate(); err != nil {
		return Commit{}, CID{}, err
	}
	if len(data) > limits.MaxCommitBytes {
		return Commit{}, CID{}, errors.New("commit bytes exceed limit")
	}
	d := cborDecoder{data: data}
	fields := d.header(5)
	var c Commit
	switch fields {
	case 4:
		d.key('h')
		h := string(d.scalar(3, limits.MaxCommitBytes))
		if d.err == nil {
			var err error
			c.HistoryID, err = historyid.ParseHistoryID(h)
			if err != nil {
				d.err = errors.New("invalid history ID")
			}
		}
		d.key('p')
		d.literal(0xf6)
	case 3:
		d.key('p')
		c.Parent = d.link()
	default:
		d.err = errors.New("expected root or successor map")
	}
	d.key('r')
	count := d.header(4)
	// A minimal record is 11 bytes. Check plausibility before allocating.
	if d.err == nil && (count == 0 || count > uint64((len(data)-d.pos)/11)) { // #nosec G115 -- the valid cursor leaves a nonnegative byte count.
		d.err = errors.New("invalid or excessive record count")
	}
	if d.err == nil {
		recordCount := int(count) // #nosec G115 -- count is bounded by an int quotient above.
		if recordCount > limits.MaxRecords {
			d.err = errors.New("invalid or excessive record count")
		} else {
			c.Records = make([]Record, recordCount)
			for i := range c.Records {
				c.Records[i] = decodeRecord(&d, limits)
				if d.err != nil {
					d.err = fmt.Errorf("record %d: %w", i, d.err)
					break
				}
			}
		}
	}
	d.key('v')
	c.FormatVersion = d.version()
	if d.err != nil {
		return Commit{}, CID{}, d.err
	}
	if d.pos != len(data) {
		return Commit{}, CID{}, errors.New("trailing commit data")
	}
	if err := c.Validate(); err != nil {
		return Commit{}, CID{}, err
	}
	return c, contentCID(data), nil
}
