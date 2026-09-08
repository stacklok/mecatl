package agent

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type ledgerTestForker struct {
	child tool.Environment
}

func (f ledgerTestForker) Fork(context.Context, tool.Environment, string) (tool.Environment, func() error, string, error) {
	return f.child, func() error { return nil }, "", nil
}

func TestPersistentReadLedgers_ForkPreservesContentBackendAndFreshLedger(t *testing.T) {
	parentWS := memfs.NewWorkspace("/parent")
	parent := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/parent"}, parentWS, memledger.New(), nil)
	forkWS := memfs.NewWorkspace("/fork")
	forkLedger := memledger.New()
	forkEnv := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/fork"}, forkWS, forkLedger, nil)
	subagent := &SubagentTool{ledgerFactory: func() tool.ReadLedger { return memledger.New() }}

	child, _, _, result, ok := subagent.forkChildEnvironment(context.Background(), "call", parent, "child", ledgerTestForker{child: forkEnv})
	if !ok {
		t.Fatalf("forkChildEnvironment failed: %s", result.Content)
	}
	if child.Workspace() != forkWS {
		t.Fatal("fork child must preserve the forker's exact Workspace/content backend")
	}
	if child.ReadLedger() == parent.ReadLedger() || child.ReadLedger() == forkLedger {
		t.Fatal("fork child must receive a fresh ledger from the injected factory")
	}
}

func TestPersistentReadLedgers_DirectWritePreservesWorkspaceAndFreshLedger(t *testing.T) {
	ws := memfs.NewWorkspace("/parent")
	parentLedger := memledger.New()
	parent := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/parent"}, ws, parentLedger, nil)
	subagent := &SubagentTool{ledgerFactory: func() tool.ReadLedger { return memledger.New() }}

	child, _, _, result, ok := subagent.forkChildEnvironment(context.Background(), "call", parent, "child", nil)
	if !ok {
		t.Fatalf("forkChildEnvironment failed: %s", result.Content)
	}
	if child.Workspace() != parent.Workspace() {
		t.Fatal("direct-write child must preserve the exact parent Workspace/content backend")
	}
	if child.ReadLedger() == parent.ReadLedger() {
		t.Fatal("direct-write child must receive a fresh ledger")
	}
}
