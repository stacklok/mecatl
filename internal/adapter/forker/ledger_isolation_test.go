package forker_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/forker"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
	"github.com/stacklok/mecatl/internal/adapter/redisstore"
)

// TestPersistentReadLedgers_Scenario1_ForkLedgerIsolation pins AC1.6: every
// fork gets fresh child-session read evidence even when the parent selected a
// durable ledger. Child file and shell operations remain bound to the child
// namespace, and neither inherited parent evidence nor child reads may appear
// in the parent's selected ledger.
func TestPersistentReadLedgers_Scenario1_ForkLedgerIsolation(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	store, err := redisstore.New(mr.Addr())
	if err != nil {
		t.Fatalf("redisstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	parentLedger := store.ReadLedger(session.SessionID("durable-parent"))
	parentRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(parentRoot, "shared.txt"), []byte("parent"), 0o600); err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	parentWS, err := osfs.NewWorkspace(parentRoot)
	if err != nil {
		t.Fatalf("parent workspace: %v", err)
	}
	parentVersion := tool.NewFileVersion("parent-only-evidence")
	if err := parentLedger.RecordRead(ctx, tool.LedgerKey(parentWS.Root(), "parent-only.txt"), parentVersion); err != nil {
		t.Fatalf("record parent evidence: %v", err)
	}
	parent := tool.MustEnvironment(
		session.EnvironmentRef{Kind: session.EnvKindLocal, ID: parentRoot},
		parentWS,
		parentLedger,
		nil,
	)

	// The forker owns ledger selection: even though the parent selected durable
	// storage, the constructor receives and composes a fresh child-session ledger.
	f := forker.New(func(root string) (tool.Workspace, error) {
		return osfs.NewWorkspace(root)
	}, forker.WithForceCopy(), forker.WithRunner(func(root string) tool.CommandRunner {
		runner, runnerErr := osfs.NewCommandRunnerShell(root, "/bin/sh")
		if runnerErr != nil {
			t.Fatalf("child runner: %v", runnerErr)
		}
		return runner
	}))
	child, cleanup, _, err := f.Fork(ctx, parent, "ledger-isolation")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	t.Cleanup(func() { _ = cleanup() })

	if child.Workspace().Root() == parentRoot {
		t.Fatal("child workspace must remain in an isolated child namespace")
	}
	if _, ok, err := child.ReadLedger().RecordedVersion(ctx, tool.LedgerKey(child.Workspace().Root(), "parent-only.txt")); err != nil {
		t.Fatalf("child lookup of parent evidence: %v", err)
	} else if ok {
		t.Fatal("child inherited evidence from the parent's durable ledger")
	}

	_, childVersion, err := child.Workspace().ReadVersion(ctx, "shared.txt")
	if err != nil {
		t.Fatalf("child ReadVersion: %v", err)
	}
	if err := child.ReadLedger().RecordRead(ctx, tool.LedgerKey(child.Workspace().Root(), "shared.txt"), childVersion); err != nil {
		t.Fatalf("child RecordRead: %v", err)
	}
	if _, ok, err := parentLedger.RecordedVersion(ctx, "shared.txt"); err != nil {
		t.Fatalf("parent durable ledger lookup: %v", err)
	} else if ok {
		t.Fatal("child wrote read evidence into the parent's durable ledger")
	}

	if _, err := child.CommandRunner().Run(ctx, "printf child > child-only.txt"); err != nil {
		t.Fatalf("child runner: %v", err)
	}
	if _, err := os.Stat(filepath.Join(child.Workspace().Root(), "child-only.txt")); err != nil {
		t.Fatalf("child runner did not write in child namespace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(parentRoot, "child-only.txt")); !os.IsNotExist(err) {
		t.Fatalf("child runner escaped into parent namespace (err=%v)", err)
	}
}
