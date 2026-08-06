package client

import (
	"math/rand/v2"
	"time"
)

// The bounded exponential backoff the mecatui live-feed reconnect loop uses
// (issue #387): a transient StreamSessionLive close/error must re-open with
// increasing delay, never hot-loop, and never grow past a cap. base doubles per
// attempt up to max; each delay is jittered ±jitterFrac so a fleet of
// reconnecting clients does not thunder-herd the server.
//
// These are package-level vars (NOT options on *Client — the reconnect path is
// ui-driven, not a per-Client knob) so tests can shrink them to keep the offline
// suite fast and assert bounded increasing delays up to the cap; the production
// values (500ms base / 30s cap / ±20%) are restored by saving+restoring in each
// test that mutates them. jitterFrac==0 disables jitter for deterministic tests.
var (
	liveReconnectBaseBackoff = 500 * time.Millisecond
	liveReconnectMaxBackoff  = 30 * time.Second
	liveReconnectJitterFrac  = 0.20
)

// RestoreBackoffForTest saves the current backoff knobs and returns a restore
// func. Tests (including cross-package ui tests) shrink the knobs (and set
// jitter to 0 for deterministic timing) and defer the restore so the
// package-level vars are reset for the next test. This is the exported seam that
// lets cmd/mecatui/ui tests shrink the client's reconnect backoff without
// importing the unexported vars.
func RestoreBackoffForTest() func() {
	b, m, j := liveReconnectBaseBackoff, liveReconnectMaxBackoff, liveReconnectJitterFrac
	return func() {
		liveReconnectBaseBackoff = b
		liveReconnectMaxBackoff = m
		liveReconnectJitterFrac = j
	}
}

// liveReconnectDelay computes the per-attempt reconnect delay for the given
// 1-based attempt index: a jittered exponential backoff, base*2^(attempt-1)
// capped at maxBackoff, ±jitterFrac (jitterFrac==0 disables jitter for
// deterministic tests). Returns 0 when base is non-positive (reconnect
// disabled). The jitter window is [d*(1-jitter), d*(1+jitter)], clamped to
// [base, maxBackoff] so a jittered early attempt never undershoots the base and
// never exceeds the cap. The jitter is drawn from the shared math/rand/v2 source
// (fine for backoff — not a security use).
func liveReconnectDelay(attempt int) time.Duration {
	base := liveReconnectBaseBackoff
	maxDelay := liveReconnectMaxBackoff
	if base <= 0 {
		return 0
	}
	if attempt < 1 {
		attempt = 1
	}
	d := base
	for i := 1; i < attempt; i++ {
		d *= 2
		if maxDelay > 0 && d >= maxDelay {
			d = maxDelay
			break
		}
		if d <= 0 { // overflow
			d = maxDelay
			break
		}
	}
	if maxDelay > 0 && d > maxDelay {
		d = maxDelay
	}
	if liveReconnectJitterFrac <= 0 {
		return d
	}
	// ±jitterFrac: a factor in [1-jitter, 1+jitter].
	//nolint:gosec // backoff jitter, not cryptographic
	j := 1 + liveReconnectJitterFrac*(2*rand.Float64()-1)
	out := time.Duration(float64(d) * j)
	if out < base {
		out = base
	}
	if maxDelay > 0 && out > maxDelay {
		out = maxDelay
	}
	return out
}
