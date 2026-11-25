package identitybroker

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	}, func(rec Record, err error) {
		mu.Lock()
		defer mu.Unlock()
		if errors.Is(err, errRecordQueueFull) {
			dropped = append(dropped, rec.Header().AuditID)
		}
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
	if err := q.wait(t.Context()); err != nil {
		t.Fatal(err)
	}
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

// TestRecordQueueCloseReportsWhatItCouldNotSend pins that close sends what is
// queued until its context ends, then reports each record still queued and
// the one being sent, and that a record pushed after close is reported
// rather than queued.
func TestRecordQueueCloseReportsWhatItCouldNotSend(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var enter sync.Once
	var mu sync.Mutex
	var sent []string
	reported := map[string]error{}
	q := newRecordQueue(func(_ context.Context, rec Record) {
		enter.Do(func() { close(entered) })
		<-release
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, rec.Header().AuditID)
	}, func(rec Record, err error) {
		mu.Lock()
		defer mu.Unlock()
		reported[rec.Header().AuditID] = err
	})
	record := func(id string) Record { return &SessionCloseRecord{RecordHeader: RecordHeader{AuditID: id}} }
	q.push(record("rec_first"))
	<-entered
	q.push(record("rec_second"))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := q.close(ctx); err == nil || !strings.Contains(err.Error(), "1 records not sent and 1 not confirmed") {
		t.Fatalf("close with a record unsent and one in flight = %v, want both counted", err)
	}
	q.push(record("rec_after"))
	close(release)
	if err := q.wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 1 || sent[0] != "rec_first" {
		t.Fatalf("sent %v, want only the record in flight at close", sent)
	}
	if !errors.Is(reported["rec_second"], errRecordQueueClosed) || !errors.Is(reported["rec_after"], errRecordQueueClosed) ||
		!errors.Is(reported["rec_first"], errRecordInFlightAtClose) || len(reported) != 3 {
		t.Fatalf("reported %v, want the unsent record, the one in flight, and the one pushed after close", reported)
	}
}

// TestRecordQueueCloseCountsARecordInFlight pins that close, when its
// context ends with one record being sent and none queued, counts and
// reports that record rather than return as if every record was sent.
func TestRecordQueueCloseCountsARecordInFlight(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var reported []error
	q := newRecordQueue(func(context.Context, Record) {
		close(entered)
		<-release
	}, func(_ Record, err error) {
		mu.Lock()
		defer mu.Unlock()
		reported = append(reported, err)
	})
	q.push(&SessionCloseRecord{RecordHeader: RecordHeader{AuditID: "rec_only"}})
	<-entered
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := q.close(ctx)
	close(release)
	if err == nil || !strings.Contains(err.Error(), "0 records not sent and 1 not confirmed") {
		t.Fatalf("close with one record in flight = %v, want it counted", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reported) != 1 || !errors.Is(reported[0], errRecordInFlightAtClose) {
		t.Fatalf("reported %v, want the record in flight", reported)
	}
}
