package microvm

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

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
func (d *RuntimeDaemon) Serve(ctx context.Context, listener net.Listener) (retErr error) {
	if d == nil || d.Daemon == nil || d.Repository == nil || listener == nil {
		return errors.New("repository microvmd server is not configured")
	}
	serveCtx, cancelServe := context.WithCancel(ctx)
	go func() {
		select {
		case <-d.Daemon.shutdown:
			cancelServe()
		case <-serveCtx.Done():
		}
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(serveCtx), 10*time.Second)
		defer cancel()
		retErr = errors.Join(retErr, d.Repository.Runtime.Shutdown(shutdownCtx))
	}()
	var handlers sync.WaitGroup
	var connections sync.Map
	var admission sync.Mutex
	defer func() {
		cancelServe()
		handlers.Wait()
	}()
	go func() {
		<-serveCtx.Done()
		_ = listener.Close()
		admission.Lock()
		connections.Range(func(key, _ any) bool {
			_ = key.(net.Conn).Close()
			return true
		})
		admission.Unlock()
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if serveCtx.Err() != nil {
				return serveCtx.Err()
			}
			return err
		}
		admission.Lock()
		connections.Store(conn, struct{}{})
		if serveCtx.Err() != nil {
			_ = conn.Close()
			connections.Delete(conn)
			admission.Unlock()
			continue
		}
		handlers.Add(1)
		go func() {
			defer handlers.Done()
			defer connections.Delete(conn)
			defer func() { _ = conn.Close() }()
			if err := d.Daemon.ServeConn(serveCtx, conn); err != nil && d.observer != nil {
				d.observer.LifecycleRequestFailed(err)
			}
		}()
		admission.Unlock()
	}
}
