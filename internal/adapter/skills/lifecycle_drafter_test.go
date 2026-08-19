package skills

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memskill"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
)

func TestLifecycleDraftToolDerivesCallerAndExactWorkspace(t *testing.T) {
	repository := memskill.New()
	drafter := NewLifecycleDrafter(repository, learning.SkillPartition{Principal: "ownerless"}, "wrong", nil)
	draftTool := NewDraftTool(drafter)
	args := []byte(`{"name":"deploy-safe","description":"Deploy safely when releasing","body":"1. Build.\n2. Verify.\nDone when: healthy."}`)

	call := func(issuer, subject, root, id string) string {
		t.Helper()
		workspace, err := osfs.NewWorkspace(root)
		if err != nil {
			t.Fatal(err)
		}
		env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: root}, workspace, nil)
		ctx := session.WithPrincipal(context.Background(), &session.Principal{Issuer: issuer, Subject: subject})
		result, err := draftTool.Execute(ctx, session.NewToolCall(session.ToolCallID(id), DraftToolName, args), env)
		if err != nil || result.IsError {
			t.Fatalf("draft result=%#v err=%v", result, err)
		}
		return workspace.Root()
	}
	rootA := call("issuer", "alice", t.TempDir(), "a")
	rootB := call("issuer", "alice", t.TempDir(), "b")
	call("issuer", "bob", rootA, "c")

	for _, want := range []struct{ issuer, subject, root string }{{"issuer", "alice", rootA}, {"issuer", "alice", rootB}, {"issuer", "bob", rootA}} {
		sum := sha256.Sum256([]byte(want.issuer + "\x00" + want.subject))
		partition := learning.SkillPartition{Principal: hex.EncodeToString(sum[:]), Project: want.root}
		page, err := repository.List(context.Background(), partition, learning.SkillList{OwnerAgent: "main"})
		if err != nil {
			t.Fatalf("list partition %+v: %v", partition, err)
		}
		if len(page.Versions) != 1 {
			t.Fatalf("partition %+v = %d versions, want 1", partition, len(page.Versions))
		}
		if page.Versions[0].Partition != partition {
			t.Errorf("version partition = %+v, want %+v", page.Versions[0].Partition, partition)
		}
		if page.Versions[0].OwnerAgent != "main" {
			t.Errorf("version owner agent = %q, want %q", page.Versions[0].OwnerAgent, "main")
		}
	}

	workspace, err := osfs.NewWorkspace(rootA)
	if err != nil {
		t.Fatal(err)
	}
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: rootA}, workspace, nil)
	result, err := draftTool.Execute(context.Background(), session.NewToolCall("missing", DraftToolName, args), env)
	if err != nil || !result.IsError {
		t.Fatalf("identity-free draft result=%#v err=%v", result, err)
	}
	page, err := repository.List(context.Background(), learning.SkillPartition{Principal: "ownerless"}, learning.SkillList{OwnerAgent: "wrong"})
	if err != nil {
		t.Fatalf("list ownerless partition: %v", err)
	}
	if len(page.Versions) != 0 {
		t.Fatalf("identity-free draft persisted %d versions in ownerless partition, want 0", len(page.Versions))
	}
}
