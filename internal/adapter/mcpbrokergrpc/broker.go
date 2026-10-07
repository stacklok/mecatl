// Package mcpbrokergrpc exposes broker-owned sessions over mecatl.broker.v1.SessionService.
package mcpbrokergrpc

import "time"

// Config carries finite client connection and RPC deadlines.
type Config struct {
	// DialTimeout bounds the initial wait for a remote broker connection to become ready.
	DialTimeout time.Duration
	// RPCDeadline is the default deadline applied to non-execution RPCs by clients.
	RPCDeadline time.Duration
	// ExecuteDeadline bounds one tool execution, including the server-side work it starts.
	ExecuteDeadline time.Duration
}

// DefaultConfig returns finite production defaults for the initial single-process broker.
func DefaultConfig() Config {
	return Config{
		DialTimeout: 5 * time.Second, RPCDeadline: 10 * time.Second,
		ExecuteDeadline: 2 * time.Minute,
	}
}
