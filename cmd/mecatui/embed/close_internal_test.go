package embed

import (
	"os"
	"sync"
	"testing"
	"time"
)

// mockGRPCServer records which shutdown methods were called and supports blocking
// GracefulStop until Stop is called, mirroring real *grpc.Server behavior.
type mockGRPCServer struct {
	mu             sync.Mutex
	gracefulCalled bool
	stopCalledB    bool
	stopOnce       sync.Once
	gracefulBlock  chan struct{} // when non-nil, GracefulStop blocks here; Stop closes it
	stopCh         chan struct{} // closed when Stop is called
}

func newMockGRPC(blockGraceful bool) *mockGRPCServer {
	m := &mockGRPCServer{
		stopCh: make(chan struct{}),
	}
	if blockGraceful {
		m.gracefulBlock = make(chan struct{})
	}
	return m
}

func (m *mockGRPCServer) GracefulStop() {
	m.mu.Lock()
	m.gracefulCalled = true
	m.mu.Unlock()
	if m.gracefulBlock != nil {
		<-m.gracefulBlock
	}
}

func (m *mockGRPCServer) Stop() {
	m.stopOnce.Do(func() {
		m.mu.Lock()
		m.stopCalledB = true
		if m.gracefulBlock != nil {
			close(m.gracefulBlock)
		}
		m.mu.Unlock()
		close(m.stopCh)
	})
}

func (m *mockGRPCServer) gracefulStopWasCalled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.gracefulCalled
}

func (m *mockGRPCServer) stopWasCalled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stopCalledB
}

func TestCloseGracefulStopBlocks(t *testing.T) {
	oldGraceful := gracefulStopTimeout
	oldComposition := compositionCloseTimeout
	gracefulStopTimeout = 100 * time.Millisecond
	compositionCloseTimeout = 50 * time.Millisecond
	t.Cleanup(func() {
		gracefulStopTimeout = oldGraceful
		compositionCloseTimeout = oldComposition
	})

	dir := t.TempDir()
	mockGRPC := newMockGRPC(true) // GracefulStop will block
	appstopCalled := make(chan struct{})
	srv := &Server{
		dir:  dir,
		grpc: mockGRPC,
		appstop: func() {
			close(appstopCalled)
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- srv.Close()
	}()

	// Stop should be called within the gracefulStopTimeout + margin.
	select {
	case <-mockGRPC.stopCh:
		// Stop was called — the timeout path engaged.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Stop was not called within expected time; Close blocked forever")
	}

	// Close should now return (Stop unblocked GracefulStop) and remove the dir.
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close() returned error: %v", err)
		}
		if _, serr := os.Stat(dir); !os.IsNotExist(serr) {
			t.Errorf("dir %s still exists after Close", dir)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Close() blocked forever after Stop")
	}
}

func TestCloseAppstopBlocks(t *testing.T) {
	oldGraceful := gracefulStopTimeout
	oldComposition := compositionCloseTimeout
	gracefulStopTimeout = 50 * time.Millisecond
	compositionCloseTimeout = 100 * time.Millisecond
	t.Cleanup(func() {
		gracefulStopTimeout = oldGraceful
		compositionCloseTimeout = oldComposition
	})

	dir := t.TempDir()
	mockGRPC := newMockGRPC(false) // GracefulStop returns immediately
	appstopBlock := make(chan struct{})
	t.Cleanup(func() { close(appstopBlock) })
	srv := &Server{
		dir:  dir,
		grpc: mockGRPC,
		appstop: func() {
			<-appstopBlock // block until test cleanup
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- srv.Close()
	}()

	// Should return within the composition timeout (plus a margin).
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close() returned error: %v", err)
		}
		if _, serr := os.Stat(dir); !os.IsNotExist(serr) {
			t.Errorf("dir %s still exists after Close", dir)
		}
		if !mockGRPC.gracefulStopWasCalled() {
			t.Error("GracefulStop was never called")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Close() blocked forever on a blocking appstop")
	}
}

func TestCloseBothBlock(t *testing.T) {
	oldGraceful := gracefulStopTimeout
	oldComposition := compositionCloseTimeout
	gracefulStopTimeout = 100 * time.Millisecond
	compositionCloseTimeout = 100 * time.Millisecond
	t.Cleanup(func() {
		gracefulStopTimeout = oldGraceful
		compositionCloseTimeout = oldComposition
	})

	dir := t.TempDir()
	mockGRPC := newMockGRPC(true) // GracefulStop blocks until Stop
	appstopBlock := make(chan struct{})
	t.Cleanup(func() { close(appstopBlock) })
	srv := &Server{
		dir:  dir,
		grpc: mockGRPC,
		appstop: func() {
			<-appstopBlock // block until test cleanup
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- srv.Close()
	}()

	// Should return within ~sum bound (200ms + margin).
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close() returned error: %v", err)
		}
		if _, serr := os.Stat(dir); !os.IsNotExist(serr) {
			t.Errorf("dir %s still exists after Close", dir)
		}
		if !mockGRPC.stopWasCalled() {
			t.Error("Stop was never called (graceful-stop timeout should have fired)")
		}
	case <-time.After(600 * time.Millisecond):
		t.Fatal("Close() blocked forever when both halves block")
	}
}

func TestCloseUncontested(t *testing.T) {
	oldGraceful := gracefulStopTimeout
	oldComposition := compositionCloseTimeout
	gracefulStopTimeout = 10 * time.Second // restore large, the test won't hit it
	compositionCloseTimeout = 10 * time.Second
	t.Cleanup(func() {
		gracefulStopTimeout = oldGraceful
		compositionCloseTimeout = oldComposition
	})

	dir := t.TempDir()
	mockGRPC := newMockGRPC(false)
	var appstopCalled bool
	srv := &Server{
		dir:  dir,
		grpc: mockGRPC,
		appstop: func() {
			appstopCalled = true
		},
	}

	if err := srv.Close(); err != nil {
		t.Fatalf("Close() returned error: %v", err)
	}

	if _, serr := os.Stat(dir); !os.IsNotExist(serr) {
		t.Errorf("dir %s still exists after Close", dir)
	}
	if !mockGRPC.gracefulStopWasCalled() {
		t.Error("GracefulStop was never called")
	}
	if !appstopCalled {
		t.Error("appstop was never called")
	}
}
