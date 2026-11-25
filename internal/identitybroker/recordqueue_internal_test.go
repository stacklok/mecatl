package identitybroker

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestRecordQueueNeverWaitsOnTheSink pins that pushing a record returns while
// the sink is stalled, that records leave in the order pushed, each with a
// deadline, and that a record pushed past the bound is reported as dropped
// rather than waited for.
func TestRecordQueueNeverWaitsOnTheSink(t *testing.T) {
	const limit = 10 * time.Second
	entered, release := make(chan struct{}), make(chan struct{})
	var enter sync.Once
	var mu sync.Mutex
	var sent, dropped []string
	var deadlines int
	q := newRecordQueue(func(ctx context.Context, rec Record) {
		enter.Do(func() { close(entered) })
		<-release
		mu.Lock()
		defer mu.Unlock()
		if _, ok := ctx.Deadline(); ok {
			deadlines++
		}
		sent = append(sent, rec.Header().AuditID)
	}, func(rec Record) {
		mu.Lock()
		defer mu.Unlock()
		dropped = append(dropped, rec.Header().AuditID)
	})
	record := func(i int) Record {
		return &SessionCloseRecord{RecordHeader: RecordHeader{AuditID: fmt.Sprintf("rec_%04d", i)}}
	}
	// The first push starts a drainer that stalls on the sink with it: the
	// others, one past the bound, still return at once.
	q.push(record(0))
	select {
	case <-entered:
	case <-time.After(limit):
		t.Fatal("the drainer did not send the first record")
	}
	pushed := make(chan struct{})
	go func() {
		for i := 1; i < maxQueuedRecords+2; i++ {
			q.push(record(i))
		}
		close(pushed)
	}()
	select {
	case <-pushed:
	case <-time.After(limit):
		t.Fatal("push waited on a stalled sink")
	}
	close(release)
	q.wait()
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != maxQueuedRecords+1 || len(dropped) != 1 || deadlines != len(sent) {
		t.Fatalf("sent %d (%d with a deadline), dropped %d; want %d sent, each with a deadline, and 1 dropped",
			len(sent), deadlines, len(dropped), maxQueuedRecords+1)
	}
	for i, id := range sent {
		if id != record(i).Header().AuditID {
			t.Fatalf("record %d sent was %s, want %s: records left out of order", i, id, record(i).Header().AuditID)
		}
	}
	if dropped[0] != record(maxQueuedRecords+1).Header().AuditID {
		t.Fatalf("dropped %s, want the record pushed past the bound", dropped[0])
	}
}
