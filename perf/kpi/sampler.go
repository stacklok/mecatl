package kpi

import (
	"sync"
	"time"
)

// RSSSampler polls the process resident-set size on a fixed interval and tracks
// the peak and the most-recent (final) sample. It is platform-pluggable through
// readRSS (a direct /proc/self/status read on linux, 0 elsewhere — see
// rss_linux.go / rss_other.go), so the harness takes no new dependency.
//
// Start launches a sampling goroutine; Stop signals it, waits for it to drain,
// takes one final sample, and returns (peak, final). A sampler is single-use.
type RSSSampler struct {
	interval time.Duration
	stop     chan struct{}
	done     chan struct{}

	mu   sync.Mutex
	peak uint64
}

// NewRSSSampler returns a sampler that polls every interval. An interval <= 0 is
// clamped to 10ms.
func NewRSSSampler(interval time.Duration) *RSSSampler {
	if interval <= 0 {
		interval = 10 * time.Millisecond
	}
	return &RSSSampler{
		interval: interval,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Start begins sampling in the background. It records an immediate first sample
// so a region shorter than one interval still yields a peak.
func (s *RSSSampler) Start() {
	s.observe()
	go func() {
		defer close(s.done)
		t := time.NewTicker(s.interval)
		defer t.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-t.C:
				s.observe()
			}
		}
	}()
}

// Stop ends sampling, takes a final reading, and returns (peak, final) in bytes.
// On a non-linux build both are 0.
func (s *RSSSampler) Stop() (peak, final uint64) {
	close(s.stop)
	<-s.done
	final = s.observe()
	s.mu.Lock()
	peak = s.peak
	s.mu.Unlock()
	return peak, final
}

// observe takes one RSS reading, folds it into the peak, and returns it.
func (s *RSSSampler) observe() uint64 {
	v := readRSS()
	s.mu.Lock()
	if v > s.peak {
		s.peak = v
	}
	s.mu.Unlock()
	return v
}
