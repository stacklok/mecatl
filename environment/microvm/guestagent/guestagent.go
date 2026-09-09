// Package guestagent composes the workspace and exec services on one authenticated guest stream.
package guestagent

import (
	"context"
	"errors"
	"io"

	"github.com/stacklok/mecatl/environment/microvm/control"
	"github.com/stacklok/mecatl/environment/microvm/guestexec"
	"github.com/stacklok/mecatl/environment/microvm/workspace"
)

// ServerConfig configures one generation-bound guest agent.
type ServerConfig struct {
	Binding          control.Binding
	CapabilityKey    []byte
	WorkspaceRoot    string
	ExecLimits       guestexec.Limits
	Shell            string
	WorkloadIdentity guestexec.WorkloadIdentity
	RuntimeContract  guestexec.RuntimeContract
}

// Server owns a guest-side verifier and both negotiated services.
type Server struct {
	binding       control.Binding
	verifier      *control.CapabilityVerifier
	workspaceRoot string
	exec          *guestexec.GuestServer
}

// NewServer constructs a guest agent without any host-shared credential registry.
func NewServer(cfg ServerConfig) (*Server, error) {
	verifier, err := control.NewCapabilityVerifier(cfg.CapabilityKey)
	if err != nil {
		return nil, err
	}
	if cfg.WorkspaceRoot == "" {
		return nil, errors.New("microvm guest workspace root is empty")
	}
	execServer, err := guestexec.NewGuestServer(guestexec.ServerConfig{
		Binding: cfg.Binding, Limits: cfg.ExecLimits, Shell: cfg.Shell, WorkspaceRoot: cfg.WorkspaceRoot,
		WorkloadIdentity: cfg.WorkloadIdentity, RuntimeContract: cfg.RuntimeContract,
	})
	if err != nil {
		return nil, err
	}
	return &Server{binding: cfg.Binding, verifier: verifier, workspaceRoot: cfg.WorkspaceRoot, exec: execServer}, nil
}

// Serve authenticates once, then multiplexes workspace and exec request IDs until disconnect.
func (s *Server) Serve(ctx context.Context, stream io.ReadWriteCloser) error {
	guestWorkspace, err := workspace.NewGuest(s.workspaceRoot, s.binding)
	if err != nil {
		return err
	}
	defer func() { _ = guestWorkspace.Close() }()
	return control.ServeMultiplex(ctx, stream, s.binding, s.verifier, map[control.ServiceName]control.Handler{
		control.ServiceWorkspace: guestWorkspace.Handler(),
		control.ServiceExec:      s.exec.Handler(),
	}, control.DefaultMaxMessageBytes)
}

// Services are the host-side adapters sharing one authenticated connection.
type Services struct {
	Workspace *workspace.Workspace
	Runner    *guestexec.Runner
	client    *control.Client
}

// Connect performs the sole guest handshake and constructs both bound adapters.
func Connect(ctx context.Context, stream io.ReadWriteCloser, binding control.Binding, capability string) (*Services, error) {
	client, err := control.OpenClient(ctx, stream, binding, capability, []control.ServiceName{control.ServiceWorkspace, control.ServiceExec}, control.DefaultMaxMessageBytes)
	if err != nil {
		return nil, err
	}
	root := binding.AssignedRoot
	if root == "" {
		root = "/workspace"
	}
	ws, err := workspace.NewAt(client, binding, root)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	runner, err := guestexec.NewRunner(guestexec.RunnerConfig{Binding: binding, Client: client})
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	return &Services{Workspace: ws, Runner: runner, client: client}, nil
}

// Agreement returns the capability and message-bound agreement from the sole production handshake.
func (s *Services) Agreement() control.Agreement {
	if s == nil || s.client == nil {
		return control.Agreement{}
	}
	return s.client.Agreement()
}

// Close closes the sole workspace+exec guest connection.
func (s *Services) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Close()
}
