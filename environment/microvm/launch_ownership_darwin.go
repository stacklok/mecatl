//go:build darwin

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
	"strings"
	"sync"
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
	childControlFD
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
	TermTimeoutNS  int64           `json:"term_timeout_ns"`
	KillTimeoutNS  int64           `json:"kill_timeout_ns"`
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

func (o *LaunchOwnership) spawn(ctx context.Context, environmentID string, cfg runner.Config) (runner.ProcessHandle, error) {
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
	intent := launchIntent{Version: launchRecordVersion, EnvironmentID: environmentID, LaunchID: launchID, HostBootID: o.bootID,
		RunnerPath: runnerPath, RunnerDigest: runnerDigest, RunnerDevice: runnerDev, RunnerInode: runnerIno,
		LauncherPath: o.launcherPath, LauncherDigest: o.launcherDigest, LauncherDevice: o.launcherDevice, LauncherInode: o.launcherInode, Config: configJSON,
		TermTimeoutNS: int64(o.termTimeout), KillTimeoutNS: int64(o.killTimeout)}
	files, err := o.createAttempt(intent)
	if err != nil {
		return nil, err
	}
	defer files.close()
	controlReader, controlWriter, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create launch-owner liveness pipe: %w", err)
	}
	defer func() { _ = controlReader.Close() }()
	cmd := exec.Command(o.launcherPath, internalLaunchOwnerArg) // #nosec G204 -- exact pinned executable, fixed internal argument.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.ExtraFiles = []*os.File{files.dir, files.lock, files.receipt, files.intent, controlReader}
	cmd.Env = scrubRunnerEnvironment(os.Environ())
	if cfg.LibDir != "" {
		cmd.Env = append(cmd.Env, runnerLibraryPathVariable()+"="+cfg.LibDir)
	}
	if cfg.VMLogPath != "" {
		logFile, openErr := os.OpenFile(cfg.VMLogPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600) // #nosec G304 -- resolved library-owned VM log path.
		if openErr != nil {
			_ = controlWriter.Close()
			return nil, fmt.Errorf("open vm log: %w", openErr)
		}
		defer func() { _ = logFile.Close() }()
		cmd.Stdout, cmd.Stderr = logFile, logFile
	}
	if err := cmd.Start(); err != nil {
		_ = controlWriter.Close()
		return nil, fmt.Errorf("start owned runner supervisor: %w", err)
	}
	_ = controlReader.Close()
	process := &ownedRunnerProcess{owner: o, environmentID: environmentID, launchID: launchID, supervisor: cmd.Process, control: controlWriter, done: make(chan error, 1)}
	go func() { process.done <- cmd.Wait(); close(process.done) }()
	receipt, err := waitReceipt(ctx, files, o.receiptWait)
	if err != nil {
		_ = process.Stop(context.Background())
		return nil, err
	}
	process.pid, process.startTime = receipt.PID, receipt.StartTime
	return process, nil
}

