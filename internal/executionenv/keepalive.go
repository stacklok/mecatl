package executionenv

import (
	"fmt"
	"time"
)

// Executor serve flag names.
const (
	KeepaliveIntervalFlag = "keepalive-interval"
	KeepaliveTimeoutFlag  = "keepalive-timeout"
)

// Keepalive bounds.
const (
	MinExecutorKeepalive = time.Second
	MaxExecutorKeepalive = 10 * time.Minute
)

// ExecutorMinClientPing is below gRPC's 10s client floor, so any provider ping is permitted.
const ExecutorMinClientPing = 5 * time.Second

// ExecutorKeepalive is the idle time before a ping and the wait for its reply.
type ExecutorKeepalive struct {
	Interval time.Duration
	Timeout  time.Duration
}

// DefaultExecutorKeepalive detects a silent peer in about 20 seconds.
func DefaultExecutorKeepalive() ExecutorKeepalive {
	return ExecutorKeepalive{Interval: 10 * time.Second, Timeout: 10 * time.Second}
}

// OrDefault treats the zero value as unset.
func (k ExecutorKeepalive) OrDefault() ExecutorKeepalive {
	if k == (ExecutorKeepalive{}) {
		return DefaultExecutorKeepalive()
	}
	return k
}

// Validate rejects values outside the bounds.
func (k ExecutorKeepalive) Validate() error {
	if !ExecutorKeepaliveInRange(k.Interval) || !ExecutorKeepaliveInRange(k.Timeout) {
		return fmt.Errorf(
			"keepalive interval and timeout must be in %s..%s",
			MinExecutorKeepalive,
			MaxExecutorKeepalive,
		)
	}
	return nil
}

// ExecutorKeepaliveInRange checks one value against the bounds.
func ExecutorKeepaliveInRange(d time.Duration) bool {
	return d >= MinExecutorKeepalive && d <= MaxExecutorKeepalive
}
