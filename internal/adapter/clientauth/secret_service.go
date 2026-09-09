//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package clientauth

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"time"
)

type secretServiceState uint8

const (
	secretServicePresent secretServiceState = iota + 1
	secretServiceAbsent
	secretServiceHelperArg = "--internal-clientauth-detect-secret-service"
)

var errSecretServiceDetection = errors.New("clientauth: Secret Service detection failed; choose --credential-store explicitly")

func detectLinuxSecretService(ctx context.Context) (secretServiceState, error) {
	return runSecretServiceDetector(ctx, 500*time.Millisecond, func(ctx context.Context, executable string) *exec.Cmd {
		return exec.CommandContext(ctx, executable, secretServiceHelperArg)
	})
}

func runSecretServiceDetector(parent context.Context, budget time.Duration, command func(context.Context, string) *exec.Cmd) (secretServiceState, error) {
	ctx, cancel := context.WithTimeout(parent, budget)
	defer cancel()
	if err := parent.Err(); err != nil {
		return 0, err
	}
	executable, err := os.Executable()
	if err != nil {
		return 0, errSecretServiceDetection
	}
	cmd := command(ctx, executable)
	// Nil streams connect to the null device; never capture bus/provider output.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	var killed atomic.Bool
	cmd.Cancel = func() error {
		err := cmd.Process.Kill()
		if err == nil {
			killed.Store(true)
		}
		return err
	}
	if err := cmd.Start(); err != nil {
		if parent.Err() != nil {
			return 0, parent.Err()
		}
		return 0, errSecretServiceDetection
	}
	err = cmd.Wait()
	if parent.Err() != nil {
		return 0, parent.Err()
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return 0, errSecretServiceDetection
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	if !ok {
		return 0, errSecretServiceDetection
	}
	if status.Signaled() {
		// Accepted residual: an independent SIGKILL racing this successful deadline
		// kill is indistinguishable. Do not extend this exception to other failures.
		if ctx.Err() == context.DeadlineExceeded && killed.Load() && status.Signal() == syscall.SIGKILL {
			return secretServiceAbsent, nil
		}
		return 0, errSecretServiceDetection
	}
	switch exit.ExitCode() {
	case 80:
		return secretServicePresent, nil
	case 81:
		return secretServiceAbsent, nil
	default:
		return 0, errSecretServiceDetection
	}
}
