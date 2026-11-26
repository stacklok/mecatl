package executionexecutor

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"

	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

const (
	workspaceCheckInterval = 2 * time.Second
	workspaceCheckTimeout  = 30 * time.Second
)

// WorkspaceHealth is a gRPC health server that reports the server and its
// services SERVING only while the workspace accepts a new file in time.
type WorkspaceHealth struct {
	*health.Server
	root     string
	services []string
	interval time.Duration
	timeout  time.Duration
	touch    func(string) error
}

// NewWorkspaceHealth reports root and services NOT_SERVING until Run's first check.
func NewWorkspaceHealth(root string, services ...string) *WorkspaceHealth {
	h := &WorkspaceHealth{
		Server:   health.NewServer(),
		root:     root,
		services: append([]string{""}, services...),
		interval: workspaceCheckInterval,
		timeout:  workspaceCheckTimeout,
		touch:    touchWorkspace,
	}
	h.report(false)
	return h
}

// Run checks the workspace every interval until ctx ends. A check blocked past
// its timeout is abandoned, and no other starts until it returns, so stuck
// storage holds at most one goroutine.
func (h *WorkspaceHealth) Run(ctx context.Context) {
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()
	var pending chan struct{}
	for {
		var ok bool
		ok, pending = h.check(pending)
		h.report(ok)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *WorkspaceHealth) check(pending chan struct{}) (bool, chan struct{}) {
	if pending != nil {
		select {
		case <-pending:
		default:
			return false, pending
		}
	}
	result := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		result <- h.touch(h.root)
	}()
	timer := time.NewTimer(h.timeout)
	defer timer.Stop()
	select {
	case err := <-result:
		return writable(err), nil
	case <-timer.C:
		return false, done
	}
}

// writable keeps a full volume or a root the agent made read-only ready, so the
// agent can still fix it.
func writable(err error) bool {
	for _, ok := range []error{syscall.ENOSPC, syscall.EDQUOT, syscall.EACCES, syscall.EPERM} {
		if errors.Is(err, ok) {
			return true
		}
	}
	return err == nil
}

func (h *WorkspaceHealth) report(ok bool) {
	st := healthpb.HealthCheckResponse_NOT_SERVING
	if ok {
		st = healthpb.HealthCheckResponse_SERVING
	}
	for _, service := range h.services {
		h.SetServingStatus(service, st)
	}
}

func touchNamed(root string) error {
	f, err := os.CreateTemp(root, ".mecatl-ready-*")
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close(), os.Remove(f.Name()))
}
