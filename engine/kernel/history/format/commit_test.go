package format_test

import (
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
)

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

func TestCommitEqual(t *testing.T) {
	base := commitFixture(t)
	if other := commitFixture(t); !base.Equal(other) || !other.Equal(base) {
		t.Fatal("independently allocated equal commits compared unequal")
	}
	for _, field := range []string{"format version", "sequence", "history", "parent", "record kind", "record version", "record payload", "later record payload", "record order", "record count"} {
		t.Run(field, func(t *testing.T) {
			other := commitFixture(t)
			switch field {
			case "format version":
				other.FormatVersion++
			case "sequence":
				other.Sequence++
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
		Sequence:      2,
		HistoryID:     h,
		Parent:        parent,
		Records: []format.Record{
			{Kind: "unknown.first", KindVersion: 2, Payload: []byte{0, 0xff, 'x'}},
			{Kind: "unknown.second", KindVersion: 3, Payload: []byte{'y'}},
		},
	}
}
