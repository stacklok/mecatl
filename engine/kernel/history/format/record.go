package format

import (
	"bytes"
	"errors"
	"unicode/utf8"

	"github.com/jcalabro/atmos/cbor"
)

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

// Validate checks the record envelope without interpreting its opaque payload or
// restricting kind namespaces or positive versions. It neither mutates nor
// freezes storage and applies no resource budget. Keep the input stable during
// the call. Validation establishes no acceptance or authorization.
func (r Record) Validate() error {
	if r.Kind == "" || !utf8.ValidString(r.Kind) {
		return errors.New("record kind must be nonempty UTF-8")
	}
	if r.KindVersion <= 0 {
		return errors.New("record version must be positive")
	}
	return nil
}

// appendCBOR appends the canonical k, p, v record map. Payload bytes are opaque.
func (r Record) appendCBOR(out []byte) []byte {
	out = cbor.AppendMapHeader(out, 3)
	out = cbor.AppendText(out, "k")
	out = cbor.AppendText(out, r.Kind)
	out = cbor.AppendText(out, "p")
	out = cbor.AppendBytes(out, r.Payload)
	out = cbor.AppendText(out, "v")
	return cbor.AppendInt(out, r.KindVersion)
}

// encodedSize checks record-specific limits and returns its exact canonical size.
func (r Record) encodedSize(l Limits) (int, error) {
	if len(r.Kind) > l.MaxKindBytes || len(r.Payload) > l.MaxPayloadBytes {
		return 0, errors.New("kind or payload exceeds limit")
	}
	// The record map and its three keys cost seven bytes. Account for each
	// component separately so even maximal caller budgets cannot overflow int.
	total := 0
	for _, n := range [...]int{7, headerSize(len(r.Kind)), len(r.Kind), headerSize(len(r.Payload)), len(r.Payload), positiveIntHeaderSize(r.KindVersion)} {
		if n > l.MaxCommitBytes-total {
			return 0, errors.New("commit bytes exceed limit")
		}
		total += n
	}
	return total, nil
}

// decodeRecord strictly decodes one k, p, v record map into owned DTO storage.
func decodeRecord(d *cborDecoder, limits Limits) Record {
	var r Record
	if d.header(5) != 3 && d.err == nil {
		d.err = errors.New("expected record map")
	}
	d.key('k')
	r.Kind = string(d.scalar(3, limits.MaxKindBytes))
	d.key('p')
	r.Payload = bytes.Clone(d.scalar(2, limits.MaxPayloadBytes))
	d.key('v')
	r.KindVersion = d.positiveInt64()
	return r
}
