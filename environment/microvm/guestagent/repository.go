package guestagent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path"
	"sync"

	"github.com/stacklok/mecatl/environment/microvm/control"
	"github.com/stacklok/mecatl/environment/microvm/guestexec"
	"github.com/stacklok/mecatl/environment/microvm/workspace"
)

// RepositoryServerConfig binds one guest agent to one repository VM generation.
type RepositoryServerConfig struct {
	Owner            string
	RepositoryKey    string
	VMID             string
	Endpoint         string
	Generation       uint32
	AuthorityKey     []byte
	ExecLimits       guestexec.Limits
	Shell            string
	WorkloadIdentity guestexec.WorkloadIdentity
	RuntimeContract  guestexec.RuntimeContract
	// ResolveRoot is the guest mount namespace resolver. Production leaves it nil,
	// making the authenticated guest path the opened path; tests may emulate a mount.
	ResolveRoot func(string) (string, error)
}

type logicalRegistration struct {
	binding   control.Binding
	workspace *workspace.Guest
	exec      *guestexec.GuestServer
	mu        sync.RWMutex
	active    bool
}

const maxRepositoryDataConnections = 16

// ErrLogicalRootUnavailable is the closed guest-control classification for an
// authenticated assigned root that cannot be resolved or opened.
var ErrLogicalRootUnavailable = errors.New("repository logical root is unavailable")

// RepositoryServer routes authenticated logical roots inside one repository VM.
type RepositoryServer struct {
	owner         string
	repositoryKey string
	vmID          string
	endpoint      string
	generation    uint32
	authorityKey  []byte
	verifier      *control.CapabilityVerifier
	execLimits    guestexec.Limits
	shell         string
	identity      guestexec.WorkloadIdentity
	runtime       guestexec.RuntimeContract
	resolveRoot   func(string) (string, error)

	mu            sync.RWMutex
	registrations map[string]*logicalRegistration
	dataSlots     chan struct{}
}

// NewRepositoryServer constructs the generation-scoped guest registration table.
func NewRepositoryServer(cfg RepositoryServerConfig) (*RepositoryServer, error) {
	if cfg.Owner == "" || cfg.Generation == 0 {
		return nil, control.ErrBindingMismatch
	}
	verifier, err := control.NewCapabilityVerifier(cfg.AuthorityKey)
	if err != nil {
		return nil, err
	}
	identity := cfg.WorkloadIdentity
	if identity == (guestexec.WorkloadIdentity{}) {
		identity = guestexec.DefaultWorkloadIdentity()
	}
	runtime := cfg.RuntimeContract
	if runtime.Home == "" {
		runtime = guestexec.DefaultRuntimeContract()
	}
	resolveRoot := cfg.ResolveRoot
	if resolveRoot == nil {
		resolveRoot = func(root string) (string, error) { return root, nil }
	}
	return &RepositoryServer{
		owner: cfg.Owner, repositoryKey: cfg.RepositoryKey, vmID: cfg.VMID, endpoint: cfg.Endpoint,
		generation: cfg.Generation, authorityKey: append([]byte(nil), cfg.AuthorityKey...), verifier: verifier,
		execLimits: cfg.ExecLimits, shell: cfg.Shell, identity: identity, runtime: runtime, resolveRoot: resolveRoot,
		registrations: make(map[string]*logicalRegistration),
		dataSlots:     make(chan struct{}, maxRepositoryDataConnections),
	}, nil
}

