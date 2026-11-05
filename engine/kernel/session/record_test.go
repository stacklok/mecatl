package session_test

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	"github.com/stacklok/mecatl/engine/kernel/session"
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
	make func(codec.Limits) (session.Record, error)
} {
	t.Helper()
	h := testHistoryID(t)
	return []struct {
		kind string
		wire []byte
		make func(codec.Limits) (session.Record, error)
	}{
		{"session.created", []byte("\xa0"), func(l codec.Limits) (session.Record, error) {
			return session.NewCreatedRecord(session.Created{HistoryID: h}, l)
		}},
		{"session.user_input", []byte("\xa3\x61i\x01\x61p\x81\xa2\x61k\x64text\x61t\x61x\x61r\x01"), func(l codec.Limits) (session.Record, error) {
			return session.NewUserInputRecord(session.UserInput{ID: session.InputID{RunID: session.RunID{HistoryID: h, Ordinal: 1}, Ordinal: 1}, Parts: []session.ContentPart{{Kind: session.ContentText, Text: "x"}}}, l)
		}},
		{"session.run_started", []byte("\xa1\x61r\x02"), func(l codec.Limits) (session.Record, error) {
			return session.NewRunStartedRecord(session.RunStarted{ID: session.RunID{HistoryID: h, Ordinal: 2}}, l)
		}},
		{"session.run_interrupted", []byte("\xa1\x61r\x02"), func(l codec.Limits) (session.Record, error) {
			return session.NewRunInterruptedRecord(session.RunInterrupted{ID: session.RunID{HistoryID: h, Ordinal: 2}}, l)
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
			if !r.Format().Equal(want) {
				t.Fatal("constructor differs from canonical fixture")
			}
			dto := r.Format()
			decoded, err := session.DecodeRecord(testHistoryID(t), dto, testLimits())
			if err != nil || !decoded.Format().Equal(want) {
				t.Fatalf("decode: %v", err)
			}
			clear(dto.Payload)
			if !r.Format().Equal(want) || !decoded.Format().Equal(want) {
				t.Fatal("record retained mutable DTO storage")
			}
			copyOfRecord := decoded
			output := decoded.Format()
			clear(output.Payload)
			if !copyOfRecord.Format().Equal(want) {
				t.Fatal("Format exposed private storage")
			}
			// Reuse existing envelope codecs, rather than inventing a session envelope.
			commit := format.Commit{FormatVersion: 1, HistoryID: new(testHistoryID(t)), Sequence: 1, Records: []format.Record{r.Format()}}
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
	if !(session.Record{}).Format().Equal(format.Record{}) {
		t.Fatal("zero record must yield unusable zero DTO")
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
				if !errors.Is(err, want) || !reflect.DeepEqual(r, session.Record{}) {
					t.Fatalf("constructor %s: %v", name, err)
				}
				r, err = session.DecodeRecord(testHistoryID(t), format.Record{Kind: test.kind, KindVersion: version, Payload: test.wire}, limits)
				if !errors.Is(err, want) || !reflect.DeepEqual(r, session.Record{}) {
					t.Fatalf("decode %s: %v", name, err)
				}
			}
			for _, version := range []int64{3, 99} {
				r, err := session.DecodeRecord(testHistoryID(t), format.Record{Kind: test.kind, KindVersion: version, Payload: test.wire}, testLimits())
				if !errors.Is(err, session.ErrUnsupportedRecord) || !reflect.DeepEqual(r, session.Record{}) {
					t.Fatalf("future version: %v", err)
				}
			}
		})
	}
	for _, kind := range []string{"session.model_request", "session.run_completed", "other"} {
		r, err := session.DecodeRecord(testHistoryID(t), format.Record{Kind: kind, KindVersion: 1, Payload: []byte{0xa0}}, testLimits())
		if !errors.Is(err, session.ErrUnsupportedRecord) || !reflect.DeepEqual(r, session.Record{}) {
			t.Fatalf("unsupported kind: %v", err)
		}
	}
	for _, dto := range []format.Record{{}, {Kind: "session.created", KindVersion: 0}, {Kind: "\xff", KindVersion: 1}} {
		r, err := session.DecodeRecord(testHistoryID(t), dto, testLimits())
		if !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(r, session.Record{}) {
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
				r, err := session.DecodeRecord(testHistoryID(t), format.Record{Kind: test.kind, KindVersion: version, Payload: wire}, testLimits())
				if !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(r, session.Record{}) || !bytes.Equal(wire, before) {
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
	kinds := []string{"session.created", "session.user_input", "session.run_started", "session.run_interrupted"}
	f.Fuzz(func(t *testing.T, kind uint8, wire []byte) {
		dto := format.Record{Kind: kinds[int(kind)%len(kinds)], KindVersion: 1, Payload: wire}
		r, err := session.DecodeRecord(testHistoryID(t), dto, testLimits())
		if err != nil {
			if !reflect.DeepEqual(r, session.Record{}) {
				t.Fatal("failed decoding returned usable record")
			}
			return
		}
		if !r.Format().Equal(dto) {
			t.Fatal("successful decoding changed canonical bytes")
		}
	})
}
