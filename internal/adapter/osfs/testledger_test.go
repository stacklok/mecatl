package osfs

import (
	"context"
	"sync"

	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/tool"
)

var testReadLedgers sync.Map

// testReadLedger returns a per-Workspace-instance ReadLedger for tests that
// need to record/inspect ledger state directly against a real *osfs.Workspace
// (the production Workspace type is content-only; the ledger lives beside it,
// selected independently, per ADR 0281).
func testReadLedger(w *Workspace) tool.ReadLedger {
	ledger, _ := testReadLedgers.LoadOrStore(w, memledger.New())
	return ledger.(tool.ReadLedger)
}

func testRecordRead(ctx context.Context, w *Workspace, path string, version tool.FileVersion) error {
	return testReadLedger(w).RecordRead(ctx, tool.LedgerKey(w.Root(), path), version)
}

func testRecordedVersion(ctx context.Context, w *Workspace, path string) (tool.FileVersion, bool, error) {
	return testReadLedger(w).RecordedVersion(ctx, tool.LedgerKey(w.Root(), path))
}
