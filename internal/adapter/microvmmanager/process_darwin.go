//go:build darwin

package microvmmanager

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"

	microvmclient "github.com/stacklok/mecatl/internal/adapter/microvm"
)

func managedStopContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 15*time.Second)
}

func requestManagedStop(ctx context.Context, paths Paths) (bool, error) {
	if err := validateOwnerSocket(paths.Socket); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("refusing to stop daemon without its owner-only socket: %w", err)
	}
	expected, err := expectedDaemonInfo(paths)
	if err != nil {
		return false, fmt.Errorf("compute expected microvmd identity: %w", err)
	}
	client, err := microvmclient.New("unix://" + paths.Socket)
	if err != nil {
		return false, err
	}
	serving, err := client.DaemonInfo(ctx)
	if err != nil {
		return false, fmt.Errorf("query serving microvmd identity: %w", err)
	}
	if !serving.Equal(expected) {
		return false, errors.New("refusing to stop microvmd whose serving identity does not match the installed daemon")
	}
	if err := client.ShutdownDaemon(ctx, expected); err != nil {
		return false, fmt.Errorf("request authenticated microvmd shutdown: %w", err)
	}
	return true, nil
}
