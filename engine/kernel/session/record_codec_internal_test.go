package session

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
)

func testLimits() codec.Limits {
	return codec.Limits{MaxBytes: 4096, MaxStringBytes: 128, MaxByteStringBytes: 1024,
		MaxArrayElements: 16, MaxMapPairs: 16, MaxDepth: 8, MaxNodes: 512, MaxCollectionElements: 128}
}

func testHistoryID(t testing.TB) historyid.HistoryID {
	t.Helper()
	id, err := historyid.ParseHistoryID("HST1111111111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// recordFixtures pins session payload schemas independently of generated append helpers.
func recordFixtures(t testing.TB) []struct {
	kind string
	wire []byte
	make func(codec.Limits) (*materializedRecord, error)
} {
	t.Helper()
	h := testHistoryID(t)
	return []struct {
		kind string
		wire []byte
		make func(codec.Limits) (*materializedRecord, error)
	}{
		{"session.created", []byte("\xa0"), func(l codec.Limits) (*materializedRecord, error) {
			return newCreatedRecord(Created{HistoryID: h}, l)
		}},
		{"session.user_input", []byte("\xa3\x61i\x01\x61p\x81\xa1\x61t\x61x\x61r\x01"), func(l codec.Limits) (*materializedRecord, error) {
			return makeInputRecord(identifiedInput{ID: allocatedInput(t, allocatedRun(t, h, 1), 1), Parts: []ContentPart{{Text: new("x")}}}, l)
		}},
		{"session.run_started", []byte("\xa1\x61r\x02"), func(l codec.Limits) (*materializedRecord, error) {
			return newRunStartedRecord(RunStarted{ID: allocatedRun(t, h, 2)}, l)
		}},
		{"session.run_ended", []byte("\xa2\x61e\x6binterrupted\x61r\x02"), func(l codec.Limits) (*materializedRecord, error) {
			return newRunEndedRecord(RunEnded{RunID: allocatedRun(t, h, 2), Reason: RunEndedInterrupted}, l)
		}},
	}
}

func TestRecordSchemasAndOwnership(t *testing.T) {
	for _, test := range recordFixtures(t) {
		t.Run(test.kind, func(t *testing.T) {
			r, err := test.make(testLimits())
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(test.wire, []byte(testHistoryID(t).String())) {
				t.Fatal("payload repeats history identity")
			}
			version := int64(1)
			want := format.Record{Kind: test.kind, KindVersion: version, Payload: test.wire}
			if !r.format().Equal(want) {
				t.Fatal("constructor differs from canonical fixture")
			}
			dto := r.format()
			decoded, err := decodeMaterialized(dto, testLimits())
			if err != nil || !decoded.format().Equal(want) {
				t.Fatalf("decode: %v", err)
			}
			clear(dto.Payload)
			if !r.format().Equal(want) || !decoded.format().Equal(want) {
				t.Fatal("record retained mutable DTO storage")
			}
			copyOfRecord := decoded
			output := decoded.format()
			clear(output.Payload)
			if !copyOfRecord.format().Equal(want) {
				t.Fatal("Format exposed private storage")
			}
			// Reuse existing envelope codecs, rather than inventing a session envelope.
			commit := format.Commit{FormatVersion: 1, HistoryID: new(testHistoryID(t)), Sequence: 1, Records: []format.Record{r.format()}}
			wire, err := codec.Encode(commit, testLimits())
			if err != nil {
				t.Fatal(err)
			}
			got, err := codec.Decode[format.Commit](wire, testLimits())
			if err != nil || !got.Equal(commit) {
				t.Fatalf("history envelope composition: %v", err)
			}
		})
	}
}

func TestRecordFailureCategories(t *testing.T) {
	for _, test := range recordFixtures(t) {
		t.Run(test.kind, func(t *testing.T) {
			version := int64(1)
			for name, limits := range map[string]codec.Limits{
				"invalid": {},
				"limited": {MaxBytes: 1, MaxStringBytes: 128, MaxByteStringBytes: 1024, MaxArrayElements: 16,
					MaxMapPairs: 16, MaxDepth: 8, MaxNodes: 512, MaxCollectionElements: 128},
			} {
				want := codec.ErrLimitExceeded
				if name == "invalid" {
					want = codec.ErrInvalidLimits
				}
				r, err := test.make(limits)
				if !errors.Is(err, want) || r != nil {
					t.Fatalf("constructor %s: %v", name, err)
				}
				r, err = decodeMaterialized(format.Record{Kind: test.kind, KindVersion: version, Payload: test.wire}, limits)
				if !errors.Is(err, want) || r != nil {
					t.Fatalf("decode %s: %v", name, err)
				}
			}
			for _, version := range []int64{3, 99} {
				r, err := decodeMaterialized(format.Record{Kind: test.kind, KindVersion: version, Payload: test.wire}, testLimits())
				if !errors.Is(err, ErrUnsupportedRecord) || r != nil {
					t.Fatalf("future version: %v", err)
				}
			}
		})
	}
	for _, kind := range []string{"session.future_request", "session.run_interrupted", "other"} {
		r, err := decodeMaterialized(format.Record{Kind: kind, KindVersion: 1, Payload: []byte{0xa0}}, testLimits())
		if !errors.Is(err, ErrUnsupportedRecord) || r != nil {
			t.Fatalf("unsupported kind: %v", err)
		}
	}
	for _, dto := range []format.Record{{}, {Kind: "session.created", KindVersion: 0}, {Kind: "\xff", KindVersion: 1}} {
		r, err := decodeMaterialized(dto, testLimits())
		if !errors.Is(err, codec.ErrInvalidData) || r != nil {
			t.Fatalf("invalid envelope: %v", err)
		}
	}
}

func TestRecordMalformedPayloads(t *testing.T) {
	for _, test := range recordFixtures(t) {
		t.Run(test.kind, func(t *testing.T) {
			version := int64(1)
			unknown := bytes.Clone(test.wire)
			unknown[0]++ // Append a new, canonically later map key.
			unknown = append(unknown, '\x61', 'z', '\x00')
			wires := [][]byte{
				nil, {0xa1}, {0xf6}, {0xbf, 0xff},
				test.wire[:len(test.wire)-1], append(bytes.Clone(test.wire), 0), unknown,
			}
			if test.kind != "session.created" {
				wires = append(wires, []byte{0xa0})
			}
			for _, wire := range wires {
				before := bytes.Clone(wire)
				r, err := decodeMaterialized(format.Record{Kind: test.kind, KindVersion: version, Payload: wire}, testLimits())
				if !errors.Is(err, codec.ErrInvalidData) || r != nil || !bytes.Equal(wire, before) {
					t.Fatalf("malformed payload: %v", err)
				}
			}
		})
	}
}

func FuzzDecodeRecord(f *testing.F) {
	for i, test := range recordFixtures(f) {
		f.Add(uint8(i), test.wire)
	}
	kinds := []string{"session.created", "session.user_input", "session.run_started", "session.run_ended"}
	f.Fuzz(func(t *testing.T, kind uint8, wire []byte) {
		dto := format.Record{Kind: kinds[int(kind)%len(kinds)], KindVersion: 1, Payload: wire}
		r, err := decodeMaterialized(dto, testLimits())
		if err != nil {
			if r != nil {
				t.Fatal("failed decoding returned usable record")
			}
			return
		}
		if !r.format().Equal(dto) {
			t.Fatal("successful decoding changed canonical bytes")
		}
	})
}
