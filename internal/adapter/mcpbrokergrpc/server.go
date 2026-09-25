package mcpbrokergrpc

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"google.golang.org/grpc"

	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/mcpbroker"
)

// Server adapts one mcpbroker.Service instance to the broker RPC service.
type Server struct {
	// UnimplementedBrokerServiceServer preserves forward compatibility with the generated RPC service.
	brokerv1.UnimplementedBrokerServiceServer

	// Immutable dependencies and configuration, set before the server accepts RPCs.
	service     mcpbroker.Service // Owns the underlying logical broker sessions.
	diagnostics port.Diagnostics  // Receives safe operational RPC observations.
	cfg         Config            // Validated deadlines, retention periods, and capacity limits.
	// instanceID is generated when the Server is constructed. Clients echo it so a
	// replacement server rejects requests tied to the lost process-local state.
	instanceID string

	// mu protects the retained registries, their session-handle and owner state, the
	// closed flag, and the execution/control counters below.
	mu              sync.Mutex
	handles         map[string]*serverHandle            // Process-local handles retained until expiry or shutdown cleanup.
	owners          map[session.SessionID]*sessionOwner // Logical-session ownership retained beyond individual handles.
	closed          bool                                // Rejects new work once Shutdown begins.
	pendingControls int                                 // Lifecycle controls awaiting settlement.

	// Shutdown and cancellation machinery.
	// stop requests sweeper shutdown; done closes after the sweeper exits.
	stop chan struct{}
	done chan struct{}
}

// NewServer constructs one authoritative broker-process instance.
func NewServer(service mcpbroker.Service, cfg Config) (*Server, error) {
	if service == nil {
		return nil, errors.New("mcpbrokergrpc: service is required")
	}
	if cfg.MaxReceipts < 0 || cfg.MaxReceiptBytes < 0 || cfg.MaxPendingControls < 0 || cfg.MaxOwners < 0 || cfg.MaxActiveExecutes < 0 {
		return nil, errors.New("mcpbrokergrpc: capacities must not be negative")
	}
	defaults := DefaultConfig()
	if cfg.MaxOwners == 0 {
		cfg.MaxOwners = defaults.MaxOwners
	}
	if cfg.MaxReceipts == 0 {
		cfg.MaxReceipts = defaults.MaxReceipts
	}
	if cfg.MaxReceiptBytes == 0 {
		cfg.MaxReceiptBytes = defaults.MaxReceiptBytes
	}
	if cfg.MaxPendingControls == 0 {
		cfg.MaxPendingControls = defaults.MaxPendingControls
	}
	if cfg.MaxActiveExecutes == 0 {
		cfg.MaxActiveExecutes = defaults.MaxActiveExecutes
	}
	if !cfg.valid() {
		return nil, errors.New("mcpbrokergrpc: all deadlines and capacities must be positive")
	}
	instanceID, err := newHandle()
	if err != nil {
		return nil, fmt.Errorf("mcpbrokergrpc: mint broker instance ID: %w", err)
	}
	s := &Server{service: service, diagnostics: port.NopDiagnostics{}, cfg: cfg, instanceID: instanceID, handles: make(map[string]*serverHandle), owners: make(map[session.SessionID]*sessionOwner), done: make(chan struct{}), stop: make(chan struct{})}
	go s.sweep()
	return s, nil
}

// WithDiagnostics injects the server's operational sink without widening the wire transport Config.
// It must be configured before the server begins accepting RPCs.
func (s *Server) WithDiagnostics(diagnostics port.Diagnostics) *Server {
	if diagnostics == nil {
		diagnostics = port.NopDiagnostics{}
	}
	s.diagnostics = diagnostics
	return s
}

// TraceRPC records an operational RPC outcome. Callers must supply only
// diagnostic-safe fields; authentication has already validated the principal.
func (s *Server) TraceRPC(ctx context.Context, operation, outcome string, fields ...any) {
	level := port.LevelDebug
	if outcome != "success" {
		level = port.LevelWarn
	}
	s.diagnostics.Log(ctx, level, "broker RPC", append([]any{"operation", operation, "outcome", outcome, "inbound_credential_kind", "workload_jwt"}, fields...)...)
}

// RegisterServer registers an explicitly owned server so its cleanup can be joined.
func RegisterServer(reg grpc.ServiceRegistrar, server *Server) {
	brokerv1.RegisterBrokerServiceServer(reg, server)
}

// Shutdown rejects new operations and bounds closure of every orphaned handle.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.stop)
	toClose := make([]*serverHandle, 0, len(s.handles))
	for id, handle := range s.handles {
		delete(s.handles, id)
		if handle.terminal == lifecycleNone {
			toClose = append(toClose, handle)
		}
	}
	clear(s.owners)
	s.mu.Unlock()
	for _, handle := range toClose {
		closeCtx, cancel := context.WithTimeout(ctx, s.cfg.CleanupTimeout)
		_, err := handle.sessionHandle.Close(closeCtx)
		cancel()
		if err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
	}
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
