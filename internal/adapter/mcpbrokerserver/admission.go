package mcpbrokerserver

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// readinessCheck verifies one bounded, side-effect-free serving prerequisite.
type readinessCheck func(context.Context) error

// admissionGate is the shared admission and drain boundary for broker gRPC and
// browser callback work. It is process-local by design and is not an ownership
// or stale-worker fence.
type admissionGate struct {
	mu           sync.Mutex
	open         bool
	drained      bool
	active       map[uint64]context.CancelFunc
	next         uint64
	idle         chan struct{}
	readyTimeout time.Duration
	checks       []readinessCheck
}

// newAdmissionGate constructs a closed gate. Open must be called only after all
// startup construction and static validation has completed.
func newAdmissionGate(readyTimeout time.Duration, checks ...readinessCheck) (*admissionGate, error) {
	if readyTimeout <= 0 {
		return nil, errors.New("mcpbrokerserver: readiness timeout must be positive")
	}
	idle := make(chan struct{})
	close(idle)
	return &admissionGate{active: make(map[uint64]context.CancelFunc), idle: idle, readyTimeout: readyTimeout, checks: append([]readinessCheck(nil), checks...)}, nil
}

// Open admits work. It is intentionally one-way in production: BeginDrain
// closes admission permanently for this process incarnation.
func (c *admissionGate) Open() {
	c.mu.Lock()
	if !c.drained {
		c.open = true
	}
	c.mu.Unlock()
}

// Ready checks every configured serving prerequisite under one finite bound.
func (c *admissionGate) Ready(parent context.Context) bool {
	c.mu.Lock()
	open := c.open
	c.mu.Unlock()
	if !open {
		return false
	}
	ctx, cancel := context.WithTimeout(parent, c.readyTimeout)
	defer cancel()
	for _, check := range c.checks {
		if check == nil || check(ctx) != nil {
			return false
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.open
}

// BeginDrain atomically closes admission to both public transports.
func (c *admissionGate) BeginDrain() {
	c.mu.Lock()
	c.open = false
	c.drained = true
	c.mu.Unlock()
}

func (c *admissionGate) begin(parent context.Context) (context.Context, func(), bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.open {
		return nil, nil, false
	}
	if len(c.active) == 0 {
		c.idle = make(chan struct{})
	}
	c.next++
	id := c.next
	ctx, cancel := context.WithCancel(parent)
	c.active[id] = cancel
	return ctx, func() { c.end(id) }, true
}

func (c *admissionGate) end(id uint64) {
	c.mu.Lock()
	cancel, ok := c.active[id]
	if ok {
		delete(c.active, id)
		cancel()
		if len(c.active) == 0 {
			close(c.idle)
		}
	}
	c.mu.Unlock()
}

// HTTP applies the shared admission gate to callback and ToolHive routes.
func (c *admissionGate) HTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, done, ok := c.begin(r.Context())
		if !ok {
			http.Error(w, "broker unavailable", http.StatusServiceUnavailable)
			return
		}
		defer done()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// UnaryInterceptor applies the same gate to broker gRPC work.
func (c *admissionGate) UnaryInterceptor(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	opCtx, done, ok := c.begin(ctx)
	if !ok {
		return nil, status.Error(codes.Unavailable, "broker draining")
	}
	defer done()
	return handler(opCtx, req)
}

// Drain closes admission, waits the endpoint propagation interval, then waits
// for admitted work until the caller's finite deadline. At the deadline all
// remaining operation contexts are cancelled so teardown can settle them.
func (c *admissionGate) Drain(ctx context.Context, propagation time.Duration) error {
	if propagation < 0 {
		return errors.New("mcpbrokerserver: drain propagation interval must be non-negative")
	}
	c.BeginDrain()
	if propagation > 0 {
		timer := time.NewTimer(propagation)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			c.cancelActive()
			return ctx.Err()
		}
	}
	c.mu.Lock()
	idle := c.idle
	c.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		c.cancelActive()
		return ctx.Err()
	}
}

func (c *admissionGate) cancelActive() {
	c.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(c.active))
	for _, cancel := range c.active {
		cancels = append(cancels, cancel)
	}
	c.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}
