package mcpbrokerserver

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"
)

// Lifecycle owns the public and local administration listeners and ordered shutdown.
type Lifecycle struct {
	broker          *brokerHost
	public          *publicListener
	admin           *http.Server
	adminListener   net.Listener
	propagation     time.Duration
	drainTimeout    time.Duration
	shutdownTimeout time.Duration
	admissionOnce   sync.Once
	propagated      chan struct{}
	closeOnce       sync.Once
	closeErr        error
}

// PublicAddress returns the actual public listener address.
func (l *Lifecycle) PublicAddress() string { return l.public.listener.Addr().String() }

// AdminAddress returns the actual loopback administration listener address.
func (l *Lifecycle) AdminAddress() string { return l.adminListener.Addr().String() }

// Ready reports bounded serving readiness through the shared admission gate.
func (l *Lifecycle) Ready(ctx context.Context) bool { return l.broker.ready(ctx) }

// Start begins both owned listeners and returns their terminal errors.
func (l *Lifecycle) Start() <-chan error {
	errs := make(chan error, 2)
	go func() { errs <- <-l.public.Serve() }()
	go func() { errs <- l.admin.Serve(l.adminListener) }()
	return errs
}

// BeginDrain closes admission and starts the endpoint propagation wait.
func (l *Lifecycle) BeginDrain() {
	l.admissionOnce.Do(func() {
		l.broker.beginDrain()
		go func() { timer := time.NewTimer(l.propagation); defer timer.Stop(); <-timer.C; close(l.propagated) }()
	})
}

// Close drains work, stops listeners, and closes broker resources in order.
func (l *Lifecycle) Close(ctx context.Context) error {
	l.closeOnce.Do(func() {
		l.BeginDrain()
		select {
		case <-l.propagated:
		case <-ctx.Done():
			l.closeErr = errors.Join(l.closeErr, ctx.Err())
		}
		drainCtx, cancelDrain := context.WithTimeout(context.Background(), l.drainTimeout)
		l.closeErr = errors.Join(l.closeErr, l.broker.drain(drainCtx, 0))
		cancelDrain()
		stopCtx, cancelStop := context.WithTimeout(context.Background(), l.shutdownTimeout)
		l.closeErr = errors.Join(l.closeErr, l.public.Shutdown(stopCtx), l.admin.Shutdown(stopCtx), l.broker.close(stopCtx))
		cancelStop()
	})
	return l.closeErr
}
func newAdminServer(listener net.Listener, maxHeaderBytes int) *http.Server {
	return &http.Server{Addr: listener.Addr().String(), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: maxHeaderBytes}
}
func (l *Lifecycle) adminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if !l.Ready(r.Context()) {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /drain", func(w http.ResponseWriter, r *http.Request) {
		l.BeginDrain()
		select {
		case <-l.propagated:
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
			http.Error(w, "drain propagation incomplete", http.StatusServiceUnavailable)
		}
	})
	return mux
}
