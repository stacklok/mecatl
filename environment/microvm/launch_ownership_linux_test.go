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
	if os.Getenv(launchOwnerHelperEnv) == "runner" && len(os.Args) == 2 && strings.HasPrefix(os.Args[1], "{") {
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
	return runner.Config{RunnerPath: executable, VMLogPath: filepath.Join(t.TempDir(), "runner.log")}
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
	handle, err := ownership.Spawner("repository-one").Spawn(context.Background(), cfg)
	if err != nil {
		log, _ := os.ReadFile(cfg.VMLogPath)
		t.Fatalf("%v; launcher log: %s", err, log)
	}
	t.Cleanup(func() { _ = handle.Stop(context.Background()) })

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
	handle, err := ownership.Spawner("repository-two").Spawn(context.Background(), testRunnerConfig(t))
	if err != nil {
		t.Fatal(err)
	}
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
	handle, err := ownership.Spawner("repository-three").Spawn(context.Background(), testRunnerConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Stop(context.Background()) }()
	receipt := waitOnlyReceipt(t, ownership, "repository-three")
	replaceReceipt(t, ownership, "repository-three", receipt.LaunchID, func(value *LaunchReceipt) {
		value.StartTime = "1"
	})
	defer replaceReceipt(t, ownership, "repository-three", receipt.LaunchID, func(value *LaunchReceipt) {
		*value = receipt
	})

	_, err = ownership.Reconcile(context.Background(), "repository-three")
	if !errors.Is(err, ErrLaunchOwnershipUncertain) {
		t.Fatalf("reconcile error = %v, want ErrLaunchOwnershipUncertain", err)
	}
	if !handle.IsAlive() {
		t.Fatal("identity-mismatched process was signalled")
	}
}

func TestLaunchOwnershipFreeLockRecoversAcrossBootIDChange(t *testing.T) {
	ownership := newTestLaunchOwnership(t)
	handle, err := ownership.Spawner("repository-four").Spawn(context.Background(), testRunnerConfig(t))
	if err != nil {
		t.Fatal(err)
	}
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
		handle, err := ownership.Spawner(environmentID).Spawn(context.Background(), testRunnerConfig(t))
		if err != nil {
			t.Fatal(err)
		}
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
