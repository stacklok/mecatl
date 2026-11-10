package session_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

// Faults stay at the real History byte boundary; afterHead appends after the pin.
type loadBackend struct {
	history.Backend
	reads                   []history.ByteReadRequest
	observes, appends       int
	afterHead               func()
	prefix                  *history.CommitRef
	corrupt                 bool
	observeError, readError error
}

func (b *loadBackend) Append(context.Context, history.HistoryID, history.CommitRef, history.ByteCommit) (history.Observation, error) {
	b.appends++
	return history.Observation{}, errors.New("load backend append")
}
func (b *loadBackend) Observe(ctx context.Context, id history.HistoryID, sequence int64) (history.Observation, error) {
	b.observes++
	if b.observeError != nil {
		return history.Observation{}, b.observeError
	}
	o, err := b.Backend.Observe(ctx, id, sequence)
	if err == nil && b.afterHead != nil {
		f := b.afterHead
		b.afterHead = nil
		f()
	}
	return o, err
}
func (b *loadBackend) Read(ctx context.Context, id history.HistoryID, r history.ByteReadRequest) ([]history.ByteCommit, error) {
	b.reads = append(b.reads, r)
	if b.readError != nil {
		return nil, b.readError
	}
	if b.prefix != nil {
		r.Through = *b.prefix
	}
	commits, err := b.Backend.Read(ctx, id, r)
	if err == nil && b.corrupt && len(commits) != 0 {
		commits[0].Bytes[0] ^= 1
	}
	return commits, err
}

func TestLoadLifecycleAndOwnership(t *testing.T) {
	backend := mem.New()
	h := newLifecycleHistory(t, backend)
	s := boundSession(t, h, sessionPolicy())
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	run := batchRun(t, b)
	input := batchInput(t, b, run, []byte{0xff, 0, 'x'})
	submitBatch(t, s, b)
	for _, stage := range []string{"active", "idle", "successor"} {
		t.Run(stage, func(t *testing.T) {
			want := s.Snapshot()
			reader := &loadBackend{Backend: readOnlyHistoryBackend{Backend: backend, t: t}}
			loaded, err := session.Load(context.Background(), newLifecycleHistory(t, reader), testHistoryID(t), sessionPolicy())
			mustBatch(t, err)
			if loaded == s || !reflect.DeepEqual(loaded.Snapshot(), want) {
				t.Fatal("fresh load differs")
			}
			if reader.observes != 1 || len(reader.reads) != int(want.Head().Sequence) {
				t.Fatal("pin/page count")
			}
			for i, r := range reader.reads {
				if r.Through != want.Head() || r.After.Sequence != int64(i) || r.MaxCommits != 1 || r.MaxBytes != sessionPolicy().CommitLimits.MaxBytes {
					t.Fatalf("page: %+v", r)
				}
			}
			v := loaded.Snapshot()
			clear(inputBytes(lookupInput(t, v, input.RunID(), 1)))
			raw := v.RecordAt(2).Format()
			clear(raw.Payload)
			if !reflect.DeepEqual(loaded.Snapshot(), want) || !reflect.DeepEqual(s.Snapshot(), want) {
				t.Fatal("loaded storage aliases exports")
			}
			assertHistoryHead(t, h, want.Head())
		})
		loaded, err := session.Load(context.Background(), h, testHistoryID(t), sessionPolicy())
		mustBatch(t, err)
		s = loaded
		if stage == "successor" {
			break
		}
		next := beginBatch(t, s)
		if stage == "active" {
			mustBatch(t, next.EndRun(session.RunEndedInterrupted))
		} else {
			nextRun := batchRun(t, next)
			second := batchInput(t, next, nextRun, []byte("second"))
			if second != allocatedInput(t, nextRun, 1) || nextRun != allocatedRun(t, testHistoryID(t), 2) {
				t.Fatal("reload numbering")
			}
		}
		submitBatch(t, s, next)
	}
	// Loading restores owner legality, not just visible facts.
	b = beginBatch(t, s)
	if _, err := b.AddUserInput(input.RunID(), session.UserInput{}); !errors.Is(err, session.ErrTransition) {
		t.Fatal("reload forgot closed owner")
	}
	mustBatch(t, b.Discard())
}

