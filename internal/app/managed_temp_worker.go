package app

import (
	"context"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/managedtemp"
)

// startManagedTempWorker owns periodic recovery for the Build's private managed
// namespace. The worker is intentionally independent of command allocation: its
// root lock is non-blocking and every individual pass has its own deadline.
func startManagedTempWorker(parent context.Context, cfg Config) func() {
	if cfg.managedTemp == nil || cfg.temporaryStorage.Mode != temporaryStorageManaged {
		return func() {}
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		sweep := func() {
			passCtx, passCancel := context.WithTimeout(ctx, cfg.temporaryStorage.ReapTimeout)
			_, err := cfg.managedTemp.namespace.Sweep(passCtx, managedSweepOptions(cfg))
			passCancel()
			if err != nil && ctx.Err() == nil {
				cfg.diag().Log(context.Background(), port.LevelWarn, "managed temporary storage sweep failed", "err", err)
			}
		}
		sweep()
		ticker := time.NewTicker(cfg.temporaryStorage.ReapInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweep()
			}
		}
	}()
	return sync.OnceFunc(func() {
		cancel()
		timer := time.NewTimer(cfg.temporaryStorage.ShutdownReapTimeout)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			cfg.diag().Log(context.Background(), port.LevelWarn, "managed temporary storage worker shutdown timed out", "timeout", cfg.temporaryStorage.ShutdownReapTimeout)
		}
	})
}

func managedSweepOptions(cfg Config) managedtemp.SweepOptions {
	return managedtemp.SweepOptions{
		Now:              time.Now().UTC(),
		Interval:         cfg.temporaryStorage.ReapInterval,
		CommandReapAfter: cfg.temporaryStorage.CommandReapAfter,
	}
}
