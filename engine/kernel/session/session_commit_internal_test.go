package session

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
)

// afterAcceptBackend injects a local invariant violation precisely after real
// storage acceptance. It is not a production callback or alternate reducer.
type afterAcceptBackend struct {
	history.Backend
	after func()
}

func (b afterAcceptBackend) Append(ctx context.Context, id history.HistoryID, expected history.CommitRef, c history.ByteCommit) (history.Observation, error) {
	o, err := b.Backend.Append(ctx, id, expected, c)
	if err == nil {
		b.after()
	}
	return o, err
}

func TestSessionCommitAcceptedLocalApplyFailure(t *testing.T) {
	limits := codec.Limits{MaxBytes: 4096, MaxStringBytes: 128, MaxByteStringBytes: 1024,
		MaxArrayElements: 16, MaxMapPairs: 8, MaxDepth: 8, MaxNodes: 512, MaxCollectionElements: 128}
	id, err := historyid.ParseHistoryID("HST1111111111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	var s *Session
	backend := afterAcceptBackend{Backend: mem.New(), after: func() {
		// Fixed policy makes this unreachable ordinarily. Violate it only inside
		// this deterministic test seam to exercise accepted-but-unapplied output.
		s.policy = Policy{}
	}}
	h, err := history.New(backend, history.Policy{CommitLimits: limits, RetryWindow: 16,
		MaxPageBytes: limits.MaxBytes, MaxPageCommits: 2})
	if err != nil {
		t.Fatal(err)
	}
	policy := Policy{CommitLimits: limits, PayloadLimits: limits,
		MaxRecordsPerCommit: 16, SoftMaxRetainedRecords: 128, SoftMaxPayloadBytes: 32768, SoftMaxDecodedWork: 1 << 24, MaxRetainedRecords: 128, MaxPayloadBytes: 32768, MaxDecodedWork: 1 << 24}
	s, err = New(h, id, policy)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.BeginCommit()
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Create(); err != nil {
		t.Fatal(err)
	}
	before := s.Snapshot()
	ref, err := s.SubmitCommit(context.Background(), b)
	if ref == (history.CommitRef{}) || !errors.Is(err, ErrAcceptedApply) {
		t.Fatalf("acceptance lost: %v, %v", ref, err)
	}
	head, err := h.Head(context.Background(), id)
	if err != nil || head != ref || !reflect.DeepEqual(before, s.Snapshot()) {
		t.Fatal("accepted reference or atomic local failure is wrong")
	}
	if _, err := s.BeginCommit(); !errors.Is(err, ErrReloadRequired) {
		t.Fatal("local failure did not fence writes")
	}
	if _, err := s.SubmitCommit(context.Background(), b); !errors.Is(err, ErrBatch) {
		t.Fatal("accepted-but-unapplied batch resubmitted")
	}
	if err := s.Apply(history.Commit{}); !errors.Is(err, ErrReloadRequired) {
		t.Fatal("Apply bypassed reload barrier")
	}
	recovered, err := Load(context.Background(), h, id, policy)
	if err != nil || recovered.Snapshot().Head() != ref || !recovered.Snapshot().Created() {
		t.Fatalf("accepted-local-failure recovery: %v", err)
	}
}
