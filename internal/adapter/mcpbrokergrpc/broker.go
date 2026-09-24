// Package mcpbrokergrpc adapts the neutral internal MCP broker seam to the
// versioned mecatl.broker.v1 RPC protocol. It deliberately carries only logical
// bindings, process-local handles, frozen tool descriptors, and invocation data.
package mcpbrokergrpc

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/status"
)

const (
	defaultMaxHandles         = 128
	defaultMaxActiveExecutes  = 64
	maxTerminalReceiptBytes   = 1024
	ambiguousOutcomeMessage   = "remote tool outcome is unknown because the broker response was lost; the operation may already have completed. Do not automatically repeat it. Reconcile through a safe status/read path first; if unavailable, report the uncertainty and seek operator direction."
	sessionUnavailableMessage = "tool temporarily unavailable"
)

// Config bounds transport calls and server-side session-handle retention.
type Config struct {
	// DialTimeout bounds the initial wait for a remote broker connection to become ready.
	DialTimeout time.Duration
	// RPCDeadline is the default deadline applied to non-execution RPCs by clients.
	RPCDeadline time.Duration
	// ExecuteDeadline bounds one tool execution, including the server-side work it starts.
	ExecuteDeadline time.Duration
	// HandleIdleTimeout expires a session handle after inactivity; polling or other
	// use does not extend the absolute lifetime of an individual execution receipt.
	HandleIdleTimeout time.Duration
	// OwnerRetention keeps logical-session ownership after its last handle closes, so
	// a short-lived session handle does not immediately make the session adoptable.
	OwnerRetention time.Duration
	// SweepInterval controls how often expired handles, owners, and receipts are reclaimed.
	SweepInterval time.Duration
	// CleanupTimeout bounds best-effort cleanup of an expired or orphaned remote handle.
	CleanupTimeout time.Duration
	// MaxHandles limits concurrently retained process-local session handles.
	MaxHandles int
	// MaxOwners limits retained logical-session owners; refusal never evicts an owner.
	MaxOwners int
	// MaxReceipts limits terminal execution receipts retained for idempotent polling/replay.
	MaxReceipts int
	// MaxReceiptBytes limits the aggregate bytes retained by execution receipts.
	MaxReceiptBytes int
	// MaxPendingControls limits authorization and other control operations awaiting settlement.
	MaxPendingControls int
	// MaxActiveExecutes limits tool executions running concurrently on this server.
	MaxActiveExecutes int
}

// DefaultConfig returns finite production defaults for the initial single-process broker.
func DefaultConfig() Config {
	return Config{
		DialTimeout: 5 * time.Second, RPCDeadline: 10 * time.Second,
		ExecuteDeadline: 2 * time.Minute, HandleIdleTimeout: 5 * time.Minute,
		OwnerRetention: 24 * time.Hour, SweepInterval: 30 * time.Second, CleanupTimeout: 10 * time.Second,
		MaxHandles: defaultMaxHandles, MaxOwners: defaultMaxHandles, MaxReceipts: 4096, MaxReceiptBytes: 8 << 20, MaxPendingControls: 1024, MaxActiveExecutes: defaultMaxActiveExecutes,
	}
}

func (c Config) valid() bool {
	return c.DialTimeout > 0 && c.RPCDeadline > 0 && c.ExecuteDeadline > 0 &&
		c.HandleIdleTimeout > 0 && c.OwnerRetention > 0 && c.SweepInterval > 0 && c.CleanupTimeout > 0 && c.MaxHandles > 0 && c.MaxOwners > 0 &&
		c.MaxReceipts > 0 && c.MaxReceiptBytes > 0 && c.MaxPendingControls > 0 && c.MaxActiveExecutes > 0
}

// Dial establishes a connection within Config.DialTimeout and returns a bounded client.
func Dial(ctx context.Context, target string, cfg Config, opts ...grpc.DialOption) (*Client, *grpc.ClientConn, error) {
	if !cfg.valid() {
		return nil, nil, errors.New("mcpbrokergrpc: all deadlines and capacities must be positive")
	}
	dialCtx, cancel := context.WithTimeout(ctx, cfg.DialTimeout)
	defer cancel()
	conn, err := grpc.NewClient(target, opts...)
	if err != nil {
		return nil, nil, status.Error(codes.Unavailable, "broker connection unavailable")
	}
	conn.Connect()
	for conn.GetState() != connectivity.Ready {
		state := conn.GetState()
		if state == connectivity.Shutdown || !conn.WaitForStateChange(dialCtx, state) {
			_ = conn.Close()
			return nil, nil, status.Error(codes.Unavailable, "broker connection unavailable")
		}
	}
	client, err := NewClientWithConfig(conn, cfg)
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	return client, conn, nil
}
