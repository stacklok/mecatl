// Package sessionadapter bridges mecatui's session interface to its gRPC client.
package sessionadapter

import (
	"context"
	"errors"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// Client is the gRPC session surface used by Adapter.
type Client interface {
	CreateSession(context.Context, mecatlv1.PermissionMode, client.ModelSelection) (string, client.Capabilities, client.ResolvedModel, error)
	CreateSessionWithCarryover(context.Context, client.ModelSelection, string) (string, client.Capabilities, client.ResolvedModel, error)
	CloseSession(context.Context, string) error
	GetSession(context.Context, string) (client.SessionSnapshot, error)
	SetMode(context.Context, string, string) (string, error)
	ForkSession(context.Context, string, string, string) (string, error)
}

// Adapter fixes launch-time mode while preserving mecatui's per-call model selection.
type Adapter struct {
	cl          Client
	workspace   string
	mode        string
	debugTarget string
	debugMCP    []string
}

// New constructs mecatui's session adapter.
func New(cl Client, workspace, mode, _ string) *Adapter {
	return &Adapter{cl: cl, workspace: workspace, mode: mode}
}

// WithDebugTarget binds a dedicated debug session to target when non-empty.
func (s *Adapter) WithDebugTarget(target string, mcpServers []string) *Adapter {
	s.debugTarget = target
	s.debugMCP = append([]string(nil), mcpServers...)
	return s
}

// CreateSession creates a session in the launch workspace.
func (s *Adapter) CreateSession(ctx context.Context, sel client.ModelSelection, mode string) (string, client.Capabilities, client.ResolvedModel, error) {
	if s.debugTarget != "" {
		if mode == "" {
			mode = s.mode
		}
		debugClient, ok := s.cl.(interface {
			CreateDebugSession(context.Context, string, mecatlv1.PermissionMode, client.ModelSelection, ...string) (string, string, client.Capabilities, client.ResolvedModel, error)
		})
		if !ok {
			return "", client.Capabilities{}, client.ResolvedModel{}, errors.New("session client does not support debugging")
		}
		id, target, capabilities, model, err := debugClient.CreateDebugSession(ctx, s.debugTarget, client.ModeFromString(mode), sel, s.debugMCP...)
		if target != "" {
			s.debugTarget = target
		}
		return id, capabilities, model, err
	}
	return s.CreateSessionInWorkspace(ctx, s.workspace, sel, mode)
}

// DebugTargetID returns the resolved debug target, if any.
func (s *Adapter) DebugTargetID() string { return s.debugTarget }

// CreateSessionInWorkspace creates a session in an explicitly selected workspace.
func (s *Adapter) CreateSessionInWorkspace(ctx context.Context, _ string, sel client.ModelSelection, mode string) (string, client.Capabilities, client.ResolvedModel, error) {
	if mode == "" {
		mode = s.mode
	}
	return s.cl.CreateSession(ctx, client.ModeFromString(mode), sel)
}

// CreateSessionWithCarryover creates a session seeded from an existing session.
func (s *Adapter) CreateSessionWithCarryover(ctx context.Context, sourceSessionID string, sel client.ModelSelection, _ string) (string, client.Capabilities, client.ResolvedModel, error) {
	return s.cl.CreateSessionWithCarryover(ctx, sel, sourceSessionID)
}

// CloseSession releases a server-side session.
func (s *Adapter) CloseSession(ctx context.Context, id string) error {
	return s.cl.CloseSession(ctx, id)
}

// GetSession returns the client-visible session snapshot.
func (s *Adapter) GetSession(ctx context.Context, id string) (client.SessionSnapshot, error) {
	return s.cl.GetSession(ctx, id)
}

// SetMode changes a session's permission mode.
func (s *Adapter) SetMode(ctx context.Context, id, mode string) (string, error) {
	return s.cl.SetMode(ctx, id, mode)
}

// ForkSession forks a session while preserving its title and model.
func (s *Adapter) ForkSession(ctx context.Context, srcID, reasoningEffort string) (string, error) {
	return s.cl.ForkSession(ctx, srcID, "", reasoningEffort)
}
