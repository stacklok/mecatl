package session

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
)

func internalTestHistory(t testing.TB, limits codec.Limits) *history.History {
	t.Helper()
	h, err := history.New(mem.New(), history.Policy{CommitLimits: limits, RetryWindow: 16, MaxPageBytes: limits.MaxBytes, MaxPageCommits: 2})
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func internalFixture(t testing.TB) (*Session, codec.Limits) {
	t.Helper()
	id, err := historyid.ParseHistoryID("HST1111111111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	limits := codec.Limits{MaxBytes: 4096, MaxStringBytes: 128, MaxByteStringBytes: 1024, MaxArrayElements: 16, MaxMapPairs: 16, MaxDepth: 8, MaxNodes: 512, MaxCollectionElements: 128}
	s, err := New(internalTestHistory(t, limits), id, Policy{CommitLimits: limits, PayloadLimits: limits, MaxRecordsPerCommit: 16, SoftMaxRetainedRecords: 32768, SoftMaxPayloadBytes: 1 << 24, SoftMaxDecodedWork: 1 << 32, MaxRetainedRecords: 32768, MaxPayloadBytes: 1 << 24, MaxDecodedWork: 1 << 32})
	if err != nil {
		t.Fatal(err)
	}
	return s, limits
}
func internalApply(t testing.TB, s *Session, records ...Record) prepared {
	t.Helper()
	p, err := s.prepare(records)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(history.Commit{Ref: p.ref, Commit: p.commit}); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestRecordMetadataAgreementAndOwnership(t *testing.T) {
	s, limits := internalFixture(t)
	id := s.Snapshot().HistoryID()
	run := RunStarted{ID: RunID{HistoryID: id, Ordinal: 24}}
	input := UserInput{ID: UserInputID{RunID: run.ID, Ordinal: 24}, Parts: internalByteParts([]byte{0xff, 0, 'x'})}
	constructors := []struct {
		name string
		make func() (Record, error)
	}{
		{"created", func() (Record, error) { return NewCreatedRecord(Created{HistoryID: id}, limits) }},
		{"input", func() (Record, error) { return NewUserInputRecord(input, limits) }},
		{"start", func() (Record, error) { return NewRunStartedRecord(run, limits) }},
		{"interruption", func() (Record, error) { return NewRunInterruptedRecord(RunInterrupted(run), limits) }},
	}
	for _, tc := range constructors {
		t.Run(tc.name, func(t *testing.T) {
			r, err := tc.make()
			if err != nil {
				t.Fatal(err)
			}
			if r.scope != id {
				t.Fatal("missing scope")
			}
			switch tc.name {
			case "input":
				if r.userInput != input.ID {
					t.Fatal("input accounting")
				}
			case "start":
				if r.runStarted.ID != run.ID.Ordinal {
					t.Fatal("start metadata")
				}
			case "interruption":
				if r.runInterrupted.ID != run.ID {
					t.Fatal("interruption metadata")
				}
			}
			dto := r.Format()
			decoded, err := DecodeRecord(id, dto, limits)
			if err != nil || decoded.scope != r.scope || !decoded.Format().Equal(r.Format()) {
				t.Fatalf("metadata differs: %v", err)
			}
			switch tc.name {
			case "input":
				if decoded.userInput != r.userInput {
					t.Fatal("imported input metadata")
				}
			case "start":
				if decoded.runStarted != r.runStarted {
					t.Fatal("imported start metadata")
				}
			case "interruption":
				if decoded.runInterrupted != r.runInterrupted {
					t.Fatal("imported interruption metadata")
				}
			}
			want := dto.Clone()
			content := input.Clone()
			clear(input.Parts[0].Media.Data)
			clear(dto.Payload)
			if !r.Format().Equal(want) || !decoded.Format().Equal(want) {
				t.Fatal("caller mutation changed bytes")
			}
			input = content
			if err := r.checkPayload(limits); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestMetadataPreflightBoundedDecodeDoesNotPublish(t *testing.T) {
	s, limits := internalFixture(t)
	id := s.Snapshot().HistoryID()
	run := RunID{HistoryID: id, Ordinal: 1}
	created, err := NewCreatedRecord(Created{HistoryID: id}, limits)
	if err != nil {
		t.Fatal(err)
	}
	start, err := NewRunStartedRecord(RunStarted{ID: run}, limits)
	if err != nil {
		t.Fatal(err)
	}
	first, err := NewUserInputRecord(UserInput{ID: UserInputID{RunID: run, Ordinal: 1}, Parts: internalByteParts(make([]byte, 512))}, limits)
	if err != nil {
		t.Fatal(err)
	}
	p := internalApply(t, s, created, start, first)
	second, err := NewUserInputRecord(UserInput{ID: UserInputID{RunID: run, Ordinal: 2}, Parts: internalByteParts(make([]byte, 512))}, limits)
	if err != nil {
		t.Fatal(err)
	}
	closeout, err := NewRunInterruptedRecord(RunInterrupted{ID: run}, limits)
	if err != nil {
		t.Fatal(err)
	}
	// Ending idle isolates metadata accounting from the real closeout reservation,
	// which intentionally encodes its own small proposal.
	records := []Record{second, closeout}
	before := s.Snapshot()
	if err := s.preflight(records, p.ref, uint64(len(second.value.Payload)+len(closeout.value.Payload))); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, s.Snapshot()) || !internalUserInputsMatch(s.Snapshot(), run, 1) {
		t.Fatal("preflight published")
	}
}

func TestPreparationRetainsOriginatingScope(t *testing.T) {
	s, limits := internalFixture(t)
	foreign, err := historyid.ParseHistoryID("HST2222222222222222222222")
	if err != nil {
		t.Fatal(err)
	}
	run := RunID{HistoryID: foreign, Ordinal: 1}
	created, err := NewCreatedRecord(Created{HistoryID: foreign}, limits)
	if err != nil {
		t.Fatal(err)
	}
	start, err := NewRunStartedRecord(RunStarted{ID: run}, limits)
	if err != nil {
		t.Fatal(err)
	}
	input, err := NewUserInputRecord(UserInput{ID: UserInputID{RunID: run, Ordinal: 1}}, limits)
	if err != nil {
		t.Fatal(err)
	}
	closeout, err := NewRunInterruptedRecord(RunInterrupted{ID: run}, limits)
	if err != nil {
		t.Fatal(err)
	}
	for _, original := range []Record{created, start, input, closeout} {
		rebound, err := DecodeRecord(foreign, original.Format(), limits)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range []Record{original, rebound} {
			before := s.Snapshot()
			if p, err := s.prepare([]Record{r}); !errors.Is(err, ErrIdentity) || !reflect.DeepEqual(p, prepared{}) {
				t.Fatalf("foreign preparation: %v", err)
			}
			if !reflect.DeepEqual(before, s.Snapshot()) {
				t.Fatal("foreign preparation mutation")
			}
		}
	}
}
