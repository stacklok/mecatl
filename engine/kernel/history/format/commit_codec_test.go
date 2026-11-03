package format_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
)

func testLimits() codec.Limits {
	return codec.Limits{MaxBytes: 4096, MaxArrayElements: 16, MaxStringBytes: 128, MaxByteStringBytes: 1024,
		MaxMapPairs: 8, MaxDepth: 8, MaxNodes: 512, MaxCollectionElements: 128}
}

// wireFixtures are literal canonical bytes, independent of codec append helpers.
func wireFixtures(t testing.TB) (root, successor []byte) {
	t.Helper()
	records := "\x61r\x81\xa3\x61k\x61x\x61p\x42\xff\x00\x61v\x01"
	root = []byte("\xa4\x61h\x78\x19HST1111111111111111111111" + records + "\x61s\x01\x61v\x01")
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
func assertContentCID(t testing.TB, data []byte, cid contentid.CID) {
	t.Helper()
	digest := sha256.Sum256(data)
	want := "b" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(append([]byte{1, 0x71, 0x12, 32}, digest[:]...)))
	if cid.String() != want {
		t.Fatalf("CID = %s; want %s", cid, want)
	}
}

func successorOf(c format.Commit, parent contentid.CID, sequence int64) format.Commit {
	c = c.Clone()
	c.HistoryID, c.Parent, c.Sequence = nil, &parent, sequence
	return c
}

func TestCodecGoldens(t *testing.T) {
	rootWire, successorWire := wireFixtures(t)
	root := validRoot(t)
	for _, fixture := range []struct {
		name   string
		commit format.Commit
		wire   []byte
		cid    string
	}{
		{"root", root, rootWire, "bafyreig2llelbbwy3sclxh3ytf4cy4bphqkzuvx6ljdqm5mzgfpd7uftza"},
		{"successor", successorOf(root, contentid.Sum(nil), 2), successorWire, "bafyreihqvw7bignyhvicgqom67gvtau7w5zaezanb27aatcxpylpfkmjoq"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			data, err := codec.Encode(fixture.commit, testLimits())
			if err != nil || !bytes.Equal(data, fixture.wire) {
				t.Fatalf("wire = %x; want %x; %v", data, fixture.wire, err)
			}
			assertContentCID(t, fixture.wire, contentid.Sum(data))
			if got := contentid.Sum(data).String(); got != fixture.cid {
				t.Fatalf("CID = %s; want %s", got, fixture.cid)
			}
			decoded, err := codec.Decode[format.Commit](fixture.wire, testLimits())
			if err != nil || !decoded.Equal(fixture.commit) {
				t.Fatalf("golden decode: %v", err)
			}
			// Input admission bounds actual bytes; encode uses a conservative
			// estimate and intentionally need not succeed at exact emitted size.
			for maxBytes := 1; maxBytes < len(data); maxBytes++ {
				l := testLimits()
				l.MaxBytes = maxBytes
				assertEncodeError(t, fixture.commit, l)
				assertDecodeError(t, data, l)
			}
		})
	}
}

