package conformance

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"runtime"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
)

func appendLimits(t *testing.T, factory historyFactory) {
	for _, name := range []string{"bytes", "records", "kind", "payload", "version", "sequence", "empty-records"} {
		t.Run(name, func(t *testing.T) {
			id := history.NewHistoryID()
			commit := format.Commit{FormatVersion: 1, HistoryID: id, Sequence: 1,
				Records: []format.Record{
					{Kind: opaqueKind, KindVersion: 7, Payload: []byte("private-payload")},
					{Kind: opaqueKind, KindVersion: 7},
				},
			}
			// Encode under larger limits first: each resource rejection belongs
			// to Append, not to fixture construction or another exceeded bound.
			b, _, err := format.EncodeCommit(commit, policy().CommitLimits)
			if err != nil {
				t.Fatal(err)
			}
			p := policy()
			switch name {
			case "bytes":
				p.CommitLimits.MaxCommitBytes = len(b) - 1
			case "records":
				p.CommitLimits.MaxRecords = 1
			case "kind":
				p.CommitLimits.MaxKindBytes = len("opaque") - 1
			case "payload":
				p.CommitLimits.MaxPayloadBytes = len("private-payload") - 1
			case "version":
				commit.FormatVersion = 2
			case "sequence":
				commit.Sequence = 0
			case "empty-records":
				commit.Records = nil
			}
			s := factory(t, p)
			got, err := s.Append(context.Background(), id, commit)
			if !errors.Is(err, history.ErrInvalid) || got != (history.Resolution{}) || bytes.Contains([]byte(err.Error()), []byte("private-payload")) {
				t.Fatalf("invalid append = %+v, %v", got, err)
			}
			if head, err := s.Head(context.Background(), id); !errors.Is(err, history.ErrNotFound) || head != (history.CommitRef{}) {
				t.Fatalf("invalid append mutated store: %+v, %v", head, err)
			}
		})
	}
}

// rejectedAppendAllocationLimit permits fixed decoder and error overhead, but is
// far below each fixture's body or record-array size. RunAllocationChecks excludes
// parallel tests and unrelated background workers from the measurement.
const (
	rejectedAppendCalls           = 4
	rejectedAppendAllocationLimit = 64 << 10
	largeInputBytes               = 512 << 10
)

