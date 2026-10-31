package format_test

import (
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
)

func TestRecordClone(t *testing.T) {
	for _, payload := range [][]byte{nil, {}, {0, 0xff, 'x'}} {
		source := format.Record{Kind: "unknown.kind", KindVersion: -1, Payload: payload}
		cloned := source.Clone()
		if !reflect.DeepEqual(cloned, source) {
			t.Fatalf("clone = %#v; want %#v", cloned, source)
		}
		if len(payload) == 0 {
			continue
		}
		source.Payload[0] = 1
		if cloned.Payload[0] != 0 {
			t.Fatal("source payload mutation changed clone")
		}
		cloned.Payload[1] = 2
		if source.Payload[1] != 0xff {
			t.Fatal("cloned payload mutation changed source")
		}
	}
}

func TestCommitClone(t *testing.T) {
	source := commitFixture(t)
	cloned := source.Clone()
	if want := commitFixture(t); !reflect.DeepEqual(cloned, want) {
		t.Fatalf("clone = %#v; want %#v", cloned, want)
	}

	for i := range source.Records {
		source.Records[i].Kind = "source"
		source.Records[i].Payload[0] = 0xff
	}
	if want := commitFixture(t); !reflect.DeepEqual(cloned, want) {
		t.Fatal("source record or nested payload mutation changed clone")
	}
	for i := range cloned.Records {
		cloned.Records[i].Kind = "clone"
		cloned.Records[i].Payload[0] = 0xfe
		if source.Records[i].Kind != "source" || source.Records[i].Payload[0] != 0xff {
			t.Fatal("cloned record or nested payload mutation changed source")
		}
	}
}

func TestCommitClonePreservesEmptyAndInvalidValues(t *testing.T) {
	for _, records := range [][]format.Record{
		nil,
		{},
		{{Payload: nil}, {Payload: []byte{}}},
	} {
		source := format.Commit{FormatVersion: -1, Records: records}
		if cloned := source.Clone(); !reflect.DeepEqual(cloned, source) {
			t.Fatalf("clone = %#v; want %#v", cloned, source)
		}
	}
}

func TestRecordEqual(t *testing.T) {
	base := format.Record{Kind: "unknown.kind", KindVersion: 7, Payload: []byte{0, 0xff}}
	for name, other := range map[string]format.Record{
		"kind":    {Kind: "other", KindVersion: 7, Payload: []byte{0, 0xff}},
		"version": {Kind: "unknown.kind", KindVersion: 8, Payload: []byte{0, 0xff}},
		"payload": {Kind: "unknown.kind", KindVersion: 7, Payload: []byte{0, 0xfe}},
		"length":  {Kind: "unknown.kind", KindVersion: 7, Payload: []byte{0}},
	} {
		t.Run(name, func(t *testing.T) {
			if base.Equal(other) || other.Equal(base) {
				t.Fatal("different records compared equal")
			}
		})
	}
	if other := (format.Record{Kind: base.Kind, KindVersion: 7, Payload: []byte{0, 0xff}}); !base.Equal(other) || !other.Equal(base) {
		t.Fatal("independently allocated equal records compared unequal")
	}
	if !(format.Record{}).Equal(format.Record{Payload: []byte{}}) || !(format.Record{Payload: []byte{}}).Equal(format.Record{}) {
		t.Fatal("nil and empty payloads must compare equal, without validation")
	}
	// Opaque payload equality is byte equality, not decoded JSON equivalence.
	if (format.Record{Payload: []byte(`{"a":1}`)}).Equal(format.Record{Payload: []byte(`{ "a": 1 }`)}) {
		t.Fatal("differently spelled opaque payloads compared equal")
	}
}

func TestCommitEqual(t *testing.T) {
	base := commitFixture(t)
	if other := commitFixture(t); !base.Equal(other) || !other.Equal(base) {
		t.Fatal("independently allocated equal commits compared unequal")
	}
	for _, field := range []string{"format version", "history", "parent", "record kind", "record version", "record payload", "later record payload", "record order", "record count"} {
		t.Run(field, func(t *testing.T) {
			other := commitFixture(t)
			switch field {
			case "format version":
				other.FormatVersion++
			case "history":
				other.HistoryID = historyid.HistoryID{}
			case "parent":
				other.Parent = format.CID{}
			case "record kind":
				other.Records[0].Kind = "changed"
			case "record version":
				other.Records[0].KindVersion++
			case "record payload":
				other.Records[0].Payload[0]++
			case "later record payload":
				other.Records[1].Payload[0]++
			case "record order":
				other.Records[0], other.Records[1] = other.Records[1], other.Records[0]
			case "record count":
				other.Records = other.Records[:1]
			}
			if base.Equal(other) || other.Equal(base) {
				t.Fatal("different commits compared equal")
			}
		})
	}
	if !(format.Commit{}).Equal(format.Commit{Records: []format.Record{}}) || !(format.Commit{Records: []format.Record{}}).Equal(format.Commit{}) {
		t.Fatal("nil and empty record groups must compare equal, without validation")
	}
	a := format.Commit{Records: []format.Record{{Payload: nil}}}
	b := format.Commit{Records: []format.Record{{Payload: []byte{}}}}
	if !a.Equal(b) || !b.Equal(a) {
		t.Fatal("nested nil and empty payloads must compare equal")
	}
}

// commitFixture intentionally combines root and successor fields: DTO cloning
// and equality must preserve data without claiming format validity.
func commitFixture(t *testing.T) format.Commit {
	t.Helper()
	h, err := historyid.ParseHistoryID("HST1111111111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	parent, err := format.ParseCID(emptyDigestCID)
	if err != nil {
		t.Fatal(err)
	}
	return format.Commit{
		FormatVersion: 1,
		HistoryID:     h,
		Parent:        parent,
		Records: []format.Record{
			{Kind: "unknown.first", KindVersion: 2, Payload: []byte{0, 0xff, 'x'}},
			{Kind: "unknown.second", KindVersion: 3, Payload: []byte{'y'}},
		},
	}
}
