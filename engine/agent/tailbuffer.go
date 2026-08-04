package agent

import "sync"

// maxBashJobTailBytes is the per-background-bash-job retained output tail.
// The per-read render cap is fstools.MaxOutputBytes (25_000); retention
// exceeds it so BashStatus can show more context than a single render.
// 8 concurrent jobs ≈ 512 KiB worst case — bounded.
const maxBashJobTailBytes = 64 << 10

// tailBuffer is a mutex-guarded bounded byte sink (an io.Writer) that retains
// the LAST capacity bytes written to it (a live-growing stdout+stderr tail) —
// the sink a tool.CommandStreamer streams a background-Bash job's output into.
//
// Implementation: a sliding window over a 2*capacity scratch — Write appends
// after the live window and slides the window forward (dropping the head),
// compacting back to the front only when the tail reaches scratch end; so a
// full buffer costs one bounded memmove per write, zero reallocations.
type tailBuffer struct {
	mu        sync.Mutex
	scratch   []byte // cap 2*capacity, allocated in newTailBuffer
	start     int    // offset of the live window inside scratch
	length    int    // live window length, ≤ capacity
	truncated bool
}

func newTailBuffer(capacity int) *tailBuffer {
	if capacity < 1 {
		capacity = 1
	}
	return &tailBuffer{scratch: make([]byte, 2*capacity)}
}

// Write appends p, keeping only the last capacity bytes; the truncated flag
// is set the first time any byte is dropped. It is io.Writer-shaped (len(p),
// nil — a tail never fails) so a streaming command runner can target it
// directly.
func (t *tailBuffer) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	capacity := len(t.scratch) / 2
	switch {
	case len(p) >= capacity:
		// The write alone covers the window: keep its tail; truncated only
		// when bytes were actually dropped (the overshoot or a prior drop).
		t.truncated = t.truncated || len(p) > capacity || t.length > 0
		copy(t.scratch[:capacity], p[len(p)-capacity:])
		t.start = 0
		t.length = capacity
	default:
		end := t.start + t.length
		if end+len(p) > len(t.scratch) {
			// Compact the live window back to the front (bounded memmove).
			copy(t.scratch[:t.length], t.scratch[t.start:end])
			t.start = 0
			end = t.length
		}
		copy(t.scratch[end:end+len(p)], p)
		total := t.length + len(p)
		if total > capacity {
			drop := total - capacity
			t.truncated = true
			t.start += drop
			t.length = capacity
		} else {
			t.length = total
		}
	}
	return len(p), nil
}

// Snapshot returns a copy of the retained tail.
func (t *tailBuffer) Snapshot() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.scratch[t.start : t.start+t.length])
}

// Truncated reports whether any byte was ever dropped.
func (t *tailBuffer) Truncated() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.truncated
}
