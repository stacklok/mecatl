package executionexecutor

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func fastHealth(touch func(string) error) *WorkspaceHealth {
	h := NewWorkspaceHealth("/workspace", "svc")
	h.interval, h.timeout, h.touch = 5*time.Millisecond, 20*time.Millisecond, touch
	return h
}

func waitStatus(t *testing.T, h *WorkspaceHealth, want healthpb.HealthCheckResponse_ServingStatus) {
	t.Helper()
	deadline := time.Now().Add(failAfter)
	for _, service := range []string{"", "svc"} {
		for {
			resp, err := h.Check(t.Context(), &healthpb.HealthCheckRequest{Service: service})
			if err != nil {
				t.Fatal(err)
			}
			if resp.GetStatus() == want {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("service %q is %v, want %v", service, resp.GetStatus(), want)
			}
			time.Sleep(time.Millisecond)
		}
	}
}

func runHealth(t *testing.T, h *WorkspaceHealth) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func TestWorkspaceHealthStartsNotServing(t *testing.T) {
	waitStatus(t, NewWorkspaceHealth("/workspace", "svc"), healthpb.HealthCheckResponse_NOT_SERVING)
}

func TestWorkspaceHealthAbandonsBlockedCheck(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	h := fastHealth(func(string) error {
		if calls.Add(1) == 1 {
			<-release
		}
		return nil
	})
	runHealth(t, h)

	time.Sleep(20 * h.interval)
	waitStatus(t, h, healthpb.HealthCheckResponse_NOT_SERVING)
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d checks started while one was blocked, want 1", n)
	}
	close(release)
	waitStatus(t, h, healthpb.HealthCheckResponse_SERVING)
}

func TestWorkspaceHealthFollowsWriteErrors(t *testing.T) {
	var errno atomic.Uintptr
	h := fastHealth(func(root string) error {
		if e := syscall.Errno(errno.Load()); e != 0 {
			return &fs.PathError{Op: "open", Path: root, Err: e}
		}
		return nil
	})
	runHealth(t, h)

	waitStatus(t, h, healthpb.HealthCheckResponse_SERVING)
	errno.Store(uintptr(syscall.EROFS))
	waitStatus(t, h, healthpb.HealthCheckResponse_NOT_SERVING)
	for _, fixable := range []syscall.Errno{
		syscall.ENOSPC,
		syscall.EDQUOT,
		syscall.EACCES,
		syscall.EPERM,
	} {
		errno.Store(uintptr(fixable))
		waitStatus(t, h, healthpb.HealthCheckResponse_SERVING)
		errno.Store(uintptr(syscall.EIO))
		waitStatus(t, h, healthpb.HealthCheckResponse_NOT_SERVING)
	}
}

func TestTouchWorkspaceReadOnlyRootStaysWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o555); err != nil {
		t.Fatal(err)
	}
	err := touchWorkspace(root)
	if err == nil || !writable(err) {
		t.Fatalf("touch of a read-only root = %v; want a writable error", err)
	}
}

func TestTouchWorkspaceLeavesNoFile(t *testing.T) {
	root := t.TempDir()
	if err := touchWorkspace(root); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("workspace entries = %v, %v; want none", entries, err)
	}
	if err := touchWorkspace(filepath.Join(root, "missing")); err == nil {
		t.Fatal("touch of a missing workspace succeeded")
	}
}
