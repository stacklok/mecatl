//go:build darwin

package microvm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stacklok/go-microvm/runner"
	"golang.org/x/sys/unix"
)

const (
	launchOwnerHelperEnv       = "GO_TEST_LAUNCH_OWNER_HELPER"
	launchOwnerTestParentEnv   = "GO_TEST_LAUNCH_OWNER_PARENT_PID"
	launchOwnerTestReadyEnv    = "GO_TEST_LAUNCH_OWNER_READY"
	launchOwnerTestLifetimeEnv = "GO_TEST_LAUNCH_OWNER_LIFETIME"
)

func TestMain(m *testing.M) {
	if handled, err := RunLaunchOwnerChild(os.Args[1:]); handled {
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(91)
		}
		os.Exit(0)
	}
	if mode := os.Getenv(launchOwnerHelperEnv); (mode == "runner" || mode == "ignore-term" || mode == "exit-7" || mode == "signal-term") && len(os.Args) == 2 && strings.HasPrefix(os.Args[1], "{") {
		if err := watchDarwinTestParent(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(92)
		}
		if err := watchDarwinTestLifetime(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(92)
		}
		if mode == "ignore-term" {
			signal.Ignore(syscall.SIGTERM)
		}
		if ready := os.Getenv(launchOwnerTestReadyEnv); ready != "" {
			if err := os.WriteFile(ready, []byte("ready\n"), 0o600); err != nil {
				os.Exit(92)
			}
		}
		if mode == "exit-7" {
			os.Exit(7)
		}
		if mode == "signal-term" {
			_ = unix.Kill(os.Getpid(), unix.SIGTERM)
		}
		select {}
	}
	os.Exit(m.Run())
}

func watchDarwinTestParent() error {
	parentPID, err := strconv.Atoi(os.Getenv(launchOwnerTestParentEnv))
	if err != nil || parentPID <= 0 {
		return errors.New("runner started without test parent identity")
	}
	queue, err := unix.Kqueue()
	if err != nil {
		return fmt.Errorf("create test parent watcher: %w", err)
	}
	change := []unix.Kevent_t{{Ident: uint64(parentPID), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ENABLE | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT}}
	if _, err := unix.Kevent(queue, change, nil, nil); err != nil {
		_ = unix.Close(queue)
		return fmt.Errorf("watch test parent: %w", err)
	}
	if err := unix.Kill(parentPID, 0); err != nil {
		_ = unix.Close(queue)
		return errors.New("test parent exited before watcher was armed")
	}
	go func() {
		events := make([]unix.Kevent_t, 1)
		_, _ = unix.Kevent(queue, nil, events, nil)
		_ = unix.Close(queue)
		os.Exit(93)
	}()
	return nil
}

func watchDarwinTestLifetime() error {
	path := os.Getenv(launchOwnerTestLifetimeEnv)
	if !filepath.IsAbs(path) {
		return errors.New("runner started without absolute test lifetime path")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return fmt.Errorf("invalid test lifetime sentinel: %v", err)
	}
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
				os.Exit(0)
			} else if err != nil {
				os.Exit(94)
			}
		}
	}()
	return nil
}

