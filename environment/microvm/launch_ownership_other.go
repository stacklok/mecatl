//go:build !linux && !darwin

package microvm

import (
	"context"

	"github.com/stacklok/go-microvm/runner"
)

// NewLaunchOwnership reports the Linux-only ownership requirement honestly.
func NewLaunchOwnership(LaunchOwnershipConfig) (*LaunchOwnership, error) {
	return nil, ErrLaunchOwnershipUnsupported
}

func (*LaunchOwnership) spawn(context.Context, string, runner.Config) (runner.ProcessHandle, error) {
	return nil, ErrLaunchOwnershipUnsupported
}

// RunLaunchOwnerChild is never handled on unsupported hosts.
func RunLaunchOwnerChild([]string) (bool, error) { return false, nil }

// Reconcile is unsupported because non-Linux hosts have no pidfd ownership proof.
func (*LaunchOwnership) Reconcile(context.Context, string) (LaunchReconcileResult, error) {
	return LaunchReconcileResult{}, ErrLaunchOwnershipUnsupported
}
