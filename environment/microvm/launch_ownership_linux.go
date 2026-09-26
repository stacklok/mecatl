//go:build linux

package microvm

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/stacklok/go-microvm/runner"
	"golang.org/x/sys/unix"
)

const (
	internalLaunchOwnerArg = "--internal-microvm-launch-owner"
	launchRecordVersion    = 1
	defaultReceiptWait     = 2 * time.Second
	defaultTermTimeout     = 5 * time.Second
	defaultKillTimeout     = 2 * time.Second
)

const (
	childDirFD = 3 + iota
	childLockFD
	childReceiptFD
	childIntentFD
)

type launchIntent struct {
	Version        int             `json:"version"`
	EnvironmentID  string          `json:"environment_id"`
	LaunchID       string          `json:"launch_id"`
	HostBootID     string          `json:"host_boot_id"`
	RunnerPath     string          `json:"runner_path"`
	RunnerDigest   string          `json:"runner_digest"`
	RunnerDevice   uint64          `json:"runner_device"`
	RunnerInode    uint64          `json:"runner_inode"`
	LauncherPath   string          `json:"launcher_path"`
	LauncherDigest string          `json:"launcher_digest"`
	LauncherDevice uint64          `json:"launcher_device"`
	LauncherInode  uint64          `json:"launcher_inode"`
	Config         json.RawMessage `json:"config"`
}

type attemptFiles struct {
	dir, lock, receipt, intent *os.File
	intentValue                launchIntent
}

// NewLaunchOwnership opens a private host-only launch root and pins the exact launcher identity.
func NewLaunchOwnership(cfg LaunchOwnershipConfig) (*LaunchOwnership, error) {
	if !filepath.IsAbs(cfg.Root) || !filepath.IsAbs(cfg.LauncherPath) {
		return nil, errors.New("launch ownership root and launcher path must be absolute")
	}
	if err := os.MkdirAll(cfg.Root, 0o700); err != nil {
		return nil, fmt.Errorf("create launch ownership root: %w", err)
	}
	if err := validatePrivateDirectory(cfg.Root); err != nil {
		return nil, err
	}
	launcherPath, launcherDigest, launcherDevice, launcherInode, err := executableIdentity(cfg.LauncherPath)
	if err != nil {
		return nil, fmt.Errorf("resolve launch owner executable: %w", err)
	}
	bootID, err := readHostBootID()
	if err != nil {
		return nil, err
	}
	if cfg.ReceiptWait <= 0 {
		cfg.ReceiptWait = defaultReceiptWait
	}
	if cfg.TermTimeout <= 0 {
		cfg.TermTimeout = defaultTermTimeout
	}
	if cfg.KillTimeout <= 0 {
		cfg.KillTimeout = defaultKillTimeout
	}
	return &LaunchOwnership{root: cfg.Root, launcherPath: launcherPath, launcherDigest: launcherDigest, launcherDevice: launcherDevice, launcherInode: launcherInode, bootID: bootID,
		receiptWait: cfg.ReceiptWait, termTimeout: cfg.TermTimeout, killTimeout: cfg.KillTimeout}, nil
}

func (o *LaunchOwnership) spawn(_ context.Context, environmentID string, cfg runner.Config) (runner.ProcessHandle, error) {
	if environmentID == "" || cfg.RunnerPath == "" {
		return nil, errors.New("launch ownership requires an environment and resolved runner path")
	}
	runnerPath, runnerDigest, runnerDev, runnerIno, err := executableIdentity(cfg.RunnerPath)
	if err != nil {
		return nil, fmt.Errorf("resolve runner identity: %w", err)
	}
	configJSON, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal runner config: %w", err)
	}
	launchID, err := randomLaunchID()
	if err != nil {
		return nil, err
	}
	intent := launchIntent{Version: launchRecordVersion, EnvironmentID: environmentID, LaunchID: launchID,
		HostBootID: o.bootID, RunnerPath: runnerPath, RunnerDigest: runnerDigest, RunnerDevice: runnerDev, RunnerInode: runnerIno,
		LauncherPath: o.launcherPath, LauncherDigest: o.launcherDigest, LauncherDevice: o.launcherDevice, LauncherInode: o.launcherInode, Config: configJSON}
	files, err := o.createAttempt(intent)
	if err != nil {
		return nil, err
	}
	defer files.close()

	cmd := exec.Command(o.launcherPath, internalLaunchOwnerArg) // #nosec G204 -- exact pinned executable, fixed internal argument.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	applyOwnedUserNamespace(cmd, cfg.UserNamespace)
	cmd.ExtraFiles = []*os.File{files.dir, files.lock, files.receipt, files.intent}
	cmd.Env = scrubRunnerEnvironment(os.Environ())
	if cfg.LibDir != "" {
		cmd.Env = append(cmd.Env, runnerLibraryPathVariable()+"="+cfg.LibDir)
	}
	if cfg.VMLogPath != "" {
		logFile, openErr := os.OpenFile(cfg.VMLogPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600) // #nosec G304 -- resolved library-owned VM log path.
		if openErr != nil {
			return nil, fmt.Errorf("open vm log: %w", openErr)
		}
		defer func() { _ = logFile.Close() }()
		cmd.Stdout, cmd.Stderr = logFile, logFile
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start owned runner launcher: %w", err)
	}
	process := &ownedRunnerProcess{owner: o, environmentID: environmentID, launchID: launchID, pid: cmd.Process.Pid}
	go func() { _ = cmd.Wait() }()

	receipt, err := waitReceipt(context.Background(), files, o.receiptWait)
	if err != nil {
		return process, err
	}
	if receipt.PID != process.pid {
		return process, fmt.Errorf("%w: launcher receipt PID mismatch", ErrLaunchOwnershipUncertain)
	}
	process.startTime, process.runnerDigest, process.runnerDevice, process.runnerInode = receipt.StartTime, receipt.RunnerDigest, receipt.RunnerDevice, receipt.RunnerInode
	return process, nil
}

