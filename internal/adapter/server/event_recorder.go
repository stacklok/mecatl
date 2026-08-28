package server

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// maxCoalescedTextBytes keeps even a worst-case JSON-escaped text payload well
// below jsonlstore's 16 MiB scanner limit (each input byte can become six JSON
// bytes). It also bounds each of the recorder's two in-memory accumulators.
const maxCoalescedTextBytes = 1 << 20

// RunEventRecorder is the run-scoped durable projection of a live event stream.
// It coalesces high-frequency text deltas while leaving the client-facing stream
// untouched. Close must be called after the stream has been drained.
type RunEventRecorder struct {
	svc *Service
	ctx context.Context
	id  session.SessionID

	turn      int
	haveTurn  bool
	nextOrder int
	message   pendingDelta
	reasoning pendingDelta
	warned    bool
}

type pendingDelta struct {
	event session.Event
	text  strings.Builder
	order int
	set   bool
}

// NewRunEventRecorder creates a recorder for one relayed run or merged relay.
func NewRunEventRecorder(ctx context.Context, svc *Service, id session.SessionID) *RunEventRecorder {
	return &RunEventRecorder{svc: svc, ctx: ctx, id: id}
}

// Observe adds ev to the durable projection. Delta events are buffered in
// bounded UTF-8 chunks; every other event first flushes buffered deltas and is
// then appended itself. Every projected event is attempted exactly once because
// EventLog.Append may return an error after durably writing it.
func (r *RunEventRecorder) Observe(ev session.Event) {
	if ev.Type != session.EvMessageDelta && ev.Type != session.EvReasoningDelta {
		r.flush()
		r.append(ev)
		return
	}

	if r.haveTurn && ev.Turn != r.turn {
		r.flush()
	}
	if !r.haveTurn {
		r.turn = ev.Turn
		r.haveTurn = true
	}

	pending := &r.message
	if ev.Type == session.EvReasoningDelta {
		pending = &r.reasoning
	}
	r.appendText(pending, ev)
}

// Close attempts any incomplete final turn's buffered deltas once.
func (r *RunEventRecorder) Close() {
	r.flush()
}

func (r *RunEventRecorder) appendText(p *pendingDelta, ev session.Event) {
	text := ev.Text
	for text != "" {
		if !p.set {
			p.event = ev
			p.event.Text = ""
			p.order = r.nextOrder
			p.set = true
			r.nextOrder++
		}
		remaining := maxCoalescedTextBytes - p.text.Len()
		n := min(len(text), remaining)
		for n > 0 && n < len(text) && !utf8.RuneStart(text[n]) {
			n--
		}
		// A valid rune is at most four bytes, while remaining can only be smaller
		// after preceding text filled this chunk. Flush first rather than split it.
		if n == 0 {
			r.appendOlderPending(p)
			r.appendPending(p)
			continue
		}
		p.text.WriteString(text[:n])
		text = text[n:]
		if p.text.Len() == maxCoalescedTextBytes {
			r.appendOlderPending(p)
			r.appendPending(p)
		}
	}
}

// appendOlderPending preserves first-observed kind ordering when p fills before
// the older kind reaches a normal flush boundary.
func (r *RunEventRecorder) appendOlderPending(p *pendingDelta) {
	other := &r.message
	if p == other {
		other = &r.reasoning
	}
	if other.set && other.order < p.order {
		r.appendPending(other)
	}
}

func (r *RunEventRecorder) flush() {
	first, second := &r.message, &r.reasoning
	if r.reasoning.set && (!r.message.set || r.reasoning.order < r.message.order) {
		first, second = second, first
	}
	r.appendPending(first)
	r.appendPending(second)
	r.haveTurn = false
}

func (r *RunEventRecorder) appendPending(p *pendingDelta) {
	if !p.set {
		return
	}
	p.event.Text = p.text.String()
	r.append(p.event)
	*p = pendingDelta{}
}

func (r *RunEventRecorder) append(ev session.Event) {
	if err := r.svc.appendEvent(r.ctx, r.id, ev); err != nil {
		if !r.warned {
			r.warned = true
			r.svc.cfg.Diagnostics.Log(r.ctx, port.LevelWarn, "event log append failed",
				"session", string(r.id), "event", string(ev.Type), "err", err.Error())
		}
	}
}
