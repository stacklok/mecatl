package session

import (
	"errors"
	"fmt"
	"iter"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/history"
)

func TestSnapshotRecordsBetweenPinnedAcrossConcurrentAppends(t *testing.T) {
	s, _ := internalFixture(t)
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	run := batchRun(t, b)
	batchInput(t, b, run, []byte("first"))
	first := submitBatch(t, s, b)
	pinned := s.Snapshot()
	seq, err := pinned.RecordsBetween(history.CommitRef{}, first)
	mustBatch(t, err)
	var readers sync.WaitGroup
	defer readers.Wait()
	entered := make(chan struct{}, 4)
	resume := make(chan struct{})
	defer close(resume)
	for range 4 {
		readers.Go(func() {
			for attempt := range 1000 {
				independent, err := pinned.RecordsBetween(history.CommitRef{}, first)
				if err != nil {
					t.Error(err)
					return
				}
				for j, records := range []iter.Seq[RecordView]{seq, independent} {
					i := 0
					for record := range records {
						if attempt == 0 && j == 0 && i == 0 {
							entered <- struct{}{}
							<-resume
						}
						if i >= pinned.RecordCount() || record != pinned.RecordAt(i) {
							t.Error("concurrent traversal changed")
							return
						}
						i++
					}
					if i != pinned.RecordCount() {
						t.Error("concurrent traversal truncated")
						return
					}
				}
			}
		})
	}
	for range 4 {
		<-entered
	}
	var spare, growth bool
	for n := 0; n < 128; n++ {
		old := s.Snapshot()
		records, err := old.RecordsBetween(history.CommitRef{}, old.Head())
		mustBatch(t, err)
		b := beginBatch(t, s)
		batchInput(t, b, run, []byte("later"))
		next := submitBatch(t, s, b)
		if n == 0 {
			// Every reader has yielded from the old range across this append.
			for range 4 {
				resume <- struct{}{}
			}
		}
		if &old.records[0] == &s.Snapshot().records[0] {
			spare = true
		} else {
			growth = true
		}
		i := 0
		for record := range records {
			if i >= old.RecordCount() || record != old.RecordAt(i) {
				t.Fatal("iterator exposed later records or changed backing")
			}
			i++
		}
		if i != old.RecordCount() {
			t.Fatal("iterator lost records")
		}
		for _, bounds := range [][2]history.CommitRef{{old.Head(), next}, {next, next}} {
			if seq, err := old.RecordsBetween(bounds[0], bounds[1]); seq != nil || !errors.Is(err, ErrIdentity) {
				t.Fatal("append made a future endpoint valid on an old snapshot")
			}
		}
	}
	if !spare || !growth {
		t.Fatal("fixture did not exercise spare capacity and reallocation")
	}
}

// recordsBetweenFixture builds accepted commits, rather than a synthetic index.
// Genesis has three records; every later commit has one input record.
func recordsBetweenFixture(t testing.TB, commits int) Snapshot {
	t.Helper()
	s, _ := internalFixture(t)
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	run := batchRun(t, b)
	batchInput(t, b, run, []byte("first"))
	submitBatch(t, s, b)
	for n := 1; n < commits; n++ {
		b := beginBatch(t, s)
		batchInput(t, b, run, []byte("later"))
		submitBatch(t, s, b)
	}
	return s.Snapshot()
}

var recordsBetweenSink iter.Seq[RecordView]
var recordsBetweenRecordSink RecordView

func TestSnapshotRecordsBetweenBorrowedTraversalAllocations(t *testing.T) {
	for _, commits := range []int{16, 1024} {
		t.Run(fmt.Sprint(commits), func(t *testing.T) {
			snapshot := recordsBetweenFixture(t, commits)
			for _, count := range []int{2, commits - 1} {
				seq, err := snapshot.RecordsBetween(snapshot.commits[commits-count-1].ref, snapshot.Head())
				mustBatch(t, err)
				visited := 0
				visit := func(record RecordView) bool {
					input, ok := record.UserInput()
					if !ok || input.Parts().Len() != 1 || input.ID().RunID().HistoryID() != snapshot.HistoryID() {
						t.Fatal("borrowed traversal lost typed data or scope")
					}
					media, _ := input.Parts().At(0).Media()
					if media.InlineData().At(0) != 'l' {
						t.Fatal("borrowed leaf changed")
					}
					visited++
					return true
				}
				allocations := testing.AllocsPerRun(100, func() { seq(visit) })
				if allocations != 0 || visited != 101*count {
					t.Fatalf("%d records: allocations=%g visits=%d", count, allocations, visited)
				}
			}
		})
	}
}

func BenchmarkSnapshotRecordsBetween(b *testing.B) {
	for _, commits := range []int{16, 1024, 8192} {
		snapshot := recordsBetweenFixture(b, commits)
		counts := []int{2}
		if commits == 1024 {
			counts = append(counts, 16, 1023)
		}
		for _, count := range counts {
			after := snapshot.commits[commits-count-1].ref
			b.Run(fmt.Sprintf("history=%d/records=%d/setup", commits, count), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					var err error
					recordsBetweenSink, err = snapshot.RecordsBetween(after, snapshot.Head())
					if err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run(fmt.Sprintf("history=%d/records=%d/traverse", commits, count), func(b *testing.B) {
				seq, err := snapshot.RecordsBetween(after, snapshot.Head())
				mustBatch(b, err)
				visit := func(record RecordView) bool {
					recordsBetweenRecordSink = record
					return true
				}
				b.ReportAllocs()
				for b.Loop() {
					seq(visit)
				}
			})
		}
	}
}