func (o *LaunchOwnership) createAttempt(intent launchIntent) (*attemptFiles, error) { //nolint:gocyclo // linear publication ordering is intentionally explicit.
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
	if err := mkdiratExisting(root.Fd(), ".staging"); err != nil {
		return nil, err
	}
	stagingFD, err := unix.Openat(int(root.Fd()), ".staging", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(stagingFD) }()
	if err := unix.Mkdirat(stagingFD, intent.LaunchID, 0o700); err != nil {
		return nil, err
	}
	dirFD, err := unix.Openat(stagingFD, intent.LaunchID, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	files := &attemptFiles{dir: os.NewFile(uintptr(dirFD), "attempt-dir"), intentValue: intent}
	fail := func(e error) (*attemptFiles, error) { files.close(); return nil, e }
	if files.lock, err = openatExclusive(dirFD, "owner.lock", unix.O_RDWR, 0o600); err != nil {
		return fail(err)
	}
	if err := unix.Flock(int(files.lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fail(err)
	}
	if files.receipt, err = openatExclusive(dirFD, "receipt.json", unix.O_RDWR, 0o600); err != nil {
		return fail(err)
	}
	if files.intent, err = openatExclusive(dirFD, "intent.json", unix.O_RDWR, 0o600); err != nil {
		return fail(err)
	}
	payload, err := json.Marshal(intent)
	if err != nil {
		return fail(err)
	}
	if _, err = files.intent.Write(append(payload, '\n')); err != nil {
		return fail(err)
	}
	for _, syncFile := range []func() error{files.intent.Sync, files.lock.Sync, files.receipt.Sync, files.dir.Sync} {
		if err = syncFile(); err != nil {
			return fail(err)
		}
	}
	if err = unix.Fsync(stagingFD); err != nil {
		return fail(err)
	}
	if err = renameatNoReplace(stagingFD, intent.LaunchID, envFD, intent.LaunchID); err != nil {
		return fail(err)
	}
	if err = unix.Fsync(stagingFD); err != nil {
		return fail(err)
	}
	if err = unix.Fsync(envFD); err != nil {
		return fail(err)
	}
	if err = root.Sync(); err != nil {
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

func runLaunchOwnerChild() (retErr error) { //nolint:gocyclo // validation and child lifecycle ordering are security-sensitive.
	dir := os.NewFile(childDirFD, "attempt-dir")
	lock := os.NewFile(childLockFD, "owner-lock")
	receipt := os.NewFile(childReceiptFD, "receipt")
	intentFile := os.NewFile(childIntentFD, "intent")
	control := os.NewFile(childControlFD, "daemon-liveness")
	defer func() { _ = control.Close() }()
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
	termTimeout, killTimeout := time.Duration(intent.TermTimeoutNS), time.Duration(intent.KillTimeoutNS)
	if termTimeout <= 0 {
		termTimeout = defaultTermTimeout
	}
	if killTimeout <= 0 {
		killTimeout = defaultKillTimeout
	}
	bootID, err := readHostBootID()
	if err != nil || bootID != intent.HostBootID {
		return errors.New("launcher host boot identity mismatch")
	}
	launcherPath, launcherDigest, launcherDev, launcherIno, err := executableIdentity(os.Args[0])
	if err != nil || launcherPath != intent.LauncherPath || launcherDigest != intent.LauncherDigest || launcherDev != intent.LauncherDevice || launcherIno != intent.LauncherInode {
		return errors.New("launcher executable identity mismatch")
	}
	runnerPath, runnerDigest, runnerDev, runnerIno, err := executableIdentity(intent.RunnerPath)
	if err != nil || runnerPath != intent.RunnerPath || runnerDigest != intent.RunnerDigest || runnerDev != intent.RunnerDevice || runnerIno != intent.RunnerInode {
		return errors.New("runner executable identity mismatch")
	}
	cmd := exec.Command(intent.RunnerPath, string(intent.Config)) // #nosec G204 -- digest- and inode-pinned runner from validated intent.
	cmd.ExtraFiles = []*os.File{lock}
	cmd.Env = os.Environ()
	if !directChildUsesWaitCompatibleIO(cmd) {
		return errors.New("runner command must use direct file descriptors")
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_, stopErr := ownDirectChild(cmd, nil, termTimeout, killTimeout, true)
			retErr = errors.Join(retErr, stopErr)
		}
	}()
	start, err := platformProcessStartIdentity(context.Background(), cmd.Process.Pid)
	if err != nil {
		return err
	}
	lockStat, err := fileStat(lock)
	if err != nil {
		return err
	}
	value := LaunchReceipt{Version: launchRecordVersion, EnvironmentID: intent.EnvironmentID, LaunchID: intent.LaunchID, HostBootID: intent.HostBootID,
		PID: cmd.Process.Pid, StartTime: start, RunnerDigest: intent.RunnerDigest, RunnerDevice: intent.RunnerDevice, RunnerInode: intent.RunnerInode,
		LauncherDigest: intent.LauncherDigest, LauncherDevice: intent.LauncherDevice, LauncherInode: intent.LauncherInode, LockDevice: uint64(uint32(lockStat.Dev)), LockInode: lockStat.Ino} // #nosec G115 -- Darwin dev_t is represented as a non-negative int32.
	payload, err := json.Marshal(value)
	if err != nil {
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
	if err := dir.Sync(); err != nil {
		return err
	}
	_ = dir.Close()
	_ = receipt.Close()
	_ = intentFile.Close()
	_ = lock.Close()
	eof := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, control); close(eof) }()
	cleanup = false
	status, err := ownDirectChild(cmd, eof, termTimeout, killTimeout, false)
	if err != nil {
		return err
	}
	if status.Exited() && status.ExitStatus() == 0 {
		return nil
	}
	return fmt.Errorf("owned runner exited: %s", formatDirectChildStatus(status))
}

func formatDirectChildStatus(status syscall.WaitStatus) string {
	if status.Signaled() {
		return fmt.Sprintf("signal %s", status.Signal())
	}
	return fmt.Sprintf("exit status %d", status.ExitStatus())
}

const directChildPollInterval = 10 * time.Millisecond

func directChildUsesWaitCompatibleIO(cmd *exec.Cmd) bool {
	for _, stream := range []any{cmd.Stdin, cmd.Stdout, cmd.Stderr} {
		if stream != nil {
			if _, ok := stream.(*os.File); !ok {
				return false
			}
		}
	}
	return true
}

// ownDirectChild is the sole owner of both Wait4 and signals for cmd. A natural
// exit remains a zombie until this loop reaps it, so its PID cannot be reused
// between the exit check and a TERM or KILL.
func ownDirectChild(cmd *exec.Cmd, controlEOF <-chan struct{}, termTimeout, killTimeout time.Duration, stopping bool) (syscall.WaitStatus, error) {
	pid := cmd.Process.Pid
	poll := time.NewTicker(directChildPollInterval)
	defer poll.Stop()

	var deadline <-chan time.Time
	var timer *time.Timer
	phase := 0
	if stopping {
		phase = 1
	}
	setDeadline := func(timeout time.Duration) {
		if timer != nil {
			timer.Stop()
		}
		timer = time.NewTimer(timeout)
		deadline = timer.C
	}
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	for {
		var status syscall.WaitStatus
		waited, err := syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
		if err != nil && !errors.Is(err, syscall.EINTR) {
			_ = cmd.Process.Release()
			return 0, fmt.Errorf("%w: wait for direct runner: %w", ErrLaunchOwnershipUncertain, err)
		}
		if waited == pid {
			_ = cmd.Process.Release()
			return status, nil
		}

		if phase == 1 && deadline == nil {
			if err := unix.Kill(pid, unix.SIGTERM); err != nil && !errors.Is(err, unix.ESRCH) {
				_ = cmd.Process.Release()
				return 0, fmt.Errorf("signal direct runner with SIGTERM: %w", err)
			}
			setDeadline(termTimeout)
		}

		select {
		case <-controlEOF:
			if phase == 0 {
				phase = 1
				controlEOF = nil
			}
		case <-deadline:
			deadline = nil
			if phase == 1 {
				if err := unix.Kill(pid, unix.SIGKILL); err != nil && !errors.Is(err, unix.ESRCH) {
					_ = cmd.Process.Release()
					return 0, fmt.Errorf("signal direct runner with SIGKILL: %w", err)
				}
				phase = 2
				setDeadline(killTimeout)
				continue
			}
			_ = cmd.Process.Release()
			return 0, fmt.Errorf("%w: direct runner did not exit after SIGKILL", ErrLaunchOwnershipUncertain)
		case <-poll.C:
		}
	}
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
		actual, statErr := fileStat(file)
		if statErr != nil || linked.Dev != actual.Dev || linked.Ino != actual.Ino || linked.Uid != uid || linked.Nlink != 1 {
			return fmt.Errorf("launcher %s inode identity mismatch", name)
		}
	}
	return nil
}

func validateLaunchIntent(value launchIntent) error {
	if value.Version != launchRecordVersion || value.EnvironmentID == "" || len(value.LaunchID) != 32 || value.HostBootID == "" || !filepath.IsAbs(value.RunnerPath) || !filepath.IsAbs(value.LauncherPath) || len(value.Config) == 0 {
		return errors.New("launcher intent is incomplete")
	}
	if _, err := hex.DecodeString(value.LaunchID); err != nil {
		return errors.New("launcher launch ID is invalid")
	}
	return nil
}

// Reconcile waits for historical supervisors to release their inherited lock. Darwin never signals a stored PID.
func (o *LaunchOwnership) Reconcile(ctx context.Context, environmentID string) (LaunchReconcileResult, error) {
	var result LaunchReconcileResult
	entries, err := os.ReadDir(filepath.Join(o.root, environmentDirectory(environmentID)))
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
		files, openErr := o.openAttempt(environmentID, entry.Name())
		if openErr != nil {
			return result, openErr
		}
		lockErr := acquireLockBounded(ctx, files.lock, o.receiptWait)
		if lockErr == nil {
			_ = unix.Flock(int(files.lock.Fd()), unix.LOCK_UN)
			files.close()
			result.Dead++
			continue
		}
		if cause := context.Cause(ctx); cause != nil {
			files.close()
			return result, cause
		}
		if !errors.Is(lockErr, context.DeadlineExceeded) {
			files.close()
			return result, lockErr
		}
		_, receiptErr := waitReceipt(ctx, files, o.receiptWait)
		files.close()
		if receiptErr != nil {
			return result, receiptErr
		}
		return result, fmt.Errorf("%w: historical Darwin launch remains locked; operator recovery is required", ErrLaunchOwnershipUncertain)
	}
	return result, nil
}

