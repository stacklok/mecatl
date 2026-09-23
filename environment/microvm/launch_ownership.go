package microvm

import (
	"context"
	"errors"
	"time"

	"github.com/stacklok/go-microvm/runner"
)

var (
	// ErrLaunchOwnershipUnsupported reports that durable runner ownership is not available on this platform.
	ErrLaunchOwnershipUnsupported = errors.New("durable microvm launch ownership is unsupported on this platform")
	// ErrLaunchOwnershipUncertain means a process may still own the retained VM state and was not signalled.
	ErrLaunchOwnershipUncertain = errors.New("microvm launch ownership is uncertain")
	// ErrLaunchReceiptPending means the launcher owns the attempt lock but has not durably published its receipt yet.
	ErrLaunchReceiptPending = errors.New("microvm launch receipt is pending")
)

// LaunchOwnershipConfig configures the concrete host runner launch owner.
type LaunchOwnershipConfig struct {
	Root         string
	LauncherPath string
	ReceiptWait  time.Duration
	TermTimeout  time.Duration
	KillTimeout  time.Duration
}

// LaunchReceipt is the durable identity written by the launcher before it execs the VM runner.
type LaunchReceipt struct {
	Version        int    `json:"version"`
	EnvironmentID  string `json:"environment_id"`
	LaunchID       string `json:"launch_id"`
	HostBootID     string `json:"host_boot_id"`
	PID            int    `json:"pid"`
	StartTime      string `json:"start_time"`
	RunnerDigest   string `json:"runner_digest"`
	RunnerDevice   uint64 `json:"runner_device"`
	RunnerInode    uint64 `json:"runner_inode"`
	LauncherDigest string `json:"launcher_digest"`
	LauncherDevice uint64 `json:"launcher_device"`
	LauncherInode  uint64 `json:"launcher_inode"`
	LockDevice     uint64 `json:"lock_device"`
	LockInode      uint64 `json:"lock_inode"`
}

// LaunchReconcileResult describes attempts proven dead or terminated through exact pidfds.
type LaunchReconcileResult struct {
	Dead       int
	Terminated int
}

// LaunchOwnership is the concrete host-only launch owner used by microvmd.
type LaunchOwnership struct {
	root, launcherPath, launcherDigest, bootID string
	launcherDevice, launcherInode              uint64
	receiptWait, termTimeout, killTimeout      time.Duration
	checkpoint                                 func(string) error
}

// Spawner returns the go-microvm spawner bound to one repository environment.
func (o *LaunchOwnership) Spawner(environmentID string) runner.Spawner {
	return &ownedRunnerSpawner{owner: o, environmentID: environmentID}
}

type ownedRunnerSpawner struct {
	owner         *LaunchOwnership
	environmentID string
}

func (s *ownedRunnerSpawner) Spawn(ctx context.Context, cfg runner.Config) (runner.ProcessHandle, error) {
	return s.owner.spawn(ctx, s.environmentID, cfg)
}