// Register authenticates and consumes registration authority before opening the
// assigned root. A ref can never be rebound to different bytes.
func (s *RepositoryServer) Register(ctx context.Context, authority string, binding control.Binding) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || binding.ValidateLogical(s.owner, s.generation) != nil {
		return control.ErrBindingMismatch
	}
	if err := s.verifier.Verify(authority, binding); err != nil {
		return err
	}
	openedRoot, err := s.resolveRoot(binding.AssignedRoot)
	if err != nil || openedRoot == "" {
		return ErrLogicalRootUnavailable
	}
	guestWorkspace, err := workspace.NewGuest(openedRoot, binding)
	if err != nil {
		if errors.Is(err, workspace.ErrAssignedRootUnavailable) {
			return ErrLogicalRootUnavailable
		}
		return err
	}
	execServer, err := guestexec.NewGuestServer(guestexec.ServerConfig{
		Binding: binding, Limits: s.execLimits, Shell: s.shell, WorkspaceRoot: openedRoot,
		GitDirectory:     path.Join(path.Dir(openedRoot), "metadata"),
		WorkloadIdentity: s.identity, RuntimeContract: s.runtime,
	})
	if err != nil {
		_ = guestWorkspace.Close()
		return err
	}
	registration := &logicalRegistration{binding: binding, workspace: guestWorkspace, exec: execServer, active: true}
	s.mu.Lock()
	defer s.mu.Unlock()
	if previous := s.registrations[binding.Ref]; previous != nil {
		_ = guestWorkspace.Close()
		return control.ErrBindingMismatch
	}
	s.registrations[binding.Ref] = registration
	return nil
}

// Unregister authenticates the complete generation/ref/root tuple, prevents new
// dispatch, removes the registration and its replay state, then closes the root.
func (s *RepositoryServer) Unregister(authority string, binding control.Binding) error {
	if s == nil || binding.ValidateLogical(s.owner, s.generation) != nil {
		return control.ErrBindingMismatch
	}
	if err := s.verifier.Verify(authority, binding); err != nil {
		return err
	}
	s.mu.Lock()
	registration := s.registrations[binding.Ref]
	if registration == nil || registration.binding != binding {
		s.mu.Unlock()
		return control.ErrBindingMismatch
	}
	registration.mu.Lock()
	registration.active = false
	delete(s.registrations, binding.Ref)
	s.mu.Unlock()
	s.verifier.Forget(binding)
	err := registration.workspace.Close()
	registration.mu.Unlock()
	return err
}

// Probe authenticates the complete registered tuple without dispatching work.
func (s *RepositoryServer) Probe(binding control.Binding) error {
	if s == nil || binding.ValidateLogical(s.owner, s.generation) != nil {
		return control.ErrBindingMismatch
	}
	s.mu.RLock()
	registration := s.registrations[binding.Ref]
	s.mu.RUnlock()
	if registration == nil || registration.binding != binding {
		return control.ErrBindingMismatch
	}
	return nil
}

// Serve performs a binding-resolving handshake and dispatches only to the root
// registered for that exact owner/generation/ref/root tuple.
func (s *RepositoryServer) Serve(ctx context.Context, stream io.ReadWriteCloser) error {
	if s == nil {
		return control.ErrUnauthenticatedCapability
	}
	return control.ServeRegisteredMultiplex(ctx, stream, s.verifier, func(binding control.Binding) (map[control.ServiceName]control.Handler, bool) {
		if s.Probe(binding) != nil {
			return nil, false
		}
		s.mu.RLock()
		registration := s.registrations[binding.Ref]
		s.mu.RUnlock()
		if registration == nil {
			return nil, false
		}
		return map[control.ServiceName]control.Handler{
			control.ServiceWorkspace: registration.guard(registration.workspace.Handler()),
			control.ServiceExec:      registration.guard(registration.exec.Handler()),
		}, true
	}, control.DefaultMaxMessageBytes)
}

func (r *logicalRegistration) guard(handler control.Handler) control.Handler {
	return func(ctx context.Context, capability string, payload json.RawMessage, send func(any) error) (any, string, error) {
		r.mu.RLock()
		defer r.mu.RUnlock()
		if !r.active {
			return nil, "unauthenticated", control.ErrUnauthenticatedCapability
		}
		return handler(ctx, capability, payload, send)
	}
}

// Close releases every registered confined root.
func (s *RepositoryServer) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	registrations := s.registrations
	s.registrations = make(map[string]*logicalRegistration)
	s.mu.Unlock()
	var err error
	for _, registration := range registrations {
		registration.mu.Lock()
		registration.active = false
		s.verifier.Forget(registration.binding)
		err = errors.Join(err, registration.workspace.Close())
		registration.mu.Unlock()
	}
	return err
}
