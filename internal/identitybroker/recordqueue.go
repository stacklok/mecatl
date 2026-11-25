package identitybroker

import (
	"context"
	"errors"
	"sync"
	"time"
)

// maxQueuedRecords bounds the records waiting in a recordQueue. A record
// queued past it is dropped and reported as a delivery failure.
const maxQueuedRecords = 1024

// queuedRecordTimeout bounds each send from a recordQueue, so a stalled sink
// delays the records behind it by at most this long each, not forever.
const queuedRecordTimeout = 10 * time.Second

// errRecordQueueFull is the delivery failure of a record dropped because the
// queue was full.
var errRecordQueueFull = errors.New("the broker's record queue is full")

// recordQueue sends the records of events that take effect whether or not
// their record is accepted, such as a close, apart from the call that caused
// them, so no call waits on the sink for them (audit.md, "Write order and
// failure"). Records leave in the order they were queued, one at a time, each
// within queuedRecordTimeout. A drainer goroutine runs only while records
// wait, so an idle broker runs none.
//
// Shortcut: the queue is in memory and bounded; a record dropped when it is full, or lost with the process, is only reported as a delivery failure; removed by #2848 (lossless delivery).
type recordQueue struct {
	// send records one record; failures are reported by send itself.
	send func(context.Context, Record)
	// dropped reports a record dropped because the queue was full.
	dropped func(Record)

	mu      sync.Mutex
	items   []Record
	full    []Record
	running bool
	idle    *sync.Cond
}

func newRecordQueue(send func(context.Context, Record), dropped func(Record)) *recordQueue {
	q := &recordQueue{send: send, dropped: dropped}
	q.idle = sync.NewCond(&q.mu)
	return q
}

// push queues rec and starts a drainer if none runs. It never waits on the
// sink, so it may be called with the session table's lock held, which keeps
// records in the order their events took effect.
func (q *recordQueue) push(rec Record) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) >= maxQueuedRecords {
		q.full = append(q.full, rec)
	} else {
		q.items = append(q.items, rec)
	}
	if !q.running {
		q.running = true
		go q.drain()
	}
}

// drain sends the queued records until none is left, then exits.
func (q *recordQueue) drain() {
	for {
		q.mu.Lock()
		full := q.full
		q.full = nil
		if len(q.items) == 0 && len(full) == 0 {
			q.running = false
			q.idle.Broadcast()
			q.mu.Unlock()
			return
		}
		var rec Record
		if len(q.items) > 0 {
			rec = q.items[0]
			q.items[0] = nil
			q.items = q.items[1:]
		}
		q.mu.Unlock()
		for _, d := range full {
			q.dropped(d)
		}
		if rec != nil {
			ctx, cancel := context.WithTimeout(context.Background(), queuedRecordTimeout)
			q.send(ctx, rec)
			cancel()
		}
	}
}

// wait returns once no record waits and no drainer runs.
func (q *recordQueue) wait() {
	q.mu.Lock()
	defer q.mu.Unlock()
	for q.running {
		q.idle.Wait()
	}
}