func TestLoadPinsBeforeLaterAppend(t *testing.T) {
	backend := mem.New()
	h := newLifecycleHistory(t, backend)
	s := boundSession(t, h, sessionPolicy())
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	run := batchRun(t, b)
	submitBatch(t, s, b)
	b = beginBatch(t, s)
	batchInput(t, b, run, []byte("first"))
	submitBatch(t, s, b)
	pinned := s.Snapshot()
	reader := &loadBackend{Backend: readOnlyHistoryBackend{Backend: backend, t: t}, afterHead: func() { b := beginBatch(t, s); batchInput(t, b, run, []byte("later")); submitBatch(t, s, b) }}
	loaded, err := session.Load(context.Background(), newLifecycleHistory(t, reader), testHistoryID(t), sessionPolicy())
	mustBatch(t, err)
	if !reflect.DeepEqual(loaded.Snapshot(), pinned) || s.Snapshot().Head() == pinned.Head() || reader.observes != 1 || len(reader.reads) != 2 {
		t.Fatal("load exceeded pin")
	}
	for _, r := range reader.reads {
		if r.Through != pinned.Head() {
			t.Fatal("endpoint drift")
		}
	}
	assertHistoryHead(t, h, s.Snapshot().Head())
}

func TestLoadAfterAcceptedBeforeLocalApply(t *testing.T) {
	lostAck := errors.New("acknowledgment lost")
	backend := &batchBackend{Backend: mem.New(), fail: lostAck, accept: true}
	h := newLifecycleHistory(t, backend)
	s := boundSession(t, h, sessionPolicy())
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	run := batchRun(t, b)
	input := batchInput(t, b, run, []byte("accepted"))
	before := s.Snapshot()
	if ref, err := s.SubmitCommit(context.Background(), b); ref != (history.CommitRef{}) || !errors.Is(err, lostAck) {
		t.Fatalf("uncertain: %v, %v", ref, err)
	}
	if !reflect.DeepEqual(s.Snapshot(), before) || len(backend.attempts) != 1 {
		t.Fatal("hidden retry/publication")
	}
	// Abandon the unresolved batch. Fresh recovery neither retries nor repairs it.
	readOnly := newLifecycleHistory(t, readOnlyHistoryBackend{Backend: backend, t: t})
	loaded, err := session.Load(context.Background(), readOnly, testHistoryID(t), sessionPolicy())
	mustBatch(t, err)
	v := loaded.Snapshot()
	if v.Head() != backend.attempts[0].commit.Ref || !v.Created() || v.ActiveRun() != run || v.RunCount() != 1 || !userInputsMatch(v, run, 1) || lookupInput(t, v, input.RunID(), 1).ID() != input || string(inputBytes(lookupInput(t, v, input.RunID(), 1))) != "accepted" || !reflect.DeepEqual(s.Snapshot(), before) || len(backend.attempts) != 1 {
		t.Fatal("crash recovery lost facts or changed abandoned object")
	}
	if _, err := s.BeginCommit(); !errors.Is(err, session.ErrBatch) {
		t.Fatal("reload released old slot")
	}
	assertHistoryHead(t, h, v.Head())
}

func TestLoadRedactsBackendFailures(t *testing.T) {
	backend := mem.New()
	h := newLifecycleHistory(t, backend)
	s := boundSession(t, h, sessionPolicy())
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	submitBatch(t, s, b)
	for _, stage := range []string{"observation", "read"} {
		t.Run(stage, func(t *testing.T) {
			marker := errors.New("backend marker must remain private")
			reader := &loadBackend{Backend: readOnlyHistoryBackend{Backend: backend, t: t}}
			if stage == "observation" {
				reader.observeError = marker
			} else {
				reader.readError = marker
			}
			loaded, err := session.Load(context.Background(), newLifecycleHistory(t, reader), testHistoryID(t), sessionPolicy())
			if loaded != nil || !errors.Is(err, marker) || strings.Contains(err.Error(), marker.Error()) || reader.appends != 0 {
				t.Fatalf("redacted failure: %v, %v", loaded, err)
			}
		})
	}
	reader := &loadBackend{Backend: readOnlyHistoryBackend{Backend: backend, t: t}}
	loaded, err := session.Load(context.Background(), newLifecycleHistory(t, reader), testHistoryID(t), sessionPolicy())
	if err != nil || loaded == nil || !loaded.Snapshot().Created() || reader.appends != 0 {
		t.Fatalf("happy load: %v, %v", loaded, err)
	}
}

