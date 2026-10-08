//go:build !linux && !darwin

package microvmmanager

import (
	"context"
	"errors"
	"time"
)

func managedStopContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, 10*time.Second)
}

func requestManagedStop(context.Context, Paths) (bool, error) {
	return false, errors.New("managed daemon stop is unsupported on this platform")
}