func (o *LaunchOwnership) createAttempt(intent launchIntent) (*attemptFiles, error) { //nolint:gocyclo // linear crash checkpoints keep publication ordering auditable.
	root, err := os.OpenFile(o.root, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	envName := environmentDirectory(intent.EnvironmentID)
	if err := o.attemptCheckpoint("before-environment-directory-create"); err != nil {
		return nil, err
	}
	if err := mkdiratExisting(root.Fd(), envName); err != nil {
		return nil, err
	}
	if err := o.attemptCheckpoint("environment-directory-created"); err != nil {
		return nil, err
	}
	envFD, err := unix.Openat(int(root.Fd()), envName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(envFD) }()
	if err := o.attemptCheckpoint("before-staging-namespace-create"); err != nil {
		return nil, err
	}
	if err := mkdiratExisting(root.Fd(), ".staging"); err != nil {
		return nil, err
	}
	if err := o.attemptCheckpoint("staging-namespace-created"); err != nil {
		return nil, err
	}
	stagingFD, err := unix.Openat(int(root.Fd()), ".staging", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(stagingFD) }()
	if err := o.attemptCheckpoint("before-attempt-directory-create"); err != nil {
		return nil, err
	}
	if err := unix.Mkdirat(stagingFD, intent.LaunchID, 0o700); err != nil {
		return nil, err
	}
	if err := o.attemptCheckpoint("attempt-directory-created"); err != nil {
		return nil, err
	}
	dirFD, err := unix.Openat(stagingFD, intent.LaunchID, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	files := &attemptFiles{dir: os.NewFile(uintptr(dirFD), "attempt-dir"), intentValue: intent}
	fail := func(e error) (*attemptFiles, error) { files.close(); return nil, e }
	if err := o.attemptCheckpoint("before-owner-lock-create"); err != nil {
		return fail(err)
	}
	if files.lock, err = openatExclusive(dirFD, "owner.lock", unix.O_RDWR, 0o600); err != nil {
		return fail(err)
	}
	if err := o.attemptCheckpoint("owner-lock-created"); err != nil {
		return fail(err)
	}
	if err := unix.Flock(int(files.lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fail(err)
	}
	if err := o.attemptCheckpoint("before-receipt-create"); err != nil {
		return fail(err)
	}
	if files.receipt, err = openatExclusive(dirFD, "receipt.json", unix.O_RDWR, 0o600); err != nil {
		return fail(err)
	}
	if err := o.attemptCheckpoint("receipt-created"); err != nil {
		return fail(err)
	}
	if err := o.attemptCheckpoint("before-intent-create"); err != nil {
		return fail(err)
	}
	if files.intent, err = openatExclusive(dirFD, "intent.json", unix.O_RDWR, 0o600); err != nil {
		return fail(err)
	}
	if err := o.attemptCheckpoint("intent-created"); err != nil {
		return fail(err)
	}
	payload, err := json.Marshal(intent)
	if err != nil {
		return fail(err)
	}
	if err := o.attemptCheckpoint("before-intent-write"); err != nil {
		return fail(err)
	}
	if _, err = files.intent.Write(append(payload, '\n')); err != nil {
		return fail(err)
	}
	if err := o.attemptCheckpoint("intent-written"); err != nil {
		return fail(err)
	}
	syncSteps := []struct {
		before string
		after  string
		run    func() error
	}{
		{"before-intent-sync", "intent-synced", files.intent.Sync},
		{"before-lock-sync", "lock-synced", files.lock.Sync},
		{"before-receipt-sync", "receipt-synced", files.receipt.Sync},
		{"before-attempt-directory-sync", "attempt-directory-synced", func() error { return unix.Fsync(dirFD) }},
		{"before-staging-directory-sync", "staging-directory-synced", func() error { return unix.Fsync(stagingFD) }},
	}
	for _, step := range syncSteps {
		if err := o.attemptCheckpoint(step.before); err != nil {
			return fail(err)
		}
		if err = step.run(); err != nil {
			return fail(err)
		}
		if err := o.attemptCheckpoint(step.after); err != nil {
			return fail(err)
		}
	}
	if err := o.attemptCheckpoint("before-attempt-publication"); err != nil {
		return fail(err)
	}
	if err = unix.Renameat2(stagingFD, intent.LaunchID, envFD, intent.LaunchID, unix.RENAME_NOREPLACE); err != nil {
		return fail(err)
	}
	if err := o.attemptCheckpoint("attempt-published"); err != nil {
		return fail(err)
	}
	if err := o.attemptCheckpoint("before-staging-publication-sync"); err != nil {
		return fail(err)
	}
	if err = unix.Fsync(stagingFD); err != nil {
		return fail(err)
	}
	if err := o.attemptCheckpoint("staging-publication-synced"); err != nil {
		return fail(err)
	}
	if err := o.attemptCheckpoint("before-environment-directory-sync"); err != nil {
		return fail(err)
	}
	if err = unix.Fsync(envFD); err != nil {
		return fail(err)
	}
	if err := o.attemptCheckpoint("environment-directory-synced"); err != nil {
		return fail(err)
	}
	if err := o.attemptCheckpoint("before-root-sync"); err != nil {
		return fail(err)
	}
	if err = root.Sync(); err != nil {
		return fail(err)
	}
	if err := o.attemptCheckpoint("root-synced"); err != nil {
		return fail(err)
	}
	return files, nil
}

func (o *LaunchOwnership) attemptCheckpoint(name string) error {
	if o.checkpoint == nil {
		return nil
	}
	return o.checkpoint(name)
}

func (f *attemptFiles) close() {
	for _, file := range []*os.File{f.intent, f.receipt, f.lock, f.dir} {
		if file != nil {
			_ = file.Close()
		}
	}
}

// RunLaunchOwnerChild handles the hidden launcher mode before normal daemon flag parsing.
func RunLaunchOwnerChild(args []string) (bool, error) {
	if len(args) != 1 || args[0] != internalLaunchOwnerArg {
		return false, nil
	}
	return true, runLaunchOwnerChild()
}

func runLaunchOwnerChild() error { //nolint:gocyclo // linear fail-closed validation keeps exec ordering auditable.
	dir := os.NewFile(childDirFD, "attempt-dir")
	lock := os.NewFile(childLockFD, "owner-lock")
	receipt := os.NewFile(childReceiptFD, "receipt")
	intentFile := os.NewFile(childIntentFD, "intent")
	if err := validateInheritedAttempt(dir, lock, receipt, intentFile); err != nil {
		return err
	}
	if _, err := intentFile.Seek(0, io.SeekStart); err != nil {
		return err
	}
	intentBytes, err := io.ReadAll(io.LimitReader(intentFile, 1<<20))
	if err != nil {
		return err
	}
	var intent launchIntent
	if err := json.Unmarshal(intentBytes, &intent); err != nil {
		return err
	}
	if err := validateLaunchIntent(intent); err != nil {
		return err
	}
	bootID, err := readHostBootID()
	if err != nil || bootID != intent.HostBootID {
		return errors.New("launcher host boot identity mismatch")
	}
	launcherDigest, launcherDev, launcherIno, err := processExecutableIdentity(os.Getpid())
	launcherPath, pathErr := os.Readlink("/proc/self/exe")
	if err != nil || pathErr != nil || filepath.Clean(strings.TrimSuffix(launcherPath, " (deleted)")) != intent.LauncherPath || launcherDigest != intent.LauncherDigest || launcherDev != intent.LauncherDevice || launcherIno != intent.LauncherInode {
		return errors.New("launcher executable identity mismatch")
	}
	runnerPath, runnerDigest, runnerDev, runnerIno, err := executableIdentity(intent.RunnerPath)
	if err != nil || runnerPath != intent.RunnerPath || runnerDigest != intent.RunnerDigest || runnerDev != intent.RunnerDevice || runnerIno != intent.RunnerInode {
		return errors.New("runner executable identity mismatch")
	}
	start, err := rawProcessStartTime(os.Getpid())
	if err != nil {
		return err
	}
	lockStat, err := fileStat(lock)
	if err != nil {
		return err
	}
	value := LaunchReceipt{Version: launchRecordVersion, EnvironmentID: intent.EnvironmentID, LaunchID: intent.LaunchID,
		HostBootID: intent.HostBootID, PID: os.Getpid(), StartTime: start, RunnerDigest: intent.RunnerDigest,
		RunnerDevice: intent.RunnerDevice, RunnerInode: intent.RunnerInode, LauncherDigest: intent.LauncherDigest,
		LauncherDevice: intent.LauncherDevice, LauncherInode: intent.LauncherInode,
		LockDevice: lockStat.Dev, LockInode: lockStat.Ino}
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := publishLaunchReceipt(dir, lock, receipt, payload); err != nil {
		return err
	}
	_ = dir.Close()
	_ = receipt.Close()
	_ = intentFile.Close()
	err = syscall.Exec(intent.RunnerPath, []string{intent.RunnerPath, string(intent.Config)}, os.Environ())
	runtime.KeepAlive(lock)
	return err
}

func publishLaunchReceipt(dir, lock, receipt *os.File, payload []byte) error {
	flags, err := unix.FcntlInt(lock.Fd(), unix.F_GETFD, 0)
	if err != nil {
		return err
	}
	if _, err := unix.FcntlInt(lock.Fd(), unix.F_SETFD, flags&^unix.FD_CLOEXEC); err != nil {
		return err
	}
	if err := receipt.Truncate(0); err != nil {
		return err
	}
	if _, err := receipt.WriteAt(append(payload, '\n'), 0); err != nil {
		return err
	}
	if err := receipt.Sync(); err != nil {
		return err
	}
	return dir.Sync()
}

func validateInheritedAttempt(dir, lock, receipt, intent *os.File) error {
	if dir == nil || lock == nil || receipt == nil || intent == nil {
		return errors.New("launcher inherited descriptors are incomplete")
	}
	uid, err := currentUID()
	if err != nil {
		return err
	}
	dirInfo, err := dir.Stat()
	if err != nil || !dirInfo.IsDir() || dirInfo.Mode().Perm() != 0o700 {
		return errors.New("launcher attempt directory is invalid")
	}
	for name, file := range map[string]*os.File{"owner.lock": lock, "receipt.json": receipt, "intent.json": intent} {
		info, statErr := file.Stat()
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return fmt.Errorf("launcher %s descriptor is invalid", name)
		}
		var linked unix.Stat_t
		if err := unix.Fstatat(int(dir.Fd()), name, &linked, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		actual, err := fileStat(file)
		if err != nil || linked.Dev != actual.Dev || linked.Ino != actual.Ino || linked.Uid != uid || linked.Nlink != 1 {
			return fmt.Errorf("launcher %s inode identity mismatch", name)
		}
	}
	return nil
}

func validateLaunchIntent(value launchIntent) error {
	if value.Version != launchRecordVersion || value.EnvironmentID == "" || len(value.LaunchID) != 32 || value.HostBootID == "" ||
		!filepath.IsAbs(value.RunnerPath) || !filepath.IsAbs(value.LauncherPath) || len(value.Config) == 0 {
		return errors.New("launcher intent is incomplete")
	}
	if _, err := hex.DecodeString(value.LaunchID); err != nil {
		return errors.New("launcher launch ID is invalid")
	}
	return nil
}

// Reconcile proves every prior attempt dead or terminates its exact pidfd-owned runner.
func (o *LaunchOwnership) Reconcile(ctx context.Context, environmentID string) (LaunchReconcileResult, error) {
	var result LaunchReconcileResult
	envPath := filepath.Join(o.root, environmentDirectory(environmentID))
	entries, err := os.ReadDir(envPath)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || len(entry.Name()) != 32 {
			return result, fmt.Errorf("%w: unexpected launch attempt entry", ErrLaunchOwnershipUncertain)
		}
		dead, terminated, err := o.reconcileAttempt(ctx, environmentID, entry.Name())
		if err != nil {
			return result, err
		}
		if dead {
			result.Dead++
		}
		if terminated {
			result.Terminated++
		}
	}
	return result, nil
}

func (o *LaunchOwnership) reconcileAttempt(ctx context.Context, environmentID, launchID string) (bool, bool, error) {
	files, err := o.openAttempt(environmentID, launchID)
	if err != nil {
		return false, false, err
	}
	defer files.close()
	if err := unix.Flock(int(files.lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
		_ = unix.Flock(int(files.lock.Fd()), unix.LOCK_UN)
		return true, false, nil
	} else if !errors.Is(err, unix.EWOULDBLOCK) {
		return false, false, err
	}
	receipt, err := waitReceipt(ctx, files, o.receiptWait)
	if err != nil {
		return false, false, err
	}
	if receipt.EnvironmentID != environmentID || receipt.LaunchID != launchID || receipt.HostBootID != files.intentValue.HostBootID ||
		receipt.RunnerDigest != files.intentValue.RunnerDigest || receipt.RunnerDevice != files.intentValue.RunnerDevice || receipt.RunnerInode != files.intentValue.RunnerInode ||
		receipt.LauncherDigest != files.intentValue.LauncherDigest || receipt.LauncherDevice != files.intentValue.LauncherDevice || receipt.LauncherInode != files.intentValue.LauncherInode {
		return false, false, fmt.Errorf("%w: launch receipt does not match intent", ErrLaunchOwnershipUncertain)
	}
	if files.intentValue.HostBootID != o.bootID {
		return false, false, fmt.Errorf("%w: old-boot launch lock remains busy", ErrLaunchOwnershipUncertain)
	}
	if err := waitOwnedProcess(ctx, receipt, files.lock, o.receiptWait); err != nil {
		return false, false, fmt.Errorf("wait for owned runner exec: %w", err)
	}
	pidfd, err := unix.PidfdOpen(receipt.PID, 0)
	if err != nil {
		return false, false, fmt.Errorf("%w: open exact runner pidfd: %v", ErrLaunchOwnershipUncertain, err)
	}
	defer func() { _ = unix.Close(pidfd) }()
	if err := validateOwnedProcess(receipt, files.lock); err != nil {
		return false, false, fmt.Errorf("revalidate owned runner after pidfd open: %w", err)
	}
	if err := signalAndWaitPidfd(ctx, pidfd, o.termTimeout, o.killTimeout); err != nil {
		return false, false, err
	}
	var status unix.WaitStatus
	_, _ = unix.Wait4(receipt.PID, &status, unix.WNOHANG, nil)
	if err := acquireLockBounded(ctx, files.lock, o.killTimeout); err != nil {
		return false, false, fmt.Errorf("%w: runner exited without releasing ownership lock: %v", ErrLaunchOwnershipUncertain, err)
	}
	_ = unix.Flock(int(files.lock.Fd()), unix.LOCK_UN)
	return false, true, nil
}

func waitOwnedProcess(ctx context.Context, receipt LaunchReceipt, lock *os.File, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		err := validateOwnedProcess(receipt, lock)
		if err == nil {
			return nil
		}
		// Exec can transiently hide /proc/<pid>/fd and /proc/<pid>/exe. Retry only
		// while the receipt PID/start identity is unchanged and its exact lock remains held.
		if err := validateOwnedStartAndReceiptLock(receipt, lock); err != nil {
			return err
		}
		digest, dev, ino, executableErr := processExecutableIdentity(receipt.PID)
		if executableErr == nil &&
			(digest != receipt.LauncherDigest || dev != receipt.LauncherDevice || ino != receipt.LauncherInode) &&
			(digest != receipt.RunnerDigest || dev != receipt.RunnerDevice || ino != receipt.RunnerInode) {
			return err
		}
		lockErr := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if lockErr == nil {
			_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
			return err
		}
		if !errors.Is(lockErr, unix.EWOULDBLOCK) {
			return lockErr
		}
		if context.Cause(ctx) != nil {
			return context.Cause(ctx)
		}
		if time.Now().After(deadline) {
			return ErrLaunchReceiptPending
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func validateOwnedProcess(receipt LaunchReceipt, lock *os.File) error {
	if err := validateOwnedStartAndLock(receipt, lock); err != nil {
		return err
	}
	digest, dev, ino, err := processExecutableIdentity(receipt.PID)
	if err != nil || digest != receipt.RunnerDigest || dev != receipt.RunnerDevice || ino != receipt.RunnerInode {
		return fmt.Errorf("%w: runner executable identity mismatch", ErrLaunchOwnershipUncertain)
	}
	return nil
}

func validateOwnedStartAndLock(receipt LaunchReceipt, lock *os.File) error {
	if err := validateOwnedStartAndReceiptLock(receipt, lock); err != nil {
		return err
	}
	lockStat, err := fileStat(lock)
	if err != nil {
		return fmt.Errorf("%w: lock inode identity mismatch", ErrLaunchOwnershipUncertain)
	}
	childLock := filepath.Join("/proc", strconv.Itoa(receipt.PID), "fd", strconv.Itoa(childLockFD))
	var childStat unix.Stat_t
	if err := unix.Stat(childLock, &childStat); err != nil || childStat.Dev != lockStat.Dev || childStat.Ino != lockStat.Ino {
		return fmt.Errorf("%w: runner does not retain the ownership lock descriptor", ErrLaunchOwnershipUncertain)
	}
	return nil
}

func validateOwnedStartAndReceiptLock(receipt LaunchReceipt, lock *os.File) error {
	start, err := rawProcessStartTime(receipt.PID)
	if err != nil || start != receipt.StartTime {
		return fmt.Errorf("%w: runner start identity mismatch", ErrLaunchOwnershipUncertain)
	}
	lockStat, err := fileStat(lock)
	if err != nil || lockStat.Dev != receipt.LockDevice || lockStat.Ino != receipt.LockInode {
		return fmt.Errorf("%w: lock inode identity mismatch", ErrLaunchOwnershipUncertain)
	}
	return nil
}

func signalAndWaitPidfd(ctx context.Context, pidfd int, termTimeout, killTimeout time.Duration) error {
	if err := unix.PidfdSendSignal(pidfd, unix.SIGTERM, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
		return err
	}
	exited, err := waitPidfd(ctx, pidfd, termTimeout)
	if err != nil {
		return err
	}
	if exited {
		return nil
	}
	if err := unix.PidfdSendSignal(pidfd, unix.SIGKILL, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
		return err
	}
	exited, err = waitPidfd(ctx, pidfd, killTimeout)
	if err != nil {
		return err
	}
	if !exited {
		return fmt.Errorf("%w: exact runner did not exit after SIGKILL", ErrLaunchOwnershipUncertain)
	}
	return nil
}

func waitPidfd(ctx context.Context, pidfd int, timeout time.Duration) (bool, error) {
	if pidfd < 0 || int64(pidfd) > int64(^uint32(0)>>1) {
		return false, errors.New("pidfd is outside poll range")
	}
	pollFD := int32(pidfd) // #nosec G115 -- range checked above.
	deadline := time.Now().Add(timeout)
	for {
		if err := context.Cause(ctx); err != nil {
			return false, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false, nil
		}
		ms := int(remaining / time.Millisecond)
		if ms < 1 {
			ms = 1
		}
		fds := []unix.PollFd{{Fd: pollFD, Events: unix.POLLIN}}
		n, err := unix.Poll(fds, ms)
		if err != nil && !errors.Is(err, unix.EINTR) {
			return false, err
		}
		if n > 0 && fds[0].Revents&unix.POLLIN != 0 {
			return true, nil
		}
	}
}

func acquireLockBounded(ctx context.Context, file *os.File, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
			return nil
		} else if !errors.Is(err, unix.EWOULDBLOCK) {
			return err
		}
		if err := context.Cause(ctx); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return context.DeadlineExceeded
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitReceipt(ctx context.Context, files *attemptFiles, timeout time.Duration) (LaunchReceipt, error) {
	if files == nil || files.receipt == nil || files.lock == nil {
		return LaunchReceipt{}, fmt.Errorf("%w: launch attempt descriptors are incomplete", ErrLaunchOwnershipUncertain)
	}
	deadline := time.Now().Add(timeout)
	for {
		data, err := io.ReadAll(io.NewSectionReader(files.receipt, 0, (1<<20)+1))
		if err != nil {
			return LaunchReceipt{}, err
		}
		if len(data) > 1<<20 {
			return LaunchReceipt{}, fmt.Errorf("%w: launch receipt exceeds limit", ErrLaunchOwnershipUncertain)
		}
		if len(data) > 0 {
			var receipt LaunchReceipt
			if json.Unmarshal(data, &receipt) == nil {
				if err := validateReceiptForAttempt(receipt, files); err != nil {
					return LaunchReceipt{}, err
				}
				return receipt, nil
			}
		}
		if err := context.Cause(ctx); err != nil {
			return LaunchReceipt{}, err
		}
		if time.Now().After(deadline) {
			return LaunchReceipt{}, ErrLaunchReceiptPending
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func validateReceiptForAttempt(receipt LaunchReceipt, files *attemptFiles) error {
	intent := files.intentValue
	lockStat, err := fileStat(files.lock)
	if err != nil || receipt.Version != launchRecordVersion || receipt.EnvironmentID != intent.EnvironmentID || receipt.LaunchID != intent.LaunchID ||
		receipt.HostBootID != intent.HostBootID || receipt.PID <= 0 || receipt.StartTime == "" ||
		receipt.RunnerDigest != intent.RunnerDigest || receipt.RunnerDevice != intent.RunnerDevice || receipt.RunnerInode != intent.RunnerInode ||
		receipt.LauncherDigest != intent.LauncherDigest || receipt.LauncherDevice != intent.LauncherDevice || receipt.LauncherInode != intent.LauncherInode ||
		lockStat == nil || receipt.LockDevice != lockStat.Dev || receipt.LockInode != lockStat.Ino {
		return fmt.Errorf("%w: launch receipt does not match intent", ErrLaunchOwnershipUncertain)
	}
	return nil
}

func (o *LaunchOwnership) openAttempt(environmentID, launchID string) (*attemptFiles, error) {
	root, err := os.OpenFile(o.root, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	envFD, err := unix.Openat(int(root.Fd()), environmentDirectory(environmentID), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(envFD) }()
	dirFD, err := unix.Openat(envFD, launchID, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	dir := os.NewFile(uintptr(dirFD), "attempt-dir")
	if err := validatePrivateOpenDirectory(dir); err != nil {
		_ = dir.Close()
		return nil, err
	}
	files := &attemptFiles{dir: dir}
	fail := func(e error) (*attemptFiles, error) { files.close(); return nil, e }
	if files.lock, err = openatExistingFile(dirFD, "owner.lock", unix.O_RDWR); err != nil {
		return fail(err)
	}
	if files.receipt, err = openatExistingFile(dirFD, "receipt.json", unix.O_RDONLY); err != nil {
		return fail(err)
	}
	if files.intent, err = openatExistingFile(dirFD, "intent.json", unix.O_RDONLY); err != nil {
		return fail(err)
	}
	if err := validateAttemptFileLinks(files); err != nil {
		return fail(err)
	}
	data, err := io.ReadAll(io.LimitReader(files.intent, (1<<20)+1))
	if err != nil || len(data) > 1<<20 || json.Unmarshal(data, &files.intentValue) != nil || validateLaunchIntent(files.intentValue) != nil || files.intentValue.EnvironmentID != environmentID || files.intentValue.LaunchID != launchID {
		return fail(fmt.Errorf("%w: invalid launch intent", ErrLaunchOwnershipUncertain))
	}
	return files, nil
}

func validatePrivateOpenDirectory(dir *os.File) error {
	info, err := dir.Stat()
	uid, uidErr := currentUID()
	stat, ok := infoSysStat(info)
	if err != nil || uidErr != nil || !ok || !info.IsDir() || info.Mode().Perm()&0o077 != 0 || stat.Uid != uid {
		return errors.New("launch attempt directory is unsafe")
	}
	return nil
}

func infoSysStat(info os.FileInfo) (*syscall.Stat_t, bool) {
	if info == nil {
		return nil, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return stat, ok
}

func validateAttemptFileLinks(files *attemptFiles) error {
	uid, err := currentUID()
	if err != nil {
		return err
	}
	for name, file := range map[string]*os.File{"owner.lock": files.lock, "receipt.json": files.receipt, "intent.json": files.intent} {
		actual, err := fileStat(file)
		var linked unix.Stat_t
		linkErr := unix.Fstatat(int(files.dir.Fd()), name, &linked, unix.AT_SYMLINK_NOFOLLOW)
		if err != nil || linkErr != nil || linked.Mode&unix.S_IFMT != unix.S_IFREG || linked.Dev != actual.Dev || linked.Ino != actual.Ino || linked.Uid != uid || linked.Nlink != 1 {
			return fmt.Errorf("launch %s inode identity mismatch", name)
		}
	}
	return nil
}

type ownedRunnerProcess struct {
	owner                              *LaunchOwnership
	environmentID, launchID, startTime string
	runnerDigest                       string
	runnerDevice, runnerInode          uint64
	pid                                int
}

func (p *ownedRunnerProcess) PID() int { return p.pid }
func (p *ownedRunnerProcess) IsAlive() bool {
	if p.pid == 0 || p.startTime == "" {
		return false
	}
	start, err := rawProcessStartTime(p.pid)
	return err == nil && start == p.startTime
}
func (p *ownedRunnerProcess) Stop(ctx context.Context) error {
	files, err := p.owner.openAttempt(p.environmentID, p.launchID)
	if err != nil {
		return err
	}
	defer files.close()
	receipt, err := waitReceipt(ctx, files, p.owner.receiptWait)
	if err != nil {
		return err
	}
	if err := waitOwnedProcess(ctx, receipt, files.lock, p.owner.receiptWait); err != nil {
		if !p.IsAlive() {
			return nil
		}
		return err
	}
	pidfd, err := unix.PidfdOpen(receipt.PID, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(pidfd) }()
	if err := validateOwnedProcess(receipt, files.lock); err != nil {
		return err
	}
	return signalAndWaitPidfd(ctx, pidfd, p.owner.termTimeout, p.owner.killTimeout)
}

func environmentDirectory(environmentID string) string {
	sum := sha256.Sum256([]byte(environmentID))
	return hex.EncodeToString(sum[:])
}

func randomLaunchID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func readHostBootID() (string, error) {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", fmt.Errorf("read host boot identity: %w", err)
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", errors.New("host boot identity is empty")
	}
	return value, nil
}

func processExecutableIdentity(pid int) (string, uint64, uint64, error) {
	path := filepath.Join("/proc", strconv.Itoa(pid), "exe")
	file, err := os.Open(path) // #nosec G304,G703 -- PID is an integer formatted as one decimal procfs path component.
	if err != nil {
		return "", 0, 0, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", 0, 0, errors.New("process executable identity is not regular")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", 0, 0, err
	}
	stat, err := rawFileStat(file)
	if err != nil {
		return "", 0, 0, err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), stat.Dev, stat.Ino, nil
}

func executableIdentity(path string) (string, string, uint64, uint64, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", "", 0, 0, err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", "", 0, 0, err
	}
	file, err := os.OpenFile(resolved, os.O_RDONLY|syscall.O_NOFOLLOW, 0) // #nosec G304 -- caller supplies already admitted executable.
	if err != nil {
		return "", "", 0, 0, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", "", 0, 0, errors.New("executable identity is not a regular executable")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", "", 0, 0, err
	}
	stat, err := rawFileStat(file)
	if err != nil {
		return "", "", 0, 0, err
	}
	return resolved, "sha256:" + hex.EncodeToString(hash.Sum(nil)), stat.Dev, stat.Ino, nil
}

func rawProcessStartTime(pid int) (string, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat")) // #nosec G703 -- PID is an integer formatted as one decimal procfs path component.
	if err != nil {
		return "", err
	}
	closeParen := strings.LastIndexByte(string(data), ')')
	if closeParen < 0 {
		return "", errors.New("malformed process stat")
	}
	fields := strings.Fields(string(data[closeParen+1:]))
	if len(fields) <= 19 {
		return "", errors.New("process stat omits start time")
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return "", err
	}
	return fields[19], nil
}

func validatePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return errors.New("launch ownership root must be a private real directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	uid, uidErr := currentUID()
	if !ok || uidErr != nil || stat.Uid != uid {
		return errors.New("launch ownership root has the wrong owner")
	}
	return nil
}

func mkdiratExisting(parent uintptr, name string) error {
	if err := unix.Mkdirat(int(parent), name, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
		return err
	}
	uid, err := currentUID()
	if err != nil {
		return err
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(int(parent), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uid || stat.Mode&0o077 != 0 {
		return errors.New("launch environment directory is unsafe")
	}
	return nil
}

func openatExclusive(dirFD int, name string, flags int, mode uint32) (*os.File, error) {
	fd, err := unix.Openat(dirFD, name, flags|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func rawFileStat(file *os.File) (*syscall.Stat_t, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, errors.New("file stat identity is unavailable")
	}
	return stat, nil
}

func fileStat(file *os.File) (*syscall.Stat_t, error) {
	stat, err := rawFileStat(file)
	if err != nil {
		return nil, err
	}
	uid, err := currentUID()
	if err != nil {
		return nil, err
	}
	if stat.Uid != uid {
		return nil, errors.New("file has the wrong owner")
	}
	return stat, nil
}

func currentUID() (uint32, error) {
	uid := os.Getuid()
	if uid < 0 || uint64(uid) > uint64(^uint32(0)) {
		return 0, errors.New("process UID is outside the supported range")
	}
	return uint32(uid), nil // #nosec G115 -- range checked above.
}

func applyOwnedUserNamespace(cmd *exec.Cmd, ns *runner.UserNamespaceConfig) {
	if ns == nil {
		return
	}
	cmd.SysProcAttr.Cloneflags |= syscall.CLONE_NEWUSER
	cmd.SysProcAttr.UidMappings = []syscall.SysProcIDMap{{ContainerID: int(ns.UID), HostID: syscall.Getuid(), Size: 1}}
	cmd.SysProcAttr.GidMappings = []syscall.SysProcIDMap{{ContainerID: int(ns.GID), HostID: syscall.Getgid(), Size: 1}}
}

func runnerLibraryPathVariable() string { return "LD_LIBRARY_PATH" }

func scrubRunnerEnvironment(base []string) []string {
	out := make([]string, 0, len(base))
	for _, value := range base {
		name, _, ok := strings.Cut(value, "=")
		if !ok || isSecretEnvironmentName(name) {
			continue
		}
		out = append(out, value)
	}
	return out
}

func isSecretEnvironmentName(name string) bool {
	if strings.HasPrefix(name, "MECATL_") || strings.HasPrefix(name, "AWS_") || strings.HasPrefix(name, "AZURE_") || name == "GOOGLE_APPLICATION_CREDENTIALS" || name == "GH_TOKEN" || name == "GITHUB_TOKEN" {
		return true
	}
	for _, suffix := range []string{"_API_KEY", "_TOKEN", "_SECRET", "_PASSWORD", "_PASSWD"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}