func TestLoadRejectsInvalidHistories(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		want       error
	}{
		{"unknown", "missing", history.ErrNotFound}, {"corrupt canonical bytes", "corrupt", history.ErrCorrupt},
		{"unsupported kind", "kind", session.ErrUnsupportedRecord}, {"unsupported version", "version", session.ErrUnsupportedRecord},
		{"malformed payload", "payload", codec.ErrInvalidData}, {"illegal lifecycle", "transition", session.ErrTransition},
		{"over retained policy", "retained", codec.ErrLimitExceeded}, {"over payload policy", "payload-limit", codec.ErrLimitExceeded},
		{"over commit policy", "commit-limit", codec.ErrLimitExceeded}, {"read failure", "read-error", context.Canceled},
		{"premature valid prefix", "prefix", history.ErrCorrupt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := mem.New()
			h := newLifecycleHistory(t, backend)
			policy := sessionPolicy()
			reader := &loadBackend{Backend: readOnlyHistoryBackend{Backend: backend, t: t}}
			if tc.kind != "missing" {
				root := format.Commit{FormatVersion: 1, Sequence: 1, HistoryID: new(testHistoryID(t)), Records: []format.Record{createdRecord(t).Format()}}
				r, err := h.Append(context.Background(), testHistoryID(t), root)
				mustBatch(t, err)
				if r.Status != history.StatusAccepted {
					t.Fatal("root")
				}
				rootRef := r.Proposal
				record := startRecord(t, 1).Format()
				switch tc.kind {
				case "corrupt":
					reader.corrupt = true
				case "kind":
					record.Kind = "session.future"
				case "version":
					record.KindVersion++
				case "payload":
					record.Payload = []byte{0xff}
				case "transition":
					record = createdRecord(t).Format()
				case "retained":
					policy.MaxRetainedRecords = 1
					policy.SoftMaxRetainedRecords = 1
				case "payload-limit":
					policy.MaxPayloadBytes = 1
					policy.SoftMaxPayloadBytes = 1
				case "commit-limit":
					policy.MaxRecordsPerCommit = 1
				case "read-error":
					reader.readError = context.Canceled
				case "prefix":
					reader.prefix = &rootRef
				}
				records := []format.Record{record}
				if tc.kind == "commit-limit" {
					records = append(records, inputRecord(t, 1).Format())
				}
				r, err = h.Append(context.Background(), testHistoryID(t), format.Commit{FormatVersion: 1, Sequence: 2, Parent: new(rootRef.CID), Records: records})
				mustBatch(t, err)
				if r.Status != history.StatusAccepted {
					t.Fatal("successor")
				}
			}
			loaded, err := session.Load(context.Background(), newLifecycleHistory(t, reader), testHistoryID(t), policy)
			if loaded != nil || !errors.Is(err, tc.want) {
				t.Fatalf("Load = %v, %v; want %v", loaded, err, tc.want)
			}
		})
	}
}

func TestLoadRejectsInvalidArgumentsBeforeIO(t *testing.T) {
	reader := &loadBackend{Backend: readOnlyHistoryBackend{Backend: mem.New(), t: t}}
	h := newLifecycleHistory(t, reader)
	for _, tc := range []struct {
		h      *history.History
		id     history.HistoryID
		policy session.Policy
		want   error
	}{
		{nil, testHistoryID(t), sessionPolicy(), session.ErrIdentity}, {h, history.HistoryID{}, sessionPolicy(), session.ErrIdentity}, {h, testHistoryID(t), session.Policy{}, session.ErrInvalidPolicy},
	} {
		loaded, err := session.Load(context.Background(), tc.h, tc.id, tc.policy)
		if loaded != nil || !errors.Is(err, tc.want) {
			t.Fatalf("invalid Load = %v, %v", loaded, err)
		}
	}
	if reader.observes != 0 || len(reader.reads) != 0 {
		t.Fatal("invalid construction did I/O")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	loaded, err := session.Load(ctx, h, testHistoryID(t), sessionPolicy())
	if loaded != nil || !errors.Is(err, context.Canceled) || reader.observes != 0 {
		t.Fatalf("canceled Load: %v, %v", loaded, err)
	}
}
