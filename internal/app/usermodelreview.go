package app

import (
	"context"
	"sync"

	"github.com/stacklok/mecatl/engine/learning"
)

// learningAdmissionGate owns the process-wide automatic-learning interval. Every
// provider-specific reviewer shares one instance, so per-session engine creation
// cannot reset throttling.
type learningAdmissionGate struct {
	interval int
	mu       sync.Mutex
	count    int
}

func newLearningAdmissionGate(interval int) *learningAdmissionGate {
	return &learningAdmissionGate{interval: interval}
}

func (a *learningAdmissionGate) admit() bool {
	if a == nil {
		return true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.count++
	return a.interval <= 1 || (a.count-1)%a.interval == 0
}

type admittedObserver struct {
	inner     learning.Observer
	admission *learningAdmissionGate
}

func newAdmittedObserver(inner learning.Observer, admission *learningAdmissionGate) learning.Observer {
	return &admittedObserver{inner: inner, admission: admission}
}

func (o *admittedObserver) Observe(ctx context.Context, tr learning.Trajectory) error {
	if !o.admission.admit() {
		return nil
	}
	return o.inner.Observe(ctx, tr)
}
