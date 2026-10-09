package telemetry

import (
	"context"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// DefaultSlowTurnCapacity is the fixed size of the slow-turn ring buffer. The
// buffer is intrinsically bounded: it holds at most this many of the most-recent
// turns, evicting the oldest when full. 256 is enough recent history for an agent
// to reason over a slow patch without growing the model context (the MCP tool
// paginates) and keeps the per-process footprint trivially small (a few KiB of
// scalars).
const DefaultSlowTurnCapacity = 256

// SlowTurn is one entry of the slow-turn ring buffer. It carries SCALARS ONLY —
// turn index, the per-turn latency numerics, and the end timestamp. There is
// deliberately NO prompt text, tool arguments, or session id field: redaction by
// storage shape means
// the buffer physically cannot leak conversation content, no matter how the data
// is later surfaced. The shape mirrors mcpperf.SlowTurn so the cmd/embed wiring
// can bridge the two with a trivial field copy (telemetry must NOT import mcpperf
// — wrong direction; see SlowTurnBuffer.Recent).
type SlowTurn struct {
	// TurnIndex is the 0-based turn index within its run (Event.Turn).
	TurnIndex int
	// DurationMs is the turn's model-call wall-clock duration in milliseconds.
	DurationMs int64
	// TTFTMs is the time-to-first-token in milliseconds (0 if not measured).
	TTFTMs int64
	// InterTokenMaxMs is the worst inter-token gap in milliseconds (0 if not measured).
	InterTokenMaxMs int64
	// EndedAt is the wall-clock time the turn ended.
	EndedAt time.Time
	// Role is the BOUNDED engine role family that produced the turn (one of the
	// Role* constants — "main", "subagent", "member", …). It is a closed enum,
	// never a def/member name or session id, so redaction-by-shape still holds.
	Role string
}

// SlowTurnBuffer is a fixed-size, in-memory ring buffer of recent turns,
// recording ONE scalar SlowTurn per EvTurnEnd it observes. It satisfies
// port.EventSink so telemetry.NewSink can fan EvTurnEnd into it alongside the
// metrics/tracing sinks — it sees the exact same TurnEndPayload the latency
// histograms do, with no second event path.
//
// It is the concrete backing for mcpperf's SlowTurnSource read seam: the cmd /
// embed composition root bridges SlowTurnBuffer.Recent (returning the telemetry
// SlowTurn type) to the adapter interface (which wants mcpperf.SlowTurn), so the
// dependency points inward (cmd → telemetry, cmd → mcpperf) and telemetry never
// imports the adapter.
//
// It spawns no goroutine — all work happens synchronously inside Emit under a
// mutex — so it is goleak-clean by construction. Reads (Recent) and writes (Emit)
// are concurrency-safe; the engine may emit from a run goroutine while an MCP
// tool reads.
type SlowTurnBuffer struct {
	mu    sync.Mutex
	ring  []SlowTurn // len == cap; a fixed backing array used circularly
	next  int        // index of the next write (mod len(ring))
	count int        // total turns ever recorded (>= len(ring) once full)
	clock func() time.Time
}

// Compile-time check: the buffer observes the event stream as a sink.
var _ port.EventSink = (*SlowTurnBuffer)(nil)

// NewSlowTurnBuffer builds a slow-turn ring buffer of the given capacity (<= 0
// falls back to DefaultSlowTurnCapacity). clock supplies the EndedAt timestamp
// for turns whose payload carries no end time of its own; nil falls back to
// time.Now.
func NewSlowTurnBuffer(capacity int, clock func() time.Time) *SlowTurnBuffer {
	if capacity <= 0 {
		capacity = DefaultSlowTurnCapacity
	}
	if clock == nil {
		clock = time.Now
	}
	return &SlowTurnBuffer{
		ring:  make([]SlowTurn, capacity),
		clock: clock,
	}
}

// Emit records a SlowTurn for every EvTurnEnd, ignoring all other event types,
// attributing the turn to the MAIN engine (role="main"); a child engine's turns
// flow through WithRole instead (mirroring Metrics.WithRole). It stores ONLY
// scalars from the TurnEndPayload (and the Event.Turn index) plus the bounded
// role family — no text ever enters the buffer. The oldest entry is overwritten
// once the ring is full (bounded memory). ctx is unused: the buffer derives
// nothing from it.
func (b *SlowTurnBuffer) Emit(_ context.Context, ev session.Event) {
	b.record(ev, RoleMain)
}

// record is the role-carrying write path behind both Emit (role="main") and the
// WithRole wrapper.
func (b *SlowTurnBuffer) record(ev session.Event, role string) {
	if ev.Type != session.EvTurnEnd || ev.TurnEnd == nil {
		return
	}
	p := ev.TurnEnd
	entry := SlowTurn{
		TurnIndex:       ev.Turn,
		DurationMs:      p.DurationMs,
		TTFTMs:          p.TTFTMs,
		InterTokenMaxMs: p.InterTokenMaxMs,
		EndedAt:         b.clock(),
		Role:            role,
	}
	b.mu.Lock()
	b.ring[b.next] = entry
	b.next = (b.next + 1) % len(b.ring)
	b.count++
	b.mu.Unlock()
}

// WithRole returns a port.EventSink view over the SAME ring buffer whose
// recorded turns carry the given bounded role family value (mirroring
// Metrics.WithRole). It holds no state of its own and spawns no goroutine.
func (b *SlowTurnBuffer) WithRole(role string) port.EventSink {
	return roleSlowTurnSink{b: b, role: role}
}

// roleSlowTurnSink is the role-scoped EventSink view returned by WithRole.
type roleSlowTurnSink struct {
	b    *SlowTurnBuffer
	role string
}

// Emit records the event's turn into the shared ring with the wrapper's role.
func (s roleSlowTurnSink) Emit(_ context.Context, ev session.Event) {
	s.b.record(ev, s.role)
}

// Recent returns the whole bounded set of recorded turns, NEWEST FIRST, filtered
// to turns whose DurationMs is at least thresholdMs (thresholdMs <= 0 returns
// every recorded turn). The order is stable across calls on an unchanged buffer,
// which is the contract mcpperf.SlowTurnSource relies on for stable in-memory
// cursor pagination and an accurate totalCount.
//
// It returns telemetry.SlowTurn (its own type); the cmd/embed wiring maps each to
// mcpperf.SlowTurn so telemetry need not import the adapter.
func (b *SlowTurnBuffer) Recent(thresholdMs int64) []SlowTurn {
	b.mu.Lock()
	snapshot := b.snapshotLocked()
	b.mu.Unlock()

	// Newest first: the ring fills oldest→newest, so reverse the chronological
	// snapshot. A stable sort by descending EndedAt would also work, but the
	// reversal is exact and allocation-light and avoids ties on equal timestamps.
	out := make([]SlowTurn, 0, len(snapshot))
	for i := len(snapshot) - 1; i >= 0; i-- {
		if thresholdMs > 0 && snapshot[i].DurationMs < thresholdMs {
			continue
		}
		out = append(out, snapshot[i])
	}
	return out
}

// snapshotLocked returns the recorded turns in chronological (oldest-first)
// order. The caller holds b.mu. When the ring has not yet wrapped, that is just
// ring[:count]; once wrapped, the oldest entry sits at b.next.
func (b *SlowTurnBuffer) snapshotLocked() []SlowTurn {
	n := b.count
	if n > len(b.ring) {
		n = len(b.ring)
	}
	out := make([]SlowTurn, 0, n)
	if b.count <= len(b.ring) {
		// Not yet wrapped: entries 0..count-1 are in chronological order.
		out = append(out, b.ring[:n]...)
		return out
	}
	// Wrapped: the oldest live entry is at b.next; read forward circularly.
	start := b.next
	for i := 0; i < n; i++ {
		out = append(out, b.ring[(start+i)%len(b.ring)])
	}
	return out
}