func totalAlloc(t *testing.T, operation func()) uint64 {
	t.Helper()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	operation()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func appendLargeLimitRejections(t *testing.T, factory historyFactory) {
	const inputMarker = "conformance-private-input"
	large := format.Limits{
		MaxCommitBytes:  2 << 20,
		MaxRecords:      9000,
		MaxKindBytes:    600 << 10,
		MaxPayloadBytes: 600 << 10,
	}
	id := history.NewHistoryID()
	fixture := func(records []format.Record) format.Commit {
		return format.Commit{FormatVersion: 1, HistoryID: id, Sequence: 1, Records: records}
	}
	largePayload := func() []byte {
		return append([]byte(inputMarker), bytes.Repeat([]byte{'p'}, largeInputBytes-len(inputMarker))...)
	}
	records := make([]format.Record, 8192)
	for i := range records {
		records[i] = format.Record{Kind: inputMarker, KindVersion: 1}
	}
	for _, test := range []struct {
		name   string
		input  format.Commit
		adjust func(*history.Policy)
	}{
		{"bytes", fixture([]format.Record{{Kind: opaqueKind, KindVersion: 1, Payload: largePayload()}}), func(p *history.Policy) { p.CommitLimits.MaxCommitBytes = 1024 }},
		{"records", fixture(records), func(p *history.Policy) { p.CommitLimits.MaxRecords = len(records) - 1 }},
		{"kind", fixture([]format.Record{{Kind: string(largePayload()), KindVersion: 1}}), func(p *history.Policy) { p.CommitLimits.MaxKindBytes = 32 }},
		{"payload", fixture([]format.Record{{Kind: opaqueKind, KindVersion: 1, Payload: largePayload()}}), func(p *history.Policy) { p.CommitLimits.MaxPayloadBytes = 32 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := policy()
			p.CommitLimits = large
			test.adjust(&p)
			s := factory(t, p)
			allocated := totalAlloc(t, func() {
				for range rejectedAppendCalls {
					got, err := s.Append(context.Background(), id, test.input)
					if !errors.Is(err, history.ErrInvalid) || got != (history.Resolution{}) || bytes.Contains([]byte(err.Error()), []byte(inputMarker)) {
						t.Fatalf("large invalid append = %+v, %v", got, err)
					}
				}
			})
			if maxAllocation := uint64(rejectedAppendCalls * rejectedAppendAllocationLimit); allocated > maxAllocation {
				t.Fatalf("rejected append allocated %d bytes; want at most %d", allocated, maxAllocation)
			}
			if head, err := s.Head(context.Background(), id); !errors.Is(err, history.ErrNotFound) || head != (history.CommitRef{}) {
				t.Fatalf("large invalid append mutated store: %+v, %v", head, err)
			}
		})
	}
}

func scopeIsolation(t *testing.T, factory historyFactory) {
	s := factory(t, policy())
	ctx := context.Background()
	id, otherID := history.NewHistoryID(), history.NewHistoryID()
	root, r1 := encoded(t, id, history.CommitRef{}, "root")
	otherRoot, otherR1 := encoded(t, otherID, history.CommitRef{}, "other root")
	appendAccepted(t, s, id, root, r1)
	appendAccepted(t, s, otherID, otherRoot, otherR1)
	foreignChild, foreignRef := encoded(t, otherID, otherR1, "foreign parent")
	got, err := s.Append(ctx, id, foreignChild)
	wantResolution(t, got, err, history.StatusConflict, foreignRef, r1)
	got, err = s.Resolve(ctx, id, otherR1)
	wantResolution(t, got, err, history.StatusConflict, otherR1, r1)
	for _, after := range []history.CommitRef{{}, r1} {
		page, err := s.Read(ctx, id, history.ReadRequest{After: after, Through: otherR1, MaxCommits: 1})
		if !errors.Is(err, history.ErrInvalid) || !reflect.DeepEqual(page, history.Page{}) {
			t.Fatalf("foreign endpoint = %+v, %v", page, err)
		}
	}
	appendAccepted(t, s, otherID, foreignChild, foreignRef)
	if head, err := s.Head(ctx, id); err != nil || head != r1 {
		t.Fatalf("other scope advanced this history: %+v, %v", head, err)
	}
	child, r2 := encoded(t, id, r1, "own parent")
	appendAccepted(t, s, id, child, r2)
}

func concurrentSuccessorAndDuplicateAppends(t *testing.T, factory historyFactory) {
	s := factory(t, policy())
	id := history.NewHistoryID()
	root, rootRef := encoded(t, id, history.CommitRef{}, "root")
	appendAccepted(t, s, id, root, rootRef)
	b1, r1 := encoded(t, id, rootRef, "successor one")
	b2, r2 := encoded(t, id, rootRef, "successor two")
	start := make(chan struct{})
	heads := make(chan history.CommitRef, 32)
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			<-start
			b, ref := b1, r1
			if i%2 == 1 {
				b, ref = b2, r2
			}
			got, err := s.Append(context.Background(), id, b)
			heads <- got.Head
			want := history.StatusConflict
			if got.Head == ref {
				want = history.StatusAccepted
			}
			if err != nil || got.Proposal != ref || (got.Head != r1 && got.Head != r2) || got.Status != want {
				t.Errorf("competing successor = %+v, %v", got, err)
			}
		})
	}
	close(start)
	wg.Wait()
	close(heads)
	head, err := s.Head(context.Background(), id)
	if err != nil || (head != r1 && head != r2) {
		t.Fatalf("single successor winner = %+v, %v", head, err)
	}
	for observed := range heads {
		if observed != head {
			t.Errorf("multiple successor winners: %+v and %+v", observed, head)
		}
	}
	page, err := s.Read(context.Background(), id, history.ReadRequest{Through: head, MaxCommits: 2})
	winner := b1
	if head == r2 {
		winner = b2
	}
	if err != nil || page.Next != head || !sameCommits(page.Commits, []format.Commit{root, winner}) {
		t.Fatalf("winner bytes = %+v, %v", page, err)
	}
}
