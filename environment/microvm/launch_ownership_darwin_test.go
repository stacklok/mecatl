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
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stacklok/go-microvm/runner"
	"golang.org/x/sys/unix"
)

const launchOwnerHelperEnv = "GO_TEST_LAUNCH_OWNER_HELPER"

func TestMain(m *testing.M) {
	if handled, err := RunLaunchOwnerChild(os.Args[1:]); handled {
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(91)
		}
		os.Exit(0)
	}
	if mode := os.Getenv(launchOwnerHelperEnv); (mode == "runner" || mode == "ignore-term" || mode == "exit-7" || mode == "signal-term") && len(os.Args) == 2 && strings.HasPrefix(os.Args[1], "{") {
		if mode == "ignore-term" {
			signal.Ignore(syscall.SIGTERM)
		}
		if ready := os.Getenv("GO_TEST_LAUNCH_OWNER_READY"); ready != "" {
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

func darwinTestRunnerConfig(t *testing.T) runner.Config {
	return darwinTestRunnerConfigMode(t, "runner")
}

func darwinTestRunnerConfigMode(t *testing.T, mode string) runner.Config {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(launchOwnerHelperEnv, mode)
	return runner.Config{RunnerPath: executable, VMLogPath: filepath.Join(t.TempDir(), "runner.log")}
}

func darwinTestProcessEnv(mode, ready string) []string {
	env := []string{launchOwnerHelperEnv + "=" + mode, "GORACE=atexit_sleep_ms=0"}
	if ready != "" {
		env = append(env, "GO_TEST_LAUNCH_OWNER_READY="+ready)
	}
	return env
}

func TestDarwinLaunchOwnerSupervisorStopsDirectRunner(t *testing.T) {
	ownership := newDarwinTestLaunchOwnership(t)
	handle, err := ownership.spawn(t.Context(), "env-stop", darwinTestRunnerConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	process := handle.(*ownedRunnerProcess)
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
	handle, err := ownership.spawn(t.Context(), "env-stop-kill", darwinTestRunnerConfigMode(t, "ignore-term"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := handle.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if handle.IsAlive() {
		t.Fatal("runner remained alive after escalated stop")
	}
}

func TestDarwinDirectChildStopEscalatesAfterIgnoredTERM(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0], `{}`)
	cmd.Env = darwinTestProcessEnv("ignore-term", ready)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitForTestFile(t, ready)
	started := time.Now()
	result, err := ownDirectChild(cmd, nil, 50*time.Millisecond, time.Second, true)
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
	cmd := exec.Command(os.Args[0], `{}`)
	cmd.Env = darwinTestProcessEnv("runner", ready)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitForTestFile(t, ready)
	eof := make(chan struct{})
	close(eof)
	result, err := ownDirectChild(cmd, eof, time.Second, time.Second, false)
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
	cmd := exec.Command(os.Args[0], `{}`)
	cmd.Env = darwinTestProcessEnv("exit-7", "")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	result, err := ownDirectChild(cmd, nil, time.Second, time.Second, false)
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
	cmd := exec.Command(os.Args[0], `{}`)
	cmd.Env = darwinTestProcessEnv("signal-term", "")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	result, err := ownDirectChild(cmd, nil, time.Second, time.Second, false)
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
	handle, err := ownership.spawn(t.Context(), "env-supervisor-killed", darwinTestRunnerConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	process := handle.(*ownedRunnerProcess)
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
	if err := unix.Kill(process.pid, unix.SIGKILL); err != nil {
		t.Fatal(err)
	}
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
	cmd := exec.Command(executable, internalLaunchOwnerArg)
	cmd.ExtraFiles = []*os.File{files.dir, files.lock, files.receipt, files.intent, controlReader}
	cmd.Env = darwinTestProcessEnv("runner", "")
	if err := cmd.Start(); err != nil {
		files.close()
		_ = controlReader.Close()
		_ = controlWriter.Close()
		t.Fatal(err)
	}
	files.close()
	_ = controlReader.Close()
	wait := make(chan error, 1)
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
	handle, err := ownership.spawn(t.Context(), "env-stop-twice", darwinTestRunnerConfig(t))
	if err != nil {
		t.Fatal(err)
	}
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
