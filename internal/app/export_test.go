package app

import (
	"context"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/port"
)

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

type discardModelSwap struct{}

func (discardModelSwap) SetModels([]*mecatlv1.ModelInfo) {}

// ObserveStartupModelRefreshForTest drives the real startup refresh entry point
// through a probe lister. The synchronous mode changes only scheduling; it uses
// the same root-context construction as the production background branch.
func ObserveStartupModelRefreshForTest(seen func(context.Context)) {
	reg := regWithLister(modelRefreshProbe(seen))
	startLiveModelRefresh(port.NopDiagnostics{}, reg, discardModelSwap{}, true, 0)()
}
