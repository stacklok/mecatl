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

	call := func(issuer, subject, root, id string) {
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
	}
	rootA, rootB := t.TempDir(), t.TempDir()
	call("issuer", "alice", rootA, "a")
	call("issuer", "alice", rootB, "b")
	call("issuer", "bob", rootA, "c")

	for _, want := range []struct{ issuer, subject, root string }{{"issuer", "alice", rootA}, {"issuer", "alice", rootB}, {"issuer", "bob", rootA}} {
		sum := sha256.Sum256([]byte(want.issuer + "\x00" + want.subject))
		partition := learning.SkillPartition{Principal: hex.EncodeToString(sum[:]), Project: want.root}
		page, err := repository.List(context.Background(), partition, learning.SkillList{OwnerAgent: "main"})
		if err != nil || len(page.Versions) != 1 {
			t.Fatalf("partition %+v = %d versions, err=%v", partition, len(page.Versions), err)
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
}
