package session

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
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
func internalApply(t testing.TB, s *Session, records ...proposedRecord) prepared {
	t.Helper()
	p, err := s.prepare(records)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.apply(history.Commit{Ref: p.ref, Commit: p.commit}); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestRecordMaterializationAgreementAndOwnership(t *testing.T) {
	s, limits := internalFixture(t)
	id := s.Snapshot().HistoryID()
	run := RunStarted{ID: RunID{historyID: id, ordinal: 24}}
	input := identifiedInput{ID: UserInputID{runID: run.ID, ordinal: 24}, Parts: internalByteParts([]byte{0xff, 0, 'x'})}
	constructors := []struct {
		name string
		make func() (proposedRecord, error)
	}{
		{"created", func() (proposedRecord, error) { return newCreatedRecord(Created{HistoryID: id}, limits) }},
		{"input", func() (proposedRecord, error) { return makeInputRecord(input, limits) }},
		{"start", func() (proposedRecord, error) { return newRunStartedRecord(run, limits) }},
		{"interruption", func() (proposedRecord, error) {
			return newRunEndedRecord(RunEnded{RunID: run.ID, Reason: RunEndedInterrupted}, limits)
		}},
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
				if !r.record.input.Equal(input.toFormat()) {
					t.Fatal("input materialization")
				}
			case "start":
				if r.record.started.ID != run.ID.ordinal {
					t.Fatal("start materialization")
				}
			case "interruption":
				if r.record.ended.RunID != run.ID.ordinal || r.record.ended.Reason != string(RunEndedInterrupted) {
					t.Fatal("interruption materialization")
				}
			}
			dto := r.format()
			decoded, err := decodeRecord(id, dto, limits)
			if err != nil || !reflect.DeepEqual(decoded, r) {
				t.Fatalf("materialization differs: %v", err)
			}
			want := dto.Clone()
			content := input.Clone()
			clear(input.Parts[0].Media.InlineData)
			clear(dto.Payload)
			if !r.format().Equal(want) || !decoded.format().Equal(want) {
				t.Fatal("caller mutation changed bytes")
			}
			input = content
			if err := r.checkPayload(limits); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestRequestAndEndingMaterializationOwnsCompleteValues(t *testing.T) {
	s, limits := internalFixture(t)
	run := RunID{historyID: s.Snapshot().HistoryID(), ordinal: 1}
	id := requestID(run.historyID, run.ordinal, 2)
	retry := requestID(run.historyID, run.ordinal, 1)
	request := identifiedRequest{ID: id, Source: SourceHead{Sequence: 1, CID: contentid.Sum([]byte("source"))}, Recipe: ConstructionRecipe{Algorithm: "retained", Version: 1, Parameters: []byte{0xff}, Check: []byte{1}}, RetryOf: &retry}
	ending := RunEnded{RunID: run, Reason: RunEndedCaptureFailed, Explanation: new("retained detail"), Capture: &CaptureFailureEvidence{RequestID: id, Evidence: &ObservedEvidence{Correlation: new("retained correlation")}, Usage: &InvocationUsage{Input: new(int64(0)), Raw: &OpaqueProviderData{Producer: "p", Format: "usage", Version: 1, Data: []byte{0xff}}}}}
	wantRequest, wantEnding := request.toFormat().Clone(), ending.toFormat().Clone()
	r := interactionRecord(t)(makeRequestRecord(request, limits))
	e := interactionRecord(t)(newRunEndedRecord(ending, limits))
	request.Recipe.Parameters[0] = 0
	request.Recipe.Check[0] = 0
	retry = id
	*ending.Explanation = "changed"
	*ending.Capture.Evidence.Correlation = "changed"
	*ending.Capture.Usage.Input = 99
	ending.Capture.Usage.Raw.Data[0] = 0
	if !r.record.request.Equal(wantRequest) || !e.record.ended.Equal(wantEnding) {
		t.Fatal("proposal backing lost full values or retained caller storage")
	}
	for _, original := range []proposedRecord{r, e} {
		dto := original.format()
		decoded, err := decodeRecord(run.historyID, dto, limits)
		if err != nil {
			t.Fatal(err)
		}
		clear(dto.Payload)
		if original.record == decoded.record || !reflect.DeepEqual(original, decoded) {
			t.Fatal("decoded backing must be detached and equivalent")
		}
	}
	if (proposedRecord{}).minimalEnding() || r.minimalEnding() || e.minimalEnding() {
		t.Fatal("zero, wrong kind or detailed ending classified as minimal")
	}
}

func TestMaterializedPreflightBoundedDecodeDoesNotPublish(t *testing.T) {
	s, limits := internalFixture(t)
	id := s.Snapshot().HistoryID()
	run := RunID{historyID: id, ordinal: 1}
	created, err := newCreatedRecord(Created{HistoryID: id}, limits)
	if err != nil {
		t.Fatal(err)
	}
	start, err := newRunStartedRecord(RunStarted{ID: run}, limits)
	if err != nil {
		t.Fatal(err)
	}
	first, err := makeInputRecord(identifiedInput{ID: UserInputID{runID: run, ordinal: 1}, Parts: internalByteParts(make([]byte, 512))}, limits)
	if err != nil {
		t.Fatal(err)
	}
	p := internalApply(t, s, created, start, first)
	second, err := makeInputRecord(identifiedInput{ID: UserInputID{runID: run, ordinal: 2}, Parts: internalByteParts(make([]byte, 512))}, limits)
	if err != nil {
		t.Fatal(err)
	}
	closeout, err := newRunEndedRecord(RunEnded{RunID: run, Reason: RunEndedInterrupted}, limits)
	if err != nil {
		t.Fatal(err)
	}
	// Ending idle isolates record accounting from the real closeout reservation,
	// which intentionally encodes its own small proposal.
	records := []proposedRecord{second, closeout}
	before := s.Snapshot()
	if err := s.preflight(records, p.ref, uint64(len(second.record.value.Payload)+len(closeout.record.value.Payload))); err != nil {
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
	run := RunID{historyID: foreign, ordinal: 1}
	created, err := newCreatedRecord(Created{HistoryID: foreign}, limits)
	if err != nil {
		t.Fatal(err)
	}
	start, err := newRunStartedRecord(RunStarted{ID: run}, limits)
	if err != nil {
		t.Fatal(err)
	}
	input, err := makeInputRecord(identifiedInput{ID: UserInputID{runID: run, ordinal: 1}}, limits)
	if err != nil {
		t.Fatal(err)
	}
	closeout, err := newRunEndedRecord(RunEnded{RunID: run, Reason: RunEndedInterrupted}, limits)
	if err != nil {
		t.Fatal(err)
	}
	for _, original := range []proposedRecord{created, start, input, closeout} {
		rebound, err := decodeRecord(foreign, original.format(), limits)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range []proposedRecord{original, rebound} {
			before := s.Snapshot()
			if p, err := s.prepare([]proposedRecord{r}); !errors.Is(err, ErrIdentity) || !reflect.DeepEqual(p, prepared{}) {
				t.Fatalf("foreign preparation: %v", err)
			}
			if !reflect.DeepEqual(before, s.Snapshot()) {
				t.Fatal("foreign preparation mutation")
			}
		}
	}
}