func newDarwinTestLaunchOwnership(t *testing.T) *LaunchOwnership {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	ownership, err := NewLaunchOwnership(LaunchOwnershipConfig{Root: root, LauncherPath: executable, ReceiptWait: 2 * time.Second, TermTimeout: time.Second, KillTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return ownership
}

type darwinRunnerFixtureConfig struct {
	runner.Config
	lifetimePath string
}

func newDarwinTestLifetime(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lifetime")
	if err := os.WriteFile(path, []byte("alive\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func darwinTestRunnerConfig(t *testing.T) darwinRunnerFixtureConfig {
	return darwinTestRunnerConfigMode(t, "runner")
}

func darwinTestRunnerConfigMode(t *testing.T, mode string) darwinRunnerFixtureConfig {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	lifetimePath := newDarwinTestLifetime(t)
	t.Setenv(launchOwnerHelperEnv, mode)
	t.Setenv(launchOwnerTestParentEnv, strconv.Itoa(os.Getpid()))
	t.Setenv(launchOwnerTestLifetimeEnv, lifetimePath)
	return darwinRunnerFixtureConfig{
		Config:       runner.Config{RunnerPath: executable, VMLogPath: filepath.Join(t.TempDir(), "runner.log")},
		lifetimePath: lifetimePath,
	}
}

func darwinTestProcessEnv(mode, ready, lifetimePath string) []string {
	env := []string{
		launchOwnerHelperEnv + "=" + mode,
		launchOwnerTestParentEnv + "=" + strconv.Itoa(os.Getpid()),
		launchOwnerTestLifetimeEnv + "=" + lifetimePath,
		"GORACE=atexit_sleep_ms=0",
	}
	if ready != "" {
		env = append(env, launchOwnerTestReadyEnv+"="+ready)
	}
	return env
}

type darwinTestRunner struct {
	*ownedRunnerProcess
	lifetimePath string
}

func spawnDarwinTestRunner(t *testing.T, ownership *LaunchOwnership, environmentID string, testConfig darwinRunnerFixtureConfig) *darwinTestRunner {
	t.Helper()
	handle, err := ownership.spawn(t.Context(), environmentID, testConfig.Config)
	if err != nil {
		t.Fatal(err)
	}
	process := &darwinTestRunner{ownedRunnerProcess: handle.(*ownedRunnerProcess), lifetimePath: testConfig.lifetimePath}
	t.Cleanup(func() { process.cleanup(t) })
	return process
}

func requestDarwinTestExit(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (p *darwinTestRunner) cleanup(t *testing.T) {
	t.Helper()
	if err := requestDarwinTestExit(p.lifetimePath); err != nil {
		t.Errorf("request test-owned Darwin runner exit: %v", err)
		return
	}
	deadline := time.Now().Add(3 * time.Second)
	for p.IsAlive() {
		if time.Now().After(deadline) {
			t.Errorf("test-owned Darwin runner did not exit after lifetime closed")
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		t.Errorf("test-owned Darwin supervisor was not reaped after lifetime closed")
		return
	}
	select {
	case <-p.done:
	case <-time.After(remaining):
		t.Errorf("test-owned Darwin supervisor was not reaped after lifetime closed")
	}
}

func TestDarwinLaunchOwnerSupervisorStopsDirectRunner(t *testing.T) {
	ownership := newDarwinTestLaunchOwnership(t)
	process := spawnDarwinTestRunner(t, ownership, "env-stop", darwinTestRunnerConfig(t))
	if process.PID() <= 0 || process.PID() == process.supervisor.Pid {
		t.Fatalf("receipt PID = %d, supervisor PID = %d", process.PID(), process.supervisor.Pid)
	}
	if !process.IsAlive() {
		t.Fatal("runner was not alive after receipt publication")
	}
	files, err := ownership.openAttempt(process.environmentID, process.launchID)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := time.Duration(files.intentValue.TermTimeoutNS), ownership.termTimeout; got != want {
		files.close()
		t.Fatalf("supervisor TERM timeout = %v, want %v", got, want)
	}
	if got, want := time.Duration(files.intentValue.KillTimeoutNS), ownership.killTimeout; got != want {
		files.close()
		t.Fatalf("supervisor KILL timeout = %v, want %v", got, want)
	}
	files.close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := process.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if process.IsAlive() {
		t.Fatal("runner remained alive after normal stop")
	}
	result, err := ownership.Reconcile(ctx, "env-stop")
	if err != nil {
		t.Fatal(err)
	}
	if result.Dead != 1 {
		t.Fatalf("Reconcile() = %+v, want one dead attempt", result)
	}
}

func TestDarwinLaunchOwnerSupervisorEscalatedStopSucceeds(t *testing.T) {
	ownership := newDarwinTestLaunchOwnership(t)
	ownership.termTimeout = 50 * time.Millisecond
	handle := spawnDarwinTestRunner(t, ownership, "env-stop-kill", darwinTestRunnerConfigMode(t, "ignore-term"))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := handle.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if handle.IsAlive() {
		t.Fatal("runner remained alive after escalated stop")
	}
}

type darwinTestCommand struct {
	cmd          *exec.Cmd
	lifetimePath string
	taken        bool
}

func startDarwinTestCommand(t *testing.T, cmd *exec.Cmd, lifetimePath string) *darwinTestCommand {
	t.Helper()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	owned := &darwinTestCommand{cmd: cmd, lifetimePath: lifetimePath}
	t.Cleanup(func() {
		if err := requestDarwinTestExit(owned.lifetimePath); err != nil {
			t.Errorf("request test-owned Darwin helper exit: %v", err)
		}
		if owned.taken {
			return
		}
		_ = owned.cmd.Process.Kill()
		if _, err := owned.cmd.Process.Wait(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Errorf("reap test-owned Darwin helper: %v", err)
		}
	})
	return owned
}

func (c *darwinTestCommand) take() *exec.Cmd {
	c.taken = true
	return c.cmd
}

func TestDarwinDirectChildStopEscalatesAfterIgnoredTERM(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	lifetimePath := newDarwinTestLifetime(t)
	cmd := exec.Command(os.Args[0], `{}`)
	cmd.Env = darwinTestProcessEnv("ignore-term", ready, lifetimePath)
	owned := startDarwinTestCommand(t, cmd, lifetimePath)
	waitForTestFile(t, ready)
	started := time.Now()
	result, err := ownDirectChild(owned.take(), nil, 50*time.Millisecond, time.Second, true)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(started) < 50*time.Millisecond {
		t.Fatal("direct child exited before TERM escalation timeout")
	}
	if !result.stopRequested || !result.status.Signaled() || result.status.Signal() != syscall.SIGKILL {
		t.Fatalf("direct child result = %+v, want requested SIGKILL stop", result)
	}
}

func waitForTestFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for helper readiness")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDarwinDirectChildStopsOnControlEOF(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	lifetimePath := newDarwinTestLifetime(t)
	cmd := exec.Command(os.Args[0], `{}`)
	cmd.Env = darwinTestProcessEnv("runner", ready, lifetimePath)
	owned := startDarwinTestCommand(t, cmd, lifetimePath)
	waitForTestFile(t, ready)
	eof := make(chan struct{})
	close(eof)
	result, err := ownDirectChild(owned.take(), eof, time.Second, time.Second, false)
	if err != nil {
		t.Fatal(err)
	}
	if !result.stopRequested || !result.status.Signaled() || result.status.Signal() != syscall.SIGTERM {
		t.Fatalf("direct child result = %+v, want requested SIGTERM stop", result)
	}
	if err := directChildExitError(result); err != nil {
		t.Fatalf("controlled stop = %v, want success", err)
	}
}

func TestDarwinDirectChildNaturalFailureIsPreserved(t *testing.T) {
	lifetimePath := newDarwinTestLifetime(t)
	cmd := exec.Command(os.Args[0], `{}`)
	cmd.Env = darwinTestProcessEnv("exit-7", "", lifetimePath)
	owned := startDarwinTestCommand(t, cmd, lifetimePath)
	result, err := ownDirectChild(owned.take(), nil, time.Second, time.Second, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.stopRequested || !result.status.Exited() || result.status.ExitStatus() != 7 {
		t.Fatalf("direct child result = %+v, want unrequested exit 7", result)
	}
	if got := formatDirectChildStatus(result.status); got != "exit status 7" {
		t.Fatalf("formatted status = %q", got)
	}
	if err := directChildExitError(result); err == nil || !strings.Contains(err.Error(), "exit status 7") {
		t.Fatalf("unrequested exit error = %v", err)
	}
}

func TestDarwinDirectChildNaturalSignalIsPreserved(t *testing.T) {
	lifetimePath := newDarwinTestLifetime(t)
	cmd := exec.Command(os.Args[0], `{}`)
	cmd.Env = darwinTestProcessEnv("signal-term", "", lifetimePath)
	owned := startDarwinTestCommand(t, cmd, lifetimePath)
	result, err := ownDirectChild(owned.take(), nil, time.Second, time.Second, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.stopRequested || !result.status.Signaled() || result.status.Signal() != syscall.SIGTERM {
		t.Fatalf("direct child result = %+v, want unrequested SIGTERM", result)
	}
	if err := directChildExitError(result); err == nil || !strings.Contains(err.Error(), "signal terminated") {
		t.Fatalf("unrequested signal error = %v", err)
	}
}

func TestDarwinDirectChildRejectsGoManagedPipes(t *testing.T) {
	cmd := exec.Command(os.Args[0], `{}`)
	cmd.Stdout = &strings.Builder{}
	if directChildUsesWaitCompatibleIO(cmd) {
		t.Fatal("Go-managed command I/O was accepted for manual Wait4 ownership")
	}
}

func TestDarwinKilledSupervisorLeavesRunnerLockHeld(t *testing.T) {
	ownership := newDarwinTestLaunchOwnership(t)
	process := spawnDarwinTestRunner(t, ownership, "env-supervisor-killed", darwinTestRunnerConfig(t))
	if err := process.supervisor.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-process.done:
	case <-time.After(2 * time.Second):
		t.Fatal("supervisor did not exit")
	}
	_ = process.control.Close()
	if !process.IsAlive() {
		t.Fatal("runner did not survive supervisor termination")
	}
	if !darwinAttemptLockHeld(t, ownership, process.environmentID, process.launchID) {
		t.Fatal("runner did not retain the launch lock")
	}
	if _, err := ownership.Reconcile(t.Context(), process.environmentID); !errors.Is(err, ErrLaunchOwnershipUncertain) {
		t.Fatalf("Reconcile() error = %v, want ownership uncertainty", err)
	}
	process.cleanup(t)
	deadline := time.Now().Add(2 * time.Second)
	for darwinAttemptLockHeld(t, ownership, process.environmentID, process.launchID) {
		if time.Now().After(deadline) {
			t.Fatal("runner lock remained held after test cleanup")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func darwinAttemptLockHeld(t *testing.T, ownership *LaunchOwnership, environmentID, launchID string) bool {
	t.Helper()
	files, err := ownership.openAttempt(environmentID, launchID)
	if err != nil {
		t.Fatal(err)
	}
	defer files.close()
	err = unix.Flock(int(files.lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		_ = unix.Flock(int(files.lock.Fd()), unix.LOCK_UN)
		return false
	}
	if !errors.Is(err, unix.EWOULDBLOCK) {
		t.Fatal(err)
	}
	return true
}

func TestDarwinReceiptPublicationFailureStopsRunnerAndReleasesLock(t *testing.T) {
	ownership := newDarwinTestLaunchOwnership(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	configJSON, err := json.Marshal(runner.Config{RunnerPath: executable})
	if err != nil {
		t.Fatal(err)
	}
	intent := launchIntent{
		Version: launchRecordVersion, EnvironmentID: "env-receipt-failure", LaunchID: strings.Repeat("b", 32), HostBootID: ownership.bootID,
		RunnerPath: ownership.launcherPath, RunnerDigest: ownership.launcherDigest, RunnerDevice: ownership.launcherDevice, RunnerInode: ownership.launcherInode,
		LauncherPath: ownership.launcherPath, LauncherDigest: ownership.launcherDigest, LauncherDevice: ownership.launcherDevice, LauncherInode: ownership.launcherInode,
		Config: configJSON, TermTimeoutNS: int64(50 * time.Millisecond), KillTimeoutNS: int64(time.Second),
	}
	files, err := ownership.createAttempt(intent)
	if err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(ownership.root, environmentDirectory(intent.EnvironmentID), intent.LaunchID, "receipt.json")
	_ = files.receipt.Close()
	files.receipt, err = os.Open(receiptPath) // #nosec G304 -- test-owned exact attempt path.
	if err != nil {
		files.close()
		t.Fatal(err)
	}
	controlReader, controlWriter, err := os.Pipe()
	if err != nil {
		files.close()
		t.Fatal(err)
	}
	lifetimePath := newDarwinTestLifetime(t)
	cmd := exec.Command(executable, internalLaunchOwnerArg)
	cmd.ExtraFiles = []*os.File{files.dir, files.lock, files.receipt, files.intent, controlReader}
	cmd.Env = darwinTestProcessEnv("runner", "", lifetimePath)
	owned := startDarwinTestCommand(t, cmd, lifetimePath)
	files.close()
	_ = controlReader.Close()
	wait := make(chan error, 1)
	cmd = owned.take()
	go func() { wait <- cmd.Wait() }()
	select {
	case err := <-wait:
		if err == nil {
			t.Fatal("launch owner unexpectedly succeeded with a read-only receipt")
		}
	case <-time.After(5 * time.Second):
		_ = controlWriter.Close()
		t.Fatal("launch owner did not clean up after receipt publication failure")
	}
	_ = controlWriter.Close()
	opened, err := ownership.openAttempt(intent.EnvironmentID, intent.LaunchID)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.close()
	if err := acquireLockBounded(t.Context(), opened.lock, time.Second); err != nil {
		t.Fatalf("runner retained lock after receipt publication failure: %v", err)
	}
	_ = unix.Flock(int(opened.lock.Fd()), unix.LOCK_UN)
}

func TestDarwinRunnerStopIsIdempotent(t *testing.T) {
	ownership := newDarwinTestLaunchOwnership(t)
	handle := spawnDarwinTestRunner(t, ownership, "env-stop-twice", darwinTestRunnerConfig(t))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := handle.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := handle.Stop(ctx); err != nil {
		t.Fatalf("second Stop() = %v", err)
	}
}

func TestDarwinHistoricalBusyAttemptNeverSignalsStoredPID(t *testing.T) {
	ownership := newDarwinTestLaunchOwnership(t)
	ownership.receiptWait = 50 * time.Millisecond
	files, err := ownership.createAttempt(launchIntent{Version: launchRecordVersion, EnvironmentID: "env-busy", LaunchID: strings.Repeat("a", 32), HostBootID: ownership.bootID,
		RunnerPath: ownership.launcherPath, RunnerDigest: ownership.launcherDigest, RunnerDevice: ownership.launcherDevice, RunnerInode: ownership.launcherInode,
		LauncherPath: ownership.launcherPath, LauncherDigest: ownership.launcherDigest, LauncherDevice: ownership.launcherDevice, LauncherInode: ownership.launcherInode, Config: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	defer files.close()
	stat, err := fileStat(files.lock)
	if err != nil {
		t.Fatal(err)
	}
	start, err := platformProcessStartIdentity(t.Context(), os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	receipt := LaunchReceipt{Version: launchRecordVersion, EnvironmentID: "env-busy", LaunchID: strings.Repeat("a", 32), HostBootID: ownership.bootID,
		PID: os.Getpid(), StartTime: start, RunnerDigest: ownership.launcherDigest, RunnerDevice: ownership.launcherDevice, RunnerInode: ownership.launcherInode,
		LauncherDigest: ownership.launcherDigest, LauncherDevice: ownership.launcherDevice, LauncherInode: ownership.launcherInode,
		LockDevice: uint64(uint32(stat.Dev)), LockInode: stat.Ino}
	payload, err := jsonMarshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := files.receipt.WriteAt(payload, 0); err != nil {
		t.Fatal(err)
	}
	cancelCause := errors.New("test reconcile cancellation")
	cancelled, cancel := context.WithCancelCause(t.Context())
	cancel(cancelCause)
	if _, err := ownership.Reconcile(cancelled, "env-busy"); !errors.Is(err, cancelCause) {
		t.Fatalf("cancelled Reconcile() error = %v, want %v", err, cancelCause)
	}
	_, err = ownership.Reconcile(t.Context(), "env-busy")
	if err == nil || !strings.Contains(err.Error(), "operator recovery is required") {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if err := unix.Kill(os.Getpid(), 0); err != nil {
		t.Fatalf("historical receipt PID was signalled: %v", err)
	}
}

func TestDarwinAcquireLockBoundedPreservesStrictErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "closed-lock")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := acquireLockBounded(t.Context(), file, time.Second); !errors.Is(err, unix.EBADF) {
		t.Fatalf("acquireLockBounded() error = %v, want EBADF", err)
	}
}

func jsonMarshal(value any) ([]byte, error) {
	return json.Marshal(value)
}
