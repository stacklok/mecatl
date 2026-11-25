package identitybroker

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// maxQueuedRecords bounds the records waiting in a recordQueue. A record
// queued past it is dropped and reported as a delivery failure; so is a
// refusal queued past maxQueuedRecords-closeReserve.
const maxQueuedRecords = 1024

// closeReserve is how many of maxQueuedRecords only closes may fill: a
// refusal is dropped once fewer remain. A refusal ends nothing, and an agent
// looping on denied calls while the sink is slow can queue them faster than
// they are sent; a close records the end of a session's authority, which
// matters for revocation. A quarter of the queue keeps room for a burst of
// closes, such as a workspace's sessions deleted together, while refusals
// fill the rest.
const closeReserve = maxQueuedRecords / 4

// errRecordQueueFull is the delivery failure of a record dropped because the
// queue was full, errRecordQueueClosed of one queued after Close or left
// unsent when Close gave up, and errRecordInFlightAtClose of one still being
// sent then, which the sink may yet accept or refuse.
var (
	errRecordQueueFull       = errors.New("the broker's record queue is full")
	errRecordQueueClosed     = errors.New("the broker closed before sending the record")
	errRecordInFlightAtClose = errors.New("the broker closed while sending the record")
)

// recordQueue sends the records of events that take effect whether or not
// their record is accepted, closes and refusals, apart from the call that
// caused them, so no call and no per-key lock waits on the sink for them
// (audit.md, "Write order and failure"). Records leave in the order they were
// queued, one at a time, each within recordTimeout. A drainer goroutine runs
// only while records wait, so an idle broker runs none.
//
// Shortcut: the queue is in memory and bounded; a record dropped when it is full, left at Close, or lost with the process is only reported as a delivery failure, and records are numbered where the sink receives them, not where they are emitted; removed by #2848 (lossless delivery) and #2808 (gap detection).
type recordQueue struct {
	// send records one record; failures are reported by send itself.
	send func(context.Context, Record)
	// dropped reports a record dropped unsent with err. It is called with
	// the session table's lock held, so it must return promptly and must
	// not call the broker.
	dropped func(Record, error)

	mu    sync.Mutex
	items []Record
	// sending is the record the drainer is sending; nil when none.
	sending Record
	closed  bool
	// idle is closed when the running drainer exits; nil while none runs.
	idle chan struct{}
}

func newRecordQueue(send func(context.Context, Record), dropped func(Record, error)) *recordQueue {
	return &recordQueue{send: send, dropped: dropped}
}

// push queues rec and starts a drainer if none runs, or reports rec dropped
// when the queue is full or closed. It never waits on the sink, so it may be
// called with the session table's lock held, which keeps records in the
// order their events took effect.
func (q *recordQueue) push(rec Record) {
	q.mu.Lock()
	switch {
	case q.closed:
		q.mu.Unlock()
		q.dropped(rec, errRecordQueueClosed)
		return
	case len(q.items) >= maxQueuedRecords, !isClose(rec) && len(q.items) >= maxQueuedRecords-closeReserve:
		q.mu.Unlock()
		q.dropped(rec, errRecordQueueFull)
		return
	}
	q.items = append(q.items, rec)
	if q.idle == nil {
		q.idle = make(chan struct{})
		go q.drain(q.idle)
	}
	q.mu.Unlock()
}

// isClose reports whether rec is a session close's record, which may use
// closeReserve.
func isClose(rec Record) bool {
	_, ok := rec.(*SessionCloseRecord)
	return ok
}

// drain sends the queued records until none is left, then exits and closes
// idle.
func (q *recordQueue) drain(idle chan struct{}) {
	for {
		q.mu.Lock()
		if len(q.items) == 0 {
			q.idle = nil
			q.mu.Unlock()
			close(idle)
			return
		}
		rec := q.items[0]
		q.items[0] = nil
		q.items = q.items[1:]
		q.sending = rec
		q.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), recordTimeout)
		q.send(ctx, rec)
		cancel()
		q.mu.Lock()
		q.sending = nil
		q.mu.Unlock()
	}
}

// wait returns once no record waits and no drainer runs, or ctx ends.
func (q *recordQueue) wait(ctx context.Context) error {
	for {
		q.mu.Lock()
		idle := q.idle
		q.mu.Unlock()
		if idle == nil {
			return nil
		}
		select {
		case <-idle:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// close stops q accepting records and sends those queued until ctx ends.
// Each record still queued then is reported dropped, and the record being
// sent, if any, is reported as unconfirmed: it finishes within its own
// deadline, after close returns, so the sink may still accept it. close
// returns an error counting both.
func (q *recordQueue) close(ctx context.Context) error {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	if q.wait(ctx) == nil {
		return nil
	}
	q.mu.Lock()
	left, sending := q.items, q.sending
	q.items = nil
	q.mu.Unlock()
	for _, rec := range left {
		q.dropped(rec, errRecordQueueClosed)
	}
	unconfirmed := 0
	if sending != nil {
		unconfirmed = 1
		q.dropped(sending, errRecordInFlightAtClose)
	}
	if len(left) == 0 && unconfirmed == 0 {
		// The drainer finished between the wait and the lock.
		return nil
	}
	return fmt.Errorf("identity broker: %d records not sent and %d not confirmed before close", len(left), unconfirmed)
}
