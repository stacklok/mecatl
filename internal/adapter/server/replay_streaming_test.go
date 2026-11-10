package server

import (
	"context"
	"errors"
	"iter"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

type countingReplayLog struct {
	port.EventLog
	reads atomic.Int64
	event session.Event
}

// Deliberately ignores cancellation: the service must stop its own collection.
func (l *countingReplayLog) Read(context.Context, session.SessionID) iter.Seq2[session.Event, error] {
	return func(yield func(session.Event, error) bool) {
		for i := range 1_000_000 {
			if i == 8 {
				// Fail safely rather than OOM if replay starts collecting again.
				yield(session.Event{}, errors.New("excessive replay read-ahead"))
				return
			}
			l.reads.Add(1)
			if !yield(l.event, nil) {
				return
			}
		}
	}
}

func TestSessionReplayStreamsBeforeReadingLargeSource(t *testing.T) {
	for _, ev := range []session.Event{{Type: session.EvUserPrompt, Text: strings.Repeat("x", 64<<10)}} {
		log := &countingReplayLog{event: ev}
		svc := &Service{cfg: Config{EventLog: log}}
		var wg sync.WaitGroup
		for range 16 {
			wg.Go(func() {
				stream, err := svc.StreamSessionEvents(t.Context(), "counting")
				if err != nil {
					t.Error(err)
					return
				}
				for _, err := range stream {
					if err != nil {
						t.Error(err)
					}
					break // Slow/disconnected readers cannot cause read-ahead.
				}
			})
		}
		wg.Wait()
		if got := log.reads.Load(); got != 16 {
			t.Fatalf("read before first yield was not bounded: %d", got)
		}
	}
}

func TestSessionReplayHonorsCancellationWithoutAdapterHelp(t *testing.T) {
	for _, before := range []bool{true, false} {
		ctx, cancel := context.WithCancel(t.Context())
		log := &countingReplayLog{event: session.Event{Type: session.EvUserPrompt}}
		svc := &Service{cfg: Config{EventLog: log}}
		stream, err := svc.StreamSessionEvents(ctx, "counting")
		if err != nil {
			t.Fatal(err)
		}
		if before {
			cancel()
		}
		yielded, canceled := 0, false
		for _, err := range stream {
			if err != nil {
				canceled = errors.Is(err, context.Canceled)
				continue
			}
			yielded++
			cancel()
		}
		cancel()
		if !canceled || yielded > 1 || log.reads.Load() > 2 || before && log.reads.Load() != 0 {
			t.Fatalf("cancellation ignored: before=%t yielded=%d reads=%d canceled=%t", before, yielded, log.reads.Load(), canceled)
		}
	}
}
