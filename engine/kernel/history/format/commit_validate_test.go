package format_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
)

// validRoot is a deterministic public-seam fixture with an opaque payload.
func validRoot(t testing.TB) format.Commit {
	t.Helper()
	h, err := historyid.ParseHistoryID("HST1111111111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	return format.Commit{FormatVersion: 1, Sequence: 1, HistoryID: h, Records: []format.Record{{Kind: "x", KindVersion: 1, Payload: []byte{0xff, 0}}}}
}

func TestCommitValidate(t *testing.T) {
	root := validRoot(t)
	parent, err := format.ParseCID(emptyDigestCID)
	if err != nil {
		t.Fatal(err)
	}
	successor := root.Clone()
	successor.HistoryID = historyid.HistoryID{}
	successor.Sequence = 2
	successor.Parent = parent
	for _, c := range []format.Commit{root, successor} {
		before := c.Clone()
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, c) {
			t.Fatal("validation mutated commit")
		}
	}
	for _, sequence := range []int64{-1, 0, 1} {
		c := successor.Clone()
		c.Sequence = sequence
		if err := c.Validate(); err == nil {
			t.Fatalf("successor sequence %d accepted", sequence)
		}
	}
	for _, change := range []func(*format.Commit){
		func(c *format.Commit) { c.FormatVersion = 0 },
		func(c *format.Commit) { c.FormatVersion = -1 },
		func(c *format.Commit) { c.FormatVersion = 2 },
		func(c *format.Commit) { c.Sequence = 0 },
		func(c *format.Commit) { c.Sequence = -1 },
		func(c *format.Commit) { c.Sequence = 2 },
		func(c *format.Commit) { c.HistoryID = historyid.HistoryID{} },
		func(c *format.Commit) { c.Parent = parent },
		func(c *format.Commit) { c.Records = nil },
		func(c *format.Commit) { c.Records = []format.Record{} },
		func(c *format.Commit) {
			c.Records = append(c.Records, format.Record{Kind: "secret-kind", KindVersion: -1, Payload: []byte("secret-payload")})
		},
	} {
		c := root.Clone()
		change(&c)
		before := c.Clone()
		err := c.Validate()
		if err == nil {
			t.Fatal("invalid commit accepted")
		}
		if !reflect.DeepEqual(before, c) {
			t.Fatal("validation mutated invalid commit")
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatal("error reflected record contents")
		}
		if len(c.Records) == 2 && !strings.Contains(err.Error(), "record 1") {
			t.Fatal("record error lacks index")
		}
	}
	large := root.Clone()
	large.Records[0].Kind = strings.Repeat("x", 1<<20)
	large.Records[0].Payload = make([]byte, 2<<20)
	if err := large.Validate(); err != nil {
		t.Fatalf("Validate must be budget independent: %v", err)
	}
}

func TestCodecEntryPoints(t *testing.T) {
	limits := format.Limits{MaxCommitBytes: 1024, MaxRecords: 10, MaxKindBytes: 128, MaxPayloadBytes: 128}
	c := validRoot(t)
	data, cid, err := format.EncodeCommit(c, limits)
	if err != nil {
		t.Fatal(err)
	}
	decoded, gotCID, err := format.DecodeCommit(data, limits)
	if err != nil || !decoded.Equal(c) || cid != gotCID {
		t.Fatalf("round trip failed: %v", err)
	}
}
