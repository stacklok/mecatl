package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type rootContractSink struct{ got ReviewDetail }

func (s *rootContractSink) PublishReviewDetail(_ context.Context, detail ReviewDetail) {
	s.got = detail
}

func TestReviewDetailPublishesExplicitRootForChild(t *testing.T) {
	sink := &rootContractSink{}
	run := &Run{reviewRoot: &reviewRoot{details: sink, rootSessionID: "root-session"}}
	run.publishReviewDetail(context.Background(), ReviewDetail{SessionID: "subagent-child", ReviewID: "review"})
	if sink.got.RootSessionID != "root-session" || sink.got.SessionID != "subagent-child" {
		t.Fatalf("published detail root/session = %q/%q", sink.got.RootSessionID, sink.got.SessionID)
	}
}

func TestReviewTargetUsesOnlyKnownLocalFileSemantics(t *testing.T) {
	for _, name := range []string{"mcp__remote__read", "CallMcpWithQuery", "Subagent", "custom_tool"} {
		target, paths := reviewTarget(session.NewToolCall("c", name, json.RawMessage(`{"path":"credentials.txt","source":"a","destination":"b"}`)))
		if target.Kind != "tool" || target.Display != "" || len(paths) != 0 {
			t.Fatalf("%s target=%+v paths=%v", name, target, paths)
		}
	}
	for name, args := range map[string]string{
		"Read":  `{"path":"a"}`,
		"Copy":  `{"source":"a","destination":"b"}`,
		"Move":  `{"source":"a","destination":"b"}`,
		"Shell": `{"command":"./script.sh"}`,
	} {
		target, paths := reviewTarget(session.NewToolCall("c", name, json.RawMessage(args)))
		if target.Kind != "workspace" || len(paths) == 0 {
			t.Fatalf("%s target=%+v paths=%v", name, target, paths)
		}
	}
}

type boundedSnapshotWorkspace struct {
	tool.Workspace
	bounded int
}

func (*boundedSnapshotWorkspace) ReadVersion(_ context.Context, _ string) ([]byte, tool.FileVersion, error) {
	panic("snapshot used unbounded ReadVersion")
}
func (w *boundedSnapshotWorkspace) ReadVersionBounded(ctx context.Context, path string, maxBytes int64) ([]byte, tool.FileVersion, error) {
	w.bounded++
	return w.Workspace.(tool.BoundedWorkspaceReader).ReadVersionBounded(ctx, path, maxBytes)
}

func TestSnapshotActionDependenciesUsesBoundedVersionedRead(t *testing.T) {
	ctx := context.Background()
	base := memfs.NewWorkspace("/review")
	if err := base.Write(ctx, "script.sh", []byte("echo safe")); err != nil {
		t.Fatal(err)
	}
	workspace := &boundedSnapshotWorkspace{Workspace: base}
	deps, complete := snapshotActionDependencies(ctx, workspace, []string{"script.sh"})
	if !complete || len(deps) != 1 || !deps[0].exists || workspace.bounded != 1 {
		t.Fatalf("deps=%+v complete=%v bounded=%d", deps, complete, workspace.bounded)
	}
}
