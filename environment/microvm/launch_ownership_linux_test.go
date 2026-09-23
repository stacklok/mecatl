//go:build linux

package microvm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	launchOwnerHelperParentEnv = "GO_TEST_LAUNCH_OWNER_PARENT_PID"
	launchOwnerHelperReadyEnv  = "GO_TEST_LAUNCH_OWNER_READY"
	launchOwnerMutantEnv       = "GO_TEST_LAUNCH_OWNER_MUTANT"
	launchOwnerMutantStateEnv  = "GO_TEST_LAUNCH_OWNER_MUTANT_STATE"
)

func TestMain(m *testing.M) {
	if handled, err := RunLaunchOwnerChild(os.Args[1:]); handled {
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(91)
		}
		os.Exit(0)
	}
	if os.Getenv(launchOwnerHelperEnv) == "runner" && len(os.Args) == 2 && strings.HasPrefix(os.Args[1], "{") {
		parentPID, err := strconv.Atoi(os.Getenv(launchOwnerHelperParentEnv))
		if err != nil || parentPID <= 0 {
			_, _ = fmt.Fprintln(os.Stderr, "runner started without test parent identity")
			os.Exit(92)
		}
		if err := unix.Prctl(unix.PR_SET_PDEATHSIG, uintptr(unix.SIGKILL), 0, 0, 0); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "runner could not arm parent-death signal:", err)
			os.Exit(92)
		}
		if os.Getppid() != parentPID {
			_, _ = fmt.Fprintln(os.Stderr, "runner test parent exited before parent-death signal was armed")
			os.Exit(92)
		}
		if err := os.WriteFile(os.Getenv(launchOwnerHelperReadyEnv), []byte("ready\n"), 0o600); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "runner could not publish test readiness:", err)
			os.Exit(92)
		}
		var lockStat unix.Stat_t
		if err := unix.Fstat(childLockFD, &lockStat); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "runner started without ownership lock descriptor:", err)
			os.Exit(92)
		}
		for {
			time.Sleep(time.Second)
		}
	}
	os.Exit(m.Run())
}

func newTestLaunchOwnership(t *testing.T) *LaunchOwnership {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	ownership, err := NewLaunchOwnership(LaunchOwnershipConfig{
		Root: root, LauncherPath: executable,
		ReceiptWait: 2 * time.Second, TermTimeout: time.Second, KillTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ownership
}

func testRunnerConfig(t *testing.T) runner.Config {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(launchOwnerHelperEnv, "runner")
	t.Setenv(launchOwnerHelperParentEnv, strconv.Itoa(os.Getpid()))
	t.Setenv(launchOwnerHelperReadyEnv, filepath.Join(t.TempDir(), "ready"))
	return runner.Config{RunnerPath: executable, VMLogPath: filepath.Join(t.TempDir(), "runner.log")}
}

type testOwnedRunner struct {
	process *ownedRunnerProcess
	pidfd   int
}

func spawnTestOwnedRunner(t *testing.T, ownership *LaunchOwnership, environmentID string, cfg runner.Config) *ownedRunnerProcess {
	t.Helper()
	handle, err := ownership.Spawner(environmentID).Spawn(context.Background(), cfg)
	if err != nil {
		log, _ := os.ReadFile(cfg.VMLogPath)
		t.Fatalf("%v; launcher log: %s", err, log)
	}
	process, ok := handle.(*ownedRunnerProcess)
	if !ok {
		t.Fatalf("spawned handle type = %T, want *ownedRunnerProcess", handle)
	}
	fixture := &testOwnedRunner{process: process, pidfd: -1}
	t.Cleanup(func() { fixture.cleanup(t) })

	ready := os.Getenv(launchOwnerHelperReadyEnv)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("inspect test-owned runner readiness: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for test-owned runner readiness")
		}
		time.Sleep(5 * time.Millisecond)
	}
	fixture.pidfd, err = unix.PidfdOpen(process.pid, 0)
	if err != nil {
		t.Fatalf("open pidfd for test-owned runner: %v", err)
	}
	startTime, err := rawProcessStartTime(process.pid)
	if err != nil || startTime != process.startTime {
		t.Fatalf("test-owned runner start identity = %q, %v; want %q", startTime, err, process.startTime)
	}
	digest, device, inode, err := processExecutableIdentity(process.pid)
	if err != nil || digest != process.runnerDigest || device != process.runnerDevice || inode != process.runnerInode {
		t.Fatalf("test-owned runner executable identity = %q/%d/%d, %v; want %q/%d/%d", digest, device, inode, err, process.runnerDigest, process.runnerDevice, process.runnerInode)
	}
	return process
}

