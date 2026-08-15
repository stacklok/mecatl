package app

import (
	"context"
	"sync"

	"github.com/stacklok/mecatl/engine/learning"
)

// learningAdmission owns the process-wide legacy completion interval. Every
// provider-specific reviewer shares one instance, so per-session engine creation
// cannot reset throttling.
type learningAdmission struct {
	interval   int
	mu         sync.Mutex
	count      int
	controller *automaticAdmissionController
}

func newLearningAdmission(interval int) *learningAdmission {
	return &learningAdmission{interval: interval}
}

func (a *learningAdmission) admit() bool {
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
	admission *learningAdmission
}

func newAdmittedObserver(inner learning.Observer, admission *learningAdmission) learning.Observer {
	return &admittedObserver{inner: inner, admission: admission}
}

func (o *admittedObserver) Observe(ctx context.Context, tr learning.Trajectory) error {
	if !o.admission.admit() {
		return nil
	}
	return o.inner.Observe(ctx, tr)
}
