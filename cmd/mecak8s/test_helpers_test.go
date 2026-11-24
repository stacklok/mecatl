package main

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/internal/app"
)

//nolint:revive // Test helpers conventionally keep testing.TB first.
func buildIsolated(t testing.TB, ctx context.Context, cfg app.Config) (*app.Built, error) {
	t.Helper()
	if cfg.UserModelDir == "" {
		cfg.UserModelDir = t.TempDir()
	}
	return app.Build(ctx, cfg)
}

// boundListeners holds loopback listeners a test binds before serve and keeps
// open until it ends, keyed by address. Its listen method is config.listen:
// serve takes these listeners instead of binding, so no other test can take a
// port between the test choosing its address and serving it, nor between a
// stop and a restart on the same address.
type boundListeners map[string]*keptListener

// bind binds a loopback listener and returns its address.
func (b boundListeners) bind(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind loopback listener: %v", err)
	}
	k := &keptListener{lis: lis, conns: make(chan net.Conn), closed: make(chan struct{})}
	go k.acceptLoop()
	t.Cleanup(func() {
		close(k.closed)
		_ = lis.Close()
	})
	addr := lis.Addr().String()
	b[addr] = k
	return addr
}

func (b boundListeners) listen(_, address string) (net.Listener, error) {
	k, ok := b[address]
	if !ok {
		return nil, fmt.Errorf("no bound listener for %q", address)
	}
	return &listenerView{keptListener: k, done: make(chan struct{})}, nil
}

// keptListener accepts on the bound listener for whichever listenerView
// serve currently holds.
type keptListener struct {
	lis    net.Listener
	conns  chan net.Conn
	closed chan struct{}
}

func (k *keptListener) acceptLoop() {
	for {
		conn, err := k.lis.Accept()
		if err != nil {
			return
		}
		select {
		case k.conns <- conn:
		case <-k.closed:
			_ = conn.Close()
			return
		}
	}
}

// listenerView is one serve's listener: closing it, as serve does on
// shutdown, stops this view's Accept and leaves the bound port open.
type listenerView struct {
	*keptListener
	once sync.Once
	done chan struct{}
}

func (v *listenerView) Accept() (net.Conn, error) {
	select {
	case <-v.done:
		return nil, net.ErrClosed
	default:
	}
	select {
	case conn := <-v.conns:
		return conn, nil
	case <-v.done:
		return nil, net.ErrClosed
	case <-v.closed:
		return nil, net.ErrClosed
	}
}

func (v *listenerView) Close() error {
	v.once.Do(func() { close(v.done) })
	return nil
}

func (v *listenerView) Addr() net.Addr { return v.lis.Addr() }
