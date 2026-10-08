package app

import (
	"context"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/server"
)

const referenceIntentReconcileInterval = time.Minute

func startReferenceIntentReconcile(ctx context.Context, lifecycle server.ReferenceIntentLifecycle, svc *server.Service) func() {
	if lifecycle == nil || svc == nil {
		return func() {}
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.ReconcileReferenceIntents(workerCtx)
		ticker := time.NewTicker(referenceIntentReconcileInterval)
		defer ticker.Stop()
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
				svc.ReconcileReferenceIntents(workerCtx)
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}