func acquireLockBounded(ctx context.Context, file *os.File, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EINTR) {
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
	if err != nil || receipt.Version != launchRecordVersion || receipt.EnvironmentID != intent.EnvironmentID || receipt.LaunchID != intent.LaunchID || receipt.HostBootID != intent.HostBootID || receipt.PID <= 0 || receipt.StartTime == "" || receipt.RunnerDigest != intent.RunnerDigest || receipt.RunnerDevice != intent.RunnerDevice || receipt.RunnerInode != intent.RunnerInode || receipt.LauncherDigest != intent.LauncherDigest || receipt.LauncherDevice != intent.LauncherDevice || receipt.LauncherInode != intent.LauncherInode || lockStat == nil || receipt.LockDevice != uint64(uint32(lockStat.Dev)) || receipt.LockInode != lockStat.Ino { // #nosec G115 -- Darwin dev_t is represented as a non-negative int32.
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
	files := &attemptFiles{dir: os.NewFile(uintptr(dirFD), "attempt-dir")}
	fail := func(e error) (*attemptFiles, error) { files.close(); return nil, e }
	if err := validatePrivateOpenDirectory(files.dir); err != nil {
		return fail(err)
	}
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
		actual, statErr := fileStat(file)
		var linked unix.Stat_t
		linkErr := unix.Fstatat(int(files.dir.Fd()), name, &linked, unix.AT_SYMLINK_NOFOLLOW)
		if statErr != nil || linkErr != nil || linked.Mode&unix.S_IFMT != unix.S_IFREG || linked.Dev != actual.Dev || linked.Ino != actual.Ino || linked.Uid != uid || linked.Nlink != 1 {
			return fmt.Errorf("launch %s inode identity mismatch", name)
		}
	}
	return nil
}

