package microvm

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/stacklok/mecatl/environment/microvm/control"
)

// RuntimeDaemonConfig is the concrete microvmd composition root. The narrow
// lifecycle seams remain injectable so ordinary tests need no KVM/HVF or network.
type RuntimeDaemonConfig struct {
	Control               *control.Service
	Backend               GoMicroVMBackend
	Network               *NetworkController
	GuestEgress           GuestEgressPolicy
	DialGuest             GuestDialer
	UnixGuestEndpoint     bool
	CapabilityKey         CapabilityKeySource
	Identities            IdentityAllocator
	Worktrees             WorktreeLifecycle
	Artifacts             ArtifactVerifier
	Registry              ReconcileRegistry
	Sessions              SessionPersister
	Admission             *AdmissionController
	Provisioner           PlacementBuilder
	ChildRequests         ChildRequestBuilder
	Observer              *OperationsObserver
	Info                  DaemonInfo
	Retention             WorktreeRetention
	Repository            *RepositoryComposition
	RepositoryProvisioner RepositoryPlacementBuilder
}

// RuntimeDaemon owns the daemon protocol and concrete go-microvm runtime.
type RuntimeDaemon struct {
	Daemon     *Daemon
	Runtime    *GoMicroVMRuntime
	Repository *RepositoryComposition
	reconciler *Reconciler
	registry   ReconcileRegistry
	admission  *AdmissionController
	observer   *OperationsObserver
}

// NewRuntimeDaemon composes artifact verification, worktree preparation,
// go-microvm, guest protocol, and durable lifecycle into one daemon root.
func NewRuntimeDaemon(cfg RuntimeDaemonConfig) (*RuntimeDaemon, error) {
	if cfg.Repository != nil {
		daemon, err := NewDaemon(DaemonConfig{
			Control: cfg.Control, Observer: cfg.Observer, Info: cfg.Info,
			RepositoryAttachments: repositoryAttachments(cfg.Repository), RepositoryProvisioner: cfg.RepositoryProvisioner,
			RepositoryStartupError: repositoryStartupError(cfg.Repository),
		})
		if err != nil {
			return nil, err
		}
		return &RuntimeDaemon{Daemon: daemon, Repository: cfg.Repository, observer: cfg.Observer}, nil
	}
	runtime, err := NewGoMicroVMRuntime(GoMicroVMRuntimeConfig{
		Backend: cfg.Backend, Network: cfg.Network, GuestEgress: cfg.GuestEgress,
		DialGuest: cfg.DialGuest, UnixGuestEndpoint: cfg.UnixGuestEndpoint, CapabilityKey: cfg.CapabilityKey, Observer: cfg.Observer,
	})
	if err != nil {
		return nil, err
	}
	if cfg.Identities == nil || cfg.Worktrees == nil || cfg.Artifacts == nil || cfg.Registry == nil || cfg.Sessions == nil || cfg.Retention == nil {
		return nil, errors.New("microvmd composition is not fully configured")
	}
	registry := cfg.Registry
	if cfg.Observer != nil {
		registry = observedRegistry{ReconcileRegistry: cfg.Registry, observer: cfg.Observer}
	}
	lifecycle := NewLifecycle(LifecycleDeps{
		Identities: cfg.Identities, Worktrees: cfg.Worktrees, Artifacts: cfg.Artifacts,
		VMs: runtime, Protocol: runtime, Registry: registry, Sessions: cfg.Sessions,
		Admission: cfg.Admission, Observer: cfg.Observer,
	})
	children := NewLifecycleChildren(lifecycle, registry, cfg.Admission, cfg.ChildRequests, cfg.Observer)
	reconciler := NewReconcilerWithAdmission(registry, runtime, cfg.Retention, cfg.Admission, cfg.Observer)
	daemon, err := NewDaemon(DaemonConfig{
		Control: cfg.Control, Creator: lifecycle, Provisioner: cfg.Provisioner, Registry: registry,
		Runtime: runtime, Worktrees: cfg.Retention, Admission: cfg.Admission, Children: children, Reconciler: reconciler, Observer: cfg.Observer, Info: cfg.Info,
		RepositoryAttachments: repositoryAttachments(cfg.Repository), RepositoryProvisioner: cfg.RepositoryProvisioner,
		RepositoryStartupError: repositoryStartupError(cfg.Repository),
	})
	if err != nil {
		return nil, err
	}
	return &RuntimeDaemon{Daemon: daemon, Runtime: runtime, Repository: cfg.Repository, reconciler: reconciler, registry: registry, admission: cfg.Admission, observer: cfg.Observer}, nil
}

func repositoryAttachments(repository *RepositoryComposition) *RepositoryAttachmentManager {
	if repository == nil {
		return nil
	}
	return repository.Attachments
}

func repositoryStartupError(repository *RepositoryComposition) error {
	if repository == nil {
		return nil
	}
	return repository.RestartHealthError
}

type observedRegistry struct {
	ReconcileRegistry
	observer *OperationsObserver
}

func (r observedRegistry) Save(ctx context.Context, record EnvironmentRecord) error {
	if err := r.ReconcileRegistry.Save(ctx, record); err != nil {
		return err
	}
	r.observer.ObserveRecord(record)
	return nil
}

// Serve accepts authenticated local management connections until cancellation.
func (d *RuntimeDaemon) Serve(ctx context.Context, listener net.Listener) error {
	if d == nil || d.Daemon == nil || listener == nil {
		return errors.New("microvmd server is not configured")
	}
	if d.Repository == nil {
		records, err := d.registry.List(ctx)
		if err != nil {
			return fmt.Errorf("load microvmd registry before serving: %w", err)
		}
		if d.observer != nil {
			d.observer.Reconstruct(records)
		}
		if d.admission != nil {
			if err := d.admission.Reconstruct(records); err != nil {
				return err
			}
		}
		err = d.reconciler.Reconcile(ctx)
		if d.observer != nil {
			outcome := OutcomeSuccess
			if err != nil {
				outcome = OutcomeFailure
			}
			d.observer.ReconciliationFinished(outcome)
		}
		if err != nil {
			return fmt.Errorf("reconcile microvmd before serving: %w", err)
		}
	}
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		go func() {
			defer func() { _ = conn.Close() }()
			if err := d.Daemon.ServeConn(ctx, conn); err != nil && d.observer != nil {
				d.observer.LifecycleRequestFailed(err)
			}
		}()
	}
}
