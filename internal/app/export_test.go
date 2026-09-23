package app

import "context"

// Test seams for the EXTERNAL app_test package (which must import
// internal/cliconfig — that package imports app, so the pinning test cannot be
// an in-package test). Only the internal goroutine starters whose
// system-principal wrap is pinned by
// TestCallerIdentity_Scenario2_InternalGoroutinesRunAsSystem are exported here.
var (
	StartChildGCForTest                = startChildGC
	StartMemoryConsolidatorForTest     = startMemoryConsolidator
	StartUserModelConsolidationForTest = startUserModelConsolidation
	StartStaleSessionReconcileForTest  = startStaleSessionReconcile
)

type modelRefreshProbe func(context.Context)

func (p modelRefreshProbe) ListModels(ctx context.Context) ([]modelEntry, error) {
	p(ctx)
	return []modelEntry{{ID: "probe/model"}}, nil
}

// ObserveStartupModelRefreshForTest drives the real startup refresh entry point
// through a probe lister. The synchronous mode changes only scheduling; it uses
// the same root-context construction as the production background branch.
func ObserveStartupModelRefreshForTest(seen func(context.Context)) {
	reg := regWithLister(modelRefreshProbe(seen))
	discovery := newProviderDiscovery(reg, Config{})
	defer discovery.Close()
	discovery.start(true, 0)
}
