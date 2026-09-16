package microvmmanager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMicroVMRedesign_Scenario1_EnsureReadyConvergesUnderManagerLock(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var running atomic.Bool
	var starts atomic.Int32
	ops1 := &lockingReadyOps{fakeOps: fakeOps{}, entered: entered, release: release, running: &running, starts: &starts}
	ops2 := &lockingReadyOps{fakeOps: fakeOps{}, entered: entered, running: &running, starts: &starts}
	request := ReadyRequest{Release: Release{URL: "https://example.invalid/release.tar.gz", SHA256: strings.Repeat("a", 64)}, Policy: testPolicy(root)}

	errCh := make(chan error, 2)
	go func() { _, err := New(paths, ops1).EnsureReady(context.Background(), request); errCh <- err }()
	<-entered
	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		_, err := New(paths, ops2).EnsureReady(context.Background(), request)
		errCh <- err
	}()
	<-secondStarted

	select {
	case <-entered:
		t.Fatal("second readiness attempt entered operations before the manager lock was released")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
	if got := starts.Load(); got != 1 {
		t.Fatalf("daemon starts = %d, want one compatible daemon", got)
	}
}

func TestEnsureReadyPreservesExistingConfigurationOnMismatch(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	if err := preparePaths(paths); err != nil {
		t.Fatal(err)
	}
	oldConfig := []byte(`{"release_identity":"sha256:old","policy_revision":"old"}`)
	if err := os.WriteFile(paths.ConfigFile, oldConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	cacheMarker := filepath.Join(paths.DataDir, "cache", "old-policy-entry")
	if err := os.MkdirAll(filepath.Dir(cacheMarker), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cacheMarker, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := ReadyRequest{Release: Release{URL: "https://example.invalid/release.tar.gz", SHA256: strings.Repeat("c", 64)}, Policy: testPolicy(root)}
	ops := &fakeOps{}
	if _, err := New(paths, ops).EnsureReady(context.Background(), request); err == nil || !strings.Contains(err.Error(), "configuration is incompatible") {
		t.Fatalf("configuration mismatch error = %v", err)
	}
	if got, err := os.ReadFile(paths.ConfigFile); err != nil || string(got) != string(oldConfig) {
		t.Fatalf("existing config changed: %q, err=%v", got, err)
	}
	if got, err := os.ReadFile(cacheMarker); err != nil || string(got) != "stale" {
		t.Fatalf("existing cache changed: %q, err=%v", got, err)
	}
	for _, forbidden := range []string{"download", "verify", "install", "start", "stop"} {
		if contains(ops.calls, forbidden) {
			t.Fatalf("mismatch called %s: %v", forbidden, ops.calls)
		}
	}
}

func TestMicroVMRedesign_Scenario1_ReadinessNeverRewritesDesiredConfig(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	if err := os.MkdirAll(filepath.Dir(paths.UserSettings), 0o700); err != nil {
		t.Fatal(err)
	}
	const desired = "posture: strict\n"
	if err := os.WriteFile(paths.UserSettings, []byte(desired), 0o600); err != nil {
		t.Fatal(err)
	}
	ops := &fakeOps{failAt: "doctor"}
	manager := New(paths, ops)
	request := ReadyRequest{Release: Release{URL: "https://example.invalid/release.tar.gz", SHA256: strings.Repeat("b", 64)}, Policy: testPolicy(root)}

	if _, err := manager.EnsureReady(context.Background(), request); err == nil || !strings.Contains(err.Error(), "retry ordinary use") {
		t.Fatalf("readiness error = %v, want actionable ordinary-use retry", err)
	}
	got, err := os.ReadFile(paths.UserSettings)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != desired {
		t.Fatalf("desired configuration was rewritten on failure:\n%s", got)
	}

	ops.failAt = ""
	if _, err := manager.EnsureReady(context.Background(), request); err != nil {
		t.Fatalf("ordinary-use retry failed: %v", err)
	}
}

type lockingReadyOps struct {
	fakeOps
	entered chan<- struct{}
	release <-chan struct{}
	running *atomic.Bool
	starts  *atomic.Int32
}

func (o *lockingReadyOps) Preflight(ctx context.Context, paths Paths) error {
	if o.entered != nil {
		o.entered <- struct{}{}
	}
	if o.release != nil {
		select {
		case <-o.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return o.fakeOps.Preflight(ctx, paths)
}

func (o *lockingReadyOps) Running(context.Context, Paths) (bool, error) {
	return o.running.Load(), nil
}

func (o *lockingReadyOps) Start(context.Context, Paths) error {
	o.starts.Add(1)
	o.running.Store(true)
	return nil
}

var _ Operations = (*lockingReadyOps)(nil)
