package format_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"reflect"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
)

// testLimits is deliberately small; no test depends on ambient codec policy.
func testLimits() format.Limits {
	return format.Limits{MaxCommitBytes: 4096, MaxRecords: 16, MaxKindBytes: 128, MaxPayloadBytes: 1024}
}

// wireFixtures are literal canonical bytes, independent of codec append helpers.
func wireFixtures(t testing.TB) (root, successor []byte) {
	t.Helper()
	records := "\x61r\x81\xa3\x61k\x61x\x61p\x42\xff\x00\x61v\x01"
	root = []byte("\xa5\x61h\x78\x19HST1111111111111111111111\x61p\xf6" + records + "\x61s\x01\x61v\x01")
	link, err := hex.DecodeString("0001711220e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
	if err != nil {
		t.Fatal(err)
	}
	successor = append([]byte("\xa4\x61p\xd8\x2a\x58\x25"), link...)
	successor = append(successor, records...)
	successor = append(successor, "\x61s\x02\x61v\x01"...)
	return root, successor
}

// assertContentCID independently builds the fixed binary profile using stdlib SHA.
func assertContentCID(t testing.TB, data []byte, cid format.CID) {
	t.Helper()
	digest := sha256.Sum256(data)
	want := cidText(append([]byte{1, 0x71, 0x12, 32}, digest[:]...))
	if cid.String() != want {
		t.Fatalf("CID = %s; want %s", cid, want)
	}
}

func TestCodecGoldens(t *testing.T) {
	rootWire, successorWire := wireFixtures(t)
	root := validRoot(t)
	successor := root.Clone()
	successor.HistoryID = historyid.HistoryID{}
	successor.Sequence = 2
	var err error
	successor.Parent, err = format.ParseCID(emptyDigestCID)
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		name   string
		commit format.Commit
		wire   []byte
	}{
		{"root", root, rootWire}, {"successor", successor, successorWire},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			data, cid, err := format.EncodeCommit(fixture.commit, testLimits())
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(data, fixture.wire) {
				t.Fatalf("wire = %x; want %x", data, fixture.wire)
			}
			if len(data) != cap(data) {
				t.Fatal("exact sizing did not match output length")
			}
			assertContentCID(t, fixture.wire, cid)
			decoded, gotCID, err := format.DecodeCommit(fixture.wire, testLimits())
			if err != nil || !decoded.Equal(fixture.commit) || gotCID != cid {
				t.Fatalf("golden decode: %v", err)
			}
			for maxBytes := 1; maxBytes <= len(data)+1; maxBytes++ {
				l := testLimits()
				l.MaxCommitBytes = maxBytes
				if maxBytes < len(data) {
					assertEncodeError(t, fixture.commit, l)
					assertDecodeError(t, data, l)
					continue
				}
				if _, _, err := format.EncodeCommit(fixture.commit, l); err != nil {
					t.Fatal(err)
				}
				if _, _, err := format.DecodeCommit(data, l); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestCodecIdentityAndOpaquePayloads(t *testing.T) {
	base := validRoot(t)
	base.Records = append(base.Records, format.Record{Kind: "arbitrary namespace / 🐈", KindVersion: math.MaxInt64, Payload: []byte(`{ "a": invalid json }`)})
	_, original, err := format.EncodeCommit(base, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*format.Commit){
		"history": func(c *format.Commit) { c.HistoryID, _ = historyid.ParseHistoryID("HST2222222222222222222222") },
		"parent": func(c *format.Commit) {
			c.HistoryID = historyid.HistoryID{}
			c.Sequence = 2
			c.Parent, _ = format.ParseCID(emptyDigestCID)
		},
		"payload":  func(c *format.Commit) { c.Records[0].Payload[0]++ },
		"kind":     func(c *format.Commit) { c.Records[0].Kind += "?" },
		"version":  func(c *format.Commit) { c.Records[0].KindVersion++ },
		"order":    func(c *format.Commit) { c.Records[0], c.Records[1] = c.Records[1], c.Records[0] },
		"grouping": func(c *format.Commit) { c.Records = c.Records[:1] },
	} {
		t.Run(name, func(t *testing.T) {
			c := base.Clone()
			mutate(&c)
			_, cid, err := format.EncodeCommit(c, testLimits())
			if err != nil || cid == original {
				t.Fatalf("identity change failed: %v", err)
			}
		})
	}
	// Successors can carry any positive ordinal above one: validating parent+1
	// requires a parent commit and is deliberately outside this codec.
	sequence2 := base.Clone()
	sequence2.HistoryID = historyid.HistoryID{}
	sequence2.Parent, _ = format.ParseCID(emptyDigestCID)
	sequence2.Sequence = 2
	sequence3 := sequence2.Clone()
	sequence3.Sequence = 3
	_, sequence2CID, err := format.EncodeCommit(sequence2, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	_, sequence3CID, err := format.EncodeCommit(sequence3, testLimits())
	if err != nil || sequence2CID == sequence3CID || !reflect.DeepEqual(sequence2.Records, sequence3.Records) {
		t.Fatalf("sequence did not independently affect commit identity: %v", err)
	}
	for _, payload := range [][]byte{nil, {}, {0xff, 0x9f, 0, 0xfe}, []byte(`{ "a": invalid json }`), []byte(`{"a":1}`), []byte(`{ "a": 1 }`)} {
		c := base.Clone()
		c.Records[0].Payload = payload
		data, cid, err := format.EncodeCommit(c, testLimits())
		if err != nil {
			t.Fatal(err)
		}
		decoded, gotCID, err := format.DecodeCommit(data, testLimits())
		if err != nil || gotCID != cid || !decoded.Equal(c) {
			t.Fatalf("opaque round trip failed: %v", err)
		}
	}
	a := base.Clone()
	a.Records[0].Payload = nil
	b := base.Clone()
	b.Records[0].Payload = []byte{}
	ad, ac, _ := format.EncodeCommit(a, testLimits())
	bd, bc, _ := format.EncodeCommit(b, testLimits())
	if !bytes.Equal(ad, bd) || ac != bc {
		t.Fatal("nil and empty must encode equally")
	}
	// This codec validates only local successor shape; parent+1 is checked during
	// acceptance or reconstruction where the parent is available.
	a.HistoryID = historyid.HistoryID{}
	a.Sequence = 2
	a.Parent = original
	b.HistoryID = historyid.HistoryID{}
	b.Sequence = 2
	b.Parent = ac
	_, ac, err = format.EncodeCommit(a, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	_, bc, err = format.EncodeCommit(b, testLimits())
	if err != nil || ac == bc {
		t.Fatal("different parent CIDs did not change identity")
	}
}

func TestCodecOwnership(t *testing.T) {
	c := validRoot(t)
	before := c.Clone()
	data, _, err := format.EncodeCommit(c, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	wireBefore := bytes.Clone(data)
	c.Records[0].Payload[0] = 5
	c.Records[0].Kind = "changed"
	sourceAfterMutation := c.Clone()
	if !bytes.Equal(data, wireBefore) {
		t.Fatal("encoder borrowed DTO storage")
	}
	decoded, _, err := format.DecodeCommit(data, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	clear(data)
	if !decoded.Equal(before) {
		t.Fatal("decoder borrowed input bytes or strings")
	}
	input := bytes.Clone(wireBefore)
	decoded, _, err = format.DecodeCommit(input, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	decoded.Records[0].Payload[0] = 10
	decoded.Records[0].Kind = "other"
	decoded.HistoryID = historyid.HistoryID{}
	if !bytes.Equal(input, wireBefore) {
		t.Fatal("decoded DTO mutation changed input")
	}
	if !c.Equal(sourceAfterMutation) {
		t.Fatal("encoder output mutation changed DTO")
	}
}

// assertDecodeError enforces the all-or-nothing public decode contract.
func assertDecodeError(t testing.TB, data []byte, limits format.Limits) {
	t.Helper()
	c, cid, err := format.DecodeCommit(data, limits)
	if err == nil || !reflect.DeepEqual(c, format.Commit{}) || cid != (format.CID{}) {
		t.Fatalf("decode error contract failed (err=%v, zero commit=%v, zero CID=%v)", err, reflect.DeepEqual(c, format.Commit{}), cid == (format.CID{}))
	}
}

// assertEncodeError enforces the all-or-nothing public encode contract.
func assertEncodeError(t testing.TB, c format.Commit, limits format.Limits) {
	t.Helper()
	data, cid, err := format.EncodeCommit(c, limits)
	if err == nil || data != nil || cid != (format.CID{}) {
		t.Fatalf("encode error contract failed: %v", err)
	}
}

func TestCodecRejectsInvalidDTO(t *testing.T) {
	for _, mutate := range []func(*format.Commit){
		func(c *format.Commit) { c.FormatVersion = 2 },
		func(c *format.Commit) { c.Sequence = 0 },
		func(c *format.Commit) { c.Sequence = 2 },
		func(c *format.Commit) { c.Records = nil },
		func(c *format.Commit) { c.HistoryID = historyid.HistoryID{} },
		func(c *format.Commit) { c.Parent, _ = format.ParseCID(emptyDigestCID) },
		func(c *format.Commit) { c.Records[0].Kind = "" },
		func(c *format.Commit) { c.Records[0].Kind = "\xff" },
		func(c *format.Commit) { c.Records[0].KindVersion = 0 },
		func(c *format.Commit) { c.Records[0].KindVersion = -1 },
	} {
		c := validRoot(t)
		mutate(&c)
		before := c.Clone()
		assertEncodeError(t, c, testLimits())
		if !reflect.DeepEqual(before, c) {
			t.Fatal("failed encoding mutated DTO")
		}
	}
}

func TestCodecConcurrency(t *testing.T) {
	c := validRoot(t)
	data, _, err := format.EncodeCommit(c, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			l := testLimits()
			l.MaxCommitBytes += i
			for range 50 {
				if i%2 == 0 {
					l.MaxPayloadBytes = 1
					assertEncodeError(t, c, l)
					assertDecodeError(t, data, l)
				} else {
					encoded, cid, err := format.EncodeCommit(c, l)
					if err != nil {
						t.Error(err)
						return
					}
					decoded, dcid, err := format.DecodeCommit(encoded, l)
					if err != nil || dcid != cid || !decoded.Equal(c) {
						t.Errorf("concurrent round trip: %v", err)
						return
					}
				}
			}
		})
	}
	wg.Wait()
}

func FuzzDecodeCommit(f *testing.F) {
	root, successor := wireFixtures(f)
	for _, seed := range [][]byte{nil, root, successor, {0xff}, root[:len(root)-1], append(bytes.Clone(root), 0)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		l := testLimits()
		c, cid, err := format.DecodeCommit(data, l)
		if err != nil {
			if !reflect.DeepEqual(c, format.Commit{}) || cid != (format.CID{}) {
				t.Fatal("partial decode result")
			}
			return
		}
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
		assertContentCID(t, data, cid)
		encoded, ecid, err := format.EncodeCommit(c, l)
		if err != nil || ecid != cid || !bytes.Equal(encoded, data) {
			t.Fatalf("accepted noncanonical or unencodable input: %v", err)
		}
	})
}