func TestCodecIdentityAndOpaquePayloads(t *testing.T) {
	base := validRoot(t)
	base.Records = append(base.Records, format.Record{Kind: "arbitrary namespace / 🐈", KindVersion: math.MaxInt64, Payload: []byte(`{ "a": invalid json }`)})
	data, err := codec.Encode(base, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	original := contentid.Sum(data)
	for name, mutate := range map[string]func(*format.Commit){
		"history": func(c *format.Commit) {
			h, _ := historyid.ParseHistoryID("HST2222222222222222222222")
			c.HistoryID = &h
		},
		"parent":   func(c *format.Commit) { *c = successorOf(*c, contentid.Sum(nil), 2) },
		"payload":  func(c *format.Commit) { c.Records[0].Payload[0]++ },
		"kind":     func(c *format.Commit) { c.Records[0].Kind += "?" },
		"version":  func(c *format.Commit) { c.Records[0].KindVersion++ },
		"order":    func(c *format.Commit) { c.Records[0], c.Records[1] = c.Records[1], c.Records[0] },
		"grouping": func(c *format.Commit) { c.Records = c.Records[:1] },
	} {
		t.Run(name, func(t *testing.T) {
			c := base.Clone()
			mutate(&c)
			b, err := codec.Encode(c, testLimits())
			if err != nil || contentid.Sum(b) == original {
				t.Fatalf("identity change failed: %v", err)
			}
		})
	}
	// Sequence and parent independently affect identity; parent+1 requires
	// context and deliberately does not belong to local DTO validation.
	a := successorOf(base, original, 2)
	b := successorOf(base, original, 3)
	ad, err := codec.Encode(a, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	bd, err := codec.Encode(b, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	if contentid.Sum(ad) == contentid.Sum(bd) {
		t.Fatal("sequence did not change identity")
	}
	b = successorOf(base, contentid.Sum(nil), 2)
	bd, err = codec.Encode(b, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	if contentid.Sum(ad) == contentid.Sum(bd) {
		t.Fatal("parent did not change identity")
	}
	for _, payload := range [][]byte{nil, {}, {0xff, 0x9f, 0, 0xfe}, []byte(`{ "a": invalid json }`), []byte(`{"a":1}`), []byte(`{ "a": 1 }`)} {
		c := base.Clone()
		c.Records[0].Payload = payload
		data, err := codec.Encode(c, testLimits())
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := codec.Decode[format.Commit](data, testLimits())
		if err != nil || !decoded.Equal(c) {
			t.Fatalf("opaque round trip failed: %v", err)
		}
	}
	a, b = base.Clone(), base.Clone()
	a.Records[0].Payload, b.Records[0].Payload = nil, []byte{}
	ad, err = codec.Encode(a, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	bd, err = codec.Encode(b, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ad, bd) || contentid.Sum(ad) != contentid.Sum(bd) {
		t.Fatal("nil and empty must encode equally")
	}
}

func TestCodecOwnership(t *testing.T) {
	c := validRoot(t)
	before := c.Clone()
	data, err := codec.Encode(c, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	wireBefore := bytes.Clone(data)
	c.Records[0].Payload[0], c.Records[0].Kind = 5, "changed"
	*c.HistoryID = historyid.HistoryID{}
	sourceAfterMutation := c.Clone()
	if !bytes.Equal(data, wireBefore) {
		t.Fatal("encoder borrowed DTO storage")
	}
	decoded, err := codec.Decode[format.Commit](data, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	clear(data)
	if !decoded.Equal(before) {
		t.Fatal("decoder borrowed input bytes or strings")
	}
	input := bytes.Clone(wireBefore)
	decoded, err = codec.Decode[format.Commit](input, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	decoded.Records[0].Payload[0], decoded.Records[0].Kind = 10, "other"
	*decoded.HistoryID = historyid.HistoryID{}
	if !bytes.Equal(input, wireBefore) || !c.Equal(sourceAfterMutation) {
		t.Fatal("mutation crossed operation ownership")
	}
}

func assertDecodeError(t testing.TB, data []byte, limits codec.Limits) {
	t.Helper()
	c, err := codec.Decode[format.Commit](data, limits)
	if err == nil || !reflect.DeepEqual(c, format.Commit{}) {
		t.Fatalf("decode error contract failed: %v", err)
	}
}

func assertEncodeError(t testing.TB, c format.Commit, limits codec.Limits) {
	t.Helper()
	data, err := codec.Encode(c, limits)
	if err == nil || data != nil {
		t.Fatalf("encode error contract failed: %v", err)
	}
}

func TestCodecRejectsInvalidDTO(t *testing.T) {
	for _, mutate := range []func(*format.Commit){
		func(c *format.Commit) { c.FormatVersion = 2 },
		func(c *format.Commit) { c.Sequence = 0 },
		func(c *format.Commit) { c.Sequence = 2 },
		func(c *format.Commit) { c.Records = nil },
		func(c *format.Commit) { c.HistoryID = nil },
		func(c *format.Commit) { c.HistoryID = new(historyid.HistoryID) },
		func(c *format.Commit) { c.Parent = new(contentid.Sum(nil)) },
		func(c *format.Commit) { c.HistoryID = nil; c.Parent = new(contentid.CID); c.Sequence = 2 },
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

func TestReadCBORPreservesReceiverOnFailure(t *testing.T) {
	root, successor := wireFixtures(t)
	for _, seed := range [][]byte{root, successor} {
		badSemantic := bytes.Clone(seed)
		badSemantic[len(badSemantic)-1] = 2
		for _, test := range []struct {
			data   []byte
			limits codec.Limits
		}{
			{seed[:len(seed)-1], testLimits()}, {badSemantic, testLimits()},
			{[]byte{0xa0}, testLimits()}, {seed, func() codec.Limits { l := testLimits(); l.MaxNodes = 4; return l }()},
		} {
			receiver := commitFixture(t)
			before := receiver.Clone()
			input := bytes.Clone(test.data)
			r, err := codec.NewReader(input, test.limits)
			if err != nil {
				t.Fatal(err)
			}
			if err := receiver.ReadCBOR(&r, 0, math.MaxInt); err == nil {
				t.Fatal("invalid nested value accepted")
			}
			if !reflect.DeepEqual(receiver, before) || !bytes.Equal(input, test.data) {
				t.Fatal("failed read changed receiver or input")
			}
		}
	}
	var nilCommit *format.Commit
	r, err := codec.NewReader(root, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	if nilCommit.ReadCBOR(&r, 0, math.MaxInt) == nil {
		t.Fatal("nil receiver accepted")
	}
	c := validRoot(t)
	if c.ReadCBOR(nil, 0, math.MaxInt) == nil {
		t.Fatal("nil reader accepted")
	}
	// Nested reads consume just their value; only whole-value Decode rejects a tail.
	r, err = codec.NewReader(append(bytes.Clone(root), 0), testLimits())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ReadCBOR(&r, 0, math.MaxInt); err != nil {
		t.Fatal(err)
	}
	if r.Done() == nil {
		t.Fatal("trailing byte vanished")
	}
	assertDecodeError(t, append(bytes.Clone(root), 0), testLimits())
}

func TestCodecConcurrency(t *testing.T) {
	c := validRoot(t)
	data, err := codec.Encode(c, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			l := testLimits()
			l.MaxBytes += i
			for range 50 {
				if i%2 == 0 {
					l.MaxByteStringBytes = 1
					assertEncodeError(t, c, l)
					assertDecodeError(t, data, l)
				} else {
					encoded, err := codec.Encode(c, l)
					if err != nil {
						t.Error(err)
						return
					}
					decoded, err := codec.Decode[format.Commit](encoded, l)
					if err != nil || !decoded.Equal(c) || !bytes.Equal(encoded, data) {
						t.Errorf("concurrent round trip: %v", err)
						return
					}
				}
			}
		})
	}
	wg.Wait()
}

func FuzzCommitDecode(f *testing.F) {
	root, successor := wireFixtures(f)
	for _, seed := range [][]byte{nil, root, successor, {0xff}, root[:len(root)-1], append(bytes.Clone(root), 0)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		l := testLimits()
		c, err := codec.Decode[format.Commit](data, l)
		if err != nil {
			if !reflect.DeepEqual(c, format.Commit{}) {
				t.Fatal("partial decode result")
			}
			return
		}
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
		assertContentCID(t, data, contentid.Sum(data))
		// Conservative encoding admission may need more than input length;
		// widen only that policy to test canonical re-encoding, not decoding.
		l.MaxBytes = math.MaxInt
		encoded, err := codec.Encode(c, l)
		if err != nil || !bytes.Equal(encoded, data) {
			t.Fatalf("accepted noncanonical input: %v", err)
		}
	})
}