type ownedRunnerProcess struct {
	owner                              *LaunchOwnership
	environmentID, launchID, startTime string
	pid                                int
	supervisor                         *os.Process
	control                            *os.File
	done                               chan error
	once                               sync.Once
}

func (p *ownedRunnerProcess) PID() int { return p.pid }
func (p *ownedRunnerProcess) IsAlive() bool {
	if p.pid <= 0 || p.startTime == "" {
		return false
	}
	start, err := platformProcessStartIdentity(context.Background(), p.pid)
	return err == nil && start == p.startTime
}
func (p *ownedRunnerProcess) Stop(ctx context.Context) error {
	p.once.Do(func() {
		if p.control != nil {
			_ = p.control.Close()
		}
	})
	select {
	case err := <-p.done:
		if err != nil {
			return fmt.Errorf("wait launch-owner supervisor: %w", err)
		}
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
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
	value, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		return "", fmt.Errorf("read host boot identity: %w", err)
	}
	return fmt.Sprintf("%d.%06d", value.Sec, value.Usec), nil
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
	file, err := os.Open(resolved)
	if err != nil {
		return "", "", 0, 0, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	stat, ok := infoSysStat(info)
	if err != nil || !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", "", 0, 0, errors.New("executable identity is not an executable regular file")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", "", 0, 0, err
	}
	return filepath.Clean(resolved), hex.EncodeToString(hash.Sum(nil)), uint64(uint32(stat.Dev)), stat.Ino, nil // #nosec G115 -- Darwin dev_t is represented as a non-negative int32.
}
func fileStat(file *os.File) (*syscall.Stat_t, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, errors.New("file stat identity unavailable")
	}
	return stat, nil
}
func currentUID() (uint32, error) {
	uid := os.Getuid()
	if uid < 0 || uint64(uid) > uint64(^uint32(0)) {
		return 0, errors.New("current UID is outside supported range")
	}
	return uint32(uid), nil
}
func validatePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	uid, uidErr := currentUID()
	stat, ok := infoSysStat(info)
	if err != nil || uidErr != nil || !ok || !info.IsDir() || info.Mode().Perm()&0o077 != 0 || stat.Uid != uid {
		return errors.New("launch ownership root must be a private owner-controlled directory")
	}
	return nil
}
func mkdiratExisting(parent uintptr, name string) error {
	err := unix.Mkdirat(int(parent), name, 0o700)
	if err != nil && !errors.Is(err, unix.EEXIST) {
		return err
	}
	fd, err := unix.Openat(int(parent), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	return validatePrivateOpenDirectory(os.NewFile(uintptr(fd), name))
}
func openatExclusive(dirFD int, name string, flags int, mode uint32) (*os.File, error) {
	fd, err := unix.Openat(dirFD, name, flags|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
func runnerLibraryPathVariable() string { return "DYLD_LIBRARY_PATH" }
func scrubRunnerEnvironment(base []string) []string {
	out := make([]string, 0, len(base))
	for _, entry := range base {
		name, _, ok := strings.Cut(entry, "=")
		if ok && !isSecretEnvironmentName(name) {
			out = append(out, entry)
		}
	}
	return out
}
func isSecretEnvironmentName(name string) bool {
	upper := strings.ToUpper(name)
	if strings.HasPrefix(upper, "MECATL_") || strings.HasPrefix(upper, "AWS_") || strings.HasPrefix(upper, "AZURE_") || upper == "GH_TOKEN" || upper == "GITHUB_TOKEN" || upper == "OPENROUTER_API_KEY" {
		return true
	}
	for _, suffix := range []string{"_API_KEY", "_TOKEN", "_SECRET", "_PASSWORD"} {
		if strings.HasSuffix(upper, suffix) {
			return true
		}
	}
	return false
}