func (f *testOwnedRunner) cleanup(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stopErr := f.process.Stop(ctx)
	if f.pidfd < 0 {
		if stopErr != nil {
			t.Errorf("stop test-owned runner without pidfd fallback: %v", stopErr)
		}
		return
	}
	defer func() {
		if err := unix.Close(f.pidfd); err != nil {
			t.Errorf("close test-owned runner pidfd: %v", err)
		}
	}()

	exited, waitErr := waitPidfd(ctx, f.pidfd, 20*time.Millisecond)
	if waitErr == nil && !exited {
		waitErr = signalAndWaitPidfd(ctx, f.pidfd, time.Second, time.Second)
	}
	if waitErr != nil {
		t.Errorf("terminate test-owned runner (Stop error: %v): %v", stopErr, waitErr)
		return
	}
	deadline := time.Now().Add(time.Second)
	for {
		_, err := rawProcessStartTime(f.process.pid)
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		if err != nil {
			t.Errorf("verify test-owned runner reaping: %v", err)
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("test-owned runner %d exited but was not reaped (Stop error: %v)", f.process.pid, stopErr)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func testLaunchIntent(t *testing.T, ownership *LaunchOwnership, environmentID string, cfg runner.Config) launchIntent {
	t.Helper()
	runnerPath, digest, device, inode, err := executableIdentity(cfg.RunnerPath)
	if err != nil {
		t.Fatal(err)
	}
	configJSON, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	launchID, err := randomLaunchID()
	if err != nil {
		t.Fatal(err)
	}
	return launchIntent{Version: launchRecordVersion, EnvironmentID: environmentID, LaunchID: launchID,
		HostBootID: ownership.bootID, RunnerPath: runnerPath, RunnerDigest: digest, RunnerDevice: device, RunnerInode: inode,
		LauncherPath: ownership.launcherPath, LauncherDigest: ownership.launcherDigest, LauncherDevice: ownership.launcherDevice, LauncherInode: ownership.launcherInode, Config: configJSON}
}

func waitOnlyReceipt(t *testing.T, ownership *LaunchOwnership, environmentID string) LaunchReceipt {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(ownership.root, environmentDirectory(environmentID)))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one launch attempt, got %d", len(entries))
	}
	files, err := ownership.openAttempt(environmentID, entries[0].Name())
	if err != nil {
		t.Fatal(err)
	}
	defer files.close()
	receipt, err := waitReceipt(context.Background(), files, ownership.receiptWait)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func attemptLockHeld(t *testing.T, ownership *LaunchOwnership, environmentID, launchID string) bool {
	t.Helper()
	files, err := ownership.openAttempt(environmentID, launchID)
	if err != nil {
		t.Fatal(err)
	}
	defer files.close()
	lockErr := unix.Flock(int(files.lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if lockErr == nil {
		_ = unix.Flock(int(files.lock.Fd()), unix.LOCK_UN)
		return false
	}
	if !errors.Is(lockErr, unix.EWOULDBLOCK) {
		t.Fatal(lockErr)
	}
	return true
}

func replaceReceipt(t *testing.T, ownership *LaunchOwnership, environmentID, launchID string, mutate func(*LaunchReceipt)) {
	t.Helper()
	path := filepath.Join(ownership.root, environmentDirectory(environmentID), launchID, "receipt.json")
	data, err := os.ReadFile(path) // #nosec G304 -- test-owned exact private path.
	if err != nil {
		t.Fatal(err)
	}
	var value LaunchReceipt
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	mutate(&value)
	data, err = json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil { // #nosec G306 -- test-owned receipt.
		t.Fatal(err)
	}
}

func TestLaunchOwnershipRejectsReplacedReceiptPath(t *testing.T) {
	ownership := newTestLaunchOwnership(t)
	intent := testLaunchIntent(t, ownership, "repository-replaced-receipt", testRunnerConfig(t))
	files, err := ownership.createAttempt(intent)
	if err != nil {
		t.Fatal(err)
	}
	files.close()
	attempt := filepath.Join(ownership.root, environmentDirectory(intent.EnvironmentID), intent.LaunchID)
	receiptPath := filepath.Join(attempt, "receipt.json")
	if err := os.Remove(receiptPath); err != nil {
		t.Fatal(err)
	}
	forged := filepath.Join(t.TempDir(), "receipt.json")
	payload, err := json.Marshal(LaunchReceipt{Version: launchRecordVersion, EnvironmentID: intent.EnvironmentID, LaunchID: intent.LaunchID, HostBootID: intent.HostBootID, PID: os.Getpid()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(forged, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(forged, receiptPath); err != nil {
		t.Fatal(err)
	}
	if _, err := ownership.openAttempt(intent.EnvironmentID, intent.LaunchID); err == nil {
		t.Fatal("replaced receipt path was trusted")
	}
}

func TestPublishLaunchReceiptMakesLockInheritableBeforePublication(t *testing.T) {
	ownership := newTestLaunchOwnership(t)
	intent := testLaunchIntent(t, ownership, "repository-receipt-order", testRunnerConfig(t))
	files, err := ownership.createAttempt(intent)
	if err != nil {
		t.Fatal(err)
	}
	defer files.close()

	if err := publishLaunchReceipt(files.dir, files.lock, files.receipt, []byte(`{"ready":true}`)); err != nil {
		t.Fatal(err)
	}
	flags, err := unix.FcntlInt(files.lock.Fd(), unix.F_GETFD, 0)
	if err != nil {
		t.Fatal(err)
	}
	if flags&unix.FD_CLOEXEC != 0 {
		t.Fatal("receipt was published before the ownership lock became exec-inheritable")
	}
	data, err := os.ReadFile(filepath.Join(ownership.root, environmentDirectory(intent.EnvironmentID), intent.LaunchID, "receipt.json")) // #nosec G304 -- test-owned exact private path.
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{\"ready\":true}\n" {
		t.Fatalf("published receipt = %q", data)
	}
}

func TestLaunchOwnershipPartialReceiptRemainsPending(t *testing.T) {
	ownership := newTestLaunchOwnership(t)
	intent := testLaunchIntent(t, ownership, "repository-partial-receipt", testRunnerConfig(t))
	files, err := ownership.createAttempt(intent)
	if err != nil {
		t.Fatal(err)
	}
	defer files.close()
	if _, err := files.receipt.WriteAt([]byte(`{"version":1`), 0); err != nil {
		t.Fatal(err)
	}
	_, err = waitReceipt(t.Context(), files, 20*time.Millisecond)
	if !errors.Is(err, ErrLaunchReceiptPending) {
		t.Fatalf("partial receipt = %v, want pending", err)
	}
}

func TestLaunchOwnershipPersistsReceiptAndKeepsLockAcrossExec(t *testing.T) {
	ownership := newTestLaunchOwnership(t)
	cfg := testRunnerConfig(t)
	handle := spawnTestOwnedRunner(t, ownership, "repository-one", cfg)

	receipt := waitOnlyReceipt(t, ownership, "repository-one")
	if receipt.PID != handle.PID() || receipt.StartTime == "" || receipt.LaunchID == "" {
		t.Fatalf("incomplete receipt: %+v", receipt)
	}
	locked := attemptLockHeld(t, ownership, "repository-one", receipt.LaunchID)
	if !locked {
		t.Fatal("ownership lock was released while exec'd runner remained alive")
	}
}

func TestLaunchOwnershipReconcileTerminatesOnlyExactOwnedRunner(t *testing.T) {
	ownership := newTestLaunchOwnership(t)
	handle := spawnTestOwnedRunner(t, ownership, "repository-two", testRunnerConfig(t))
	receipt := waitOnlyReceipt(t, ownership, "repository-two")

	result, err := ownership.Reconcile(context.Background(), "repository-two")
	if err != nil {
		t.Fatal(err)
	}
	if result.Terminated != 1 || result.Dead != 0 {
		t.Fatalf("reconcile result = %+v", result)
	}
	if handle.IsAlive() {
		t.Fatal("owned runner remains alive after reconciliation")
	}
	locked := attemptLockHeld(t, ownership, "repository-two", receipt.LaunchID)
	if locked {
		t.Fatal("ownership lock remains held after runner exit")
	}
}

func TestLaunchOwnershipIdentityMismatchIsUntouched(t *testing.T) {
	ownership := newTestLaunchOwnership(t)
	handle := spawnTestOwnedRunner(t, ownership, "repository-three", testRunnerConfig(t))
	receipt := waitOnlyReceipt(t, ownership, "repository-three")
	replaceReceipt(t, ownership, "repository-three", receipt.LaunchID, func(value *LaunchReceipt) {
		value.StartTime = "1"
	})
	defer replaceReceipt(t, ownership, "repository-three", receipt.LaunchID, func(value *LaunchReceipt) {
		*value = receipt
	})

	_, err := ownership.Reconcile(context.Background(), "repository-three")
	if !errors.Is(err, ErrLaunchOwnershipUncertain) {
		t.Fatalf("reconcile error = %v, want ErrLaunchOwnershipUncertain", err)
	}
	if !handle.IsAlive() {
		t.Fatal("identity-mismatched process was signalled")
	}
}

func TestLaunchOwnershipFreeLockRecoversAcrossBootIDChange(t *testing.T) {
	ownership := newTestLaunchOwnership(t)
	handle := spawnTestOwnedRunner(t, ownership, "repository-four", testRunnerConfig(t))
	_ = waitOnlyReceipt(t, ownership, "repository-four")
	if err := handle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	ownership.bootID = "replacement-host-boot"

	result, err := ownership.Reconcile(context.Background(), "repository-four")
	if err != nil {
		t.Fatal(err)
	}
	if result.Dead != 1 || result.Terminated != 0 {
		t.Fatalf("reconcile result = %+v", result)
	}
}

func TestLaunchOwnershipAttemptPublicationCrashCheckpointsAreRecoverable(t *testing.T) {
	checkpoints := []string{
		"before-environment-directory-create", "environment-directory-created",
		"before-staging-namespace-create", "staging-namespace-created",
		"before-attempt-directory-create", "attempt-directory-created",
		"before-owner-lock-create", "owner-lock-created",
		"before-receipt-create", "receipt-created",
		"before-intent-create", "intent-created",
		"before-intent-write", "intent-written",
		"before-intent-sync", "intent-synced",
		"before-lock-sync", "lock-synced",
		"before-receipt-sync", "receipt-synced",
		"before-attempt-directory-sync", "attempt-directory-synced",
		"before-staging-directory-sync", "staging-directory-synced",
		"before-attempt-publication", "attempt-published",
		"before-staging-publication-sync", "staging-publication-synced",
		"before-environment-directory-sync", "environment-directory-synced",
		"before-root-sync", "root-synced",
	}
	for _, checkpoint := range checkpoints {
		t.Run(checkpoint, func(t *testing.T) {
			ownership := newTestLaunchOwnership(t)
			const environmentID = "repository-publication-crash"
			intent := testLaunchIntent(t, ownership, environmentID, testRunnerConfig(t))
			injected := errors.New("injected publication crash")
			ownership.checkpoint = func(name string) error {
				if name == checkpoint {
					return injected
				}
				return nil
			}
			if files, err := ownership.createAttempt(intent); !errors.Is(err, injected) {
				if files != nil {
					files.close()
				}
				t.Fatalf("checkpoint error = %v", err)
			}
			ownership.checkpoint = nil
			if _, err := ownership.Reconcile(t.Context(), environmentID); err != nil {
				t.Fatalf("reconcile after checkpoint: %v", err)
			}
			retry := testLaunchIntent(t, ownership, environmentID, testRunnerConfig(t))
			files, err := ownership.createAttempt(retry)
			if err != nil {
				t.Fatalf("retry after checkpoint: %v", err)
			}
			files.close()
			if _, err := ownership.Reconcile(t.Context(), environmentID); err != nil {
				t.Fatalf("reconcile retry: %v", err)
			}
		})
	}
}

func TestLaunchOwnershipPreSpawnAttemptIsRecoverable(t *testing.T) {
	ownership := newTestLaunchOwnership(t)
	intent := testLaunchIntent(t, ownership, "repository-pre-spawn", testRunnerConfig(t))
	files, err := ownership.createAttempt(intent)
	if err != nil {
		t.Fatal(err)
	}
	files.close() // crash before spawn: no process inherited the lock

	result, err := ownership.Reconcile(context.Background(), intent.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Dead != 1 || result.Terminated != 0 {
		t.Fatalf("reconcile result = %+v", result)
	}
}

func TestLaunchOwnershipBusyMissingReceiptIsBoundedPending(t *testing.T) {
	ownership := newTestLaunchOwnership(t)
	ownership.receiptWait = 40 * time.Millisecond
	intent := testLaunchIntent(t, ownership, "repository-pending", testRunnerConfig(t))
	files, err := ownership.createAttempt(intent)
	if err != nil {
		t.Fatal(err)
	}
	defer files.close()

	started := time.Now()
	_, err = ownership.Reconcile(context.Background(), intent.EnvironmentID)
	if !errors.Is(err, ErrLaunchReceiptPending) {
		t.Fatalf("reconcile error = %v, want pending receipt", err)
	}
	if elapsed := time.Since(started); elapsed < ownership.receiptWait || elapsed > time.Second {
		t.Fatalf("bounded receipt wait took %v", elapsed)
	}
}

func TestLaunchOwnershipChildReceiptFailureReleasesLock(t *testing.T) {
	ownership := newTestLaunchOwnership(t)
	intent := testLaunchIntent(t, ownership, "repository-child-failure", testRunnerConfig(t))
	intent.RunnerDigest = "sha256:" + strings.Repeat("0", 64)
	files, err := ownership.createAttempt(intent)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(ownership.launcherPath, internalLaunchOwnerArg) // #nosec G204 -- pinned test executable.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.ExtraFiles = []*os.File{files.dir, files.lock, files.receipt, files.intent}
	cmd.Env = scrubRunnerEnvironment(os.Environ())
	if err := cmd.Start(); err != nil {
		files.close()
		t.Fatal(err)
	}
	files.close()
	if err := cmd.Wait(); err == nil {
		t.Fatal("invalid runner identity unexpectedly exec'd")
	}

	result, err := ownership.Reconcile(context.Background(), intent.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Dead != 1 {
		t.Fatalf("reconcile result = %+v", result)
	}
}

func TestLaunchOwnershipRepeatedAttemptsLeaveNoLiveOwner(t *testing.T) {
	ownership := newTestLaunchOwnership(t)
	const environmentID = "repository-repeated"
	for range 3 {
		handle := spawnTestOwnedRunner(t, ownership, environmentID, testRunnerConfig(t))
		if err := handle.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	result, err := ownership.Reconcile(context.Background(), environmentID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Dead != 3 || result.Terminated != 0 {
		t.Fatalf("reconcile result = %+v", result)
	}
}

type testOwnedRunnerState struct {
	PID       int    `json:"pid"`
	StartTime string `json:"start_time"`
}

func TestLaunchOwnershipFixtureCleanupMutant(t *testing.T) {
	mode := os.Getenv(launchOwnerMutantEnv)
	if mode == "" {
		t.Skip("fixture lifecycle subprocess only")
	}
	ownership := newTestLaunchOwnership(t)
	process := spawnTestOwnedRunner(t, ownership, "fixture-lifecycle-"+mode, testRunnerConfig(t))
	receipt := waitOnlyReceipt(t, ownership, "fixture-lifecycle-"+mode)
	replaceReceipt(t, ownership, "fixture-lifecycle-"+mode, receipt.LaunchID, func(value *LaunchReceipt) {
		value.StartTime = "corrupt-test-receipt"
	})
	state, err := json.Marshal(testOwnedRunnerState{PID: process.pid, StartTime: process.startTime})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv(launchOwnerMutantStateEnv), state, 0o600); err != nil { // #nosec G306 -- private test fixture.
		t.Fatal(err)
	}
	switch mode {
	case "success":
		return
	case "failure":
		t.Fatal("intentional fixture failure")
	case "parent-exit":
		os.Exit(23)
	default:
		t.Fatalf("unknown fixture lifecycle mode %q", mode)
	}
}

func TestLaunchOwnershipFixtureLifecycleLeavesNoHelpers(t *testing.T) {
	for _, tc := range []struct {
		mode     string
		exitCode int
	}{{"success", 0}, {"failure", 1}, {"parent-exit", 23}} {
		t.Run(tc.mode, func(t *testing.T) {
			statePath := filepath.Join(t.TempDir(), "runner.json")
			cmd := exec.Command(os.Args[0], "-test.run=^TestLaunchOwnershipFixtureCleanupMutant$") // #nosec G204 -- current test executable and fixed arguments.
			cmd.Env = []string{
				launchOwnerMutantEnv + "=" + tc.mode,
				launchOwnerMutantStateEnv + "=" + statePath,
				"GORACE=atexit_sleep_ms=0",
			}
			output, err := cmd.CombinedOutput()
			if tc.exitCode == 0 {
				if err != nil {
					t.Fatalf("mutant exited with %v: %s", err, output)
				}
			} else {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != tc.exitCode {
					t.Fatalf("mutant exit = %v, want %d: %s", err, tc.exitCode, output)
				}
			}
			data, err := os.ReadFile(statePath) // #nosec G304 -- exact private test fixture path.
			if err != nil {
				t.Fatal(err)
			}
			var state testOwnedRunnerState
			if err := json.Unmarshal(data, &state); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				startTime, identityErr := rawProcessStartTime(state.PID)
				if errors.Is(identityErr, os.ErrNotExist) || (identityErr == nil && startTime != state.StartTime) {
					break
				}
				if identityErr != nil {
					t.Fatalf("inspect fixture helper %d: %v", state.PID, identityErr)
				}
				if time.Now().After(deadline) {
					t.Fatalf("fixture helper %d/%s survived %s mutant", state.PID, state.StartTime, tc.mode)
				}
				time.Sleep(5 * time.Millisecond)
			}
		})
	}
}
