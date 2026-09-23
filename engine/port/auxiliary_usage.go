package port

import (
	"context"
	"sync"

	"github.com/stacklok/mecatl/engine/session"
)

// AuxiliaryUsageReporter is a synchronous, request-scoped auxiliary-usage callback.
// Callers must not retain it; an Engine deactivates the installed callback when the
// current hook request returns.
type AuxiliaryUsageReporter func(session.AuxiliaryUsage)

type auxiliaryUsageReporterKey struct{}

// WithAuxiliaryUsageReporter installs reporter for one synchronous request and
// returns a deactivate function that makes retained copies inert.
func WithAuxiliaryUsageReporter(ctx context.Context, reporter AuxiliaryUsageReporter) (context.Context, func()) {
	var mu sync.Mutex
	active := true
	guarded := AuxiliaryUsageReporter(func(usage session.AuxiliaryUsage) {
		mu.Lock()
		defer mu.Unlock()
		if active {
			reporter(usage)
		}
	})
	return context.WithValue(ctx, auxiliaryUsageReporterKey{}, guarded), func() {
		mu.Lock()
		active = false
		mu.Unlock()
	}
}

// AuxiliaryUsageReporterFromContext returns the current request's reporter, if any.
func AuxiliaryUsageReporterFromContext(ctx context.Context) AuxiliaryUsageReporter {
	reporter, _ := ctx.Value(auxiliaryUsageReporterKey{}).(AuxiliaryUsageReporter)
	return reporter
}
