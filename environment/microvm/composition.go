package microvm

import (
	"context"
	"errors"
	"net"

	"github.com/stacklok/mecatl/environment/microvm/control"
)

// RuntimeDaemonConfig is the concrete repository-scoped microvmd composition root.
type RuntimeDaemonConfig struct {
	Control               *control.Service
	Observer              *OperationsObserver
	Info                  DaemonInfo
	Repository            *RepositoryComposition
	RepositoryProvisioner RepositoryPlacementBuilder
}

// RuntimeDaemon owns the repository-scoped daemon protocol.
type RuntimeDaemon struct {
	Daemon     *Daemon
	Repository *RepositoryComposition
	observer   *OperationsObserver
}

// NewRuntimeDaemon composes the single repository-VM lifecycle.
func NewRuntimeDaemon(cfg RuntimeDaemonConfig) (*RuntimeDaemon, error) {
	if cfg.Repository == nil || cfg.RepositoryProvisioner == nil {
		return nil, errors.New("repository microvmd composition is not fully configured")
	}
	daemon, err := NewDaemon(DaemonConfig{
		Control: cfg.Control, Observer: cfg.Observer, Info: cfg.Info,
		RepositoryAttachments: cfg.Repository.Attachments, RepositoryProvisioner: cfg.RepositoryProvisioner,
		RepositoryStartupError: cfg.Repository.RestartHealthError,
	})
	if err != nil {
		return nil, err
	}
	return &RuntimeDaemon{Daemon: daemon, Repository: cfg.Repository, observer: cfg.Observer}, nil
}

// Serve accepts authenticated local management connections until cancellation.
func (d *RuntimeDaemon) Serve(ctx context.Context, listener net.Listener) error {
	if d == nil || d.Daemon == nil || d.Repository == nil || listener == nil {
		return errors.New("repository microvmd server is not configured")
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
