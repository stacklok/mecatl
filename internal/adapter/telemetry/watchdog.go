package telemetry

import (
	"context"
	"log/slog"
	"time"
)

// StartGoroutineWatchdog launches a background ticker that samples the live
// goroutine count (via the injected count func, normally runtime.NumGoroutine)
// every interval and logs a slog.Warn when it exceeds threshold — the live leak
// ALARM, complementing the
// test-time goleak gate and the runtime collector's goroutine-count /metrics
// series.
//
// The goroutine exits when ctx is cancelled (shutdown), so the watchdog itself
// never leaks — verified by the package's goleak-free shutdown and by
// TestStartGoroutineWatchdogStopsOnCancel. count and logger are injected so the
// behaviour is unit-testable without spawning real goroutines or racing the
// global logger.
//
// It lives in the telemetry adapter (not a cmd main) so BOTH composition roots —
// the standalone cmd/mecated daemon and the cmd/mecatui embedded server — arm the
// SAME helper. The injected count func keeps it import-clean (no runtime import
// here; the caller passes runtime.NumGoroutine).
//
// A threshold <= 0 is a no-op (the alarm is disabled and no goroutine is spawned).
// A non-positive interval falls back to 30s.
func StartGoroutineWatchdog(ctx context.Context, threshold int, interval time.Duration, count func() int, logger *slog.Logger) {
	if threshold <= 0 {
		return
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if n := count(); n > threshold {
					logger.Warn("goroutine count exceeds the configured watchdog threshold — possible goroutine leak",
						"goroutines", n, "threshold", threshold)
				}
			}
		}
	}()
}
