package embed

import (
	"os"
	"sync"
	"testing"
	"testing/synctest"
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

	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		mockGRPC := newMockGRPC(true)
		defer func() {
			mockGRPC.Stop()
			synctest.Wait()
		}()
		appstopCalled := false
		srv := &Server{
			dir:  dir,
			grpc: mockGRPC,
			appstop: func() {
				appstopCalled = true
			},
		}
		done := make(chan error, 1)
		go func() { done <- srv.Close() }()

		time.Sleep(gracefulStopTimeout)
		synctest.Wait()
		if !mockGRPC.stopWasCalled() {
			t.Fatal("Stop was not called when GracefulStop exceeded its timeout")
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Close() returned error: %v", err)
			}
		default:
			t.Fatal("Close() exceeded its shutdown deadline")
		}
		if !appstopCalled {
			t.Error("appstop was never called")
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("dir %s still exists after Close", dir)
		}
	})
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

	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		mockGRPC := newMockGRPC(false)
		appstopBlock := make(chan struct{})
		defer func() {
			close(appstopBlock)
			synctest.Wait()
		}()
		appstopStarted := make(chan struct{})
		srv := &Server{
			dir:  dir,
			grpc: mockGRPC,
			appstop: func() {
				close(appstopStarted)
				<-appstopBlock
			},
		}
		done := make(chan error, 1)
		go func() { done <- srv.Close() }()

		<-appstopStarted
		time.Sleep(compositionCloseTimeout)
		synctest.Wait()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Close() returned error: %v", err)
			}
		default:
			t.Fatal("Close() exceeded its shutdown deadline")
		}
		if !mockGRPC.gracefulStopWasCalled() {
			t.Error("GracefulStop was never called")
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("dir %s still exists after Close", dir)
		}
	})
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

	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		mockGRPC := newMockGRPC(true)
		appstopBlock := make(chan struct{})
		defer func() {
			close(appstopBlock)
			mockGRPC.Stop()
			synctest.Wait()
		}()
		appstopStarted := make(chan struct{})
		srv := &Server{
			dir:  dir,
			grpc: mockGRPC,
			appstop: func() {
				close(appstopStarted)
				<-appstopBlock
			},
		}
		done := make(chan error, 1)
		go func() { done <- srv.Close() }()

		time.Sleep(gracefulStopTimeout)
		synctest.Wait()
		if !mockGRPC.stopWasCalled() {
			t.Fatal("Stop was not called when GracefulStop exceeded its timeout")
		}
		<-appstopStarted
		time.Sleep(compositionCloseTimeout)
		synctest.Wait()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Close() returned error: %v", err)
			}
		default:
			t.Fatal("Close() exceeded its shutdown deadline")
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("dir %s still exists after Close", dir)
		}
	})
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
